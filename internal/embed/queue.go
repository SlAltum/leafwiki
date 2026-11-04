package embed

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Queue states (docs/srs/zh-search-and-retrieval.srs.md §6.4).
const (
	QueueStatePending = "pending"
	QueueStateFailed  = "failed"
)

// QueueEntry is one page waiting to be embedded.
type QueueEntry struct {
	PageID    string
	BuildID   string
	State     string
	QueuedAt  time.Time
	Attempts  int
	LastError string
}

// EnqueuePage adds a page to the queue, or re-points the existing entry at the
// newest build. One row per page is what makes three saves in a row cost one
// encode (FR-410).
func (s *Store) EnqueuePage(pageID, buildID string) error {
	if pageID == "" {
		return fmt.Errorf("embed: enqueue needs a page id")
	}
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`INSERT INTO queue (page_id, build_id, state, queued_at, attempts, last_error)
			VALUES (?, ?, ?, ?, 0, '')
			ON CONFLICT(page_id) DO UPDATE SET
				build_id  = excluded.build_id,
				state     = excluded.state,
				queued_at = excluded.queued_at`,
			pageID, buildID, QueueStatePending, time.Now().UTC().Format(time.RFC3339))
		return err
	})
}

// QueuedPages returns the pending queue entries, oldest first. A limit <= 0
// means no limit.
func (s *Store) QueuedPages(limit int) ([]QueueEntry, error) {
	var entries []QueueEntry
	err := s.withDBRead(func(db *sql.DB) error {
		query := "SELECT page_id, build_id, state, queued_at, attempts, last_error FROM queue WHERE state = ? ORDER BY queued_at ASC, page_id ASC"
		args := []any{QueueStatePending}
		if limit > 0 {
			query += " LIMIT ?"
			args = append(args, limit)
		}

		rows, err := db.Query(query, args...)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()

		for rows.Next() {
			var entry QueueEntry
			var queuedAt string
			if err := rows.Scan(&entry.PageID, &entry.BuildID, &entry.State, &queuedAt, &entry.Attempts, &entry.LastError); err != nil {
				return err
			}
			entry.QueuedAt, _ = time.Parse(time.RFC3339, queuedAt)
			entries = append(entries, entry)
		}
		return rows.Err()
	})
	return entries, err
}

// RemoveQueuedPage drops a page's entry once it was embedded, but only if it is
// still the entry that was embedded: a save that lands during the embedding
// re-points the row, and deleting it would drop that newer content from the
// queue until the page is edited again. Timestamps carry whole seconds, so a
// save inside the same second as the read entry still loses its row.
func (s *Store) RemoveQueuedPage(pageID string, queuedAt time.Time) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec("DELETE FROM queue WHERE page_id = ? AND queued_at = ?",
			pageID, queuedAt.UTC().Format(time.RFC3339))
		return err
	})
}

// ClearQueueFailure drops a page's failed queue row once the page is embedded
// again. A pending row stays: it belongs to a save that landed after the
// embedding started, and that content needs its own pass.
func (s *Store) ClearQueueFailure(pageID string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec("DELETE FROM queue WHERE page_id = ? AND state = ?", pageID, QueueStateFailed)
		return err
	})
}

// RemoveFromQueue drops a page's entry once it was embedded.
func (s *Store) RemoveFromQueue(pageID string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM queue WHERE page_id = ?`, pageID)
		return err
	})
}

// MarkQueuedFailure records why a page could not be embedded and raises its
// attempt count, without dropping the entry: one bad page must not stop the
// rest of the batch (FR-407).
func (s *Store) MarkQueuedFailure(pageID, reason string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE queue SET attempts = attempts + 1, state = ?, last_error = ? WHERE page_id = ?`,
			QueueStateFailed, reason, pageID)
		return err
	})
}

// QueueCount returns how many pages are waiting.
func (s *Store) QueueCount() (int, error) {
	var count int
	err := s.withDBRead(func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM queue WHERE state = ?`, QueueStatePending).Scan(&count)
	})
	return count, err
}

// FailedCount returns how many pages are queued but will not be retried.
func (s *Store) FailedCount() (int, error) {
	var count int
	err := s.withDBRead(func(db *sql.DB) error {
		return db.QueryRow("SELECT COUNT(*) FROM queue WHERE state = ?", QueueStateFailed).Scan(&count)
	})
	return count, err
}

// QueueEntryFor returns the queue row of one page, or nil when it is not
// queued. Both the pending and the failed row are returned, because a page's
// status has to tell "waiting" from "gave up" (FR-410).
func (s *Store) QueueEntryFor(pageID string) (*QueueEntry, error) {
	var entry *QueueEntry
	err := s.withDBRead(func(db *sql.DB) error {
		row := db.QueryRow(`SELECT page_id, build_id, state, queued_at, attempts, last_error
			FROM queue WHERE page_id = ?`, pageID)
		var scanned QueueEntry
		var queuedAt string
		if err := row.Scan(&scanned.PageID, &scanned.BuildID, &scanned.State, &queuedAt, &scanned.Attempts, &scanned.LastError); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		scanned.QueuedAt, _ = time.Parse(time.RFC3339, queuedAt)
		entry = &scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}
