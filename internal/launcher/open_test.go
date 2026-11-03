package launcher

import (
	"runtime"
	"slices"
	"testing"
)

func captureRunner(calls *[][]string) Runner {
	return func(name string, args ...string) error {
		*calls = append(*calls, append([]string{name}, args...))
		return nil
	}
}

func TestOpenURL_UsesThePlatformOpener(t *testing.T) {
	var calls [][]string
	if err := OpenURL(captureRunner(&calls), "http://127.0.0.1:51234/wiki"); err != nil {
		t.Fatalf("OpenURL() error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(calls))
	}

	call := calls[0]
	if !slices.Contains(call, "http://127.0.0.1:51234/wiki") {
		t.Fatalf("call %v does not carry the URL", call)
	}

	switch runtime.GOOS {
	case "windows":
		if call[0] != "rundll32" || len(call) != 3 {
			t.Fatalf("windows call = %v, want rundll32 url.dll,FileProtocolHandler <url>", call)
		}
	case "darwin":
		if call[0] != "open" {
			t.Fatalf("darwin call = %v, want open", call)
		}
	default:
		if call[0] != "xdg-open" {
			t.Fatalf("linux call = %v, want xdg-open", call)
		}
	}
}

func TestOpenDir_PassesTheDirectory(t *testing.T) {
	var calls [][]string
	if err := OpenDir(captureRunner(&calls), "/data/wiki"); err != nil {
		t.Fatalf("OpenDir() error = %v", err)
	}
	if len(calls) != 1 || !slices.Contains(calls[0], "/data/wiki") {
		t.Fatalf("calls = %v, want one call carrying the directory", calls)
	}
}

func TestOpenURL_ReportsFailures(t *testing.T) {
	failing := func(string, ...string) error { return errFake }
	if err := OpenURL(failing, "https://example.com"); err == nil {
		t.Fatal("OpenURL() error = nil, want the runner's failure surfaced")
	}
}

var errFake = fakeError("no browser")

type fakeError string

func (e fakeError) Error() string { return string(e) }
