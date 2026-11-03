//go:build launcher && !windows

package main

import (
	"context"
	"image"
)

// applyWindowIcon has no portable equivalent outside Windows; the launcher only
// ships for Windows.
func applyWindowIcon(context.Context, image.Image) {}
