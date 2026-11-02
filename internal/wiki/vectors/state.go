package vectors

import (
	"time"

	"github.com/perber/wiki/internal/embed"
)

// State is the semantic index state. The wire values are part of the API
// contract: clients switch on them, so they never change (FR-701).
type State string

const (
	// StateOff means semantic search is switched off.
	StateOff State = "off"
	// StateMissing means the feature is on but no build exists yet.
	StateMissing State = "missing"
	// StateFresh means the current build matches the content and the model.
	StateFresh State = "fresh"
	// StateQueued means pages are waiting to be embedded.
	StateQueued State = "queued"
	// StateRunning means a build is in progress.
	StateRunning State = "running"
	// StateStale means the current build was made by another model or
	// tokenizer, or with different content.
	StateStale State = "stale"
	// StateFailed means the last build did not finish.
	StateFailed State = "failed"
)

// PublicStatus is what any reader may see: the state and the pause flag, no
// detail. Queue age, failure reasons and build ids stay on the admin status
// (FR-703).
type PublicStatus struct {
	State  State `json:"state"`
	Paused bool  `json:"paused"`
}

// Status is the full picture, for the admin status endpoint.
type Status struct {
	State            State      `json:"state"`
	Paused           bool       `json:"paused"`
	Pending          int        `json:"pending"`
	BuildID          string     `json:"build_id,omitempty"`
	ModelID          string     `json:"model_id,omitempty"`
	TokenizerVersion string     `json:"tokenizer_version,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	RunningSince     *time.Time `json:"running_since,omitempty"`
	QueuedSince      *time.Time `json:"queued_since,omitempty"`
}

// Public reduces the status to what any reader may see.
func (s Status) Public() PublicStatus {
	return PublicStatus{State: s.State, Paused: s.Paused}
}

// Status reports the state of the semantic index. enabled is false when the
// feature is off, which is the only way to get StateOff.
func (s *Service) Status(enabled bool) (Status, error) {
	if !enabled {
		return Status{State: StateOff}, nil
	}

	// The configured model is what will produce the next build, so it is the
	// honest answer while no build exists yet (or while one is running); a
	// committed build overrides it below.
	status := Status{
		State:            StateFresh,
		ModelID:          s.modelID,
		TokenizerVersion: s.tokenizerVersion,
	}

	paused, err := s.Paused()
	if err != nil {
		return Status{}, err
	}
	status.Paused = paused

	pending, err := s.store.QueueCount()
	if err != nil {
		return Status{}, err
	}
	status.Pending = pending
	if pending > 0 {
		// The oldest entry is what tells an operator how long indexing has
		// been stuck.
		if entries, entryErr := s.store.QueuedPages(1); entryErr == nil && len(entries) > 0 {
			queuedAt := entries[0].QueuedAt
			status.QueuedSince = &queuedAt
		}
	}

	runningSince, err := s.store.Meta(embed.MetaRunningSince)
	if err != nil {
		return Status{}, err
	}
	if runningSince != "" {
		status.State = StateRunning
		if parsed, parseErr := time.Parse(time.RFC3339, runningSince); parseErr == nil {
			status.RunningSince = &parsed
		}
	}

	// A build in progress or pages waiting say more about what is happening
	// than the current generation does, so they win over missing/failed/stale.
	if status.State == StateRunning {
		return status, nil
	}
	if pending > 0 {
		status.State = StateQueued
		return status, nil
	}

	current, err := s.store.CurrentBuild()
	if err != nil {
		return Status{}, err
	}
	if current == nil {
		status.State = StateMissing
		return status, nil
	}
	status.BuildID = current.ID
	status.ModelID = current.ModelID
	status.TokenizerVersion = current.TokenizerVersion

	lastError, err := s.store.Meta(embed.MetaLastError)
	if err != nil {
		return Status{}, err
	}
	status.LastError = lastError
	if lastError != "" {
		status.State = StateFailed
		return status, nil
	}

	if current.ModelID != s.modelID || current.TokenizerVersion != s.tokenizerVersion {
		status.State = StateStale
		return status, nil
	}
	return status, nil
}
