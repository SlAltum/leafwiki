package vectors

import (
	"errors"
	"fmt"
	"time"

	"github.com/perber/wiki/internal/embed"
)

// PageState is what one page's vectorisation looks like on its own, so a single
// page can be tracked and rebuilt without touching the rest of the index.
type PageState string

const (
	// PageIndexed means the page's current content is in the index.
	PageIndexed PageState = "indexed"
	// PageStale means the page is in the index, but the index holds an older
	// version of its content.
	PageStale PageState = "stale"
	// PageQueued means the page is waiting to be embedded.
	PageQueued PageState = "queued"
	// PageFailed means embedding the page failed and nothing retries it.
	PageFailed PageState = "failed"
	// PageMissing means the page is not in the index at all.
	PageMissing PageState = "missing"
)

// PageStatus is the per-page answer the page view asks for.
type PageStatus struct {
	PageID    string     `json:"page_id"`
	State     PageState  `json:"state"`
	Passages  int        `json:"passages"`
	LastError string     `json:"last_error,omitempty"`
	QueuedAt  *time.Time `json:"queued_at,omitempty"`
}

// errPageUnreadable marks a page the source cannot read any more.
var errPageUnreadable = errors.New("vectors: page is not readable")

// PageStatus reports one page's state. It never reports a page as indexed
// without comparing the stored passages against the current content, because
// "in the index" and "up to date" are different promises.
func (s *Service) PageStatus(pageID string) (PageStatus, error) {
	status := PageStatus{PageID: pageID, State: PageMissing}
	if s.encoder == nil {
		return PageStatus{}, ErrEncoderUnavailable
	}

	entry, err := s.store.QueueEntryFor(pageID)
	if err != nil {
		return PageStatus{}, err
	}
	if entry != nil {
		queuedAt := entry.QueuedAt
		status.QueuedAt = &queuedAt
		status.LastError = entry.LastError
		if entry.State == embed.QueueStatePending {
			status.State = PageQueued
			return status, nil
		}
		status.State = PageFailed
		return status, nil
	}

	current, err := s.store.CurrentBuild()
	if err != nil {
		return PageStatus{}, err
	}
	if current == nil {
		return status, nil
	}

	stored, err := s.store.PageChunks(current.ID, pageID)
	if err != nil {
		return PageStatus{}, err
	}
	status.Passages = len(stored)
	if len(stored) == 0 {
		return status, nil
	}

	currentHashes, err := s.currentHashes(pageID)
	if err != nil {
		if errors.Is(err, errPageUnreadable) {
			return status, nil
		}
		return PageStatus{}, err
	}

	if sameHashes(stored, currentHashes) {
		status.State = PageIndexed
		return status, nil
	}
	status.State = PageStale
	return status, nil
}

// currentHashes hashes the passages the page has right now, in the order they
// would be written to the index.
func (s *Service) currentHashes(pageID string) ([]string, error) {
	if s.pages == nil {
		return nil, errNoPageSource
	}
	page, err := s.pages.GetPage(pageID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPageUnreadable, err)
	}
	passages := embed.SplitPage(page.Content)
	hashes := make([]string, len(passages))
	for i, passage := range passages {
		hashes[i] = contentHash(passage.Text)
	}
	return hashes, nil
}

// sameHashes reports whether the stored passages still match the page, by
// number and by content: a section added, removed or edited all change it.
func sameHashes(stored []embed.PageChunk, current []string) bool {
	if len(stored) != len(current) {
		return false
	}
	for i, chunk := range stored {
		if chunk.ChunkNo != i || chunk.ContentHash != current[i] {
			return false
		}
	}
	return true
}
