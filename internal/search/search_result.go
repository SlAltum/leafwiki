package search

type SearchResult struct {
	Limit     int                `json:"limit"`
	Offset    int                `json:"offset"`
	Count     int                `json:"count"`
	Items     []SearchResultItem `json:"items"`
	TagFacets []SearchTagFacet   `json:"tag_facets"`

	// Suggestion is the query with the terms the corpus does not contain
	// replaced by their closest indexed spelling, empty when nothing was
	// corrected. It tells the reader which words the results are actually for
	// (FR-304).
	Suggestion string `json:"suggestion,omitempty"`

	// Semantic reports whether semantic recall took part: off, ok, stale or
	// unavailable. Always present, so a client can tell "off" from "not
	// reported" (FR-414).
	Semantic string `json:"semantic"`
}

// SearchResultItem is one hit. PageID, Path, Kind and Rank come from the
// index; Title and Excerpt are filled by the use case from the tree service,
// because the index keeps analysed text (CJK bigram tokens) that is right for
// matching and wrong for reading.
type SearchResultItem struct {
	PageID  string   `json:"page_id"`
	Title   string   `json:"title"`
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	Rank    float64  `json:"rank"`
	Excerpt string   `json:"excerpt"`
	Tags    []string `json:"tags,omitempty"`

	// Anchor is the id of the section the hit sits in, Section its heading
	// text; both empty when the hit is in the page's introduction, and both
	// omitempty on the wire.
	Anchor  string `json:"anchor,omitempty"`
	Section string `json:"section,omitempty"`

	// Matched names what produced the hit when it is not the page's title or
	// body: "alias" for a declared alias, empty otherwise. MatchedText is
	// that alias exactly as the author wrote it, since the indexed column
	// holds analysed text (FR-203).
	Matched     string `json:"matched,omitempty"`
	MatchedText string `json:"matched_text,omitempty"`
}

type SearchTagFacet struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}
