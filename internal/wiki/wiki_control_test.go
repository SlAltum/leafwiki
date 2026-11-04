package wiki

import (
	"context"
	"errors"
	"testing"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/wiki/control"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
)

// ControlStatus is what an agent reads before it writes (FR-122): the values have
// to be honest, not just present.
func TestControlStatus_ReportsTheWiki(t *testing.T) {
	w := createWikiTestInstance(t)
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	}()

	before := w.ControlStatus(context.Background())
	if before.AuthDisabled {
		t.Fatal("authDisabled = true on an instance with authentication")
	}
	if before.Vectors.Mode != "off" {
		t.Fatalf("vectors.mode = %q, want off", before.Vectors.Mode)
	}
	if before.Vectors.EncoderAvailable {
		t.Fatal("encoder_available = true without the vectors tag")
	}

	// A section and a page below it: only the page is a page.
	section, err := wikipages.NewCreatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(
		context.Background(),
		wikipages.CreatePageInput{UserID: SYSTEM_USER_ID, Title: "Handbook", Slug: "handbook", Kind: pageNodeKind()},
	)
	if err != nil {
		t.Fatalf("create section: %v", err)
	}
	sectionID := section.Page.ID
	if _, err := wikipages.NewCreatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(
		context.Background(),
		wikipages.CreatePageInput{UserID: SYSTEM_USER_ID, ParentID: &sectionID, Title: "Deploy", Slug: "deploy", Kind: pageNodeKind()},
	); err != nil {
		t.Fatalf("create page: %v", err)
	}

	after := w.ControlStatus(context.Background())
	if after.Pages != before.Pages+1 {
		t.Fatalf("pages %d -> %d, want one more page (a section is not a page)", before.Pages, after.Pages)
	}
	pages, sections := w.tree.NodeCounts()
	if after.Pages != pages {
		t.Fatalf("pages = %d, want the %d leaf pages", after.Pages, pages)
	}
	if sections == 0 {
		t.Fatal("the fixture has no section, so the page/section split is not exercised")
	}
}

// The channel's identity hooks read the live user store (FR-111, FR-117).
func TestControlIdentity(t *testing.T) {
	w := createWikiTestInstance(t)
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	}()

	user, err := w.ControlVerifyCredentials(context.Background(), "admin", "adminpassword")
	if err != nil {
		t.Fatalf("ControlVerifyCredentials: %v", err)
	}
	if user.ID == "" || user.Role != auth.RoleAdmin {
		t.Fatalf("verified user = %#v", user)
	}

	current, err := w.ControlUser(context.Background(), user.ID)
	if err != nil || current.Role != auth.RoleAdmin || current.Username != user.Username {
		t.Fatalf("ControlUser = %#v, %v", current, err)
	}
	if _, err := w.ControlUser(context.Background(), "no-such-user"); !errors.Is(err, control.ErrNoSuchUser) {
		t.Fatalf("ControlUser(unknown) = %v, want ErrNoSuchUser", err)
	}

	if _, err := w.ControlVerifyCredentials(context.Background(), "admin", "wrong-password"); !errors.Is(err, auth.ErrUserInvalidCredentials) {
		t.Fatalf("wrong password = %v, want ErrUserInvalidCredentials", err)
	}
}

// A live restore swaps users.db and closes the boot-time user service, so the
// channel must re-read accounts through the restore-tracking accessor (FR-117).
func TestControlUser_SurvivesALiveRestore(t *testing.T) {
	w := createWikiTestInstance(t)
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	}()

	admin, err := w.ControlVerifyCredentials(context.Background(), "admin", "adminpassword")
	if err != nil {
		t.Fatalf("ControlVerifyCredentials: %v", err)
	}
	if _, err := w.ControlUser(context.Background(), admin.ID); err != nil {
		t.Fatalf("ControlUser before the restore: %v", err)
	}

	// What a live restore does: a fresh UserService replaces the boot-time one,
	// which is closed afterwards.
	if err := w.AuthService().ReplaceUserStore(w.GetStorageDir()); err != nil {
		t.Fatalf("ReplaceUserStore: %v", err)
	}

	current, err := w.ControlUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("ControlUser after a live restore = %v, want the account", err)
	}
	if current.Role != auth.RoleAdmin {
		t.Fatalf("role = %q after the restore", current.Role)
	}
}

// The channel verifies credentials and provisions nothing (FR-116).
func TestControlVerifyCredentials_CreatesNoAccounts(t *testing.T) {
	w := createWikiTestInstance(t)
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	}()

	before, err := w.UserService().GetUsers()
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if _, err := w.ControlVerifyCredentials(context.Background(), "admin", "adminpassword"); err != nil {
		t.Fatalf("ControlVerifyCredentials: %v", err)
	}
	if _, err := w.ControlVerifyCredentials(context.Background(), "ghost", "whatever"); err == nil {
		t.Fatal("an unknown account was accepted")
	}
	after, err := w.UserService().GetUsers()
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("user count %d -> %d; a credential check provisioned an account", len(before), len(after))
	}
}

// An instance started with --disable-auth has no user store to authenticate
// against (FR-118).
func TestControlVerifyCredentials_DisabledAuthInstance(t *testing.T) {
	w, err := NewWiki(&WikiOptions{
		StorageDir:         t.TempDir(),
		AuthDisabled:       true,
		EnableRevision:     false,
		MaxRevisionHistory: 10,
	})
	if err != nil {
		t.Fatalf("NewWiki: %v", err)
	}
	defer func() {
		if closeErr := w.Close(); closeErr != nil {
			t.Errorf("close wiki: %v", closeErr)
		}
	}()

	if !w.ControlStatus(context.Background()).AuthDisabled {
		t.Fatal("the status does not report the disabled instance")
	}
	if _, err := w.ControlVerifyCredentials(context.Background(), "admin", "adminpassword"); err == nil {
		t.Fatal("a disable-auth instance authenticated somebody")
	}
}
