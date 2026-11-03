package launcher

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/perber/wiki/internal/core/shared"
)

const (
	jwtSecretFileName     = "jwt.secret"
	adminPasswordFileName = "admin.password"
	usersDBFileName       = "users.db"

	// jwtSecretBytes is FR-201: 32 random bytes, base64 RawURL encoded = 43
	// characters, exactly what the server expects for --jwt-secret.
	jwtSecretBytes = 32
	// adminPasswordLen is FR-202's 16 characters.
	adminPasswordLen = 16
	// placeholderLen is FR-203's third state: the server requires a non-empty
	// --admin-password even when it never uses it.
	placeholderLen = 32

	// passwordAlphabet omits 0 O 1 l I o: this password is read off a window
	// and typed into a login form.
	passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// CredState is which of FR-203's three situations the admin password is in.
type CredState int

const (
	// CredSeeded means there was no users.db yet, so the password handed to
	// the server is the one that seeds the database.
	CredSeeded CredState = iota
	// CredFromFile means credentials exist and the file still holds the
	// password the database was seeded with.
	CredFromFile
	// CredUnknown means the database exists but its password file is gone.
	CredUnknown
)

// EnsureJWT returns the stored JWT secret, generating and storing one on first
// start (FR-201). credDir is the program's own directory: the credentials sit
// next to the executable, next to the data directory (SRS §6.10). A file
// holding only whitespace counts as missing.
func EnsureJWT(dataDir string) (string, error) {
	value, err := readSecret(dataDir, jwtSecretFileName)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}

	raw := make([]byte, jwtSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate %s: %w", jwtSecretFileName, err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	if err := writeSecret(dataDir, jwtSecretFileName, secret); err != nil {
		return "", err
	}
	return secret, nil
}

// ResolveAdminPassword implements FR-202/203. value is always usable as
// --admin-password: in the unknown state it is a throwaway placeholder, so the
// caller must show state rather than value.
func ResolveAdminPassword(credDir, dataDir string) (value string, state CredState, err error) {
	fileValue, err := readSecret(credDir, adminPasswordFileName)
	if err != nil {
		return "", CredUnknown, err
	}

	// Whether the database exists is a property of the data directory; the
	// password file itself lives next to the program (SRS §6.10's package).
	usersDBExists := false
	if _, statErr := os.Stat(filepath.Join(dataDir, usersDBFileName)); statErr == nil {
		usersDBExists = true
	}

	switch {
	case !usersDBExists:
		if fileValue != "" {
			return fileValue, CredSeeded, nil
		}
		generated, genErr := randomString(passwordAlphabet, adminPasswordLen)
		if genErr != nil {
			return "", CredSeeded, genErr
		}
		if writeErr := writeSecret(credDir, adminPasswordFileName, generated); writeErr != nil {
			return "", CredSeeded, writeErr
		}
		return generated, CredSeeded, nil

	case fileValue != "":
		return fileValue, CredFromFile, nil

	default:
		placeholder, genErr := randomString(passwordAlphabet, placeholderLen)
		// Never written, never shown, never logged (FR-203).
		return placeholder, CredUnknown, genErr
	}
}

// ReadAdminPasswordFile reports the stored password, empty when there is none.
// The window uses it to decide between a shown value and "already set".
func ReadAdminPasswordFile(dataDir string) (string, error) {
	return readSecret(dataDir, adminPasswordFileName)
}

// randomString returns n characters drawn from alphabet with crypto/rand.
func randomString(alphabet string, n int) (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := range out {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate random string: %w", err)
		}
		out[i] = alphabet[index.Int64()]
	}
	return string(out), nil
}

func readSecret(dataDir, name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func writeSecret(dataDir, name, value string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("prepare %s: %w", dataDir, err)
	}
	if err := shared.WriteFileAtomic(filepath.Join(dataDir, name), []byte(value+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
