package pages_test

import (
	"context"
	"log/slog"
	"testing"

	coresearch "github.com/perber/wiki/internal/search"
	"github.com/perber/wiki/internal/wiki/pages"
	"github.com/perber/wiki/internal/wiki/pagesave"
)

func TestApplyPageRefactorUseCase_ReindexesRewrittenPages(t *testing.T) {
	deps := newTestDeps(t)

	index, err := coresearch.NewSQLiteIndex(deps.storageDir)
	if err != nil {
		t.Fatalf("NewSQLiteIndex: %v", err)
	}
	t.Cleanup(func() {
		if err := index.Close(); err != nil {
			t.Errorf("close search index: %v", err)
		}
	})
	// The same side effects the composition root registers, so the paths under
	// test are the production ones: a refactor writes rewritten pages through
	// BulkUpdateContent, which fires no save event, so the use case has to run
	// the effects itself (FR-601, FR-602).
	orchestrator := pagesave.NewPageSaveOrchestrator(nil,
		pagesave.NewSearchIndexSideEffect(index, deps.tree, slog.Default(), nil),
		pagesave.NewLinkIndexSideEffect(deps.links, slog.Default(), nil),
		pagesave.NewRevisionSideEffect(deps.revision, slog.Default(), nil),
	)
	createUC := pages.NewCreatePageUseCase(deps.tree, deps.slug, orchestrator, slog.Default(), nil)
	updateUC := pages.NewUpdatePageUseCase(deps.tree, deps.slug, orchestrator, slog.Default(), nil)
	applyUC := pages.NewApplyPageRefactorUseCase(deps.tree, deps.slug, deps.revision, deps.links, slog.Default(), nil, orchestrator)

	target, err := createUC.Execute(context.Background(), pages.CreatePageInput{
		UserID: "system", Title: "Target", Slug: "target", Kind: pageKind(),
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	ref, err := createUC.Execute(context.Background(), pages.CreatePageInput{
		UserID: "system", Title: "Ref", Slug: "ref", Kind: pageKind(),
	})
	if err != nil {
		t.Fatalf("create ref: %v", err)
	}
	content := "keepword [[Target]]"
	if _, err := updateUC.Execute(context.Background(), pages.UpdatePageInput{
		UserID: "system", ID: ref.Page.ID, Version: ref.Page.Version(), Title: ref.Page.Title, Slug: ref.Page.Slug, Content: &content, Kind: pageKind(),
	}); err != nil {
		t.Fatalf("update ref: %v", err)
	}

	find := func(query string) []string {
		t.Helper()
		result, err := index.Search(query, nil, 0, 10)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		ids := make([]string, len(result.Items))
		for i, item := range result.Items {
			ids[i] = item.PageID
		}
		return ids
	}

	// The rewritten page only changes its wiki-link text, which is what the
	// index stores: the new word must become findable through it.
	if got := find("renamed"); len(got) != 0 {
		t.Fatalf("search for the new title before the rename returned %v", got)
	}

	if _, err := applyUC.Execute(context.Background(), pages.RefactorApplyInput{
		UserID:  "system",
		Version: target.Page.Version(),
		RefactorPreviewInput: pages.RefactorPreviewInput{
			PageID:  target.Page.ID,
			Kind:    pages.RefactorKindRename,
			Title:   "Target Renamed",
			Slug:    "target-renamed",
			Content: &target.Page.Content,
		},
		RewriteLinks: true,
	}); err != nil {
		t.Fatalf("apply refactor: %v", err)
	}

	found := false
	for _, id := range find("renamed") {
		if id == ref.Page.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("search after the rename did not return the rewritten page %q", ref.Page.ID)
	}
	if plain := find("keepword"); len(plain) != 1 || plain[0] != ref.Page.ID {
		t.Fatalf("search for the page's own word returned %v, want %q", plain, ref.Page.ID)
	}
}
