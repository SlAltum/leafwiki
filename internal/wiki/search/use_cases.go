package search

import (
	"context"
	"sort"
	"strings"

	"github.com/perber/wiki/internal/core/excerpt"
	"github.com/perber/wiki/internal/core/markdown"
	sharederrors "github.com/perber/wiki/internal/core/shared/errors"
	"github.com/perber/wiki/internal/core/shared/htmlutil"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/http/dto"
	coresearch "github.com/perber/wiki/internal/search"
	coretags "github.com/perber/wiki/internal/tags"
)

var ErrSearchUnavailable = sharederrors.NewLocalizedError(
	ErrCodeSearchUnavailable,
	"Search is currently unavailable",
	"search is currently unavailable",
	nil,
)

// ─── SearchUseCase ───────────────────────────────────────────────────────────

type SearchInput struct {
	Query  string
	Tags   []string
	Offset int
	Limit  int
}

type SearchOutput struct {
	Result *coresearch.SearchResult
}

// SemanticState is the read endpoint's coarse view of the semantic index
// (docs/srs/zh-search-and-retrieval.srs.md §6.1). The fine-grained enum lives
// in the vectors slice; this is what a reader is told.
type SemanticState string

const (
	// SemanticOff means semantic search is switched off.
	SemanticOff SemanticState = "off"
	// SemanticOK means the index answered from a build it considers current.
	SemanticOK SemanticState = "ok"
	// SemanticStale means the index is behind the content.
	SemanticStale SemanticState = "stale"
	// SemanticUnavailable means the feature is on but cannot answer.
	SemanticUnavailable SemanticState = "unavailable"
)

// SemanticReporter reports the coarse semantic state. The vectors slice
// implements it through an adapter in the composition root, because slices do
// not import each other.
type SemanticReporter interface {
	SemanticState() SemanticState
}

type SearchUseCase struct {
	index    *coresearch.SQLiteIndex
	tags     *coretags.TagsService
	tree     *tree.TreeService
	semantic SemanticReporter
	searcher SemanticSearcher
}

// fusionWindow is how deep each ranking is read before fusing. A page's fused
// rank depends only on positions, so a fixed window bounds the work per query.
const fusionWindow = 200

func NewSearchUseCase(idx *coresearch.SQLiteIndex, tags *coretags.TagsService, tree *tree.TreeService, semantic SemanticReporter) *SearchUseCase {
	return &SearchUseCase{index: idx, tags: tags, tree: tree, semantic: semantic}
}

// WithSemanticSearcher enables semantic recall and fusion. Without one the use
// case answers from the keyword index alone.
func (uc *SearchUseCase) WithSemanticSearcher(searcher SemanticSearcher) *SearchUseCase {
	uc.searcher = searcher
	return uc
}

// semanticState reports what the response says about semantic search. A
// missing reporter means the feature is off, which is also the state of an
// instance that never configured it.
func (uc *SearchUseCase) semanticState() SemanticState {
	if uc.semantic == nil {
		return SemanticOff
	}
	return uc.semantic.SemanticState()
}

func (uc *SearchUseCase) Execute(_ context.Context, in SearchInput) (*SearchOutput, error) {
	if uc.index == nil {
		return nil, ErrSearchUnavailable
	}

	var pageIDs []string
	if len(in.Tags) > 0 {
		pageIDs = []string{}
		if uc.tags != nil {
			var err error
			pageIDs, err = uc.tags.GetPageIDsByTags(normalizeTags(in.Tags))
			if err != nil {
				return nil, err
			}
		}
	}

	if strings.TrimSpace(in.Query) == "" && len(pageIDs) > 0 {
		return uc.searchByTags(pageIDs, in.Offset, in.Limit)
	}

	// Semantic recall widens the candidate set before pagination; when it finds
	// nothing the untouched lexical path answers, with its exact count.
	fused, ok := uc.fusedResult(in, pageIDs)
	if ok {
		uc.decorateFromTree(fused.Items, in.Query)
		uc.attachTags(fused.Items)
		pageIDs := make([]string, len(fused.Items))
		for i, item := range fused.Items {
			pageIDs[i] = item.PageID
		}
		fused.TagFacets = uc.buildTagFacets(pageIDs)
		return &SearchOutput{Result: fused}, nil
	}

	result, err := uc.index.Search(in.Query, pageIDs, in.Offset, in.Limit)
	if err != nil {
		return nil, err
	}
	// Lexical results are always what the reader gets; the field only says
	// whether semantic recall took part (FR-414).
	result.Semantic = string(uc.semanticState())

	uc.decorateFromTree(result.Items, in.Query)

	fullMatchPageIDs, err := uc.index.SearchPageIDs(in.Query, pageIDs)
	if err != nil {
		return nil, err
	}

	uc.attachTags(result.Items)
	result.TagFacets = uc.buildTagFacets(fullMatchPageIDs)
	return &SearchOutput{Result: result}, nil
}

// fusedResult runs the keyword and the semantic ranking, fuses them and pages
// the result. It reports false when semantic recall contributed nothing, so the
// caller keeps the plain keyword path.
func (uc *SearchUseCase) fusedResult(in SearchInput, pageIDs []string) (*coresearch.SearchResult, bool) {
	if uc.searcher == nil || strings.TrimSpace(in.Query) == "" {
		return nil, false
	}

	semanticHits, err := uc.searcher.SearchPages(in.Query, fusionWindow)
	if err != nil {
		// A failed recall falls back to the keyword result; the semantic field
		// already reports what the index thinks of itself (FR-414).
		return nil, false
	}
	if len(semanticHits) == 0 {
		return nil, false
	}

	// Read deep enough that the requested page is reachable: fusion reorders,
	// it must not make a later page unreachable.
	window := fusionWindow
	if in.Offset+in.Limit > window {
		window = in.Offset + in.Limit
	}
	lexical, err := uc.index.Search(in.Query, pageIDs, 0, window)
	if err != nil {
		return nil, false
	}

	fused := fuseSearch(lexical.Items, semanticHits)

	offset, limit := in.Offset, in.Limit
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	// The count a client paginates by. Semantic recall can only add pages to
	// the keyword result, never remove one, so the union is at least the
	// keyword total - reporting the candidate window instead made "found 225"
	// out of a query that matches 300 pages.
	count := lexical.Count
	if len(fused) > count {
		count = len(fused)
	}
	if offset > count {
		offset = count
	}
	end := offset + limit
	if end > count {
		end = count
	}

	return &coresearch.SearchResult{
		Count:     count,
		Items:     fused[offset:end],
		Offset:    offset,
		Limit:     limit,
		TagFacets: []coresearch.SearchTagFacet{},
		Semantic:  string(uc.semanticState()),
	}, true
}

func (uc *SearchUseCase) searchByTags(pageIDs []string, offset, limit int) (*SearchOutput, error) {
	if uc.tags == nil || uc.tree == nil {
		return &SearchOutput{
			Result: &coresearch.SearchResult{
				Count:     0,
				Items:     []coresearch.SearchResultItem{},
				Offset:    offset,
				Limit:     limit,
				TagFacets: []coresearch.SearchTagFacet{},
				Semantic:  string(uc.semanticState()),
			},
		}, nil
	}

	items := make([]coresearch.SearchResultItem, 0, len(pageIDs))
	for _, pageID := range pageIDs {
		node, err := uc.tree.FindPageByID(pageID)
		if err != nil || node == nil {
			continue
		}

		items = append(items, coresearch.SearchResultItem{
			PageID: node.ID,
			Title:  htmlutil.EscapeText(node.Title),
			Path:   dto.BuildPathFromNode(node),
			Kind:   string(node.Kind),
			Rank:   1,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Title == items[j].Title {
			return items[i].Path < items[j].Path
		}
		return items[i].Title < items[j].Title
	})

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}

	count := len(items)
	if offset > count {
		offset = count
	}
	end := offset + limit
	if end > count {
		end = count
	}
	pagedItems := items[offset:end]
	displayedIDs := make([]string, len(pagedItems))
	for i, item := range pagedItems {
		displayedIDs[i] = item.PageID
	}
	excerpts, err := uc.tags.GetExcerptsForPages(displayedIDs)
	if err != nil {
		return nil, err
	}
	for i := range pagedItems {
		pagedItems[i].Excerpt = excerpts[pagedItems[i].PageID]
	}
	uc.attachTags(pagedItems)

	return &SearchOutput{
		Result: &coresearch.SearchResult{
			Count:     count,
			Items:     pagedItems,
			Offset:    offset,
			Limit:     limit,
			TagFacets: uc.buildTagFacets(pageIDs),
			Semantic:  string(uc.semanticState()),
		},
	}, nil
}

// decorateFromTree fills title, path, kind and excerpt from the tree service.
// The index stores analysed text (bigram tokens for CJK), which is right for
// matching and wrong for reading, so the displayed strings and the highlight
// around the typed words come from the page itself (FR-107).
func (uc *SearchUseCase) decorateFromTree(items []coresearch.SearchResultItem, query string) {
	if uc.tree == nil || len(items) == 0 {
		return
	}

	pageIDs := make([]string, len(items))
	for i, item := range items {
		pageIDs[i] = item.PageID
	}
	pages, _ := uc.tree.GetPages(pageIDs)
	terms := coresearch.QueryTerms(query)

	for i := range items {
		page := pages[i]
		if page == nil || page.PageNode == nil {
			// The page vanished between matching and display; the index
			// values stay as they are.
			continue
		}
		items[i].Title = highlightedTitle(page.Title, terms)
		items[i].Path = dto.BuildPathFromNode(page.PageNode)
		items[i].Kind = string(page.Kind)
		items[i].Excerpt = excerpt.HighlightedExcerpt(page.Content, terms)

		if anchor, section := matchSection(page.Content, terms); anchor != "" {
			items[i].Anchor = anchor
			items[i].Section = section
		}

		if alias, ok := aliasMatch(page.RawContent, terms); ok {
			items[i].Matched = "alias"
			items[i].MatchedText = alias
		}
	}
}

// matchSection returns the anchor and title of the first section whose text
// contains one of the query terms, so a hit can be linked to the section it came
// from. A hit in the page's introduction has no section: the reader would land
// on the top of the page anyway (FR-501, FR-506).
func matchSection(body string, terms []string) (anchor, title string) {
	sections := markdown.Sections(body)
	if len(sections) == 0 || len(terms) == 0 {
		return "", ""
	}

	lines := strings.Split(body, "\n")
	for i, section := range sections {
		start := section.Line - 1
		if start < 0 || start >= len(lines) {
			continue
		}
		end := len(lines)
		if i+1 < len(sections) {
			end = sections[i+1].Line - 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		if start >= end {
			continue
		}
		if excerpt.ContainsTerm(strings.Join(lines[start:end], "\n"), terms) {
			return section.Anchor, htmlutil.EscapeText(section.Title)
		}
	}
	return "", ""
}

// aliasMatch returns the alias declared in raw that contains one of the query
// terms. The aliases column holds analysed text, so the author's own spelling
// is read back here (FR-203).
func aliasMatch(raw string, terms []string) (string, bool) {
	fm, _, _, err := markdown.ParseFrontmatter(raw)
	if err != nil {
		return "", false
	}
	for _, alias := range fm.LeafWikiAliases {
		folded := strings.ToLower(alias)
		for _, term := range terms {
			if term != "" && strings.Contains(folded, strings.ToLower(term)) {
				return alias, true
			}
		}
	}
	return "", false
}

// highlightedTitle marks the query words in a page title and escapes the rest,
// so a title containing markup stays text.
func highlightedTitle(title string, terms []string) string {
	return htmlutil.EscapeTextWithAllowedMarkers(
		excerpt.HighlightTerms(title, terms, "", ""),
		htmlutil.AllowedMarker{Marker: "", HTML: "<b>"},
		htmlutil.AllowedMarker{Marker: "", HTML: "</b>"},
	)
}

func (uc *SearchUseCase) attachTags(items []coresearch.SearchResultItem) {
	if uc.tags == nil || len(items) == 0 {
		return
	}

	pageIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.PageID != "" {
			pageIDs = append(pageIDs, item.PageID)
		}
	}
	if len(pageIDs) == 0 {
		return
	}

	tagsByPage, err := uc.tags.GetTagsForPages(pageIDs)
	if err != nil {
		return
	}

	for i := range items {
		if tags, ok := tagsByPage[items[i].PageID]; ok {
			items[i].Tags = tags
		} else {
			items[i].Tags = []string{}
		}
	}
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func (uc *SearchUseCase) buildTagFacets(pageIDs []string) []coresearch.SearchTagFacet {
	if uc.tags == nil || len(pageIDs) == 0 {
		return []coresearch.SearchTagFacet{}
	}

	tagsByPage, err := uc.tags.GetTagsForPages(pageIDs)
	if err != nil {
		return []coresearch.SearchTagFacet{}
	}

	counts := make(map[string]int)
	for _, pageID := range pageIDs {
		for _, tag := range tagsByPage[pageID] {
			counts[tag]++
		}
	}

	facets := make([]coresearch.SearchTagFacet, 0, len(counts))
	for tag, count := range counts {
		facets = append(facets, coresearch.SearchTagFacet{
			Tag:   tag,
			Count: count,
		})
	}

	sort.Slice(facets, func(i, j int) bool {
		if facets[i].Count == facets[j].Count {
			return facets[i].Tag < facets[j].Tag
		}
		return facets[i].Count > facets[j].Count
	})

	return facets
}

// ─── GetIndexingStatusUseCase ────────────────────────────────────────────────

type GetIndexingStatusOutput struct {
	Status *coresearch.IndexingStatus
}

type GetIndexingStatusUseCase struct {
	status *coresearch.IndexingStatus
}

func NewGetIndexingStatusUseCase(s *coresearch.IndexingStatus) *GetIndexingStatusUseCase {
	return &GetIndexingStatusUseCase{status: s}
}

func (uc *GetIndexingStatusUseCase) Execute(_ context.Context) *GetIndexingStatusOutput {
	if uc.status == nil {
		return &GetIndexingStatusOutput{Status: nil}
	}
	return &GetIndexingStatusOutput{Status: uc.status.Snapshot()}
}
