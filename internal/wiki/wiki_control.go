package wiki

import (
	"context"
	"errors"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/wiki/control"
)

// ControlStatus implements control.Source: the facts an agent asks for before it
// writes (docs/srs/agent-operations.srs.md FR-122). It reads only.
func (w *Wiki) ControlStatus(_ context.Context) control.SourceStatus {
	status := control.SourceStatus{
		AuthDisabled: w.authDisabled,
		Vectors:      control.VectorsStatus{Mode: "off"},
		Indexing:     control.IndexingStatus{},
	}

	if w.tree != nil {
		// Sections are chapters, not pages: an agent asking "how big is this
		// wiki" means the leaf pages, which is what the metrics report too.
		pages, _ := w.tree.NodeCounts()
		status.Pages = pages
	}

	if w.status != nil {
		status.Indexing.Running = w.status.IsActive()
	}

	if w.vectorService == nil {
		return status
	}

	status.Vectors = control.VectorsStatus{
		Mode:             string(w.vectorService.Mode()),
		EncoderAvailable: w.vectorService.EncoderAvailable(),
	}
	if vectorStatus, err := w.vectorService.Status(true); err == nil {
		status.Vectors.Paused = vectorStatus.Paused
		status.Vectors.Pending = vectorStatus.Pending
	} else {
		w.log.Warn("could not read the semantic index state for the control status", "error", err)
	}
	if failed, err := w.vectorService.FailedCount(); err == nil {
		status.Vectors.Failed = failed
	}

	return status
}

// ControlUser re-reads one account for the channel's per-request identity check
// (FR-117). A user that disappeared or lost its role stops working immediately,
// because the answer is read now and not cached on the connection.
func (w *Wiki) ControlUser(_ context.Context, userID string) (control.SourceUser, error) {
	// UserService() is the live, restore-swap-tracking one: the field built at
	// boot stays suspended after a live restore, and reading it would report
	// every account as gone.
	users := w.UserService()
	if users == nil || userID == "" {
		return control.SourceUser{}, control.ErrNoSuchUser
	}
	user, err := users.GetUserByID(userID)
	if err != nil || user == nil {
		// The store tells "no such user" and "cannot answer" apart only by the
		// error the lookup returns; anything that is not a clean miss is passed
		// on, so a transient failure does not end a session.
		if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
			return control.SourceUser{}, err
		}
		return control.SourceUser{}, control.ErrNoSuchUser
	}
	return control.SourceUser{ID: user.ID, Username: user.Username, Role: user.Role}, nil
}

// ControlVerifyCredentials checks a credential pair without issuing a session
// (FR-111): the channel authenticates one connection, it does not log the user in
// everywhere.
func (w *Wiki) ControlVerifyCredentials(ctx context.Context, identifier, password string) (control.SourceUser, error) {
	if w.auth == nil {
		return control.SourceUser{}, auth.ErrUserInvalidCredentials
	}
	user, err := w.auth.VerifyCredentials(identifier, password)
	if err != nil {
		return control.SourceUser{}, err
	}
	return control.SourceUser{ID: user.ID, Username: user.Username, Role: user.Role}, nil
}
