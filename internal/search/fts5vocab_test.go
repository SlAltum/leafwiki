package search

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFTS5VocabAvailable is the O-5 gate: whether the driver exposes
// fts5vocab, which decides between reading the term vocabulary straight off
// the index and maintaining a separate terms table beside it.
func TestFTS5VocabAvailable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE pages USING fts5(title, content);`); err != nil {
		t.Fatalf("create pages: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pages VALUES ('receive the parcel', 'nothing here');`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE pages_vocab USING fts5vocab(pages, 'row');`); err != nil {
		t.Fatalf("fts5vocab is not available: %v", err)
	}

	var term string
	var doc int
	if err := db.QueryRow(`SELECT term, doc FROM pages_vocab WHERE term = 'receive'`).Scan(&term, &doc); err != nil {
		t.Fatalf("reading the vocabulary: %v", err)
	}
	if term != "receive" || doc != 1 {
		t.Fatalf("vocab row = %q/%d, want receive/1", term, doc)
	}
}
