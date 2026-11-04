package vectors

import (
	"fmt"

	"github.com/perber/wiki/internal/embed"
)

// PageDiff is one page's comparison result, with no encoder involved.
type PageDiff struct {
	PageID string
	State  PageState
}

// SyncResult reports what one in-place batch did.
type SyncResult struct {
	// Pages is how many pages the batch embedded in place.
	Pages int
	// Chunks is how many passages those pages wrote.
	Chunks int
	// Failed lists the pages the batch could not read or embed.
	Failed []string
}

// SyncPages re-embeds the given pages in place, under one build slot for the
// whole batch (FR-165, FR-173b). It never starts a generation: the pages it was
// not given keep their chunks.
func (s *Service) SyncPages(pageIDs []string) (SyncResult, error) {
	result := SyncResult{}
	if len(pageIDs) == 0 {
		return result, nil
	}
	if err := s.claimBuild(); err != nil {
		return result, err
	}
	defer s.releaseBuild()

	current, err := s.store.CurrentBuild()
	if err != nil {
		return result, err
	}
	// A batch may only open the first build when it covers every page: a
	// generation holding some of the pages would claim to be the whole index
	// (FR-164). Another model's build cannot take our vectors either, so that
	// case rebuilds too (FR-408).
	if current == nil || current.ModelID != s.modelID || current.TokenizerVersion != s.tokenizerVersion {
		return s.rebuildAll(pageIDs)
	}

	for _, pageID := range pageIDs {
		chunks, err := s.chunksFor(pageID)
		if err != nil {
			s.log.Warn("could not sync a page for semantic search", "pageID", pageID, "error", err)
			result.Failed = append(result.Failed, pageID)
			continue
		}
		if err := s.store.ReplacePageChunks(current.ID, pageID, chunks); err != nil {
			return result, err
		}
		// The page is current again, so a row that recorded an earlier failure
		// is no longer true.
		if err := s.store.ClearQueueFailure(pageID); err != nil {
			s.log.Warn("could not clear a stale queue failure", "pageID", pageID, "error", err)
		}
		result.Pages++
		result.Chunks += len(chunks)
	}

	// The index matches the Markdown again, so a failure marker from an earlier
	// build no longer describes it.
	if len(result.Failed) == 0 {
		if err := s.store.DeleteMeta(embed.MetaLastError); err != nil {
			s.log.Warn("could not clear the recorded build failure", "error", err)
		}
	}
	return result, nil
}

// rebuildAll builds the whole index and reports it the way the in-place path
// does: what landed, and which pages could not be embedded.
func (s *Service) rebuildAll(pageIDs []string) (SyncResult, error) {
	result := SyncResult{}
	all, err := s.allPageIDs()
	if err != nil {
		return result, err
	}
	written, err := s.buildClaimed(all, nil)
	if err != nil {
		return result, err
	}
	result.Chunks = written

	// buildClaimed skips what it cannot read, so the report has to be measured
	// rather than assumed from the list that went in.
	for _, pageID := range pageIDs {
		status, err := s.PageDiffStatus(pageID)
		if err != nil {
			return result, err
		}
		if status.State == PageIndexed {
			result.Pages++
			continue
		}
		result.Failed = append(result.Failed, pageID)
	}
	return result, nil
}

// DiffPages compares every page on disk against the current build without an
// encoder, so an offline run can decide what to re-embed (FR-165b). Without a
// build every page reports off: there is nothing to compare against.
func (s *Service) DiffPages() ([]PageDiff, error) {
	if s.pages == nil {
		return nil, errNoPageSource
	}
	current, err := s.store.CurrentBuild()
	if err != nil {
		return nil, err
	}
	if current == nil {
		// Nothing is embedded, so every page is off: there is nothing to
		// compare against (FR-165b).
		var off []PageDiff
		if err := s.pages.WalkNodes(func(pageID string) error {
			off = append(off, PageDiff{PageID: pageID, State: PageStateOff})
			return nil
		}); err != nil {
			return nil, err
		}
		return off, nil
	}

	var diffs []PageDiff
	if err := s.pages.WalkNodes(func(pageID string) error {
		status, err := s.PageDiffStatus(pageID)
		if err != nil {
			return fmt.Errorf("compare page %s: %w", pageID, err)
		}
		diffs = append(diffs, PageDiff{PageID: pageID, State: status.State})
		return nil
	}); err != nil {
		return nil, err
	}
	return diffs, nil
}

// Orphans returns the pages a build holds that the page source no longer
// offers, so an offline run can drop their chunks (FR-165).
func (s *Service) Orphans(buildID string) ([]string, error) {
	if s.pages == nil {
		return nil, errNoPageSource
	}
	indexed, err := s.store.PageIDs(buildID)
	if err != nil {
		return nil, err
	}
	live := map[string]struct{}{}
	if err := s.pages.WalkNodes(func(pageID string) error {
		live[pageID] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	var orphans []string
	for _, pageID := range indexed {
		if _, ok := live[pageID]; !ok {
			orphans = append(orphans, pageID)
		}
	}
	return orphans, nil
}
