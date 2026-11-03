package launcher

import (
	"slices"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

func testFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "port", Category: "Server", Value: "8080", Usage: "port to run the server on"},
		&cli.BoolFlag{Name: "snapshot", Category: "Snapshots", Value: true, Usage: "enable snapshots"},
		&cli.IntFlag{Name: "editor-limit", Category: "Auth", Value: 5, Usage: "editor limit"},
		&cli.DurationFlag{Name: "snapshot-interval", Category: "Snapshots", Value: time.Hour, Usage: "interval"},
		&cli.StringSliceFlag{Name: "weird", Usage: "unsupported type"},
		&cli.StringFlag{Name: "jwt-secret", Category: "Authentication"},
		&cli.StringFlag{Name: "base-path", Category: "Server", Usage: "URL prefix"},
	}
}

func TestCollect_MapsEveryFlagIntoARow(t *testing.T) {
	params := Collect(testFlags())
	if len(params) != 7 {
		t.Fatalf("Collect() returned %d rows, want 7", len(params))
	}

	byName := map[string]Param{}
	for _, param := range params {
		byName[param.Name] = param
	}

	if got := byName["port"]; got.Kind != KindString || got.Default != "8080" || got.Group != "Server" || got.Changed {
		t.Fatalf("port row = %+v", got)
	}
	if got := byName["snapshot"]; got.Kind != KindBool || got.Default != "true" {
		t.Fatalf("snapshot row = %+v", got)
	}
	if got := byName["editor-limit"]; got.Kind != KindInt || got.Default != "5" {
		t.Fatalf("editor-limit row = %+v", got)
	}
	if got := byName["snapshot-interval"]; got.Kind != KindDuration {
		t.Fatalf("snapshot-interval kind = %v", got.Kind)
	}
	if got := byName["weird"]; got.Kind != KindUnsupported {
		t.Fatalf("weird kind = %v", got.Kind)
	}
}

// The table follows the CLI's declaration order so the sections read like
// --help, not like an alphabetical dump.
func TestCollect_KeepsDeclarationOrder(t *testing.T) {
	params := Collect(testFlags())
	names := make([]string, 0, len(params))
	for _, param := range params {
		names = append(names, param.Name)
	}
	want := []string{"port", "snapshot", "editor-limit", "snapshot-interval", "weird", "jwt-secret", "base-path"}
	if !slices.Equal(names, want) {
		t.Fatalf("Collect() = %v, want %v", names, want)
	}
}

func TestEditable_ExcludesTheLauncherOwnedFlags(t *testing.T) {
	for _, name := range []string{"data-dir", "public-access", "disable-auth", "jwt-secret", "admin-password", "host", "unix-socket", "log-format"} {
		if Editable(name) {
			t.Errorf("Editable(%q) = true, want false (FR-511)", name)
		}
	}
	for _, name := range []string{"port", "base-path", "vectors"} {
		if !Editable(name) {
			t.Errorf("Editable(%q) = false, want true", name)
		}
	}
}

func TestVisible_ChangedRowsAndTheShortList(t *testing.T) {
	params := Collect(testFlags())
	byName := map[string]Param{}
	for _, param := range params {
		byName[param.Name] = param
	}

	if !Visible(byName["port"]) {
		t.Error("port should be visible without the advanced toggle (FR-505)")
	}
	if Visible(byName["base-path"]) {
		t.Error("base-path should wait behind the advanced toggle")
	}

	changed := byName["base-path"]
	changed.Value = "/wiki"
	changed.Changed = true
	if !Visible(changed) {
		t.Error("a changed row should be visible")
	}
}

func TestFilter_MatchesNameAndUsage(t *testing.T) {
	params := Collect(testFlags())

	if got := Filter(params, "snapshot", false); len(got) != 2 {
		t.Fatalf("Filter(snapshot) returned %d rows, want 2", len(got))
	}
	if got := Filter(params, "URL prefix", false); len(got) != 1 || got[0].Name != "base-path" {
		t.Fatalf("Filter(URL prefix) = %v, want base-path", got)
	}
	if got := Filter(params, "", true); len(got) != 0 {
		t.Fatalf("Filter(onlyChanged) returned %v, want nothing", got)
	}
}

func TestChangedParams_KeepsOnlyEditableChanges(t *testing.T) {
	params := Collect(testFlags())
	for i := range params {
		switch params[i].Name {
		case "port":
			params[i].Value = "9090"
			params[i].Changed = true
		case "jwt-secret":
			params[i].Value = "leak"
			params[i].Changed = true
		}
	}

	got := ChangedParams(params)
	if len(got) != 1 || got["port"] != "9090" {
		t.Fatalf("ChangedParams() = %v, want only port", got)
	}
}
