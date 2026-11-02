package search

import (
	"context"
	"testing"
)

type fakeSearcher struct {
	hits []SemanticHit
	err  error
}

func (f fakeSearcher) SearchPages(string, int) ([]SemanticHit, error) {
	return f.hits, f.err
}

func TestSearchUseCase_Execute_FusesSemanticHits(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Lexical", "lexical", "keyword here")
	semanticOnly := createIndexedPage(t, uc, treeSvc, "Semantic", "semantic", "unrelated words")
	uc.semantic = fakeSemantic{state: SemanticOK}
	uc.WithSemanticSearcher(fakeSearcher{hits: []SemanticHit{{PageID: semanticOnly, Anchor: "a", Section: "S"}}})

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Count != 2 {
		t.Fatalf("count = %d, want the fused candidate count 2", out.Result.Count)
	}
	seen := map[string]bool{}
	for _, item := range out.Result.Items {
		seen[item.PageID] = true
	}
	if !seen[semanticOnly] {
		t.Fatalf("the semantic-only page is missing from %#v", out.Result.Items)
	}
	if out.Result.Semantic != string(SemanticOK) {
		t.Fatalf("semantic = %q, want ok", out.Result.Semantic)
	}
}

func TestSearchUseCase_Execute_KeepsLexicalResultWhenSemanticFindsNothing(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Only", "only", "keyword here")
	uc.WithSemanticSearcher(fakeSearcher{})

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Count != 1 || len(out.Result.Items) != 1 {
		t.Fatalf("result = %#v, want the plain lexical hit", out.Result)
	}
	if out.Result.Items[0].Title == "" {
		t.Fatal("the lexical path must still decorate titles")
	}
}

func TestSearchUseCase_Execute_FusedPagination(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	ids := make([]string, 0, 3)
	for _, slug := range []string{"one", "two", "three"} {
		ids = append(ids, createIndexedPage(t, uc, treeSvc, slug, slug, "keyword here"))
	}
	hits := []SemanticHit{{PageID: ids[0]}, {PageID: ids[1]}, {PageID: ids[2]}}
	uc.WithSemanticSearcher(fakeSearcher{hits: hits})

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Count != 3 {
		t.Fatalf("count = %d, want 3 candidates", out.Result.Count)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("page has %d items, want 1", len(out.Result.Items))
	}
	if out.Result.Offset != 1 || out.Result.Limit != 1 {
		t.Fatalf("offset/limit = %d/%d", out.Result.Offset, out.Result.Limit)
	}
}
