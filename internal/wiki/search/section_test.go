package search

import (
	"context"
	"testing"
)

func TestSearchUseCase_Execute_ReportsTheMatchingSection(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Operations", "operations", "intro text only here\n\n## 部署流程\n\nrun deploy now\n\n## Rollback\n\nnothing about it here\n")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "部署", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}
	item := out.Result.Items[0]
	if item.Anchor != "部署流程" || item.Section != "部署流程" {
		t.Fatalf("anchor/section = %q/%q, want the section holding the match", item.Anchor, item.Section)
	}

	// A hit in the introduction has no section: the reader lands on the top of
	// the page anyway.
	out, err = uc.Execute(context.Background(), SearchInput{Query: "intro", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}
	if got := out.Result.Items[0]; got.Anchor != "" || got.Section != "" {
		t.Fatalf("intro hit reported anchor/section %q/%q, want empty", got.Anchor, got.Section)
	}

	// A hit in the second section reports that section, not the first.
	out, err = uc.Execute(context.Background(), SearchInput{Query: "rollback", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}
	if got := out.Result.Items[0]; got.Anchor != "rollback" || got.Section != "Rollback" {
		t.Fatalf("anchor/section = %q/%q, want the Rollback section", got.Anchor, got.Section)
	}
}
