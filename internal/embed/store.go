package embed

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/perber/wiki/internal/core/shared/sqliteutil"
	_ "modernc.org/sqlite"
)

// Meta keys of the vectors database (docs/srs/zh-search-and-retrieval.srs.md
// §6.4).
const (
	MetaCurrentBuildID   = "current_build_id"
	MetaModelID          = "model_id"
	MetaTokenizerVersion = "tokenizer_version"
	MetaSchemaVersion    = "schema_version"
	MetaPaused           = "paused"
	MetaRunningSince     = "running_since"
	MetaLastError        = "last_error"
	schemaVersion        = "1"
)

// Chunk is one indexed passage: a heading's worth of text and its vector.
type Chunk struct {
	PageID      string
	ChunkNo     int
	Heading     string
	HeadingPath string
	Anchor      string
	ContentHash string
	Vec         []float32
}

// BuildInfo is the identity of the build the store currently answers from.
type BuildInfo struct {
	ID               string
	ModelID          string
	TokenizerVersion string
}

// Store is the vectors database. It is separate from search.db so that
// deleting it leaves keyword search untouched (FR-406).
type Store struct {
	mu   sync.RWMutex
	path string
	db   *sql.DB
}

// OpenStore opens the vectors database at path, creating its schema. A corrupt
// file is reported, never replaced: the vectors are derived, but a silently
// rebuilt index would hide that a rebuild is needed and cost minutes of
// embedding (ADR-0013).
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}

	err := sqliteutil.OpenWithPolicy(path, sqliteutil.FailOnCorruption, func() error {
		if _, err := s.connect(); err != nil {
			return err
		}
		if err := s.ensureSchema(); err != nil {
			if closeErr := s.Close(); closeErr != nil {
				slog.Default().Warn("failed to close the vectors database after a schema error", "error", closeErr)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// connect returns the open *sql.DB, opening it on first use.
func (s *Store) connect() (*sql.DB, error) {
	s.mu.RLock()
	if s.db != nil {
		db := s.db
		s.mu.RUnlock()
		return db, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		db, err := sql.Open("sqlite", filepath.FromSlash(s.path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		if err != nil {
			return nil, err
		}
		s.db = db
	}
	return s.db, nil
}

// withDB runs fn under the exclusive lock, for anything that writes.
func (s *Store) withDB(fn func(db *sql.DB) error) error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(db)
}

// withDBRead runs fn under a shared lock, for reads.
func (s *Store) withDBRead(fn func(db *sql.DB) error) error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(db)
}

// Close closes the database handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}

// Ping reports whether the database answers.
func (s *Store) Ping() error {
	return s.withDBRead(func(db *sql.DB) error { return db.Ping() })
}

// ensureSchema creates the three tables and records the schema version.
func (s *Store) ensureSchema() error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS meta (
				key   TEXT PRIMARY KEY,
				value TEXT NOT NULL
			);
			CREATE TABLE IF NOT EXISTS chunks (
				build_id     TEXT NOT NULL,
				page_id      TEXT NOT NULL,
				chunk_no     INTEGER NOT NULL,
				heading      TEXT NOT NULL,
				heading_path TEXT NOT NULL,
				anchor       TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				vec          BLOB NOT NULL,
				PRIMARY KEY (build_id, page_id, chunk_no)
			);
			CREATE TABLE IF NOT EXISTS queue (
				page_id    TEXT PRIMARY KEY,
				build_id   TEXT NOT NULL,
				state      TEXT NOT NULL,
				queued_at  TEXT NOT NULL,
				attempts   INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NOT NULL DEFAULT ''
			);
		`)
		if err != nil {
			return err
		}

		var version string
		err = db.QueryRow(`SELECT value FROM meta WHERE key = ?`, MetaSchemaVersion).Scan(&version)
		switch {
		case err == sql.ErrNoRows:
			_, err = db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, MetaSchemaVersion, schemaVersion)
			return err
		case err != nil:
			return err
		case version != schemaVersion:
			return fmt.Errorf("embed: vectors database has schema version %q, this build understands %q", version, schemaVersion)
		}
		return nil
	})
}

// Meta returns one meta value, or "" when the key is unset.
func (s *Store) Meta(key string) (string, error) {
	var value string
	err := s.withDBRead(func(db *sql.DB) error {
		rowErr := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
		if rowErr == sql.ErrNoRows {
			return nil
		}
		return rowErr
	})
	return value, err
}

// SetMeta writes one meta value.
func (s *Store) SetMeta(key, value string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}

// DeleteMeta removes one meta value.
func (s *Store) DeleteMeta(key string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec("DELETE FROM meta WHERE key = ?", key)
		return err
	})
}

// BeginBuild mints the id of a new build generation and records that a build
// is running. The current build keeps answering until CommitBuild switches the
// pointer, so an interrupted build costs the work, never the index (FR-407).
func (s *Store) BeginBuild() (string, error) {
	id, err := newBuildID()
	if err != nil {
		return "", err
	}

	err = s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			MetaRunningSince, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// CommitBuild makes buildID the generation reads answer from and drops every
// generation that came before it. The model and tokenizer identities are
// recorded with it, so a change in either is visible against the build that
// produced the current vectors (FR-408).
func (s *Store) CommitBuild(buildID, modelID, tokenizerVersion string) error {
	if strings.TrimSpace(buildID) == "" {
		return fmt.Errorf("embed: commit needs a build id")
	}

	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		for key, value := range map[string]string{
			MetaCurrentBuildID:   buildID,
			MetaModelID:          modelID,
			MetaTokenizerVersion: tokenizerVersion,
		} {
			if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM meta WHERE key IN (?, ?)`, MetaRunningSince, MetaLastError); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM chunks WHERE build_id <> ?`, buildID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// CurrentBuild returns the generation reads answer from, or nil when no build
// has ever been committed.
func (s *Store) CurrentBuild() (*BuildInfo, error) {
	info := &BuildInfo{}
	err := s.withDBRead(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT key, value FROM meta WHERE key IN (?, ?, ?)`,
			MetaCurrentBuildID, MetaModelID, MetaTokenizerVersion)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return err
			}
			switch key {
			case MetaCurrentBuildID:
				info.ID = value
			case MetaModelID:
				info.ModelID = value
			case MetaTokenizerVersion:
				info.TokenizerVersion = value
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if info.ID == "" {
		return nil, nil
	}
	return info, nil
}

// DropBuild removes everything one build wrote, for a build that failed.
func (s *Store) DropBuild(buildID string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM chunks WHERE build_id = ?`, buildID)
		return err
	})
}

// PutChunks writes a page's chunks into one build. Vectors are L2-normalized
// before they are stored, so a query can use a dot product as the cosine
// similarity.
func (s *Store) PutChunks(buildID string, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		stmt, err := tx.Prepare(`INSERT INTO chunks
			(build_id, page_id, chunk_no, heading, heading_path, anchor, content_hash, vec)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(build_id, page_id, chunk_no) DO UPDATE SET
				heading = excluded.heading,
				heading_path = excluded.heading_path,
				anchor = excluded.anchor,
				content_hash = excluded.content_hash,
				vec = excluded.vec`)
		if err != nil {
			return err
		}
		defer func() {
			_ = stmt.Close()
		}()

		for _, chunk := range chunks {
			if _, err := stmt.Exec(buildID, chunk.PageID, chunk.ChunkNo, chunk.Heading,
				chunk.HeadingPath, chunk.Anchor, chunk.ContentHash, encodeVec(chunk.Vec)); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// Reset throws the vectors away and forgets which build made them, so the next
// reader sees StateMissing and a rebuild is what fixes it. It is what a
// snapshot restore needs: the restored Markdown says nothing about the vectors
// that were indexed before it, and keeping them would answer from the past
// (FR-604, NFR-7). The pause flag survives, because it is an operator decision
// about indexing, not about this content.
func (s *Store) Reset() error {
	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		for _, statement := range []string{
			"DELETE FROM chunks",
			"DELETE FROM queue",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		for _, key := range []string{
			MetaCurrentBuildID,
			MetaModelID,
			MetaTokenizerVersion,
			MetaLastError,
			MetaRunningSince,
		} {
			if _, err := tx.Exec("DELETE FROM meta WHERE key = ?", key); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// DeletePageChunks drops everything a page contributed to any build. A deleted
// page must leave no orphan chunk behind in the generation that is still
// answering (FR-605).
func (s *Store) DeletePageChunks(pageID string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec("DELETE FROM chunks WHERE page_id = ?", pageID)
		return err
	})
}

// ReplacePageChunks replaces everything one page contributes to a build. A page
// that lost a section must lose the chunks of that section too, which an
// upsert alone would leave behind as orphans.
func (s *Store) ReplacePageChunks(buildID, pageID string, chunks []Chunk) error {
	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		if _, err := tx.Exec("DELETE FROM chunks WHERE build_id = ? AND page_id = ?", buildID, pageID); err != nil {
			return err
		}
		for _, chunk := range chunks {
			if _, err := tx.Exec(`INSERT INTO chunks
				(build_id, page_id, chunk_no, heading, heading_path, anchor, content_hash, vec)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				buildID, chunk.PageID, chunk.ChunkNo, chunk.Heading, chunk.HeadingPath,
				chunk.Anchor, chunk.ContentHash, encodeVec(chunk.Vec)); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// PageChunk is one stored passage of a page, without its vector: callers that
// report status need the passage identity and the hash, not the numbers.
type PageChunk struct {
	ChunkNo     int
	ContentHash string
}

// PageChunks returns the passages one page contributed to a build, ordered by
// chunk number. An empty result means the page is not in that build.
func (s *Store) PageChunks(buildID, pageID string) ([]PageChunk, error) {
	var chunks []PageChunk
	err := s.withDBRead(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT chunk_no, content_hash FROM chunks
			WHERE build_id = ? AND page_id = ? ORDER BY chunk_no`, buildID, pageID)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var chunk PageChunk
			if err := rows.Scan(&chunk.ChunkNo, &chunk.ContentHash); err != nil {
				return err
			}
			chunks = append(chunks, chunk)
		}
		return rows.Err()
	})
	return chunks, err
}

// PageIDs returns every page one build holds chunks for, so a caller can tell
// which pages are in the index and which are gone from disk.
func (s *Store) PageIDs(buildID string) ([]string, error) {
	var pageIDs []string
	err := s.withDBRead(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT DISTINCT page_id FROM chunks WHERE build_id = ? ORDER BY page_id`, buildID)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var pageID string
			if err := rows.Scan(&pageID); err != nil {
				return err
			}
			pageIDs = append(pageIDs, pageID)
		}
		return rows.Err()
	})
	return pageIDs, err
}

// ChunkCount returns how many chunks a build holds.
func (s *Store) ChunkCount(buildID string) (int, error) {
	var count int
	err := s.withDBRead(func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE build_id = ?`, buildID).Scan(&count)
	})
	return count, err
}

// encodeVec normalizes vec and serializes it as little-endian float32.
func encodeVec(vec []float32) []byte {
	normalized := normalizeVec(vec)
	out := make([]byte, len(normalized)*4)
	for i, value := range normalized {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out
}

// decodeVec reads back what encodeVec wrote.
func decodeVec(raw []byte) []float32 {
	vec := make([]float32, len(raw)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return vec
}

// normalizeVec returns a unit-length copy of vec. A zero vector stays zero
// rather than becoming NaN.
func normalizeVec(vec []float32) []float32 {
	var sum float64
	for _, value := range vec {
		sum += float64(value) * float64(value)
	}
	out := make([]float32, len(vec))
	if sum == 0 {
		return out
	}
	norm := float32(math.Sqrt(sum))
	for i, value := range vec {
		out[i] = value / norm
	}
	return out
}

// newBuildID mints a random build id. Random rather than derived from the
// model: a rerun must never reuse rows from a build that was interrupted.
func newBuildID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
