package search

import (
	"sort"

	coresearch "github.com/perber/wiki/internal/search"
)

// RRFK is the reciprocal-rank-fusion constant
// (docs/srs/zh-search-and-retrieval.srs.md §6.5).
const RRFK = 60

// SemanticHit is one page recalled by meaning.
type SemanticHit struct {
	PageID  string
	Anchor  string
	Section string
}

// SemanticSearcher recalls pages by meaning for a query. The vectors slice
// implements it through an adapter in the composition root, because slices do
// not import each other.
type SemanticSearcher interface {
	SearchPages(query string, limit int) ([]SemanticHit, error)
}

// fuseSearch merges the two rankings by reciprocal rank fusion: a page's score
// is the sum of 1/(RRFK + rank) over the rankings it appears in. Summing
// reciprocals keeps bm25 (open scale) and cosine (bounded) comparable without
// normalizing either, and a page both sides found outranks a page only one side
// found (FR-413).
func fuseSearch(lexical []coresearch.SearchResultItem, semantic []SemanticHit) []coresearch.SearchResultItem {
	if len(semantic) == 0 {
		return lexical
	}

	scores := make(map[string]float64, len(lexical)+len(semantic))
	items := make(map[string]coresearch.SearchResultItem, len(lexical)+len(semantic))

	for rank, item := range lexical {
		scores[item.PageID] += 1 / float64(RRFK+rank+1)
		items[item.PageID] = item
	}
	for rank, hit := range semantic {
		if hit.PageID == "" {
			continue
		}
		scores[hit.PageID] += 1 / float64(RRFK+rank+1)
		if _, exists := items[hit.PageID]; !exists {
			items[hit.PageID] = coresearch.SearchResultItem{
				PageID:  hit.PageID,
				Anchor:  hit.Anchor,
				Section: hit.Section,
			}
		}
	}

	fused := make([]coresearch.SearchResultItem, 0, len(items))
	for _, item := range items {
		fused = append(fused, item)
	}
	sort.Slice(fused, func(i, j int) bool {
		left, right := scores[fused[i].PageID], scores[fused[j].PageID]
		if left == right {
			return fused[i].PageID < fused[j].PageID
		}
		return left > right
	})
	return fused
}
