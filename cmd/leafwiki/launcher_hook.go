package main

import "context"

// launchLauncherHook is installed by the desktop build (launcher_entry.go).
// Server builds leave it nil and go straight to the server path, so no launcher
// code is reachable from them (FR-103, C-3).
var launchLauncherHook func(ctx context.Context) (handled bool, err error)
