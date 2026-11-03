//go:build launcher && !windows

package main

import "log/slog"

// showMessageBox has no portable equivalent; the log line is the fallback.
func showMessageBox(title, text string) {
	slog.Error("launcher failed before a window existed", "title", title, "message", text)
}
