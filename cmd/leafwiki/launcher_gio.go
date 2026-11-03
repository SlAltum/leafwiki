//go:build launcher

package main

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/perber/wiki/internal/launcher"
)

// launcherUI is the window. It only lays out and forwards events; every
// decision lives in internal/launcher (NFR-8). The geometry follows the
// prototype's stylesheet, including its 720 px breakpoint: below it the frame
// disappears, the ticket stacks and the title row becomes a column.
type launcherUI struct {
	window  *app.Window
	ctrl    *launcher.Controller
	dataDir string
	theme   *material.Theme
	mono    font.Font

	page     int
	tabBasic widget.Clickable
	tabAdv   widget.Clickable
	openWiki widget.Clickable
	restart  widget.Clickable
	stop     widget.Clickable
	openDir  widget.Clickable
	refresh  widget.Clickable
	copyURL  widget.Clickable
	copyPass widget.Clickable
	scroll   layout.List

	btnMin   widget.Clickable
	btnMax   widget.Clickable
	btnClose widget.Clickable

	settings settingsPage
	notice   string

	// focus is the keyboard ring over this frame's controls (policy lives in
	// internal/launcher); focusTargets maps its index to the widget.
	focus        launcher.Focus
	focusTargets []*widget.Clickable
	claimedFocus bool

	// startedAt and heapLogged are for the one-shot memory line: support needs
	// to know whether the process is holding Go heap or native memory.
	startedAt  time.Time
	heapLogged bool
}

func newLauncherUI(ctrl *launcher.Controller, dataDir string) *launcherUI {
	return &launcherUI{
		ctrl:    ctrl,
		dataDir: dataDir,
		theme:   newTheme(),
		mono:    font.Font{Typeface: monoTypeface},
		scroll:  layout.List{Axis: layout.Vertical},
	}
}

func (u *launcherUI) run(w *app.Window) error {
	u.window = w
	var ops op.Ops

	for {
		event := w.Event()
		switch event := event.(type) {
		case app.DestroyEvent:
			return event.Err
		case app.FrameEvent:
			u.logHeapOnce()
			// Every frame starts from an empty op list: without the reset the
			// ops buffer grows for the life of the window (hundreds of MB per
			// minute at 60 fps).
			ops.Reset()
			u.focus.Begin()
			u.focusTargets = u.focusTargets[:0]
			gtx := layout.Context{
				Ops:         &ops,
				Metric:      event.Metric,
				Constraints: layout.Exact(event.Size),
				Source:      event.Source,
				Now:         event.Now,
			}
			u.layout(gtx)
			u.focus.End()
			event.Frame(&ops)
		}
	}
}

func (u *launcherUI) compact(gtx layout.Context) bool {
	return gtx.Constraints.Max.X < gtx.Dp(narrowWidth)
}

func (u *launcherUI) layout(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, colFrame, clip.Rect{Max: gtx.Constraints.Max}.Op())
	u.handleClicks(gtx)
	u.handleWindowClicks(gtx)
	u.handleKeyboard(gtx)
	u.keepAnimating(gtx)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.headbar(gtx) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.sheet(gtx) }),
	)
}

// sheet is the white card the two pages live on (the prototype's .sheet): inset
// 8 px from the frame's right and bottom edge, 16 px radius, 24 px padding.
// Below the 720 px breakpoint the frame disappears and the sheet is the screen.
func (u *launcherUI) sheet(gtx layout.Context) layout.Dimensions {
	compact := u.compact(gtx)

	padInset := layout.Inset{Top: pad, Bottom: pad, Left: pad, Right: pad}
	if compact {
		padInset = layout.Inset{Top: padCompact, Bottom: unit.Dp(8), Left: unit.Dp(14), Right: unit.Dp(14)}
	}

	page := func(gtx layout.Context) layout.Dimensions {
		if u.page == 1 {
			return u.settings.layout(u, gtx)
		}
		return u.basicPage(gtx)
	}

	if compact {
		paint.FillShape(gtx.Ops, colSurface, clip.Rect{Max: gtx.Constraints.Max}.Op())
		return padInset.Layout(gtx, page)
	}

	return layout.Inset{Right: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		macro := op.Record(gtx.Ops)
		dims := padInset.Layout(gtx, page)
		call := macro.Stop()

		rect := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(radiusSheet))
		paint.FillShape(gtx.Ops, colSurface, rect.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, colBorder, clip.Stroke{Path: rect.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())
		call.Add(gtx.Ops)
		return dims
	})
}

// keepAnimating redraws while the controller works in the background (C-8).
// startHeapProfile writes a Go heap profile once the window has settled, for
// reports of a large working set. Off unless LEAFWIKI_LAUNCHER_HEAP_PROFILE
// names a file.
func startHeapProfile() {
	path := os.Getenv("LEAFWIKI_LAUNCHER_HEAP_PROFILE")
	if path == "" {
		return
	}
	go func() {
		time.Sleep(60 * time.Second)
		file, err := os.Create(path)
		if err != nil {
			slog.Error("cannot create heap profile", "path", path, "error", err)
			return
		}
		defer func() {
			if err := file.Close(); err != nil {
				slog.Error("cannot close heap profile", "error", err)
			}
		}()
		runtime.GC()
		if err := pprof.WriteHeapProfile(file); err != nil {
			slog.Error("cannot write heap profile", "error", err)
			return
		}
		slog.Info("heap profile written", "path", path)
	}()
}

// logHeapOnce records the Go heap ten seconds in. Native memory (the GPU
// context, the ONNX Runtime arenas) does not show up here, so the line tells
// the two apart when someone reports a large working set.
func (u *launcherUI) logHeapOnce() {
	if u.heapLogged {
		return
	}
	if u.startedAt.IsZero() {
		u.startedAt = time.Now()
		return
	}
	if time.Since(u.startedAt) < 10*time.Second {
		return
	}
	u.heapLogged = true

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	slog.Info("launcher memory",
		"heap_alloc_mb", stats.HeapAlloc/(1<<20),
		"heap_sys_mb", stats.HeapSys/(1<<20),
		"go_sys_mb", stats.Sys/(1<<20),
	)
}

func (u *launcherUI) keepAnimating(gtx layout.Context) {
	status := u.ctrl.Status()
	switch status.State {
	case launcher.StateStarting, launcher.StateStopping:
		// A start or stop is in flight: keep the state word and the log moving.
		gtx.Execute(op.InvalidateCmd{})
	case launcher.StateRunning:
		// One redraw per second for the uptime. Asking through the frame
		// coalesces with Gio's own scheduling; a timer per frame would pile
		// up timers and pin the loop at display speed.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second)})
	}
}

// registerFocus records a control in the Tab order and reports whether it holds
// the ring.
func (u *launcherUI) registerFocus(btn *widget.Clickable) (index int, focused bool) {
	index, focused = u.focus.Register()
	u.focusTargets = append(u.focusTargets, btn)
	return index, focused
}

// focusRing outlines the control that holds the keyboard focus. The stroke sits
// outside the control's own border so it never covers its label.
func (u *launcherUI) focusRing(gtx layout.Context, dims layout.Dimensions, radius unit.Dp) {
	inset := gtx.Dp(2)
	rect := image.Rect(-inset, -inset, dims.Size.X+inset, dims.Size.Y+inset)
	corner := gtx.Dp(radius + 2)
	path := clip.RRect{Rect: rect, NW: corner, NE: corner, SE: corner, SW: corner}.Path(gtx.Ops)
	stroke := clip.Stroke{Path: path, Width: float32(inset)}.Op().Push(gtx.Ops)
	paint.Fill(gtx.Ops, colAccent)
	stroke.Pop()
}

// handleKeyboard moves the focus with Tab and acts on Enter. Escape hands the
// keyboard back to the window without acting on anything.
func (u *launcherUI) handleKeyboard(gtx layout.Context) {
	if !u.claimedFocus {
		// Nothing else holds the keyboard yet: take it, so Tab reaches the ring
		// instead of being dropped.
		u.claimedFocus = true
		gtx.Execute(key.FocusCmd{Tag: u})
	}

	filters := []event.Filter{
		key.Filter{Name: key.NameTab},
		key.Filter{Name: key.NameEnter},
		key.Filter{Name: key.NameEscape},
	}
	event.Op(gtx.Ops, u)

	for {
		pressed, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		stroke, ok := pressed.(key.Event)
		if !ok || stroke.State != key.Press {
			continue
		}
		slog.Debug("launcher key", "name", stroke.Name)
		switch stroke.Name {
		case key.NameTab:
			if index, visible := u.focus.Step(stroke.Modifiers&key.ModShift != 0); visible {
				slog.Info("keyboard focus moved", "index", index, "of", len(u.focusTargets))
			}
			// The window's own tag holds the keyboard so the ring, not a text
			// field, receives the next key.
			gtx.Execute(key.FocusCmd{Tag: u})
		case key.NameEnter:
			u.activateFocus()
		case key.NameEscape:
			u.focus.Hide()
			gtx.Execute(key.FocusCmd{Tag: u})
		}
	}
}

// activateFocus runs the control under the ring.
func (u *launcherUI) activateFocus() {
	index, visible := u.focus.Current()
	if !visible || index < 0 || index >= len(u.focusTargets) {
		return
	}
	u.focusTargets[index].Click()
}

func (u *launcherUI) handleClicks(gtx layout.Context) {
	switch {
	case u.tabBasic.Clicked(gtx):
		u.page = 0
	case u.tabAdv.Clicked(gtx):
		u.page = 1
	}
	if u.openWiki.Clicked(gtx) {
		u.report(u.ctrl.OpenWiki())
	}
	if u.restart.Clicked(gtx) {
		u.ctrl.Restart()
	}
	if u.stop.Clicked(gtx) {
		u.ctrl.Stop()
	}
	if u.openDir.Clicked(gtx) {
		u.report(u.ctrl.OpenDataDir())
	}
	if u.refresh.Clicked(gtx) {
		u.ctrl.RecheckRefresh()
		u.ctrl.Refresh()
	}
	if u.copyURL.Clicked(gtx) {
		u.copy(gtx, u.ctrl.Status().URL)
	}
	if u.copyPass.Clicked(gtx) {
		u.copy(gtx, u.ctrl.Status().Password)
	}
}

// handleWindowClicks drives the framed-in-Gio window controls.
func (u *launcherUI) handleWindowClicks(gtx layout.Context) {
	if u.window == nil {
		return
	}
	switch {
	case u.btnMin.Clicked(gtx):
		u.window.Perform(system.ActionMinimize)
	case u.btnMax.Clicked(gtx):
		u.window.Perform(system.ActionMaximize)
	case u.btnClose.Clicked(gtx):
		u.window.Perform(system.ActionClose)
	}
}

func (u *launcherUI) report(err error) {
	if err != nil {
		u.notice = err.Error()
	}
}

func (u *launcherUI) copy(gtx layout.Context, text string) {
	if text == "" {
		return
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
	u.notice = launcher.Text.Copy
}

// headbar is the prototype's .topbar: the window has no system title bar, so
// this 48 dp strip carries the brand, the scope, the two tabs and the three
// window buttons, and it doubles as the drag handle (system.ActionMove).
//
// The height is enforced once, here: the strip paints and returns exactly 48 dp,
// and the content row is measured against that same exact height so every item
// is centred on the strip's midline.
func (u *launcherUI) headbar(gtx layout.Context) layout.Dimensions {
	compact := u.compact(gtx)
	width := gtx.Constraints.Max.X
	height := gtx.Dp(topbarHeight)

	paint.FillShape(gtx.Ops, colFrame, clip.Rect{Max: image.Pt(width, height)}.Op())

	// Drag handle first: the buttons below register after it and keep their clicks.
	area := clip.Rect{Max: image.Pt(width, height)}.Push(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	area.Pop()

	inset := layout.Inset{Left: unit.Dp(20), Right: unit.Dp(16)}
	if compact {
		inset = layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}
	}

	// Min.Y stays 0 on purpose: children must size to their content so the row
	// can centre them. An exact height here stretches every child to 48 dp and
	// they then draw their text at their own top edge, which is what made the
	// brand and the tabs sit on different lines.
	gtx.Constraints = layout.Constraints{
		Min: image.Pt(width, 0),
		Max: image.Pt(width, height),
	}

	// Measure the row at its natural height, then replay it shifted down by
	// half the leftover space. Centre-by-constraint depends on each child
	// reporting its own height; the clickable tabs report their padded box, so
	// the row as a whole is what gets centred here - exactly, every time.
	rowMacro := op.Record(gtx.Ops)
	rowDims := inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			// Icon and title come from the wiki's own branding (FR-110): the
			// configured logo, and the configured site name. The built-in mark is
			// only the fallback when the wiki has not set either.
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.brandMark(gtx, gtx.Dp(unit.Dp(16)))
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(7)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				brand := material.Body1(u.theme, u.ctrl.Branding().Name)
				brand.TextSize = fsBody
				brand.Color = colText
				brand.MaxLines = 1
				return brand.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if compact {
					return layout.Dimensions{}
				}
				text := material.Body1(u.theme, "本机 · "+shortDataDir(u.dataDir))
				text.TextSize = fsSmall
				text.Color = colFaint
				text.MaxLines = 1
				return text.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.tab(gtx, &u.tabBasic, launcher.Text.TabBasic, u.page == 0)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.tab(gtx, &u.tabAdv, launcher.Text.TabAdvanced, u.page == 1)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.windowButtons(gtx)
			}),
		)
	})

	// Stop the macro before pushing anything: a macro boundary must not sit
	// inside a transform stack (gio panics on it).
	rowCall := rowMacro.Stop()

	offset := (height - rowDims.Size.Y) / 2
	if offset < 0 {
		offset = 0
	}
	translate := op.Offset(image.Pt(0, offset)).Push(gtx.Ops)
	rowCall.Add(gtx.Ops)
	translate.Pop()

	return layout.Dimensions{Size: image.Pt(width, height)}
}

// windowButtons are the prototype's three .winbtn: 28 dp squares, 2 dp apart.
func (u *launcherUI) windowButtons(gtx layout.Context) layout.Dimensions {
	// Window chrome stays mouse-only, as in every other desktop window: Tab
	// walks the page's own controls.
	button := func(click *widget.Clickable, kind int, danger bool) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return material.Clickable(gtx, click, func(gtx layout.Context) layout.Dimensions {
				side := gtx.Dp(unit.Dp(28))
				colour := colMuted
				if danger {
					colour = colMuted
				}
				drawGlyph(gtx, kind, colour, side)
				return layout.Dimensions{Size: image.Pt(side, side)}
			})
		})
	}

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		button(&u.btnMin, glyphMinimize, false),
		layout.Rigid(layout.Spacer{Width: unit.Dp(2)}.Layout),
		button(&u.btnMax, glyphMaximize, false),
		layout.Rigid(layout.Spacer{Width: unit.Dp(2)}.Layout),
		button(&u.btnClose, glyphClose, true),
	)
}

// brandMark draws the wiki's logo, or the built-in leaf when none is set.
func (u *launcherUI) brandMark(gtx layout.Context, size int) layout.Dimensions {
	if logo := u.ctrl.Branding().Logo; logo != nil {
		gtx.Constraints = layout.Exact(image.Pt(size, size))
		image := widget.Image{Src: paint.NewImageOp(logo), Fit: widget.Contain}
		return image.Layout(gtx)
	}

	drawLeaf(gtx, size, colPositive)
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// drawLeaf fills the fallback mark: a leaf blade with a short stem.
func drawLeaf(gtx layout.Context, size int, colour color.NRGBA) {
	s := float32(size)

	var blade clip.Path
	blade.Begin(gtx.Ops)
	blade.MoveTo(f32.Pt(s*0.24, s*0.80))
	blade.CubeTo(f32.Pt(s*0.30, s*0.32), f32.Pt(s*0.60, s*0.10), f32.Pt(s*0.86, s*0.14))
	blade.CubeTo(f32.Pt(s*0.90, s*0.46), f32.Pt(s*0.60, s*0.74), f32.Pt(s*0.24, s*0.80))
	blade.Close()
	paint.FillShape(gtx.Ops, colour, clip.Outline{Path: blade.End()}.Op())

	var stem clip.Path
	stem.Begin(gtx.Ops)
	stem.MoveTo(f32.Pt(s*0.24, s*0.80))
	stem.LineTo(f32.Pt(s*0.10, s*0.94))
	paint.FillShape(gtx.Ops, colour,
		clip.Stroke{Path: stem.End(), Width: float32(gtx.Dp(unit.Dp(1.2)))}.Op())
}

const (
	glyphMinimize = iota
	glyphMaximize
	glyphClose
)

// drawGlyph strokes one window-control glyph in a side x side box.
func drawGlyph(gtx layout.Context, kind int, colour color.NRGBA, side int) {
	half := float32(side) / 2
	arm := half * 0.42
	width := float32(gtx.Dp(unit.Dp(1.2)))

	var path clip.Path
	path.Begin(gtx.Ops)

	switch kind {
	case glyphMinimize:
		path.MoveTo(f32.Pt(half-arm, half))
		path.LineTo(f32.Pt(half+arm, half))
	case glyphMaximize:
		path.MoveTo(f32.Pt(half-arm, half-arm))
		path.LineTo(f32.Pt(half+arm, half-arm))
		path.LineTo(f32.Pt(half+arm, half+arm))
		path.LineTo(f32.Pt(half-arm, half+arm))
		path.Close()
	default:
		path.MoveTo(f32.Pt(half-arm, half-arm))
		path.LineTo(f32.Pt(half+arm, half+arm))
		path.MoveTo(f32.Pt(half+arm, half-arm))
		path.LineTo(f32.Pt(half-arm, half+arm))
	}

	paint.FillShape(gtx.Ops, colour, clip.Stroke{Path: path.End(), Width: width}.Op())
}

// tab is the prototype's .tab: 10 dp side padding, the 2 px underline sits
// under the text (border-bottom), never appended to the row's height.
func (u *launcherUI) tab(gtx layout.Context, clickable *widget.Clickable, label string, active bool) layout.Dimensions {
	_, focused := u.registerFocus(clickable)
	dims := material.Clickable(gtx, clickable, func(gtx layout.Context) layout.Dimensions {
		style := material.Body1(u.theme, label)
		style.TextSize = fsBody
		style.MaxLines = 1
		if active {
			style.Color = colText
		} else {
			style.Color = colMuted
		}

		return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10), Top: unit.Dp(7), Bottom: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Stacked(style.Layout),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					width := gtx.Constraints.Min.X
					if width == 0 {
						return layout.Dimensions{}
					}
					if active {
						paint.FillShape(gtx.Ops, colAccent, clip.Rect{Max: image.Pt(width, 2)}.Op())
					}
					return layout.Dimensions{Size: image.Pt(width, 2)}
				}),
			)
		})
	})
	if focused {
		u.focusRing(gtx, dims, radiusControl)
	}
	return dims
}

// statePill is the 22 dp status pill (.pill, 4 px radius).
func (u *launcherUI) statePill(gtx layout.Context, status launcher.Status) layout.Dimensions {
	bg, ink := colPillNeutralBg, colPillNeutralInk
	switch status.State {
	case launcher.StateRunning:
		bg, ink = colPillOKBg, colPillOKInk
	case launcher.StateStarting, launcher.StateStopping, launcher.StateClosing:
		bg, ink = colPillWarnBg, colPillWarnInk
	case launcher.StateFailed:
		bg, ink = colPillBadBg, colPillBadInk
	}

	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				paint.FillShape(gtx.Ops, ink, clip.Ellipse(image.Rect(0, 0, 6, 6)).Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(6, 6)}
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, launcher.StatusWord(string(status.State)))
				label.TextSize = fsSmall
				label.Color = ink
				return label.Layout(gtx)
			}),
		)
	})
	call := macro.Stop()

	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(unit.Dp(4))).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// basicPage: title row, ticket, finding, operations and the log, stacked with
// the prototype's 24 px gutter (12 px when compact).
func (u *launcherUI) basicPage(gtx layout.Context) layout.Dimensions {
	status := u.ctrl.Status()
	gap := gutter
	if u.compact(gtx) {
		gap = gapCompact
	}

	items := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { return u.titleRow(gtx, status) },
		func(gtx layout.Context) layout.Dimensions { return u.ticket(gtx, status) },
	}
	if u.ctrl.PendingChanges() {
		items = append(items, func(gtx layout.Context) layout.Dimensions { return u.finding(gtx) })
	}
	items = append(items, func(gtx layout.Context) layout.Dimensions { return u.opsBar(gtx, status) })
	items = append(items, func(gtx layout.Context) layout.Dimensions { return u.logPanel(gtx) })

	return u.scroll.Layout(gtx, len(items), func(gtx layout.Context, index int) layout.Dimensions {
		return layout.Inset{Bottom: gap}.Layout(gtx, items[index])
	})
}

func (u *launcherUI) titleRow(gtx layout.Context, status launcher.Status) layout.Dimensions {
	compact := u.compact(gtx)

	title := launcher.Text.AppTitle
	if status.State == launcher.StateFailed && status.Failed != "" {
		title = launcher.Text.AppTitle
	}

	meta := ""
	if status.State == launcher.StateRunning {
		meta = launcher.UpTime(u.ctrl.Uptime())
		if status.Port != 0 {
			meta += " · 端口 " + fmt.Sprint(status.Port)
		}
	}
	if u.notice != "" {
		meta += "  " + u.notice
	}

	heading := func(gtx layout.Context) layout.Dimensions {
		text := material.H6(u.theme, title)
		text.TextSize = fsTitle
		if compact {
			text.TextSize = fsTitleSm
		}
		text.Color = colText
		return text.Layout(gtx)
	}

	sub := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.statePill(gtx, status) }),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, meta)
				label.TextSize = fsSmall
				label.Color = colMuted
				return label.Layout(gtx)
			}),
		)
	}

	note := func(gtx layout.Context) layout.Dimensions {
		label := material.Body1(u.theme, launcher.Text.TitleNote)
		label.TextSize = fsSmall
		if compact {
			label.TextSize = fsTiny
		}
		label.Color = colFaint
		return label.Layout(gtx)
	}

	detail := func(gtx layout.Context) layout.Dimensions {
		if status.State != launcher.StateFailed || status.Detail == "" {
			return layout.Dimensions{}
		}
		label := material.Body1(u.theme, status.Detail)
		label.TextSize = fsSmall
		label.Color = colNegative
		return label.Layout(gtx)
	}

	block := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, sub)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				top := unit.Dp(8)
				if compact {
					top = unit.Dp(6)
				}
				return layout.Inset{Top: top}.Layout(gtx, note)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, detail)
			}),
		)
	}

	// The prototype puts the one primary action in the title row, not at the
	// bottom of the page: top-right when wide, full width under the note when
	// compact (.title-row .btn-primary).
	openButton := func(gtx layout.Context) layout.Dimensions {
		if !compact {
			return u.button(gtx, &u.openWiki, launcher.Text.OpenWiki, true)
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y = gtx.Dp(unit.Dp(44))
		return u.button(gtx, &u.openWiki, launcher.Text.OpenWiki, true)
	}

	if compact {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(block),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, openButton)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		)
	}

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
		layout.Flexed(1, block),
		layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
		layout.Rigid(openButton),
		layout.Rigid(layout.Spacer{Width: unit.Dp(0)}.Layout),
	)
}

// ticket is the hand-off sheet: three cells side by side, stacked when compact
// (.ticket / .ticket .cell).
func (u *launcherUI) ticket(gtx layout.Context, status launcher.Status) layout.Dimensions {
	compact := u.compact(gtx)
	stats := u.ctrl.Stats()

	dataDirLine := shortDataDir(u.dataDir)
	if stats.Pages > 0 || stats.Assets > 0 {
		dataDirLine += fmt.Sprintf("   %d 个页面 · %d 个附件 · %s", stats.Pages, stats.Assets, humanBytes(stats.Bytes))
	}

	cells := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions {
			return u.ticketCell(gtx, launcher.Text.Address, status.URL, "", &u.copyURL, true)
		},
		func(gtx layout.Context) layout.Dimensions {
			password := status.Password
			hint := launcher.Text.CopyHint
			copyBtn := &u.copyPass
			if status.CredState == launcher.CredUnknown {
				// The value on screen is not the password, so copying it would
				// hand over a string that cannot log anyone in.
				password = launcher.Text.PasswordUnknown
				hint = launcher.Text.CredentialsNote
				copyBtn = nil
			}
			return u.ticketCell(gtx, launcher.Text.Account, launcher.Text.AccountName+" / "+password, hint, copyBtn, true)
		},
		func(gtx layout.Context) layout.Dimensions {
			return u.ticketCell(gtx, launcher.Text.DataDir, dataDirLine, launcher.Text.MoveHint, nil, false)
		},
	}

	var body layout.Widget
	if compact {
		body = func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cells[0](gtx) }),
				layout.Rigid(u.rule),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cells[1](gtx) }),
				layout.Rigid(u.rule),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cells[2](gtx) }),
			)
		}
	} else {
		body = func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Flexed(1.1, func(gtx layout.Context) layout.Dimensions { return cells[0](gtx) }),
				layout.Rigid(u.vrule),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return cells[1](gtx) }),
				layout.Rigid(u.vrule),
				layout.Flexed(1.1, func(gtx layout.Context) layout.Dimensions { return cells[2](gtx) }),
			)
		}
	}

	return u.panel(gtx, 0, body)
}

func (u *launcherUI) ticketCell(gtx layout.Context, key, value, note string, copyBtn *widget.Clickable, mono bool) layout.Dimensions {
	compact := u.compact(gtx)
	inset := layout.Inset{Top: unit.Dp(16), Bottom: unit.Dp(16), Left: unit.Dp(20), Right: unit.Dp(20)}
	if compact {
		inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(14), Right: unit.Dp(14)}
	}

	return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, key)
				label.TextSize = fsSmall
				label.Color = colMuted
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							label := material.Body1(u.theme, value)
							label.TextSize = unit.Sp(16)
							if mono {
								label.Font = u.mono
								label.TextSize = unit.Sp(15)
							}
							label.Color = colText
							return label.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if copyBtn == nil {
								return layout.Dimensions{}
							}
							return u.iconButton(gtx, copyBtn)
						}),
					)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if note == "" {
					return layout.Dimensions{}
				}
				top := unit.Dp(10)
				size := fsSmall
				if compact {
					top = unit.Dp(6)
					size = fsTiny
				}
				return layout.Inset{Top: top}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					label := material.Body1(u.theme, note)
					label.TextSize = size
					label.Color = colFaint
					return label.Layout(gtx)
				})
			}),
		)
	})
}

// finding is the "disk changed" line with its one action (.finding).
func (u *launcherUI) finding(gtx layout.Context) layout.Dimensions {
	compact := u.compact(gtx)

	body := func(gtx layout.Context) layout.Dimensions {
		text := func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					paint.FillShape(gtx.Ops, colWarning, clip.Ellipse(image.Rect(0, 0, 14, 14)).Op(gtx.Ops))
					return layout.Dimensions{Size: image.Pt(14, 14)}
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body1(u.theme, launcher.Text.FindingPending)
					label.TextSize = fsBody
					label.Color = colText
					return label.Layout(gtx)
				}),
			)
		}
		button := func(gtx layout.Context) layout.Dimensions {
			return u.button(gtx, &u.refresh, launcher.Text.RefreshNow, false)
		}

		if compact {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(text),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, button)
				}),
			)
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Flexed(1, text),
			layout.Rigid(button),
		)
	}

	return u.panel(gtx, unit.Dp(12), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, body)
	})
}

// opsBar is the operations row: one primary action, the rest quiet.
func (u *launcherUI) opsBar(gtx layout.Context, status launcher.Status) layout.Dimensions {
	compact := u.compact(gtx)

	refreshLine := func(gtx layout.Context) layout.Dimensions {
		line := ""
		switch status.Refresh.State {
		case launcher.RefreshRunning:
			line = launcher.Text.RefreshRunning
		case launcher.RefreshFailed:
			line = launcher.Text.RefreshFailed + " " + status.Refresh.Error
		case launcher.RefreshDone:
			line = launcher.UpdatedAt(status.Refresh.DoneAt)
		}
		if line == "" {
			return layout.Dimensions{}
		}
		label := material.Body1(u.theme, line)
		label.TextSize = fsSmall
		label.Color = colMuted
		return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, label.Layout)
	}

	if compact {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.restart, launcher.Text.Restart, false)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.stop, launcher.Text.Stop, false)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.openDir, launcher.Text.OpenDataDir, false)
				})
			}),
			layout.Rigid(refreshLine),
		)
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.restart, launcher.Text.Restart, false)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.stop, launcher.Text.Stop, false)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.openDir, launcher.Text.OpenDataDir, false)
				}),
			)
		}),
		layout.Rigid(refreshLine),
	)
}

// logPanel is the bordered log card with the newest line first (.panel.grow).
func (u *launcherUI) logPanel(gtx layout.Context) layout.Dimensions {
	lines := launcherLogLines()

	return u.panel(gtx, unit.Dp(16), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(20), Right: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							label := material.Body1(u.theme, launcher.Text.LogSection)
							label.TextSize = fsSection
							label.Color = colText
							return label.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: gtx.Constraints.Min}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							label := material.Body1(u.theme, launcher.Text.LogWrittenTo+" data\\logs\\launcher.log")
							label.TextSize = fsSmall
							label.Color = colFaint
							return label.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						if len(lines) == 0 {
							label := material.Body1(u.theme, launcher.Text.NoLogs)
							label.TextSize = fsSmall
							label.Color = colFaint
							return label.Layout(gtx)
						}
						return u.logLines(gtx, lines)
					})
				}),
			)
		})
	})
}

func (u *launcherUI) logLines(gtx layout.Context, lines []string) layout.Dimensions {
	// Newest first, bounded so the card never eats the whole page.
	shown := lines
	if len(shown) > 40 {
		shown = shown[len(shown)-40:]
	}

	rows := make([]layout.Widget, 0, len(shown))
	for i := len(shown) - 1; i >= 0; i-- {
		line := shown[i]
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.logLine(gtx, line) })
	}

	list := layout.List{Axis: layout.Vertical}
	return list.Layout(gtx, len(rows), func(gtx layout.Context, index int) layout.Dimensions {
		return rows[index](gtx)
	})
}

// logLine: 62 px time, 42 px level, the rest message (proto .log li).
func (u *launcherUI) logLine(gtx layout.Context, line string) layout.Dimensions {
	stamp, level, message := splitLogLine(line)
	levelColor := colFaint
	switch level {
	case "WARN":
		levelColor = colWarning
	case "ERROR":
		levelColor = colNegative
	}

	compact := u.compact(gtx)
	widths := [3]float32{62, 42, 0}
	if compact {
		widths = [3]float32{56, 40, 0}
	}
	toDp := func(px float32) unit.Dp {
		// The stylesheet numbers are CSS px at 1× - dp is the honest match.
		return unit.Dp(px)
	}

	cell := func(width unit.Dp, text string, colour color.NRGBA, mono bool) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{
				Min: image.Pt(0, 0),
				Max: image.Pt(gtx.Dp(width), gtx.Constraints.Max.Y),
			}
			label := material.Body1(u.theme, text)
			label.TextSize = fsMonoLog
			label.Font = u.mono
			label.Color = colour
			return label.Layout(gtx)
		})
	}

	pad := unit.Dp(10)
	if compact {
		pad = unit.Dp(6)
	}

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		cell(toDp(widths[0]), stamp, colFaint, true),
		layout.Rigid(layout.Spacer{Width: pad}.Layout),
		cell(toDp(widths[1]), level, levelColor, true),
		layout.Rigid(layout.Spacer{Width: pad}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			head, attrs := splitLogAttrs(message)
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body1(u.theme, head)
					label.TextSize = fsMonoLog
					label.Font = u.mono
					label.Color = colText
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if attrs == "" {
						return layout.Dimensions{}
					}
					label := material.Body1(u.theme, attrs)
					label.TextSize = fsMonoLog
					label.Font = u.mono
					label.Color = colMuted
					return label.Layout(gtx)
				}),
			)
		}),
	)
}

// splitLogAttrs cuts the message from its "key=value" tail, the part the
// stylesheet dims (.log .kv).
func splitLogAttrs(message string) (head, attrs string) {
	for i := 1; i < len(message)-2; i++ {
		if message[i] != ' ' {
			continue
		}
		rest := message[i+1:]
		if eq := strings.IndexByte(rest, '='); eq > 0 {
			key := rest[:eq]
			if !strings.ContainsAny(key, " =") {
				return message[:i], message[i:]
			}
		}
		break
	}
	return message, ""
}

// button is the stylesheet's .btn: 32 dp high, 12 px padding, 1 px control
// border; .btn-primary fills with ink. Compact makes the primary action taller.
func (u *launcherUI) button(gtx layout.Context, btn *widget.Clickable, label string, primary bool) layout.Dimensions {
	style := material.Button(u.theme, btn, label)
	style.TextSize = fsBody
	style.CornerRadius = radiusControl
	style.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}
	style.Background = color.NRGBA{}
	if primary {
		style.Color = colSurface
	} else {
		style.Color = colText
	}

	fill, stroke := colSurface, colControl
	if primary {
		fill, stroke = colInk, colInk
	}

	// Every .btn is the same 32 dp box; the title row's primary action asks for
	// a taller one (44 dp) by setting Min.Y before calling this.
	if gtx.Constraints.Min.Y == 0 {
		gtx.Constraints.Min.Y = gtx.Dp(controlHeight)
	}
	return u.framedButton(gtx, btn, style.Layout, fill, stroke)
}

// iconButton is the ticket's .btn-icon: a bordered 32 dp chip.
func (u *launcherUI) iconButton(gtx layout.Context, btn *widget.Clickable) layout.Dimensions {
	style := material.Button(u.theme, btn, launcher.Text.Copy)
	style.TextSize = fsSmall
	style.CornerRadius = radiusControl
	style.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(10), Right: unit.Dp(10)}
	style.Background = color.NRGBA{}
	style.Color = colMuted
	return u.framedButton(gtx, btn, style.Layout, colSurface, colControl)
}

// framedButton paints the .btn fill and its 1 px border, then replays the
// text content on top.
func (u *launcherUI) framedButton(gtx layout.Context, btn *widget.Clickable, content layout.Widget, fill, stroke color.NRGBA) layout.Dimensions {
	_, focused := u.registerFocus(btn)
	macro := op.Record(gtx.Ops)
	dims := content(gtx)
	call := macro.Stop()

	rect := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(radiusControl))
	paint.FillShape(gtx.Ops, fill, rect.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, stroke, clip.Stroke{Path: rect.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())
	call.Add(gtx.Ops)
	if focused {
		u.focusRing(gtx, dims, radiusControl)
	}
	return dims
}

// switchControl is the prototype's .switch: a 28x16 track with a 12 dp knob,
// 8 dp from its label.
func (u *launcherUI) switchControl(gtx layout.Context, click *widget.Clickable, box *widget.Bool, label string) layout.Dimensions {
	if click.Clicked(gtx) {
		box.Value = !box.Value
	}

	_, focused := u.registerFocus(click)
	dims := click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				trackW, trackH := gtx.Dp(unit.Dp(28)), gtx.Dp(unit.Dp(16))
				knob := gtx.Dp(unit.Dp(12))

				fill := colControl
				if box.Value {
					fill = colInk
				}
				paint.FillShape(gtx.Ops, fill,
					clip.UniformRRect(image.Rect(0, 0, trackW, trackH), trackH/2).Op(gtx.Ops))

				x := 2
				if box.Value {
					x = trackW - knob - 2
				}
				paint.FillShape(gtx.Ops, colSurface,
					clip.Ellipse(image.Rect(x, 2, x+knob, 2+knob)).Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(trackW, trackH)}
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				text := material.Body1(u.theme, label)
				text.TextSize = fsSmall
				text.Color = colMuted
				return text.Layout(gtx)
			}),
		)
	})
	if focused {
		u.focusRing(gtx, dims, radiusControl)
	}
	return dims
}

// panel draws a bordered card (.ticket, .finding, .panel).
func (u *launcherUI) panel(gtx layout.Context, padDp unit.Dp, body layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(padDp).Layout(gtx, body)
	call := macro.Stop()

	rect := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(radiusCard))
	paint.FillShape(gtx.Ops, colSurface, rect.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, colBorder, clip.Stroke{Path: rect.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())
	call.Add(gtx.Ops)
	return dims
}

func (u *launcherUI) rule(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, colBorder, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 1)}
}

func (u *launcherUI) vrule(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, colBorder, clip.Rect{Max: image.Pt(1, gtx.Constraints.Max.Y)}.Op())
	return layout.Dimensions{Size: image.Pt(1, gtx.Constraints.Max.Y)}
}

// splitLogLine cuts "<date> <time> <LEVEL> <msg k=v>" (the handler's format).
func splitLogLine(line string) (stamp, level, message string) {
	parts := strings.SplitN(line, " ", 4)
	if len(parts) < 4 {
		return "", "", line
	}
	return parts[1], parts[2], parts[3]
}

func humanBytes(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// shortDataDir keeps the top bar from growing with a long path.
func shortDataDir(path string) string {
	if len(path) <= 28 {
		return path
	}
	return "…" + path[len(path)-27:]
}
