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
	return runTarget(runner, url, platformOpenURL())
}

// OpenDir opens dir in the system file manager (FR-803).
func OpenDir(runner Runner, dir string) error {
	return runTarget(runner, dir, platformOpenDir())
}

// runTarget calls the platform's opener, falling back to the other platform's
// spelling only when there is none.
func runTarget(runner Runner, target string, command openCommand) error {
	if runner == nil {
		runner = DefaultRunner
	}
	if command.name == "" {
		return fmt.Errorf("no system opener available on %s", runtime.GOOS)
	}
	if err := runner(command.name, append(command.args, target)...); err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	return nil
}

type openCommand struct {
	name string
	args []string
}

func platformOpenURL() openCommand {
	switch runtime.GOOS {
	case "windows":
		// The documented shell handler for a URL; "start" is a cmd built-in and
		// needs a shell to exist.
		return openCommand{name: "rundll32", args: []string{"url.dll,FileProtocolHandler"}}
	case "darwin":
		return openCommand{name: "open"}
	default:
		return openCommand{name: "xdg-open"}
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
