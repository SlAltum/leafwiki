// Package vectors keeps the semantic index in step with the pages: it queues
// pages that changed, embeds them, and switches the vectors database to the new
// build when a batch finishes.
package vectors

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/embed"
)

// ErrBuildRunning means another build is in progress. Manual triggers answer
// 409 with this condition rather than queueing a second build (FR-411).
var ErrBuildRunning = errors.New("vectors: a build is already running")

// ErrEncoderUnavailable means no encoder is configured, which is the case for
// an untagged build or --vectors=off.
var ErrEncoderUnavailable = errors.New("vectors: no encoder configured")

// errNoPageSource means the build cannot read pages, which is a wiring bug.
var errNoPageSource = errors.New("vectors: no page source configured")

// Encoder turns passages into vectors, one vector per input text.
type Encoder interface {
	Encode(texts []string) ([][]float32, error)
}

// QueryEncoder is an encoder that treats queries differently from documents,
// which retrieval models trained with an instruction prefix require.
type QueryEncoder interface {
	EncodeQuery(query string) ([]float32, error)
}

// PageSource is what the indexer needs from the tree.
type PageSource interface {
	GetPage(id string) (*tree.Page, error)
	WalkNodes(fn func(id string) error) error
}

// Service owns the queue and the build loop.
type Service struct {
	store   *embed.Store
	encoder Encoder
	pages   PageSource
	log     *slog.Logger

	// modelID and tokenizerVersion identify what produced the vectors; a change
	// in either makes the current build stale (FR-408).
	modelID          string
	tokenizerVersion string
	mode             Mode

	// searchSlots bounds concurrent semantic recall. A request that finds no
	// slot degrades to keyword search instead of queueing, so latency stays
	// flat under load (FR-415).
	searchSlots chan struct{}

	buildMu  sync.Mutex
	building bool
}

// NewService returns a service over store. encoder may be nil, in which case
// every build answers ErrEncoderUnavailable. mode is required: a service that
// forgot it would silently never index anything.
func NewService(store *embed.Store, encoder Encoder, pages PageSource, modelID, tokenizerVersion string, mode Mode, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:            store,
		encoder:          encoder,
		pages:            pages,
		log:              log,
		modelID:          modelID,
		tokenizerVersion: tokenizerVersion,
		mode:             mode,
		searchSlots:      make(chan struct{}, SemanticMaxConcurrency),
	}
}

// EncoderAvailable reports whether this build can embed at all, so a caller can
// skip starting work that would only fail.
func (s *Service) EncoderAvailable() bool {
	return s.encoder != nil
}

// AutoIndex reports whether a save should queue the page.
func (s *Service) AutoIndex() bool {
	return s.mode.AutoIndex()
}

// Paused reports whether automatic indexing is paused. It is a flag beside the
// state enum, not a state of its own (FR-411).
func (s *Service) Paused() (bool, error) {
	value, err := s.store.Meta(embed.MetaPaused)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetPaused turns automatic indexing on or off. Queued pages stay queued.
func (s *Service) SetPaused(paused bool) error {
	return s.store.SetMeta(embed.MetaPaused, strconv.FormatBool(paused))
}

// Enqueue records that pageID needs embedding. While paused the page is not
// queued at all: the flag stops the queue from filling up, it does not stop a
// running build (FR-411).
func (s *Service) Enqueue(pageID string) error {
	paused, err := s.Paused()
	if err != nil {
		return err
	}
	if paused {
		return nil
	}

	buildID := ""
	if current, err := s.store.CurrentBuild(); err != nil {
		return err
	} else if current != nil {
		buildID = current.ID
	}
	return s.store.EnqueuePage(pageID, buildID)
}

// ReindexIfStale rebuilds the whole index when it cannot answer from a current
// build: none exists, the model or tokenizer changed, or the last build failed.
// A build that is already running or a queue that is waiting is left alone:
// those are already converging (FR-603).
func (s *Service) ReindexIfStale() (bool, error) {
	status, err := s.Status(true)
	if err != nil {
		return false, err
	}
	switch status.State {
	case StateMissing, StateStale, StateFailed:
		if _, err := s.Rebuild(nil); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

// Forget drops a deleted page's vectors and pending queue entry.
func (s *Service) Forget(pageID string) error {
	if err := s.store.DeletePageChunks(pageID); err != nil {
		return err
	}
	return s.store.RemoveFromQueue(pageID)
}

// Drain embeds the queued pages in one build and makes it current. A page that
// fails is recorded and skipped; the rest still commit (FR-407). limit <= 0
// drains everything.
func (s *Service) Drain(limit int) (int, error) {
	entries, err := s.store.QueuedPages(limit)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	pageIDs := make([]string, len(entries))
	for i, entry := range entries {
		pageIDs[i] = entry.PageID
	}
	return s.build(pageIDs, entries)
}

// Rebuild embeds the given pages, or every page when pageIDs is nil. This is
// the manual path: one build at a time, and the current index keeps answering
// until the new build commits (FR-407, FR-411).
func (s *Service) Rebuild(pageIDs []string) (int, error) {
	if pageIDs == nil {
		collected, err := s.allPageIDs()
		if err != nil {
			return 0, err
		}
		pageIDs = collected
	}
	return s.build(pageIDs, nil)
}

// IndexPage re-embeds one page in place: it writes into the build reads answer
// from instead of starting a generation, because starting one would drop every
// other page's chunks on commit. With no build at all it starts the first one.
func (s *Service) IndexPage(pageID string) error {
	if s.encoder == nil {
		return ErrEncoderUnavailable
	}

	current, err := s.store.CurrentBuild()
	if err != nil {
		return err
	}
	if current == nil {
		_, err := s.build([]string{pageID}, nil)
		return err
	}

	s.buildMu.Lock()
	if s.building {
		s.buildMu.Unlock()
		return ErrBuildRunning
	}
	s.building = true
	s.buildMu.Unlock()
	defer func() {
		s.buildMu.Lock()
		s.building = false
		s.buildMu.Unlock()
	}()

	chunks, err := s.chunksFor(pageID)
	if err != nil {
		return err
	}
	return s.store.ReplacePageChunks(current.ID, pageID, chunks)
}

// build embeds pages into a fresh generation and commits it.
func (s *Service) build(pageIDs []string, queued []embed.QueueEntry) (int, error) {
	if err := s.claimBuild(); err != nil {
		return 0, err
	}
	defer s.releaseBuild()
	return s.buildClaimed(pageIDs, queued)
}

// claimBuild takes the one build slot, or reports that it is taken.
func (s *Service) claimBuild() error {
	if s.encoder == nil {
		return ErrEncoderUnavailable
	}
	if s.pages == nil {
		return errNoPageSource
	}

	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.building {
		return ErrBuildRunning
	}
	s.building = true
	return nil
}

func (s *Service) releaseBuild() {
	s.buildMu.Lock()
	s.building = false
	s.buildMu.Unlock()
}

// StartRebuild claims the build slot and rebuilds every page in the background,
// so an admin request can answer 202 and poll the status (FR-411).
func (s *Service) StartRebuild() error {
	if err := s.claimBuild(); err != nil {
		return err
	}
	go func() {
		defer s.releaseBuild()
		pageIDs, err := s.allPageIDs()
		if err != nil {
			s.recordBuildFailure(err)
			s.log.Warn("semantic rebuild failed", "error", err)
			return
		}
		if _, err := s.buildClaimed(pageIDs, nil); err != nil {
			s.log.Warn("semantic rebuild failed", "error", err)
		}
	}()
	return nil
}

// allPageIDs collects every page the source can offer.
func (s *Service) allPageIDs() ([]string, error) {
	if s.pages == nil {
		return nil, errNoPageSource
	}
	var pageIDs []string
	if err := s.pages.WalkNodes(func(id string) error {
		pageIDs = append(pageIDs, id)
		return nil
	}); err != nil {
		return nil, err
	}
	return pageIDs, nil
}

// ClearAndRebuild throws the index away and builds it again. It also clears a
// stale running marker, which is what an operator uses when a crashed process
// left the state claiming to be running (FR-411).
func (s *Service) ClearAndRebuild() error {
	if err := s.store.Reset(); err != nil {
		return err
	}
	return s.StartRebuild()
}

// buildClaimed is the build itself; the caller holds the build slot.
func (s *Service) buildClaimed(pageIDs []string, queued []embed.QueueEntry) (int, error) {

	buildID, err := s.store.BeginBuild()
	if err != nil {
		return 0, err
	}

	written := 0
	for _, pageID := range pageIDs {
		chunks, err := s.chunksFor(pageID)
		if err != nil {
			s.log.Warn("could not index a page for semantic search", "pageID", pageID, "error", err)
			if qErr := s.store.MarkQueuedFailure(pageID, err.Error()); qErr != nil {
				s.log.Warn("could not record the queue failure", "pageID", pageID, "error", qErr)
			}
			continue
		}
		if err := s.store.PutChunks(buildID, chunks); err != nil {
			s.recordBuildFailure(err)
			return written, err
		}
		written += len(chunks)
	}

	// A build that wrote nothing would still switch the pointer and drop the
	// current generation, so keep the old one. This is also the guard that
	// catches a page list collected wrongly: an empty rebuild must never look
	// like a successful one.
	if written == 0 {
		failure := fmt.Errorf("no page could be embedded")
		s.recordBuildFailure(failure)
		if err := s.store.DropBuild(buildID); err != nil {
			return 0, err
		}
		// Reporting success here would tell a manual rebuild that it worked
		// when nothing was written.
		return 0, failure
	}
	if err := s.store.CommitBuild(buildID, s.modelID, s.tokenizerVersion); err != nil {
		s.recordBuildFailure(err)
		return written, err
	}
	for _, entry := range queued {
		if err := s.store.RemoveFromQueue(entry.PageID); err != nil {
			s.log.Warn("could not clear an indexed page from the queue", "pageID", entry.PageID, "error", err)
		}
	}
	return written, nil
}

// recordBuildFailure keeps the reason a build did not finish and clears the
// running marker, so the status reports StateFailed instead of claiming a build
// that is not running (FR-701).
func (s *Service) recordBuildFailure(err error) {
	if setErr := s.store.SetMeta(embed.MetaLastError, err.Error()); setErr != nil {
		s.log.Warn("could not record the build failure", "error", setErr)
	}
	if clearErr := s.store.DeleteMeta(embed.MetaRunningSince); clearErr != nil {
		s.log.Warn("could not clear the running marker", "error", clearErr)
	}
}

// chunksFor reads a page and cuts it into the chunks to embed.
func (s *Service) chunksFor(pageID string) ([]embed.Chunk, error) {
	page, err := s.pages.GetPage(pageID)
	if err != nil {
		return nil, err
	}

	passages := embed.SplitPage(page.Content)
	if len(passages) == 0 {
		return nil, nil
	}

	texts := make([]string, len(passages))
	for i, passage := range passages {
		texts[i] = passage.Text
	}
	vectors, err := s.encoder.Encode(texts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(passages) {
		return nil, fmt.Errorf("vectors: encoder returned %d vectors for %d passages", len(vectors), len(passages))
	}

	chunks := make([]embed.Chunk, len(passages))
	for i, passage := range passages {
		chunks[i] = embed.Chunk{
			PageID:      pageID,
			ChunkNo:     i,
			Heading:     passage.Heading,
			HeadingPath: passage.HeadingPath,
			Anchor:      passage.Anchor,
			ContentHash: contentHash(passage.Text),
			Vec:         vectors[i],
		}
	}
	return chunks, nil
}

// contentHash identifies the text a chunk was built from, so a later start can
// tell whether the Markdown changed under the index (FR-409).
func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
