package search

import (
	"context"
	"strings"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/http/dto"
	coresearch "github.com/perber/wiki/internal/search"
	coretags "github.com/perber/wiki/internal/tags"
	"github.com/perber/wiki/internal/test_utils"
)

func setupSearchUseCaseForTags(t *testing.T) (*SearchUseCase, *coretags.TagsService, *tree.TreeService) {
	t.Helper()

	dir := t.TempDir()

	treeSvc := tree.NewTreeService(dir)
	if err := treeSvc.LoadTree(); err != nil {
		t.Fatalf("LoadTree: %v", err)
	}

	index, err := coresearch.NewSQLiteIndex(dir)
	if err != nil {
		t.Fatalf("NewSQLiteIndex: %v", err)
	}
	t.Cleanup(func() { test_utils.WrapCloseWithErrorCheck(index.Close, t) })

	tagsStore, err := coretags.NewTagsStore(dir)
	if err != nil {
		t.Fatalf("NewTagsStore: %v", err)
	}
	t.Cleanup(func() { test_utils.WrapCloseWithErrorCheck(tagsStore.Close, t) })

	tagsSvc := coretags.NewTagsService(tagsStore)
	return NewSearchUseCase(index, tagsSvc, treeSvc, nil), tagsSvc, treeSvc
}

func pageKind() *tree.NodeKind {
	k := tree.NodeKindPage
	return &k
}

func createTaggedPage(t *testing.T, treeSvc *tree.TreeService, title, slug string, tags []string) string {
	t.Helper()

	idPtr, err := treeSvc.CreateNode("system", nil, title, slug, pageKind())
	if err != nil {
		t.Fatalf("CreateNode %q: %v", slug, err)
	}

	content := "---\ntags:\n"
	for _, tag := range tags {
		content += "  - " + tag + "\n"
	}
	content += "---\n\n# Page body"

	if err := treeSvc.UpdateNode(tree.UpdateNodeInput{UserID: "system", ID: *idPtr, Title: title, Slug: slug, Content: &content, ExpectedVersion: tree.VersionUnchecked, PreserveFrontmatter: true}); err != nil {
		t.Fatalf("UpdateNode %q: %v", slug, err)
	}

	return *idPtr
}

type fakeSemantic struct{ state SemanticState }

func (f fakeSemantic) SemanticState() SemanticState { return f.state }

func TestSearchUseCase_Execute_ReportsSemanticState(t *testing.T) {
	for _, state := range []SemanticState{SemanticOff, SemanticOK, SemanticStale, SemanticUnavailable} {
		t.Run(string(state), func(t *testing.T) {
			uc, _, treeSvc := setupSearchUseCaseForTags(t)
			uc.semantic = fakeSemantic{state: state}
			createIndexedPage(t, uc, treeSvc, "Doc", "doc", "body with keyword")

			out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out.Result.Semantic != string(state) {
				t.Fatalf("semantic = %q, want %q", out.Result.Semantic, state)
			}
			// The lexical results are unaffected by the semantic state (FR-414).
			if len(out.Result.Items) != 1 {
				t.Fatalf("got %d items, want the lexical hit", len(out.Result.Items))
			}
		})
	}
}

func TestSearchUseCase_Execute_SemanticOffWithoutReporter(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Doc", "doc", "body with keyword")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Semantic != string(SemanticOff) {
		t.Fatalf("semantic = %q, want off without a reporter", out.Result.Semantic)
	}
}

func TestSearchUseCase_SearchByTags_ReportsSemanticState(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	uc.semantic = fakeSemantic{state: SemanticStale}
	createIndexedPage(t, uc, treeSvc, "Doc", "doc", "body")

	out, err := uc.Execute(context.Background(), SearchInput{Tags: []string{"missing"}, Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result.Semantic != string(SemanticStale) {
		t.Fatalf("semantic = %q, want stale on the tag-only path too", out.Result.Semantic)
	}
}

func TestSearchUseCase_Execute_TagOnlySearchEscapesTitle(t *testing.T) {
	uc, tagsSvc, treeSvc := setupSearchUseCaseForTags(t)

	pageID := createTaggedPage(t, treeSvc, `<img src=x onerror="alert(1)">`, "tagged-title", []string{"docs"})

	page, err := treeSvc.GetPage(pageID)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if err := tagsSvc.IndexPageContent(page.ID, page.RawContent); err != nil {
		t.Fatalf("IndexPageContent: %v", err)
	}

	out, err := uc.Execute(context.Background(), SearchInput{
		Tags:  []string{"docs"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}

	got := out.Result.Items[0].Title
	if got != "&lt;img src=x onerror=&#34;alert(1)&#34;&gt;" {
		t.Fatalf("escaped title = %q", got)
	}
}

// createIndexedPage creates a page in the tree and indexes the same content, so
// the use case can read the display text back from the tree.
func createIndexedPage(t *testing.T, uc *SearchUseCase, treeSvc *tree.TreeService, title, slug, body string) string {
	t.Helper()

	idPtr, err := treeSvc.CreateNode("system", nil, title, slug, pageKind())
	if err != nil {
		t.Fatalf("CreateNode %q: %v", slug, err)
	}
	content := body
	if err := treeSvc.UpdateNode(tree.UpdateNodeInput{UserID: "system", ID: *idPtr, Title: title, Slug: slug, Content: &content, ExpectedVersion: tree.VersionUnchecked, PreserveFrontmatter: true}); err != nil {
		t.Fatalf("UpdateNode %q: %v", slug, err)
	}
	page, err := treeSvc.GetPage(*idPtr)
	if err != nil {
		t.Fatalf("GetPage %q: %v", slug, err)
	}
	if err := uc.index.IndexPage(dto.BuildPathFromNode(page.PageNode), slug+".md", *idPtr, title, tree.NodeKindPage, page.RawContent); err != nil {
		t.Fatalf("IndexPage %q: %v", slug, err)
	}
	return *idPtr
}

func TestSearchUseCase_Execute_ShowsPageTextForChineseMatch(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "部署指南", "deploy-guide", "这是一段中文文本，说明部署流程的最后一步。")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "部署", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}

	item := out.Result.Items[0]
	if item.Title != "<b>部署</b>指南" {
		t.Fatalf("title = %q, want the page title with the match highlighted", item.Title)
	}
	if !strings.Contains(item.Excerpt, "中文文本") {
		t.Fatalf("excerpt = %q, want the page's own text", item.Excerpt)
	}
	if strings.Contains(item.Excerpt, "中文 文文 文本") {
		t.Fatalf("excerpt = %q, still shows analysed bigram tokens", item.Excerpt)
	}
	if !strings.Contains(item.Excerpt, "<b>部署</b>") {
		t.Fatalf("excerpt = %q, want the match highlighted", item.Excerpt)
	}
}

func TestSearchUseCase_Execute_EscapesTitleWhenOnlyContentMatches(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, `<img src=x onerror="alert(1)">`, "malicious", "This page contains the keyword in content only.")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "keyword", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}

	if got := out.Result.Items[0].Title; got != "&lt;img src=x onerror=&#34;alert(1)&#34;&gt;" {
		t.Fatalf("title = %q, want it escaped", got)
	}
}

func TestSearchUseCase_Execute_KeepsHighlightWhileEscapingTitle(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "<script>alert(1)</script> Search", "highlighted", "Body content without additional matches.")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "search", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}

	got := out.Result.Items[0].Title
	if strings.Contains(got, "<script>") {
		t.Fatalf("title = %q, must not contain a raw script tag", got)
	}
	if !strings.Contains(got, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("title = %q, want the tag escaped", got)
	}
	if !strings.Contains(got, "<b>Search</b>") {
		t.Fatalf("title = %q, want the match highlighted", got)
	}
}

func TestSearchUseCase_Execute_ReportsAliasMatch(t *testing.T) {
	uc, _, treeSvc := setupSearchUseCaseForTags(t)
	createIndexedPage(t, uc, treeSvc, "Apple iPhone", "phone", "---\nleafwiki_aliases:\n  - 移动电话\n  - mobile plan\n---\n\nA phone page.")

	out, err := uc.Execute(context.Background(), SearchInput{Query: "移动电话", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Result == nil || len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}
	item := out.Result.Items[0]
	if item.Matched != "alias" {
		t.Fatalf("matched = %q, want %q", item.Matched, "alias")
	}
	if item.MatchedText != "移动电话" {
		t.Fatalf("matched_text = %q, want the author's spelling", item.MatchedText)
	}

	// A hit in the title reports no alias.
	out, err = uc.Execute(context.Background(), SearchInput{Query: "iPhone", Limit: 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.Result.Items) != 1 {
		t.Fatalf("expected 1 result item, got %#v", out.Result)
	}
	if out.Result.Items[0].Matched != "" || out.Result.Items[0].MatchedText != "" {
		t.Fatalf("title hit reported %q / %q, want empty", out.Result.Items[0].Matched, out.Result.Items[0].MatchedText)
	}
}
