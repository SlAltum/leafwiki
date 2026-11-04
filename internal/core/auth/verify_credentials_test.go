package auth

import (
	"errors"
	"testing"
)

// VerifyCredentials proves an account for one caller without logging it in
// everywhere: nothing may be written to sessions.db (FR-111).
func TestVerifyCredentials_ReturnsTheUserWithoutASession(t *testing.T) {
	service := setupTestAuthService(t)

	user, err := service.VerifyCredentials("testuser", "securepass")
	if err != nil {
		t.Fatalf("VerifyCredentials: %v", err)
	}
	if user.ID == "" || user.Username != "testuser" || user.Password != "" {
		t.Fatalf("user = %#v, want the account without its password", user)
	}

	var sessions int
	if err := service.sessions.sessionStore.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("sessions.db holds %d rows, want none from a credential check", sessions)
	}
}

// The strict-subset rule: the same failure counter decides both paths, so a
// caller cannot dodge the lockout by switching from Login to VerifyCredentials
// (FR-111, FR-119).
func TestVerifyCredentials_SharesTheLockoutCounter(t *testing.T) {
	service := setupTestAuthService(t)

	for i := 0; i < loginMaxFailures; i++ {
		if _, err := service.VerifyCredentials("testuser", "wrong"); !errors.Is(err, ErrUserInvalidCredentials) {
			t.Fatalf("attempt %d = %v, want invalid credentials", i+1, err)
		}
	}

	if _, err := service.VerifyCredentials("testuser", "securepass"); !errors.Is(err, ErrUserAccountLocked) {
		t.Fatalf("VerifyCredentials after the lockout = %v, want ErrUserAccountLocked", err)
	}
	if _, err := service.Login("testuser", "securepass"); !errors.Is(err, ErrUserAccountLocked) {
		t.Fatalf("Login after a VerifyCredentials lockout = %v, want ErrUserAccountLocked", err)
	}
}

// A wrong password counts against the account, not against the username: an
// unknown user is refused without creating anything.
func TestVerifyCredentials_RefusesUnknownUser(t *testing.T) {
	service := setupTestAuthService(t)

	if _, err := service.VerifyCredentials("nobody", "securepass"); !errors.Is(err, ErrUserInvalidCredentials) {
		t.Fatalf("VerifyCredentials(unknown) = %v, want invalid credentials", err)
	}
}

// An account with TOTP enabled cannot be authenticated through the channel: the
// second step does not exist there (FR-111).
func TestVerifyCredentials_RefusesTOTPAccounts(t *testing.T) {
	fixture := setupTOTPTestFixture(t)

	if _, err := fixture.authService.VerifyCredentials("testuser", "securepass"); !errors.Is(err, ErrUserTOTPRequired) {
		t.Fatalf("VerifyCredentials(totp account) = %v, want ErrUserTOTPRequired", err)
	}
}

// The capability matrix is the one place that decides access (FR-115).
func TestCan(t *testing.T) {
	tests := []struct {
		role      string
		operation Operation
		want      bool
	}{
		{role: "", operation: OpPageGet, want: true},
		{role: "", operation: OpPageCreate, want: true},
		{role: "", operation: OpPageUpdate, want: true},
		{role: "", operation: OpPageDelete, want: false},
		{role: RoleViewer, operation: OpPageDelete, want: false},
		{role: RoleEditor, operation: OpPageDelete, want: true},
		{role: RoleAdmin, operation: OpPageDelete, want: true},
		{role: RoleAdmin, operation: Operation("page.rebuild"), want: false},
	}
	for _, test := range tests {
		if got := Can(test.role, test.operation); got != test.want {
			t.Fatalf("Can(%q, %q) = %v, want %v", test.role, test.operation, got, test.want)
		}
	}

	for _, operation := range []Operation{OpPageGet, OpPageCreate, OpPageUpdate} {
		if NeedsAuthentication(operation) {
			t.Fatalf("%s needs no authentication but reported that it does", operation)
		}
	}
	if !NeedsAuthentication(OpPageDelete) {
		t.Fatal("delete reported as open to anonymous callers")
	}
}
