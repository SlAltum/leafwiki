package launcher

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/perber/wiki/internal/core/ignore"
)

// FileSignature is one markdown file's change fingerprint.
type FileSignature struct {
	Size    int64
	ModTime time.Time
}

// Snapshot is the fingerprint of every page file under the content root.
type Snapshot map[string]FileSignature

// TakeSnapshot walks root for markdown pages, applying the same layered ignore
// rules the tree service uses (FR-406): one ignore.Cache over root, asked per
// directory, with paths relative to root.
func TakeSnapshot(root string) (Snapshot, error) {
	cache := ignore.NewCache(root)
	snapshot := Snapshot{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		if entry.IsDir() {
			if relPath != "." && cache.Get(path).Matches(filepath.ToSlash(relPath), true) {
				return fs.SkipDir
			}
			return nil
		}

		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		if cache.Get(filepath.Dir(path)).Matches(filepath.ToSlash(relPath), false) {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		snapshot[relPath] = FileSignature{Size: info.Size(), ModTime: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Changed reports whether current differs from previous: a page was added,
// removed, or edited (FR-405).
func Changed(previous, current Snapshot) bool {
	if len(previous) != len(current) {
		return true
	}
	for path, signature := range current {
		before, ok := previous[path]
		if !ok || before.Size != signature.Size || !before.ModTime.Equal(signature.ModTime) {
			return true
		}
	}
	return false
}

// RefreshState is what the window shows for the content refresh (FR-402).
type RefreshState int

const (
	// RefreshIdle means nothing has been triggered since the last finish.
	RefreshIdle RefreshState = iota
	// RefreshRunning means the reload is still going.
	RefreshRunning
	// RefreshDone means the last refresh succeeded.
	RefreshDone
	// RefreshFailed means it finished with an error worth showing.
	RefreshFailed
)

// RefreshStatus is the tracker's snapshot for the view.
type RefreshStatus struct {
	State  RefreshState
	DoneAt time.Time
	Error  string
}

// TrackerOptions are the tracker's seams. Interval and Limit default to SRS
// §4.4's 100 ms and 10 s.
type TrackerOptions struct {
	// Trigger starts one background reload; false means one is already running.
	Trigger  func() bool
	Status   func() ResyncStatus
	Interval time.Duration
	Limit    time.Duration
	Now      func() time.Time
}

// Tracker follows one content refresh at a time. The poll is bounded: it runs
// only while a refresh is in flight and stops at Limit, so the process stays
// idle afterwards (C-8, FR-403).
type Tracker struct {
	opts TrackerOptions

	mu     sync.Mutex
	status RefreshStatus
}

// NewTracker builds a tracker; the caller keeps it for the process lifetime.
func NewTracker(opts TrackerOptions) *Tracker {
	if opts.Interval <= 0 {
		opts.Interval = 100 * time.Millisecond
	}
	if opts.Limit <= 0 {
		opts.Limit = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Tracker{opts: opts}
}

// Status reports the current refresh state.
func (t *Tracker) Status() RefreshStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// Start triggers a refresh and follows it in the background. It returns the
// state right after triggering, so the view can show "still loading" at once.
func (t *Tracker) Start() RefreshStatus {
	if t.opts.Status().Running {
		// A reload is already in flight: join its result instead of starting a
		// second one (the job itself would refuse anyway).
		t.setStatus(RefreshStatus{State: RefreshRunning})
		go t.follow(t.opts.Limit)
		return t.Status()
	}

	if !t.opts.Trigger() {
		t.setStatus(RefreshStatus{State: RefreshRunning})
		go t.follow(t.opts.Limit)
		return t.Status()
	}

	t.setStatus(RefreshStatus{State: RefreshRunning})
	go t.follow(t.opts.Limit)
	return t.Status()
}

// Recheck reads the job once, without polling. It is how a refresh that
// outlived the poll window is picked up again on the next user event (FR-403).
func (t *Tracker) Recheck() RefreshStatus {
	return t.apply(t.opts.Status())
}

func (t *Tracker) follow(limit time.Duration) {
	ticker := time.NewTicker(t.opts.Interval)
	defer ticker.Stop()

	deadline := time.NewTimer(limit)
	defer deadline.Stop()

	for {
		select {
		case <-ticker.C:
			status := t.opts.Status()
			if status.Done {
				t.apply(status)
				return
			}
		case <-deadline.C:
			// Long refresh: keep showing "still loading", stop polling, and let
			// the next user event re-check (FR-403).
			return
		}
	}
}

func (t *Tracker) apply(status ResyncStatus) RefreshStatus {
	switch {
	case status.Done && status.Error == "":
		t.setStatus(RefreshStatus{State: RefreshDone, DoneAt: t.opts.Now()})
	case status.Done && isCanceled(status.Error):
		// A stop or restart cancels the reload; that is not a failure
		// (FR-402's exception).
		t.setStatus(RefreshStatus{State: RefreshIdle})
	case status.Done:
		t.setStatus(RefreshStatus{State: RefreshFailed, Error: status.Error})
	case status.Running:
		t.setStatus(RefreshStatus{State: RefreshRunning})
	}
	return t.Status()
}

func (t *Tracker) setStatus(status RefreshStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
}

// isCanceled recognises the reload's own cancellation through the job's error
// text. JobStatus carries a string, so the message is the only signal available.
func isCanceled(message string) bool {
	return strings.Contains(message, context.Canceled.Error())
}
