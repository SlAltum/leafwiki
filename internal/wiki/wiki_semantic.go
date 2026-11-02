package wiki

import (
	wikisearch "github.com/perber/wiki/internal/wiki/search"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// semanticBridge maps the vector service onto the two interfaces the search
// slice defines: the coarse state a response carries, and the recall it may use.
// It lives in the composition root because the search and vectors slices do not
// import each other.
type semanticBridge struct {
	service *vectors.Service
}

// SemanticState implements the search slice's reporter.
func (a semanticBridge) SemanticState() wikisearch.SemanticState {
	if a.service == nil {
		return wikisearch.SemanticOff
	}
	status, err := a.service.Status(true)
	if err != nil {
		return wikisearch.SemanticUnavailable
	}
	switch status.State {
	case vectors.StateFresh:
		return wikisearch.SemanticOK
	case vectors.StateQueued, vectors.StateRunning:
		// The current build still answers; it is simply catching up.
		return wikisearch.SemanticOK
	case vectors.StateStale:
		return wikisearch.SemanticStale
	default: // off, missing, failed
		return wikisearch.SemanticUnavailable
	}
}

// SearchPages implements the search slice's recall port.
func (a semanticBridge) SearchPages(query string, limit int) ([]wikisearch.SemanticHit, error) {
	if a.service == nil {
		return nil, nil
	}
	hits, err := a.service.SearchPages(query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]wikisearch.SemanticHit, len(hits))
	for i, hit := range hits {
		out[i] = wikisearch.SemanticHit{PageID: hit.PageID, Anchor: hit.Anchor, Section: hit.Section}
	}
	return out, nil
}
