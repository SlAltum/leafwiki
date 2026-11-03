//go:build launcher && windows

package main

import (
	"context"
	"image"
	"image/color"
	"log/slog"
	"syscall"
	"time"
	"unsafe"
)

// The window icon follows the wiki's own logo (FR-110): gio has no icon API, so
// this is a Windows-side override of the icon the executable was built with.
// The logo reaches us as an already decoded PNG/JPEG, so nothing is converted
// to .ico at runtime - the icon is built straight from the pixels.

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	procEnumWindows      = user32.NewProc("EnumWindows")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procGetWindowPID     = user32.NewProc("GetWindowThreadProcessId")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procCreateIconInd    = user32.NewProc("CreateIconIndirect")
	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap     = gdi32.NewProc("CreateBitmap")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
)

const (
	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
	biRGB     = 0
	dibRGBCol = 0

	// icons stay referenced by the window for its whole life
	iconSize = 32
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	Icon     int32
	XHotspot uint32
	YHotspot uint32
	Mask     syscall.Handle
	Color    syscall.Handle
}

// applyWindowIcon makes the wiki's logo the taskbar and Alt-Tab icon. It polls
// briefly for the window because the handle exists only after the first event.
func applyWindowIcon(ctx context.Context, logo image.Image) {
	if logo == nil {
		return
	}

	go func() {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if hwnd := ownWindowHandle(); hwnd != 0 {
				if hicon := iconFromImage(logo, iconSize); hicon != 0 {
					_, _, _ = procSendMessageW.Call(uintptr(hwnd), wmSetIcon, iconBig, uintptr(hicon))
					_, _, _ = procSendMessageW.Call(uintptr(hwnd), wmSetIcon, iconSmall, uintptr(hicon))
					slog.Info("window icon follows the wiki logo", "size", iconSize)
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
}

// ownWindowHandle finds the first visible top-level window of this process.
func ownWindowHandle() syscall.Handle {
	var found syscall.Handle
	pid := uint32(syscall.Getpid())

	callback := syscall.NewCallback(func(hwnd syscall.Handle, _ uintptr) uintptr {
		var windowPID uint32
		_, _, _ = procGetWindowPID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&windowPID)))
		if windowPID != pid {
			return 1
		}
		if visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); visible == 0 {
			return 1
		}
		found = hwnd
		return 0
	})

	_, _, _ = procEnumWindows.Call(callback, 0)
	return found
}

// iconFromImage builds an HICON from the decoded pixels.
func iconFromImage(src image.Image, size int) syscall.Handle {
	if size <= 0 {
		size = 32
	}

	header := bitmapInfoHeader{
		Size:  uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width: int32(size),
		// a DIB is bottom-up unless the height is negative
		Height:      int32(-size),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
		SizeImage:   uint32(size * size * 4),
	}

	var bits unsafe.Pointer
	dib, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), dibRGBCol,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		return 0
	}
	defer func() {
		_, _, _ = procDeleteObject.Call(dib)
	}()

	// straight (non-premultiplied) BGRA, scaled with nearest-neighbour: an icon
	// this small does not benefit from filtering.
	pixels := unsafe.Slice((*byte)(bits), size*size*4)
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	for y := 0; y < size; y++ {
		sy := bounds.Min.Y + y*sh/size
		for x := 0; x < size; x++ {
			sx := bounds.Min.X + x*sw/size
			r, g, b, a := src.At(sx, sy).RGBA()
			if a == 0 {
				continue
			}
			offset := (y*size + x) * 4
			pixels[offset+0] = byte(b >> 8)
			pixels[offset+1] = byte(g >> 8)
			pixels[offset+2] = byte(r >> 8)
			pixels[offset+3] = byte(a >> 8)
		}
	}

	mask, _, _ := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	if mask == 0 {
		return 0
	}

	info := iconInfo{Color: syscall.Handle(dib), Mask: syscall.Handle(mask)}
	hicon, _, _ := procCreateIconInd.Call(uintptr(unsafe.Pointer(&info)))
	_, _, _ = procDeleteObject.Call(mask)
	return syscall.Handle(hicon)
}

var _ = color.NRGBA{}
