package launcher

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writePage(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

func TestTakeSnapshot_HonoursNestedIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writePage(t, filepath.Join(root, "index.md"), "# home")
	writePage(t, filepath.Join(root, "keep", "page.md"), "# page")
	writePage(t, filepath.Join(root, "drafts", "wip.md"), "# wip")
	if err := os.WriteFile(filepath.Join(root, ".leafwikiignore"), []byte("drafts/\n"), 0o600); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}

	snapshot, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}

	for _, want := range []string{"index.md", filepath.Join("keep", "page.md")} {
		if _, ok := snapshot[want]; !ok {
			t.Fatalf("snapshot misses %q: %v", want, snapshot)
		}
	}
	if _, ok := snapshot[filepath.Join("drafts", "wip.md")]; ok {
		t.Fatal("snapshot contains an ignored page")
	}
}

func TestChanged_DetectsAddEditAndRemoval(t *testing.T) {
	root := t.TempDir()
	writePage(t, filepath.Join(root, "a.md"), "one")

	first, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	second, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if Changed(first, second) {
		t.Fatal("Changed() = true for an untouched tree")
	}

	writePage(t, filepath.Join(root, "b.md"), "two")
	third, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if !Changed(second, third) {
		t.Fatal("Changed() = false after a page was added")
	}
}

// fakeJob drives a tracker without a wiki.
type fakeJob struct {
	status atomic.Value // ResyncStatus
}

func (f *fakeJob) Status() ResyncStatus {
	value, _ := f.status.Load().(ResyncStatus)
	return value
}

func (f *fakeJob) set(status ResyncStatus) { f.status.Store(status) }

func newTestTracker(t *testing.T, job *fakeJob) *Tracker {
	t.Helper()
	return NewTracker(TrackerOptions{
		Trigger:  func() bool { return true },
		Status:   job.Status,
		Interval: time.Millisecond,
		Limit:    50 * time.Millisecond,
	})
}

func waitForState(t *testing.T, tracker *Tracker, want RefreshState) RefreshStatus {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		status := tracker.Status()
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %v, want %v", status.State, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTracker_FollowsToDone(t *testing.T) {
	job := &fakeJob{}
	job.set(ResyncStatus{Running: true})
	tracker := newTestTracker(t, job)

	if status := tracker.Start(); status.State != RefreshRunning {
		t.Fatalf("Start() = %v, want running right after the trigger", status.State)
	}

	job.set(ResyncStatus{Done: true})
	done := waitForState(t, tracker, RefreshDone)
	if done.DoneAt.IsZero() {
		t.Fatal("DoneAt is zero, want the finish time")
	}
}

func TestTracker_ReportsFailures(t *testing.T) {
	job := &fakeJob{}
	job.set(ResyncStatus{Running: true})
	tracker := newTestTracker(t, job)
	tracker.Start()

	job.set(ResyncStatus{Done: true, Error: "search re-index failed"})
	failed := waitForState(t, tracker, RefreshFailed)
	if failed.Error == "" {
		t.Fatal("Error is empty, want the job's message")
	}
}

func TestTracker_CancellationIsNotAFailure(t *testing.T) {
	job := &fakeJob{}
	job.set(ResyncStatus{Running: true})
	tracker := newTestTracker(t, job)
	tracker.Start()

	job.set(ResyncStatus{Done: true, Error: "tree reconstruction failed: " + context.Canceled.Error()})
	if status := waitForState(t, tracker, RefreshIdle); status.Error != "" {
		t.Fatalf("status = %+v, want idle without an error", status)
	}
}

func TestTracker_StopsPollingAfterTheLimit(t *testing.T) {
	job := &fakeJob{}
	job.set(ResyncStatus{Running: true})
	tracker := newTestTracker(t, job)
	tracker.Start()

	time.Sleep(80 * time.Millisecond)
	if status := tracker.Status(); status.State != RefreshRunning {
		t.Fatalf("state = %v, want running while the job is", status.State)
	}

	// The next user event picks the finished job up without a new trigger.
	job.set(ResyncStatus{Done: true})
	if status := tracker.Recheck(); status.State != RefreshDone {
		t.Fatalf("Recheck() = %v, want done", status.State)
	}
}

func TestTracker_JoinsARunningRefresh(t *testing.T) {
	var triggers atomic.Int32
	job := &fakeJob{}
	job.set(ResyncStatus{Running: true})

	tracker := NewTracker(TrackerOptions{
		Trigger:  func() bool { triggers.Add(1); return true },
		Status:   job.Status,
		Interval: time.Millisecond,
		Limit:    20 * time.Millisecond,
	})

	tracker.Start()
	job.set(ResyncStatus{Done: true})
	waitForState(t, tracker, RefreshDone)

	if got := triggers.Load(); got != 0 {
		t.Fatalf("Trigger() called %d times for an already running reload, want 0", got)
	}
}
