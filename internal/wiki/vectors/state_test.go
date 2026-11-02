package vectors

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/perber/wiki/internal/embed"
)

func TestStatus_Off(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	status, err := svc.Status(false)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateOff {
		t.Fatalf("state = %q, want off", status.State)
	}
}

func TestStatus_MissingBeforeAnyBuild(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateMissing {
		t.Fatalf("state = %q, want missing", status.State)
	}
}

func TestStatus_QueuedWhilePagesWait(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	if err := svc.Enqueue("p1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateQueued || status.Pending != 1 {
		t.Fatalf("state = %q, pending = %d; want queued/1", status.State, status.Pending)
	}
}

func TestStatus_FreshAfterACommit(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateFresh {
		t.Fatalf("state = %q, want fresh", status.State)
	}
	if status.BuildID == "" || status.ModelID != "minilm" {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatus_RunningWhileABuildIsOpen(t *testing.T) {
	svc, store, _, _ := newTestService(t)

	if _, err := store.BeginBuild(); err != nil {
		t.Fatalf("BeginBuild: %v", err)
	}
	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateRunning {
		t.Fatalf("state = %q, want running", status.State)
	}
}

func TestStatus_StaleAfterAModelChange(t *testing.T) {
	svc, store, _, _ := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// A different tokenizer makes the committed build unusable for this one.
	other := NewService(store, &fakeEncoder{}, &fakePages{}, "minilm", "wordpiece-2", ModeAuto, nil)
	status, err := other.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateStale {
		t.Fatalf("state = %q, want stale", status.State)
	}
}

func TestStatus_FailedAfterABuildError(t *testing.T) {
	svc, store, encoder, _ := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	encoder.err = errors.New("encoder exploded")
	if _, err := svc.Rebuild([]string{"p1"}); err == nil {
		t.Fatal("a failing encoder did not fail the rebuild")
	}

	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateFailed || status.LastError == "" {
		t.Fatalf("state = %q, last error = %q; want failed with a reason", status.State, status.LastError)
	}

	// A successful build clears the failure.
	encoder.err = nil
	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	status, err = svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateFresh || status.LastError != "" {
		t.Fatalf("state = %q, last error = %q; want fresh and clean", status.State, status.LastError)
	}

	if _, err := store.Meta(embed.MetaLastError); err != nil {
		t.Fatalf("Meta: %v", err)
	}
}

func TestStatus_PausedIsOrthogonal(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := svc.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}

	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != StateFresh || !status.Paused {
		t.Fatalf("state = %q, paused = %v; want fresh and paused", status.State, status.Paused)
	}
}

func TestReindexIfStale(t *testing.T) {
	cases := []struct {
		name      string
		prepare   func(t *testing.T, svc *Service, store *embed.Store)
		wantBuilt bool
	}{
		{
			name:      "no build yet",
			prepare:   func(t *testing.T, svc *Service, store *embed.Store) {},
			wantBuilt: true,
		},
		{
			name: "stale after a model change",
			prepare: func(t *testing.T, svc *Service, store *embed.Store) {
				if _, err := svc.Rebuild([]string{"p1"}); err != nil {
					t.Fatalf("Rebuild: %v", err)
				}
				if err := store.SetMeta(embed.MetaTokenizerVersion, "wordpiece-old"); err != nil {
					t.Fatalf("SetMeta: %v", err)
				}
			},
			wantBuilt: true,
		},
		{
			name: "fresh stays untouched",
			prepare: func(t *testing.T, svc *Service, store *embed.Store) {
				if _, err := svc.Rebuild([]string{"p1"}); err != nil {
					t.Fatalf("Rebuild: %v", err)
				}
			},
			wantBuilt: false,
		},
		{
			name: "queued work is already converging",
			prepare: func(t *testing.T, svc *Service, store *embed.Store) {
				if _, err := svc.Rebuild([]string{"p1"}); err != nil {
					t.Fatalf("Rebuild: %v", err)
				}
				if err := svc.Enqueue("p2"); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
			},
			wantBuilt: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, encoder, _ := newTestService(t)
			tc.prepare(t, svc, store)
			callsBefore := encoder.callCount()

			built, err := svc.ReindexIfStale()
			if err != nil {
				t.Fatalf("ReindexIfStale: %v", err)
			}
			if built != tc.wantBuilt {
				t.Fatalf("built = %v, want %v", built, tc.wantBuilt)
			}
			if built && encoder.callCount() == callsBefore {
				t.Fatal("a rebuild ran without encoding anything")
			}
			if built {
				if status, err := svc.Status(true); err != nil || status.State != StateFresh {
					t.Fatalf("state after the rebuild = %q, %v", status.State, err)
				}
			}
		})
	}
}

func TestStatus_PublicCarriesNoDetail(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	status, err := svc.Status(true)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	raw, err := json.Marshal(status.Public())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded) != 2 || decoded["state"] == nil || decoded["paused"] == nil {
		t.Fatalf("public status = %s, want only state and paused", raw)
	}
}
