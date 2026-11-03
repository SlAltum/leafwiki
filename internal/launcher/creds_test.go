package launcher

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perber/wiki/internal/core/auth"
)

func TestEnsureJWT_GeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureJWT(dir)
	if err != nil {
		t.Fatalf("EnsureJWT() error = %v", err)
	}
	if len(first) != 43 {
		t.Fatalf("secret length = %d, want 43 (32 bytes base64 raw url)", len(first))
	}
	if _, err := base64.RawURLEncoding.DecodeString(first); err != nil {
		t.Fatalf("secret is not base64 raw url: %v", err)
	}

	second, err := EnsureJWT(dir)
	if err != nil {
		t.Fatalf("EnsureJWT() second call error = %v", err)
	}
	if second != first {
		t.Fatalf("EnsureJWT() regenerated the secret: %q then %q", first, second)
	}
}

func TestEnsureJWT_BlankFileIsRegenerated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, jwtSecretFileName), []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	secret, err := EnsureJWT(dir)
	if err != nil {
		t.Fatalf("EnsureJWT() error = %v", err)
	}
	if strings.TrimSpace(secret) == "" {
		t.Fatal("EnsureJWT() returned a blank secret")
	}
}

func TestResolveAdminPassword_ThreeStates(t *testing.T) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	t.Run("no database seeds and stores", func(t *testing.T) {
		dir := t.TempDir()
		value, state, err := ResolveAdminPassword(dir, dir)
		if err != nil {
			t.Fatalf("ResolveAdminPassword() error = %v", err)
		}
		if state != CredSeeded {
			t.Fatalf("state = %v, want CredSeeded", state)
		}
		if len(value) < auth.MinPasswordLength {
			t.Fatalf("password length = %d, want at least %d", len(value), auth.MinPasswordLength)
		}
		for _, r := range value {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("password contains %q, which is not in the unambiguous alphabet", r)
			}
		}
		stored, err := ReadAdminPasswordFile(dir)
		if err != nil {
			t.Fatalf("ReadAdminPasswordFile() error = %v", err)
		}
		if stored != value {
			t.Fatalf("stored password = %q, want the generated %q", stored, value)
		}
	})

	t.Run("database and file use the file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, usersDBFileName), []byte("db"), 0o600); err != nil {
			t.Fatalf("write users.db: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, adminPasswordFileName), []byte("known-password\n"), 0o600); err != nil {
			t.Fatalf("write password: %v", err)
		}

		value, state, err := ResolveAdminPassword(dir, dir)
		if err != nil {
			t.Fatalf("ResolveAdminPassword() error = %v", err)
		}
		if state != CredFromFile || value != "known-password" {
			t.Fatalf("ResolveAdminPassword() = %q, %v, want known-password and CredFromFile", value, state)
		}
	})

	t.Run("database without file uses a placeholder and stores nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, usersDBFileName), []byte("db"), 0o600); err != nil {
			t.Fatalf("write users.db: %v", err)
		}

		value, state, err := ResolveAdminPassword(dir, dir)
		if err != nil {
			t.Fatalf("ResolveAdminPassword() error = %v", err)
		}
		if state != CredUnknown {
			t.Fatalf("state = %v, want CredUnknown", state)
		}
		if value == "" {
			t.Fatal("placeholder is empty; the server rejects an empty --admin-password")
		}
		if _, err := os.Stat(filepath.Join(dir, adminPasswordFileName)); !os.IsNotExist(err) {
			t.Fatalf("placeholder was written to disk (stat err = %v)", err)
		}
	})
}
