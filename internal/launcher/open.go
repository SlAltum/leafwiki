package launcher

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Runner starts one helper command. It is injected so tests can assert the
// exact command line without opening anything (FR-801/803).
type Runner func(name string, args ...string) error

// DefaultRunner starts the command and does not wait for it.
func DefaultRunner(name string, args ...string) error {
	command := exec.Command(name, args...)
	if err := command.Start(); err != nil {
		return err
	}
	// Nothing waits for the child: releasing it here keeps the helper from
	// becoming a zombie for the launcher's lifetime.
	return command.Process.Release()
}

// OpenURL opens url in the default browser (FR-801).
func OpenURL(runner Runner, url string) error {
	return runTarget(runner, url, platformOpenURLs()...)
}

// OpenDir opens dir in the system file manager (FR-803).
func OpenDir(runner Runner, dir string) error {
	return runTarget(runner, dir, platformOpenDir())
}

// runTarget tries each opener in order and reports the last failure. The list
// exists for openers that are only a fallback when the first one is missing.
func runTarget(runner Runner, target string, commands ...openCommand) error {
	if runner == nil {
		runner = DefaultRunner
	}

	var lastErr error
	for _, command := range commands {
		if command.name == "" {
			continue
		}
		if err := runner(command.name, append(append([]string{}, command.args...), target)...); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("open %s: %w", target, lastErr)
	}
	return fmt.Errorf("no system opener available on %s", runtime.GOOS)
}

type openCommand struct {
	name string
	args []string
}

func platformOpenURLs() []openCommand {
	switch runtime.GOOS {
	case "windows":
		// explorer.exe hands the URL to the shell as the interactive user, so a
		// launcher that was itself started elevated does not drag the browser up
		// with it (an elevated Edge refuses to share its instance and asks about
		// restarting with normal privileges). rundll32 is the fallback: it
		// inherits the caller's integrity level but is always present.
		return []openCommand{
			{name: "explorer"},
			{name: "rundll32", args: []string{"url.dll,FileProtocolHandler"}},
		}
	case "darwin":
		return []openCommand{{name: "open"}}
	default:
		return []openCommand{{name: "xdg-open"}}
	}
}

func platformOpenDir() openCommand {
	switch runtime.GOOS {
	case "windows":
		return openCommand{name: "explorer"}
	case "darwin":
		return openCommand{name: "open"}
	default:
		return openCommand{name: "xdg-open"}
	}
}
