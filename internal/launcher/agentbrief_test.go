package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The brief next to the executable is what an agent reads: it must name the
// binary, the socket and the password file, and it must be rewritten on every
// start so a changed port is never stale.
func TestWriteAgentBrief_NamesTheHandles(t *testing.T) {
	exeDir := t.TempDir()
	dataDir := filepath.Join(exeDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := WriteAgentBrief(exeDir, dataDir, "http://127.0.0.1:47831/"); err != nil {
		t.Fatalf("WriteAgentBrief: %v", err)
	}
	path := filepath.Join(exeDir, AgentBriefName)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	body := string(payload)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	for _, want := range []string{
		exe,
		filepath.Join(dataDir, "control.sock"),
		filepath.Join(exeDir, "admin.password"),
		"http://127.0.0.1:47831/",
		"status --json",
		"page delete",
		"75",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("brief does not mention %q:\n%s", want, body)
		}
	}

	if err := WriteAgentBrief(exeDir, dataDir, "http://127.0.0.1:47832/"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten brief: %v", err)
	}
	if strings.Contains(string(again), "47831") || !strings.Contains(string(again), "47832") {
		t.Fatalf("the brief was not rewritten with the new port:\n%s", again)
	}
}
