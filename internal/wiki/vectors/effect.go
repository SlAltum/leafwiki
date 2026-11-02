package vectors

import (
	"log/slog"

	"github.com/perber/wiki/internal/wiki/pagesave"
)

// SideEffect feeds page mutations into the vector queue. The composition root
// registers it only when semantic search is enabled, so an instance with the
// feature off never writes a queue entry or a vectors database (FR-401, FR-410).
type SideEffect struct {
	service *Service
	log     *slog.Logger
}

// NewSideEffect returns the side effect for service.
func NewSideEffect(service *Service, log *slog.Logger) *SideEffect {
	if log == nil {
		log = slog.Default()
	}
	return &SideEffect{service: service, log: log}
}

// Name implements pagesave's named side effect interface, which the
// orchestrator uses for the metrics label.
func (e *SideEffect) Name() string {
	return "vectors"
}

// Apply implements pagesave.PageSideEffect.
func (e *SideEffect) Apply(event pagesave.PageSaveEvent) {
	if e.service == nil {
		return
	}

	// manual mode only builds when an operator asks for it.
	if !e.service.AutoIndex() {
		return
	}

	switch event.Operation {
	case pagesave.PageOperationCreate, pagesave.PageOperationUpdate, pagesave.PageOperationRestore:
		if event.After != nil {
			e.enqueue(event.After.ID, event.Operation)
		}

	case pagesave.PageOperationMove:
		// Every page in the subtree keeps its text but changes its path, which
		// is part of what the anchor and the heading path describe.
		for _, page := range event.AffectedPages {
			e.enqueue(page.ID, event.Operation)
		}

	case pagesave.PageOperationDelete:
		for _, page := range event.AffectedPages {
			if err := e.service.Forget(page.ID); err != nil {
				e.log.Warn("could not drop a deleted page from the semantic index", "pageID", page.ID, "error", err)
			}
		}
	}
}

// enqueue hands one page to the queue; a queue error must not fail the save.
func (e *SideEffect) enqueue(pageID string, operation pagesave.PageOperationType) {
	if pageID == "" {
		return
	}
	if err := e.service.Enqueue(pageID); err != nil {
		e.log.Warn("could not queue a page for semantic search",
			"pageID", pageID, "operation", string(operation), "error", err)
	}
}
