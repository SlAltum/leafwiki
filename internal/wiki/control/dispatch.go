package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/perber/wiki/internal/core/auth"
)

// loginBudgetPerMinute bounds login attempts on one connection, so a local
// process cannot lock an interactive account out (FR-119b).
const loginBudgetPerMinute = 5

// handler answers one method. A request that cannot be answered comes back as an
// *Error, which travels as the JSON-RPC error object.
type handler func(ctx context.Context, session *session, params json.RawMessage) (any, *Error)

// methodSpec is what a method needs before it runs.
type methodSpec struct {
	handle handler
	// public methods need no credentials at all (FR-112).
	public bool
	// operation is the capability the method consumes; empty for methods the
	// page capability matrix does not cover.
	operation auth.Operation
}

// session carries what one connection learned about its peer. Until auth.login
// succeeds the session is anonymous, which is a tier of its own (FR-113): it may
// read and write content, not delete it.
type session struct {
	// userID is empty for an anonymous session.
	userID string
	// username is what the account is called, for the reply to auth.login.
	username string
	// role is the account's role, empty for an anonymous session.
	role string
	// logins records when this connection tried to log in, for the budget above.
	logins []time.Time
}

// anonymous reports whether the session never authenticated.
func (s *session) anonymous() bool { return s.userID == "" }

// caller is who this session writes as (FR-113, FR-114).
func (s *session) caller() Caller {
	return Caller{Identity: s.identity(), Role: s.role}
}

// identity is what a write records as its author: the account, or the system
// user for an anonymous session (FR-113, FR-114).
func (s *session) identity() string {
	if s.userID == "" {
		return SystemUserID
	}
	return s.userID
}

// allowLogin reports whether this connection may try another credential, and
// records the attempt.
func (s *session) allowLogin(now time.Time) bool {
	windowStart := now.Add(-time.Minute)
	recent := s.logins[:0]
	for _, at := range s.logins {
		if at.After(windowStart) {
			recent = append(recent, at)
		}
	}
	s.logins = recent
	if len(s.logins) >= loginBudgetPerMinute {
		return false
	}
	s.logins = append(s.logins, now)
	return true
}

// dispatch answers one request, without panicking on a bad frame.
func (s *Service) dispatch(ctx context.Context, request *Request, session *session) (response *Response) {
	if request == nil {
		return errorResponse(nil, errInvalidRequest("empty request"))
	}
	if request.JSONRPC != "" && request.JSONRPC != "2.0" {
		return errorResponse(request.ID, errInvalidRequest("jsonrpc must be 2.0"))
	}
	if request.Method == "" {
		return errorResponse(request.ID, errInvalidRequest("method is required"))
	}

	spec, ok := s.methods()[request.Method]
	if !ok {
		return errorResponse(request.ID, errMethodNotFound(request.Method))
	}

	// A handler that panics must not take the connection or the server down.
	defer func() {
		if recovered := recover(); recovered != nil {
			s.log.Error("control channel: handler panicked", "method", request.Method, "panic", recovered)
			response = errorResponse(request.ID, errInternal("the server could not answer this request"))
		}
	}()

	if gateErr := s.authorize(ctx, session, spec); gateErr != nil {
		return errorResponse(request.ID, gateErr)
	}

	result, handlerErr := spec.handle(ctx, session, request.Params)
	if handlerErr != nil {
		return errorResponse(request.ID, handlerErr)
	}
	return resultResponse(request.ID, result)
}

// authorize applies the authentication policy and the capability matrix
// (FR-112, FR-115). An authenticated session is re-read on every request, so a
// disabled or deleted account stops working immediately (FR-117).
func (s *Service) authorize(ctx context.Context, session *session, spec methodSpec) *Error {
	if spec.public {
		return nil
	}

	if !session.anonymous() && s.source != nil {
		current, err := s.source.ControlUser(ctx, session.userID)
		switch {
		case errors.Is(err, ErrNoSuchUser):
			// The account is gone: this connection is anonymous again.
			session.userID, session.username, session.role = "", "", ""
			return errUnauthenticated()
		case err != nil:
			// A store that cannot answer is not an account that disappeared:
			// ending the session here would let one hiccup downgrade later writes
			// to the anonymous identity without any error.
			s.log.Warn("control channel: could not re-read the session's account", "userID", session.userID, "error", err)
			return errInternal("the server could not re-read your account; try again")
		}
		session.role = current.Role
	}

	if s.authMode == AuthRequired && session.anonymous() {
		return errUnauthenticated()
	}
	if spec.operation == "" {
		// A privileged method without a capability would be open to everyone:
		// the table has to say what it needs (FR-115).
		s.log.Error("control channel: a non-public method declares no capability")
		return errInternal("the server refused this method; it declares no capability")
	}
	if auth.Can(session.role, spec.operation) {
		return nil
	}
	if session.anonymous() && auth.NeedsAuthentication(spec.operation) {
		return errUnauthenticated()
	}
	return errPermissionDenied()
}

// Methods lists the methods this service implements, sorted. A caller checks the
// advertised capabilities against it, so an advertisement cannot drift from the
// table (FR-122b).
func (s *Service) Methods() []string {
	names := make([]string, 0, len(s.methods()))
	for name := range s.methods() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// methods is the method table of this protocol version (FR-121, FR-131, FR-141).
// A method that is not listed answers -32601, which the CLI reads as a version
// mismatch (FR-123).
func (s *Service) methods() map[string]methodSpec {
	return map[string]methodSpec{
		"status":     {handle: s.handleStatus, public: true},
		"auth.login": {handle: s.handleLogin, public: true},
		"page.get":   {handle: s.handlePageGet, operation: auth.OpPageGet},
		// A whole batch is gated at create level; the per-item rules, including
		// the delete capability, are the operation's own job (FR-147).
		"page.apply": {handle: s.handlePageApply, operation: auth.OpPageCreate},
	}
}

// handleStatus answers the state of this instance (FR-121, FR-122). It needs no
// credentials: an agent has to be able to ask what it is talking to.
func (s *Service) handleStatus(_ context.Context, _ *session, params json.RawMessage) (any, *Error) {
	if len(params) != 0 {
		var decoded struct{}
		if err := json.Unmarshal(params, &decoded); err != nil {
			return nil, errInvalidParams("status takes no parameters")
		}
	}

	status := Status{
		Protocol:     Protocol,
		Capabilities: append([]string{}, s.capabilities...),
		Auth:         AuthStatus{Mode: string(s.authMode)},
	}
	if s.source != nil {
		source := s.source.ControlStatus(context.Background())
		status.Auth.Disabled = source.AuthDisabled
		status.Vectors = source.Vectors
		status.Pages = source.Pages
		status.Indexing = source.Indexing
	}
	return status, nil
}

// loginParams is the credential pair a client sends.
type loginParams struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// loginResult is what the session became.
type loginResult struct {
	User loginUser `json:"user"`
}

type loginUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// handleLogin upgrades one connection to an authenticated session (FR-111). It
// verifies the same way Login does but issues no session: nothing is written to
// sessions.db.
func (s *Service) handleLogin(ctx context.Context, session *session, params json.RawMessage) (any, *Error) {
	var credentials loginParams
	if len(params) == 0 {
		return nil, errInvalidParams("auth.login needs a user and a password")
	}
	if err := json.Unmarshal(params, &credentials); err != nil {
		return nil, errInvalidParams("auth.login needs a user and a password")
	}
	if credentials.User == "" || credentials.Password == "" {
		return nil, errInvalidParams("auth.login needs a user and a password")
	}

	// The budget comes before the password: a wrong credential must cost the same
	// as a right one (FR-119b).
	if !session.allowLogin(time.Now()) {
		return nil, errLoginBudget()
	}

	// An instance without a user store cannot authenticate anybody (FR-118).
	if s.source == nil || s.source.ControlStatus(ctx).AuthDisabled {
		return nil, errUnauthenticated()
	}

	user, err := s.source.ControlVerifyCredentials(ctx, credentials.User, credentials.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUserAccountLocked):
			return nil, errAccountLocked()
		case errors.Is(err, auth.ErrUserTOTPRequired):
			return nil, errTOTPRequired()
		case errors.Is(err, auth.ErrUserInvalidCredentials):
			return nil, errUnauthenticated()
		default:
			s.log.Warn("control channel: credential check failed", "error", err)
			return nil, errInternal("the server could not check the credentials")
		}
	}

	session.userID = user.ID
	session.username = user.Username
	session.role = user.Role
	s.log.Info("control channel: session authenticated", "userID", user.ID, "role", user.Role)
	return loginResult{User: loginUser{ID: user.ID, Username: user.Username, Role: user.Role}}, nil
}

// handlePageGet answers one page (FR-131, FR-132).
func (s *Service) handlePageGet(ctx context.Context, session *session, params json.RawMessage) (any, *Error) {
	if s.pages == nil {
		return nil, errInternal("page access is not wired on this server")
	}
	var ref PageRef
	if len(params) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&ref); err != nil {
			return nil, errInvalidParams("page.get takes {path} or {id}")
		}
	}
	if ref.Path == "" && ref.ID == "" {
		return nil, errInvalidParams("page.get takes {path} or {id}").
			withHint("send one of them, for example {\"path\":\"platform/deploy\"}")
	}
	return s.pages.PageGet(ctx, session.caller(), ref)
}

// handlePageApply answers one batch (FR-141 to FR-149). The wire checks happen
// here, before anything reaches the wiki.
func (s *Service) handlePageApply(ctx context.Context, session *session, params json.RawMessage) (any, *Error) {
	if s.pages == nil {
		return nil, errInternal("page writing is not wired on this server")
	}
	if len(params) > MaxApplyBytes {
		return nil, errInvalidParams("the batch exceeds the size limit").
			withHint("split it into smaller batches")
	}

	var request ApplyParams
	decoder := json.NewDecoder(bytes.NewReader(params))
	// Unknown fields are refused on purpose: slug, expected_version and
	// content_file are P1 and must not be silently ignored (FR-141, FR-142).
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, errInvalidParams("page.apply takes {items:[...], dry_run?}: " + err.Error())
	}
	if len(request.Items) > MaxApplyItems {
		return nil, errInvalidParams("the batch holds more items than the limit").
			withHint("send at most " + strconv.Itoa(MaxApplyItems) + " items per batch")
	}
	for _, item := range request.Items {
		if item.Content != nil && len(*item.Content) > MaxContentBytes {
			return nil, errInvalidParams("an item's content exceeds the size limit").
				withHint("keep one page under 1 MiB and split the rest into separate pages")
		}
	}

	return s.pages.PageApply(ctx, session.caller(), request)
}
