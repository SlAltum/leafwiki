package search

import (
	"testing"

	coresearch "github.com/perber/wiki/internal/search"
)

func TestFuseSearch(t *testing.T) {
	lexical := []coresearch.SearchResultItem{
		{PageID: "a", Title: "A"},
		{PageID: "b", Title: "B"},
	}

	t.Run("no semantic hits leaves the lexical order untouched", func(t *testing.T) {
		fused := fuseSearch(lexical, nil)
		if len(fused) != 2 || fused[0].PageID != "a" || fused[1].PageID != "b" {
			t.Fatalf("fused = %#v, want the lexical order", fused)
		}
	})

	t.Run("a page both sides found outranks a page only one found", func(t *testing.T) {
		fused := fuseSearch(lexical, []SemanticHit{{PageID: "b"}, {PageID: "c"}})
		if len(fused) != 3 {
			t.Fatalf("fused has %d items, want 3: %#v", len(fused), fused)
		}
		if fused[0].PageID != "b" {
			t.Fatalf("first = %q, want b (found by both rankings)", fused[0].PageID)
		}
	})

	t.Run("a semantic-only page arrives with its section", func(t *testing.T) {
		fused := fuseSearch(lexical, []SemanticHit{{PageID: "c", Anchor: "cats", Section: "Cats"}})
		var found *coresearch.SearchResultItem
		for i := range fused {
			if fused[i].PageID == "c" {
				found = &fused[i]
			}
		}
		if found == nil {
			t.Fatalf("semantic-only page missing from %#v", fused)
		}
		if found.Anchor != "cats" || found.Section != "Cats" {
			t.Fatalf("item = %#v, want the semantic hit's section", found)
		}
		if found.Title != "" {
			t.Fatalf("a semantic-only item has no lexical fields yet: %#v", found)
		}
	})

	t.Run("equal ranks break ties by page id", func(t *testing.T) {
		fused := fuseSearch([]coresearch.SearchResultItem{{PageID: "z"}}, []SemanticHit{{PageID: "y"}})
		if fused[0].PageID != "y" {
			t.Fatalf("order = %#v, want y before z", fused)
		}
	})
}
