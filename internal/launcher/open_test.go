package launcher

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func captureRunner(calls *[][]string) Runner {
	return func(name string, args ...string) error {
		*calls = append(*calls, append([]string{name}, args...))
		return nil
	}
}

// failingRunner records every call and fails them all, so a test can count how
// many attempts a target really gets.
func failingRunner(calls *[][]string) Runner {
	return func(name string, args ...string) error {
		*calls = append(*calls, append([]string{name}, args...))
		return errFake
	}
}

func lastArg(call []string) string {
	if len(call) == 0 {
		return ""
	}
	return call[len(call)-1]
}

// Every platform hands the target as the last argument and stops after the first
// opener that works. A reordered or truncated command line breaks the open.
func TestOpenURL_PassesTheURLAsTheLastArgumentAndStops(t *testing.T) {
	url := "http://127.0.0.1:51234/wiki"
	var calls [][]string
	if err := OpenURL(captureRunner(&calls), url); err != nil {
		t.Fatalf("OpenURL() error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1: a failing first opener is the only reason to continue", len(calls))
	}
	if got := lastArg(calls[0]); got != url {
		t.Fatalf("call %v ends with %q, want the URL", calls[0], got)
	}

	switch runtime.GOOS {
	case "windows":
		if calls[0][0] != "explorer" {
			t.Fatalf("windows first opener = %q, want explorer: it is the only one that starts the browser at the interactive user's integrity level", calls[0][0])
		}
	case "darwin":
		if calls[0][0] != "open" {
			t.Fatalf("darwin opener = %q, want open", calls[0][0])
		}
	default:
		if calls[0][0] != "xdg-open" {
			t.Fatalf("linux opener = %q, want xdg-open", calls[0][0])
		}
	}
}

// The Windows fallback is the documented contract, not whatever happens to be
// next in a slice: rundll32 exists on every Windows install.
func TestOpenURL_WindowsFallsBackToRundll32(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the fallback is Windows-specific")
	}
	url := "http://127.0.0.1:51234/wiki"
	var calls [][]string
	failingFirst := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return errFake
		}
		return nil
	}
	if err := OpenURL(failingFirst, url); err != nil {
		t.Fatalf("OpenURL() error = %v, want success once the second opener works", err)
	}
	if len(calls) != 2 {
		t.Fatalf("runner called %d times, want 2: explorer, then rundll32", len(calls))
	}
	want := [][]string{{"explorer", url}, {"rundll32", "url.dll,FileProtocolHandler", url}}
	for i, call := range calls {
		if !slices.Equal(call, want[i]) {
			t.Fatalf("call %d = %v, want %v", i, call, want[i])
		}
	}
}

// A target that no opener accepted is a failure, and the message names both the
// target and the last real error.
func TestOpenURL_ReportsTheLastFailureAndTheTarget(t *testing.T) {
	url := "https://example.com/page"
	var calls [][]string
	err := OpenURL(failingRunner(&calls), url)
	if err == nil {
		t.Fatal("OpenURL() error = nil, want a failure")
	}
	if len(calls) != len(platformOpenURLs()) {
		t.Fatalf("runner called %d times, want %d: one attempt per opener, no retries", len(calls), len(platformOpenURLs()))
	}
	if !strings.Contains(err.Error(), url) || !strings.Contains(err.Error(), errFake.Error()) {
		t.Fatalf("error = %q, want it to name the target and the last failure", err)
	}
}

// An empty candidate list must fail loudly: reporting success would hide a build
// or platform where the wiki simply never opens.
func TestRunTarget_WithoutAnyOpener_Fails(t *testing.T) {
	err := runTarget(nil, "https://example.com")
	if err == nil {
		t.Fatal("runTarget() error = nil, want a failure when no opener exists")
	}
	if !strings.Contains(err.Error(), "no system opener available") || !strings.Contains(err.Error(), runtime.GOOS) {
		t.Fatalf("error = %q, want it to name the missing opener and the platform", err)
	}
}

func TestOpenDir_PassesTheDirectory(t *testing.T) {
	var calls [][]string
	if err := OpenDir(captureRunner(&calls), "/data/wiki"); err != nil {
		t.Fatalf("OpenDir() error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(calls))
	}
	if got := lastArg(calls[0]); got != "/data/wiki" {
		t.Fatalf("call %v ends with %q, want the directory", calls[0], got)
	}
}

var errFake = fakeError("no browser")

type fakeError string

func (e fakeError) Error() string { return string(e) }
