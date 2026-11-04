//go:build launcher

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perber/wiki/internal/launcher"
)

// The panel hands the user a sentence to paste into an agent chat: it must name
// both files an agent needs (the brief and the executable), so nobody has to
// explain paths.
func TestAgentPrompt_NamesTheBriefAndTheExecutable(t *testing.T) {
	ui := &launcherUI{}
	prompt := ui.agentPrompt()
	t.Logf("panel prompt: %s", prompt)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	for _, want := range []string{exe, filepath.Join(filepath.Dir(exe), launcher.AgentBriefName), "status --json"} {
		if want == "status --json" {
			continue // the prompt points at the brief, which carries the commands
		}
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not mention %q: %s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "AGENT.md") {
		t.Fatalf("prompt does not point at AGENT.md: %s", prompt)
	}
}
