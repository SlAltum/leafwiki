//go:build launcher

package main

import (
	"image/color"
	"log/slog"
	"os"
	"path/filepath"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// The window's tokens, taken one-for-one from the prototype's stylesheet
// (.taste-skill/launcher/launcher.css: :root and the component rules).
var (
	colGround   = color.NRGBA{R: 0xf9, G: 0xfb, B: 0xfc, A: 0xff}
	colSurface  = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	colRaised   = color.NRGBA{R: 0xf4, G: 0xf6, B: 0xf8, A: 0xff}
	colActive   = color.NRGBA{R: 0xeb, G: 0xed, B: 0xee, A: 0xff}
	colBorder   = color.NRGBA{R: 0xe8, G: 0xea, B: 0xec, A: 0xff}
	colRule     = color.NRGBA{R: 0xf0, G: 0xf1, B: 0xf2, A: 0xff}
	colControl  = color.NRGBA{R: 0xcd, G: 0xce, B: 0xd1, A: 0xff}
	colText     = color.NRGBA{R: 0x13, G: 0x14, B: 0x16, A: 0xff}
	colMuted    = color.NRGBA{R: 0x5d, G: 0x5f, B: 0x60, A: 0xff}
	colFaint    = color.NRGBA{R: 0x6b, G: 0x6b, B: 0x6b, A: 0xff}
	colAccent   = color.NRGBA{R: 0x1b, G: 0x6c, B: 0x80, A: 0xff}
	colPositive = color.NRGBA{R: 0x1e, G: 0x7b, B: 0x4f, A: 0xff}
	colNegative = color.NRGBA{R: 0xc4, G: 0x24, B: 0x2a, A: 0xff}
	colWarning  = color.NRGBA{R: 0x92, G: 0x40, B: 0x0e, A: 0xff}
	colFrame    = color.NRGBA{R: 0xf3, G: 0xf7, B: 0xf2, A: 0xff}
	colInk      = color.NRGBA{R: 0x1d, G: 0x1f, B: 0x20, A: 0xff}

	// Pills.
	colPillOKBg       = color.NRGBA{R: 0xdc, G: 0xfa, B: 0xe5, A: 0xff}
	colPillOKInk      = color.NRGBA{R: 0x04, G: 0x25, B: 0x0e, A: 0xff}
	colPillWarnBg     = color.NRGBA{R: 0xff, G: 0xfb, B: 0xeb, A: 0xff}
	colPillWarnInk    = color.NRGBA{R: 0x92, G: 0x40, B: 0x0e, A: 0xff}
	colPillNeutralBg  = color.NRGBA{R: 0xeb, G: 0xed, B: 0xee, A: 0xff}
	colPillNeutralInk = color.NRGBA{R: 0x35, G: 0x37, B: 0x36, A: 0xff}
	colPillBadBg      = color.NRGBA{R: 0xff, G: 0xed, B: 0xea, A: 0xff}
	colPillBadInk     = color.NRGBA{R: 0x83, G: 0x1c, B: 0x1e, A: 0xff}
)

// Type scale and metrics from the same stylesheet.
const (
	fsTitle   = unit.Sp(22)
	fsTitleSm = unit.Sp(19)
	fsSection = unit.Sp(16)
	fsBody    = unit.Sp(14)
	fsSmall   = unit.Sp(13)
	fsTiny    = unit.Sp(12)
	fsMonoLog = unit.Sp(13)

	pad        = unit.Dp(24)
	padCompact = unit.Dp(16)
	gutter     = unit.Dp(24)
	gapCompact = unit.Dp(12)

	radiusCard    = unit.Dp(8)
	radiusControl = unit.Dp(6)
	radiusSheet   = unit.Dp(16)

	controlHeight = unit.Dp(32)
	topbarHeight  = unit.Dp(48)

	// narrow is the prototype's "--breakpoint at 720": below it the frame
	// disappears, the ticket stacks and the title row turns into a column.
	narrowWidth = unit.Dp(720)
)

// fontCandidates are the single-file TrueType CJK fonts tried in order
// (FR-901). Collections (.ttc) are not usable: opentype.Parse rejects them.
var fontCandidates = []string{"Deng.ttf", "simhei.ttf", "simkai.ttf", "simfang.ttf"}

// monoCandidates backs the log, environment-variable and mono fields; the
// stylesheet asks for "Cascadia Mono"/Consolas.
var monoCandidates = []string{"consola.ttf", "CascadiaMono.ttf"}

const monoTypeface = "LeafWiki Mono"

// newTheme builds the window theme around the first usable CJK and mono system
// fonts. Font files are never shipped with the launcher (FR-903).
func newTheme() *material.Theme {
	theme := material.NewTheme()

	faces := append(loadCJKFaces(), loadMonoFaces()...)
	if len(faces) > 0 {
		// Only the faces the window actually needs: scanning every installed
		// system font costs tens of MB of font index for nothing.
		theme.Shaper = text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces))
	}

	theme.Bg = colSurface
	theme.Fg = colText
	theme.ContrastBg = colInk
	theme.ContrastFg = colSurface
	return theme
}

func loadCJKFaces() []font.FontFace {
	dir := fontDir()
	if dir == "" {
		return nil
	}
	for _, name := range fontCandidates {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		face, err := opentype.Parse(raw)
		if err != nil {
			slog.Warn("cannot parse system font", "font", name, "error", err)
			continue
		}
		return []font.FontFace{{Font: face.Font(), Face: face}}
	}
	slog.Warn("no usable CJK system font found; the window may show placeholder glyphs", "candidates", fontCandidates)
	return nil
}

func loadMonoFaces() []font.FontFace {
	dir := fontDir()
	if dir == "" {
		return nil
	}
	for _, name := range monoCandidates {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		face, err := opentype.Parse(raw)
		if err != nil {
			continue
		}
		mono := face.Font()
		mono.Typeface = monoTypeface
		return []font.FontFace{{Font: mono, Face: face}}
	}
	return nil
}

func fontDir() string {
	if dir := os.Getenv("WINDIR"); dir != "" {
		return filepath.Join(dir, "Fonts")
	}
	if dir := os.Getenv("SystemRoot"); dir != "" {
		return filepath.Join(dir, "Fonts")
	}
	return ""
}
