package auth

import "fmt"

// VerifyCredentialsOffline checks a credential pair against a users.db without a
// session store: the offline path has no session to issue and must not create
// sessions.db (docs/srs/agent-operations.srs.md FR-174).
func VerifyCredentialsOffline(storageDir, identifier, password string) (*User, error) {
	store, err := NewUserStore(storageDir)
	if err != nil {
		return nil, fmt.Errorf("opening the user database: %w", err)
	}
	defer func() {
		_ = store.Close()
	}()

	service := NewAuthService(NewUserService(store), nil, nil)
	return service.VerifyCredentials(identifier, password)
}
