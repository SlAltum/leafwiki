package search

import (
	"context"
	"testing"
)

func TestSearchUseCase_Execute_FusedCountIsAtLeastTheKeywordTotal(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	// Both pages match the keyword; semantic recall finds one of them again.
	first := createIndexedPage(t, uc, treeSvc, "One", "one", "keyword here")
	createIndexedPage(t, uc, treeSvc, "Two", "two", "keyword also")
	uc.WithSemanticSearcher(fakeSearcher{hits: []SemanticHit{{PageID: first}}})

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Two keyword matches, only one fits on the page; the count a client
	// paginates by must still be two, not the size of the candidate window.
	if out.Result.Count != 2 {
		t.Fatalf("count = %d, want the keyword total 2", out.Result.Count)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("page has %d items, want 1", len(out.Result.Items))
	}
}

func TestSearchUseCase_Execute_FusedCountIncludesSemanticOnlyPages(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Lexical", "lexical", "keyword here")
	semanticOnly := createIndexedPage(t, uc, treeSvc, "Semantic", "semantic", "unrelated words")
	uc.WithSemanticSearcher(fakeSearcher{hits: []SemanticHit{{PageID: semanticOnly}}})

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Count != 2 {
		t.Fatalf("count = %d, want 2 (one keyword match plus one semantic-only page)", out.Result.Count)
	}
}

func TestSearchUseCase_Execute_FusedPagesStayReachable(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	ids := make([]string, 0, 5)
	for _, slug := range []string{"a", "b", "c", "d", "e"} {
		ids = append(ids, createIndexedPage(t, uc, treeSvc, slug, slug, "keyword here"))
	}
	uc.WithSemanticSearcher(fakeSearcher{hits: []SemanticHit{{PageID: ids[4]}}})

	// Read the last page of the fused list: fetching only the shallow window
	// would leave it empty.
	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Offset: 4, Limit: 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Count != 5 {
		t.Fatalf("count = %d, want 5", out.Result.Count)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("last page has %d items, want 1", len(out.Result.Items))
	}
}
