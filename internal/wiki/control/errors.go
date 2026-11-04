package control

import "fmt"

// JSON-RPC 2.0 error codes, plus the server-defined ones the CLI maps onto exit
// codes (docs/srs/agent-operations.srs.md §6.1, FR-195, FR-196).
const (
	CodeParse             = -32700
	CodeInvalidRequest    = -32600
	CodeMethodNotFound    = -32601
	CodeInvalidParams     = -32602
	CodeInternal          = -32603
	CodeUnauthenticated   = -32001
	CodeRestoreInProgress = -32002
	CodePageNotFound      = -32010
	CodeForbidden         = -32011
	CodeRateLimited       = -32013
)

// Error kinds are the stable strings an agent switches on. They are the ones
// §6.1 lists; a code without a listed kind carries no kind at all, because the
// code already says everything.
const (
	KindInvalidParams       = "invalid_params"
	KindInternal            = "internal_error"
	KindUnauthorized        = "unauthorized"
	KindAccountLocked       = "account_locked"
	KindTOTPRequired        = "totp_required"
	KindRestoreInProgress   = "restore_in_progress"
	KindPageNotFound        = "page_not_found"
	KindPageSlugConflict    = "page_slug_conflict"
	KindPageHasChildren     = "page_has_children"
	KindPageVersionConflict = "page_version_conflict"
	KindPermissionDenied    = "permission_denied"
	KindRateLimited         = "rate_limited"
)

// ErrorData carries the machine-readable part of an error.
type ErrorData struct {
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

// Error is the JSON-RPC error object. Message is for humans; Kind is what a
// client may act on.
type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// Error lets an *Error be used as an error value.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Data != nil && e.Data.Kind != "" {
		return fmt.Sprintf("%d %s: %s", e.Code, e.Data.Kind, e.Message)
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

// newError builds an error that carries a kind. Every kind this file emits is
// one §6.1 lists, so a client's switch never sees a surprise.
func newError(code int, kind, message string) *Error {
	return &Error{Code: code, Message: message, Data: &ErrorData{Kind: kind}}
}

// withHint adds the actionable half of an error: the next command to run, or the
// conclusion to act on (§6.1).
func (e *Error) withHint(hint string) *Error {
	if e.Data == nil {
		e.Data = &ErrorData{}
	}
	e.Data.Hint = hint
	return e
}

// errParseError reports a frame that is not valid JSON. The code says it; §6.1
// lists no kind for it.
func errParseError(message string) *Error {
	return &Error{Code: CodeParse, Message: message}
}

// errFrameTooLarge reports a frame over the size limit. §6.1 folds this into
// -32600, which needs no kind.
func errFrameTooLarge(limit int) *Error {
	return (&Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("frame exceeds %d bytes", limit)}).
		withHint("send one JSON-RPC object per frame, under the size limit")
}

// errInvalidRequest reports a request object that is not JSON-RPC 2.0.
func errInvalidRequest(message string) *Error {
	return (&Error{Code: CodeInvalidRequest, Message: message}).
		withHint("send one JSON-RPC 2.0 object with a method")
}

// errMethodNotFound tells a client the server speaks another version of the
// protocol (FR-123).
func errMethodNotFound(method string) *Error {
	return (&Error{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", method)}).
		withHint("this server does not implement the method; upgrade the client or use the offline commands")
}

func errInvalidParams(message string) *Error {
	return newError(CodeInvalidParams, KindInvalidParams, message).
		withHint("check the parameters of this method")
}

// errUnauthenticated says the caller must present credentials, or that the ones
// it presented no longer work (FR-112, FR-117, FR-118).
func errUnauthenticated() *Error {
	return newError(CodeUnauthenticated, KindUnauthorized, "this request needs a signed-in account").
		withHint("call auth.login first, or run the command with --user and --password")
}

// errAccountLocked reports the shared lockout: too many failed attempts
// (FR-119).
func errAccountLocked() *Error {
	return newError(CodeUnauthenticated, KindAccountLocked, "the account is locked after too many failed logins").
		withHint("wait for the lockout to pass, or reset the password from the server host")
}

// errTOTPRequired reports an account the channel cannot finish logging in.
func errTOTPRequired() *Error {
	return newError(CodeUnauthenticated, KindTOTPRequired, "this account requires a TOTP code").
		withHint("the channel cannot collect a TOTP code; use an account without TOTP")
}

// errLoginBudget reports the per-connection login limit (FR-119b).
func errLoginBudget() *Error {
	return newError(CodeRateLimited, KindRateLimited, "too many login attempts on this connection").
		withHint("wait a minute before trying again")
}

// errRestoreInProgress reports a write that arrived while a restore is swapping
// the data directory (FR-150).
func errRestoreInProgress() *Error {
	return newError(CodeRestoreInProgress, KindRestoreInProgress, "a restore is running").
		withHint("wait for the restore to finish and send the batch again")
}

// ErrRestoreInProgress is the composition root's builder for the same condition.
func ErrRestoreInProgress() *Error { return errRestoreInProgress() }

// errPermissionDenied reports an authenticated caller whose role is too low
// (FR-115).
func errPermissionDenied() *Error {
	return newError(CodeForbidden, KindPermissionDenied, "this account may not do that").
		withHint("deleting needs an editor or an admin account")
}

// errPageNotFound reports a page the wiki does not have (FR-133).
func errPageNotFound(path string) *Error {
	return newError(CodePageNotFound, KindPageNotFound, "no page matches "+path).
		withHint("create it with \"create\": true, or check the path")
}

// errPageHasChildren reports a delete that would take a subtree with it (FR-143).
func errPageHasChildren(path string) *Error {
	return newError(CodePageNotFound, KindPageHasChildren, "the page has children: "+path).
		withHint("pass \"recursive\": true to delete the whole subtree")
}

// errInvalidItem reports a batch the precheck refused (FR-143). Nothing is
// written for it.
func errInvalidItem(message, hint string) *Error {
	error := newError(CodeInvalidParams, KindInvalidParams, message)
	if hint != "" {
		error = error.withHint(hint)
	}
	return error
}

// The exported builders are for the composition root that implements Pages: it
// maps its own failures onto the same codes and kinds the channel speaks.
func ErrPageNotFound(path string) *Error    { return errPageNotFound(path) }
func ErrPageHasChildren(path string) *Error { return errPageHasChildren(path) }
func ErrInvalidItem(message, hint string) *Error {
	return errInvalidItem(message, hint)
}
func ErrPermissionDenied() *Error { return errPermissionDenied() }

// ErrInternal reports a failure the caller cannot fix, keeping the cause in the
// message: this channel is local, and the log is the operator's.
func ErrInternal(err error) *Error {
	message := "the server could not answer this request"
	if err != nil {
		message = "the server could not answer this request: " + err.Error()
	}
	return errInternal(message)
}

// itemError is the per-item shape of a partial batch (FR-148).
func itemError(err *Error) *ItemError {
	if err == nil {
		return nil
	}
	out := &ItemError{Code: err.Code}
	if err.Data != nil {
		out.Kind = err.Data.Kind
		out.Hint = err.Data.Hint
	}
	return out
}

func errInternal(message string) *Error {
	return newError(CodeInternal, KindInternal, message).
		withHint("try again; if it keeps failing, check the server log or restart the server")
}

func errRateLimited() *Error {
	return newError(CodeRateLimited, KindRateLimited, "too many requests on this connection").
		withHint("slow down; the limit is per connection and resets within a second")
}
