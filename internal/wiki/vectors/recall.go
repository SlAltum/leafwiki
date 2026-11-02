package vectors

import "strings"

// Fusion and recall constants (docs/srs/zh-search-and-retrieval.srs.md §6.5).
const (
	// RRFK is the reciprocal-rank-fusion constant.
	RRFK = 60
	// SemanticMaxConcurrency bounds how many queries may recall at once. One
	// more than that degrades to keyword search rather than waiting, which is
	// what keeps an anonymous instance's latency flat (FR-415).
	SemanticMaxConcurrency = 2
	// SemanticMinSimilarity is the cosine below which a passage is not recalled.
	// Below it, fusing adds noise rather than recall: a wiki that answers
	// "量子退火" with three unrelated pages is worse than one that answers
	// nothing.
	//
	// Measured on the real documentation (1,112 pages, 375 queries) at three
	// floors - the description queries hold at 0.92 hit@3 in all three, while
	// unanswerable queries return pages 100% of the time at 0.35, 29% at 0.50
	// and never at 0.60. This is that last one; re-measure on other content.
	SemanticMinSimilarity = 0.6
	// recallLimit caps how many pages one query recalls by meaning.
	recallLimit = 50
)

// encodeQuery uses the query side of the encoder when the model has one, so a
// retrieval model's instruction prefix is applied to queries only.
func encodeQuery(encoder Encoder, query string) ([]float32, error) {
	if queryEncoder, ok := encoder.(QueryEncoder); ok {
		return queryEncoder.EncodeQuery(query)
	}
	encoded, err := encoder.Encode([]string{query})
	if err != nil || len(encoded) == 0 {
		return nil, err
	}
	return encoded[0], nil
}

// PageHit is one page recalled by meaning, with the passage that matched.
type PageHit struct {
	PageID  string
	Anchor  string
	Section string
}

// SearchPages recalls pages for query by meaning. It returns nothing when the
// feature cannot answer: no encoder, no build, or a build made by another
// model, all of which leave keyword search to do the work (FR-414).
func (s *Service) SearchPages(query string, limit int) ([]PageHit, error) {
	query = strings.TrimSpace(query)
	if s.encoder == nil || query == "" {
		return nil, nil
	}

	// No slot means this request answers from the keyword index; waiting would
	// turn one slow query into a queue of slow queries.
	select {
	case s.searchSlots <- struct{}{}:
		defer func() { <-s.searchSlots }()
	default:
		s.log.Debug("semantic recall skipped: concurrency limit reached")
		return nil, nil
	}

	current, err := s.store.CurrentBuild()
	if err != nil {
		return nil, err
	}
	if current == nil || current.ModelID != s.modelID || current.TokenizerVersion != s.tokenizerVersion {
		return nil, nil
	}

	vector, err := encodeQuery(s.encoder, query)
	if err != nil || len(vector) == 0 {
		return nil, err
	}

	if limit <= 0 || limit > recallLimit {
		limit = recallLimit
	}
	hits, err := s.store.SearchVector(current.ID, vector, SemanticMinSimilarity, limit)
	if err != nil {
		return nil, err
	}

	pages := make([]PageHit, len(hits))
	for i, hit := range hits {
		pages[i] = PageHit{PageID: hit.PageID, Anchor: hit.Anchor, Section: hit.Heading}
	}
	return pages, nil
}
