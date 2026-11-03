//go:build launcher && windows

package main

import (
	"syscall"
	"unsafe"
)

// showMessageBox is the last resort before a window exists: a plain system
// dialog with one sentence (FR-115).
func showMessageBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}

	const mbIconWarning = 0x00000030
	_, _, _ = messageBox.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbIconWarning),
	)
}
