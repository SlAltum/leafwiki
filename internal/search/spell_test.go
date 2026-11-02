package search

import (
	"strings"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/test_utils"
)

// spellIndex indexes a small corpus: three pages share the common words, one
// page holds a word that is too rare to be suggested (FR-302).
func spellIndex(t *testing.T) *SQLiteIndex {
	t.Helper()

	index, err := NewSQLiteIndex(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create SQLiteIndex: %v", err)
	}
	t.Cleanup(func() { test_utils.WrapCloseWithErrorCheck(index.Close, t) })

	inputs := []IndexPageInput{
		{Path: "docs/a.md", FilePath: "docs/a.md", PageID: "a", Title: "Alpha", Kind: tree.NodeKindPage, Raw: "Please receive the parcel at the westside desk and separate the labels."},
		{Path: "docs/b.md", FilePath: "docs/b.md", PageID: "b", Title: "Beta", Kind: tree.NodeKindPage, Raw: "We receive mail at the westside office; separate rooms."},
		{Path: "docs/c.md", FilePath: "docs/c.md", PageID: "c", Title: "Gamma", Kind: tree.NodeKindPage, Raw: "They receive visitors at the westside entrance, separate entrance."},
		{Path: "docs/d.md", FilePath: "docs/d.md", PageID: "d", Title: "Delta", Kind: tree.NodeKindPage, Raw: "A onceonly word lives only here."},
	}
	if _, err := index.IndexPages(inputs); err != nil {
		t.Fatalf("indexing failed: %v", err)
	}
	return index
}

func TestSQLiteIndex_SuggestTerm(t *testing.T) {
	index := spellIndex(t)

	cases := []struct {
		name      string
		term      string
		want      string
		wantFound bool
	}{
		{"one deletion", "recive", "receive", true},
		{"transposition costs two edits", "recieve", "receive", true},
		{"one substitution", "seperate", "separate", true},
		{"known word is not corrected", "westside", "", false},
		{"term below the frequency floor", "onceonlyy", "", false},
		{"prefix lock", "xeceive", "", false},
		{"cjk is never corrected", "接収", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := index.SuggestTerm(tc.term)
			if err != nil {
				t.Fatalf("SuggestTerm(%q): %v", tc.term, err)
			}
			if found != tc.wantFound || got != tc.want {
				t.Fatalf("SuggestTerm(%q) = %q/%v, want %q/%v", tc.term, got, found, tc.want, tc.wantFound)
			}
		})
	}
}

func TestSQLiteIndex_SuggestTerm_VocabularyFollowsTheIndex(t *testing.T) {
	index := spellIndex(t)

	if _, found, err := index.SuggestTerm("recieve"); err != nil || !found {
		t.Fatalf("expected a suggestion before clearing: found=%v err=%v", found, err)
	}

	if err := index.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, found, err := index.SuggestTerm("recieve"); err != nil || found {
		t.Fatalf("expected no suggestion after clearing: found=%v err=%v", found, err)
	}
}

func TestSQLiteIndex_Search_CorrectsATypo(t *testing.T) {
	index := spellIndex(t)

	for _, query := range []string{"recieve", "recive", "seperate"} {
		t.Run(query, func(t *testing.T) {
			result, err := index.Search(query, nil, 0, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", query, err)
			}
			if len(result.Items) != 3 {
				t.Fatalf("Search(%q) returned %d items, want the three pages holding the word", query, len(result.Items))
			}
		})
	}

	// A correction on the second term must still build one valid MATCH: a
	// parenthesised group may not simply follow a phrase.
	result, err := index.Search("westside recieve", nil, 0, 10)
	if err != nil {
		t.Fatalf("Search with a corrected second term: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("got %d items, want the three pages", len(result.Items))
	}

	// A query the corpus contains is not rewritten, so it keeps its own hits.
	result, err = index.Search("westside", nil, 0, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("Search(westside) returned %d items, want 3", len(result.Items))
	}

	// A typo with no close neighbour still returns nothing.
	result, err = index.Search("zzzzzz", nil, 0, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("Search(zzzzzz) returned %v, want nothing", result.Items)
	}
}

func TestSQLiteIndex_Search_ReportsTheCorrectedQuery(t *testing.T) {
	index := spellIndex(t)

	cases := []struct{ query, want string }{
		{"recieve", "receive"},
		{"westside recieve", "westside receive"},
		{"westside", ""},
		{"接収", ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			result, err := index.Search(tc.query, nil, 0, 10)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if result.Suggestion != tc.want {
				t.Fatalf("suggestion = %q, want %q", result.Suggestion, tc.want)
			}
		})
	}
}

func TestQuoteFieldTerms(t *testing.T) {
	// The suggester only adds alternatives; with no suggestions the query is
	// built exactly as before.
	terms := QueryTerms("receive parcel")
	without := quoteFieldTerms(terms, nil)
	withEmpty := quoteFieldTerms(terms, func(string) (string, bool) { return "", false })
	if without != withEmpty {
		t.Fatalf("quoteFieldTerms differs without suggestions: %q vs %q", without, withEmpty)
	}
	if got := quoteFieldTerms([]string{"recieve"}, func(string) (string, bool) { return "receive", true }); !strings.Contains(got, "OR") {
		t.Fatalf("quoteFieldTerms = %q, want an OR with the correction", got)
	}
}
