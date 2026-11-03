//go:build launcher

package main

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/perber/wiki/internal/launcher"
)

// settingsPage is the advanced page: one section per CLI category - label and
// explanation on the left, the parameter table in a panel on the right - and a
// footer with the check/save actions. It mirrors the prototype's .sec/.ptable.
type settingsPage struct {
	search       widget.Editor
	onlyChanged  widget.Bool
	showAdvanced widget.Bool
	autoRefresh  widget.Bool

	onlyChangedClick  widget.Clickable
	showAdvancedClick widget.Clickable
	autoRefreshClick  widget.Clickable

	editors map[string]*widget.Editor
	list    layout.List

	testParse    widget.Clickable
	save         widget.Clickable
	restartApply widget.Clickable
	resetAdmin   widget.Clickable

	message   string
	messageOK bool
	loaded    bool
	// saved unlocks the "restart and apply" action after a successful save.
	saved bool
}

func (s *settingsPage) layout(u *launcherUI, gtx layout.Context) layout.Dimensions {
	if !s.loaded {
		s.load(u)
	}
	s.syncFromEditors(u)
	s.handleClicks(u, gtx)

	compact := u.compact(gtx)
	gap := gutter
	if compact {
		gap = gapCompact
	}

	sections := s.sections(u)

	items := make([]layout.Widget, 0, len(sections)+4)
	items = append(items, func(gtx layout.Context) layout.Dimensions { return s.titleRow(u, gtx) })
	items = append(items, func(gtx layout.Context) layout.Dimensions { return s.toolbar(u, gtx) })
	for i := range sections {
		section := sections[i]
		items = append(items, func(gtx layout.Context) layout.Dimensions { return s.section(u, gtx, section) })
	}
	if len(sections) == 0 {
		items = append(items, func(gtx layout.Context) layout.Dimensions {
			label := material.Body1(u.theme, launcher.Text.NoMatchingRows)
			label.TextSize = fsBody
			label.Color = colMuted
			return label.Layout(gtx)
		})
	}
	// The prototype's last section is the account block, not a footer button.
	items = append(items, func(gtx layout.Context) layout.Dimensions { return s.accountSection(u, gtx) })
	items = append(items, func(gtx layout.Context) layout.Dimensions { return s.footer(u, gtx) })

	return u.scroll.Layout(gtx, len(items), func(gtx layout.Context, index int) layout.Dimensions {
		return layout.Inset{Bottom: gap}.Layout(gtx, items[index])
	})
}

func (s *settingsPage) load(u *launcherUI) {
	s.loaded = true
	s.editors = map[string]*widget.Editor{}
	s.list = layout.List{Axis: layout.Vertical}
	s.autoRefresh.Value = u.ctrl.AutoRefresh()

	// Every editable row is a text field, booleans included: the prototype
	// shows "true"/"false" as input values and lets the CLI parse them.
	for _, param := range u.ctrl.Params() {
		editor := &widget.Editor{SingleLine: true}
		editor.SetText(param.Value)
		s.editors[param.Name] = editor
	}
}

func (s *settingsPage) syncFromEditors(u *launcherUI) {
	params := u.ctrl.Params()
	for i := range params {
		if editor, ok := s.editors[params[i].Name]; ok {
			params[i].Value = editor.Text()
		}
		params[i].Changed = params[i].Value != params[i].Default
	}
	u.ctrl.SetParams(params)
	u.ctrl.SetAutoRefresh(s.autoRefresh.Value)
}

func (s *settingsPage) handleClicks(u *launcherUI, gtx layout.Context) {
	if s.testParse.Clicked(gtx) {
		message, err := u.ctrl.Validate()
		if err != nil {
			s.message, s.messageOK = err.Error(), false
		} else {
			s.message, s.messageOK = message, true
		}
	}

	if s.save.Clicked(gtx) {
		if _, err := u.ctrl.Validate(); err != nil {
			s.message, s.messageOK = err.Error(), false
		} else {
			config := launcher.DefaultConfig()
			config.Params = launcher.ChangedParams(u.ctrl.Params())
			config.AutoRefresh = s.autoRefresh.Value
			if err := config.Save(u.dataDir, launcher.Keep); err != nil {
				s.message, s.messageOK, s.saved = err.Error(), false, false
			} else {
				s.message, s.messageOK, s.saved = launcher.Text.NeedsRestart, true, true
			}
		}
	}

	if s.restartApply.Clicked(gtx) {
		u.ctrl.Restart()
	}

	if s.resetAdmin.Clicked(gtx) {
		if err := u.ctrl.ResetAdminPassword(); err != nil {
			s.message, s.messageOK = err.Error(), false
		} else {
			s.message, s.messageOK = launcher.Text.PasswordReset, true
		}
	}
}

// titleRow is the page heading and the line explaining the table's origin.
func (s *settingsPage) titleRow(u *launcherUI, gtx layout.Context) layout.Dimensions {
	heading := material.H6(u.theme, launcher.Text.AdvancedTitle)
	heading.TextSize = fsTitle
	if u.compact(gtx) {
		heading.TextSize = fsTitleSm
	}
	heading.Color = colText

	intro := material.Body1(u.theme, fmt.Sprintf("%s 共 %d 项。", launcher.Text.AdvancedIntro, len(u.ctrl.Params())))
	intro.TextSize = fsSmall
	intro.Color = colMuted

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(heading.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(20)}.Layout(gtx, intro.Layout)
		}),
	)
}

// toolbar is the search field, the switches and the hidden-items hint
// (.toolbar, .search input 240x32, .switch .track 28x16).
func (s *settingsPage) toolbar(u *launcherUI, gtx layout.Context) layout.Dimensions {
	compact := u.compact(gtx)
	hidden := s.hiddenCount(u)

	// .search input: a plain 240x32 bordered field, not a card.
	search := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{
			Min: image.Pt(gtx.Dp(unit.Dp(240)), gtx.Dp(controlHeight)),
			Max: image.Pt(gtx.Dp(unit.Dp(240)), gtx.Dp(controlHeight)),
		}
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			editor := material.Editor(u.theme, &s.search, launcher.Text.Search)
			editor.TextSize = fsBody
			editor.Color = colText
			editor.HintColor = colFaint
			return editor.Layout(gtx)
		})
		call := macro.Stop()

		rect := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(radiusControl))
		paint.FillShape(gtx.Ops, colSurface, rect.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, colControl, clip.Stroke{Path: rect.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())
		call.Add(gtx.Ops)
		return dims
	}

	switches := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.switchControl(gtx, &s.onlyChangedClick, &s.onlyChanged, launcher.Text.OnlyChanged)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.switchControl(gtx, &s.showAdvancedClick, &s.showAdvanced, launcher.Text.ShowAdvanced)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.switchControl(gtx, &s.autoRefreshClick, &s.autoRefresh, launcher.Text.AutoRefreshLabel)
			}),
		)
	}

	hint := func(gtx layout.Context) layout.Dimensions {
		if s.showAdvanced.Value || hidden == 0 {
			return layout.Dimensions{}
		}
		label := material.Body1(u.theme, fmt.Sprintf("还有 %d 项收在「%s」里", hidden, launcher.Text.ShowAdvanced))
		label.TextSize = fsSmall
		label.Color = colFaint
		return label.Layout(gtx)
	}

	if compact {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(search),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, switches)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, hint)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		)
	}

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(search),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Flexed(1, switches),
		layout.Rigid(hint),
	)
}

func (s *settingsPage) hiddenCount(u *launcherUI) int {
	count := 0
	for _, param := range launcher.Filter(u.ctrl.Params(), s.search.Text(), s.onlyChanged.Value) {
		if !launcher.Visible(param) {
			count++
		}
	}
	return count
}

// tableSection is one .sec: a group of parameters with its explanation.
type tableSection struct {
	group  string
	title  string
	note   string
	params []launcher.Param
}

func (s *settingsPage) sections(u *launcherUI) []tableSection {
	params := launcher.Filter(u.ctrl.Params(), s.search.Text(), s.onlyChanged.Value)

	visible := make([]launcher.Param, 0, len(params))
	for _, param := range params {
		if s.showAdvanced.Value || launcher.Visible(param) {
			visible = append(visible, param)
		}
	}
	// Keep the CLI's declaration order: groups appear in --help order and rows
	// keep the order they are declared in.

	sections := make([]tableSection, 0, 8)
	for i := range visible {
		group := visible[i].Group
		if len(sections) == 0 || sections[len(sections)-1].group != group {
			sections = append(sections, tableSection{
				group: group,
				title: groupTitle(group),
				note:  launcher.Text.GroupNotes[group],
			})
		}
		sections[len(sections)-1].params = append(sections[len(sections)-1].params, visible[i])
	}
	return sections
}

// groupTitle is the CLI category in the window's language.
func groupTitle(group string) string {
	if title, ok := launcher.Text.GroupTitles[group]; ok {
		return title
	}
	if group == "" {
		return launcher.Text.GroupOther
	}
	return group
}

// section lays out .sec: label column + panel with the table (single column
// below the 720 px breakpoint).
func (s *settingsPage) section(u *launcherUI, gtx layout.Context, section tableSection) layout.Dimensions {
	label := func(gtx layout.Context) layout.Dimensions {
		heading := material.Body1(u.theme, section.title)
		heading.TextSize = fsSection
		heading.Color = colText
		note := material.Body1(u.theme, section.note)
		note.TextSize = fsSmall
		note.Color = colMuted
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if section.note == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, note.Layout)
			}),
		)
	}

	panel := func(gtx layout.Context) layout.Dimensions {
		return s.table(u, gtx, section.params)
	}

	if u.compact(gtx) {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(label),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, panel)
			}),
		)
	}

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{
				Min: image.Pt(gtx.Dp(unit.Dp(340)), 0),
				Max: image.Pt(gtx.Dp(unit.Dp(340)), gtx.Constraints.Max.Y),
			}
			return label(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(32)}.Layout),
		layout.Flexed(1, panel),
	)
}

// table is the prototype's .ptable: 27/36/13/24 columns, a header row, one
// hairline above every row.
func (s *settingsPage) table(u *launcherUI, gtx layout.Context, params []launcher.Param) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(16), Bottom: unit.Dp(16), Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		macro := op.Record(gtx.Ops)
		dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return s.tableHeader(u, gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				rows := make([]layout.Widget, 0, len(params))
				for i := range params {
					param := params[i]
					rows = append(rows, func(gtx layout.Context) layout.Dimensions {
						return s.tableRow(u, gtx, param)
					})
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, wrapRigid(rows)...)
			}),
		)
		call := macro.Stop()

		rect := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(radiusSheet))
		paint.FillShape(gtx.Ops, colGround, rect.Op(gtx.Ops))
		call.Add(gtx.Ops)
		return dims
	})
}

// wrapRigid is sugar for passing a slice of widget funcs to a Flex.
func wrapRigid(widgets []layout.Widget) []layout.FlexChild {
	children := make([]layout.FlexChild, 0, len(widgets))
	for _, widget := range widgets {
		children = append(children, layout.Rigid(widget))
	}
	return children
}

func (s *settingsPage) tableHeader(u *launcherUI, gtx layout.Context) layout.Dimensions {
	cell := func(weight float32, label string) layout.FlexChild {
		return layout.Flexed(weight, func(gtx layout.Context) layout.Dimensions {
			text := material.Body1(u.theme, label)
			text.TextSize = fsSmall
			text.Color = colFaint
			return text.Layout(gtx)
		})
	}
	return layout.Inset{Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
			cell(27, launcher.Text.ColumnName),
			cell(36, launcher.Text.ColumnValue),
			cell(13, launcher.Text.ColumnDefault),
			cell(24, launcher.Text.ColumnEnv),
		)
	})
}

func (s *settingsPage) tableRow(u *launcherUI, gtx layout.Context, param launcher.Param) layout.Dimensions {
	editable := launcher.Editable(param.Name) && param.Kind != launcher.KindUnsupported

	// .pname: the change dot sits inline before the name, so an unchanged row
	// starts exactly where the column header does.
	name := layout.Flexed(27, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !param.Changed {
					return layout.Dimensions{}
				}
				paint.FillShape(gtx.Ops, colAccent, clip.Ellipse(image.Rect(0, 0, 5, 5)).Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(5, 5)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !param.Changed {
					return layout.Dimensions{}
				}
				return layout.Spacer{Width: unit.Dp(8)}.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, param.Name)
				label.TextSize = fsBody
				label.Color = colText
				label.MaxLines = 1
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if editable {
					return layout.Dimensions{}
				}
				label := material.Body1(u.theme, launcher.Text.ReadOnly)
				label.TextSize = fsSmall
				label.Color = colFaint
				label.MaxLines = 1
				return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, label.Layout)
			}),
		)
	})

	value := layout.Flexed(36, func(gtx layout.Context) layout.Dimensions {
		if !editable {
			label := material.Body1(u.theme, param.Value)
			label.TextSize = fsSmall
			label.Font = u.mono
			label.Color = colMuted
			return label.Layout(gtx)
		}
		editor, ok := s.editors[param.Name]
		if !ok {
			return layout.Dimensions{}
		}
		return layout.Inset{Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			dims := u.framedField(gtx, func(gtx layout.Context) layout.Dimensions {
				style := material.Editor(u.theme, editor, param.Default)
				style.TextSize = fsSmall
				style.Color = colText
				style.HintColor = colFaint
				return style.Layout(gtx)
			})
			return dims
		})
	})

	def := layout.Flexed(13, func(gtx layout.Context) layout.Dimensions {
		value := param.Default
		if value == "" {
			value = "空"
		}
		label := material.Body1(u.theme, value)
		label.TextSize = fsSmall
		label.Color = colFaint
		label.MaxLines = 1
		return label.Layout(gtx)
	})

	env := layout.Flexed(24, func(gtx layout.Context) layout.Dimensions {
		value := "-"
		if len(param.Env) > 0 {
			value = param.Env[0]
		}
		label := material.Body1(u.theme, value)
		label.TextSize = fsSmall
		if u.compact(gtx) {
			label.TextSize = fsTiny
		}
		label.Font = u.mono
		label.Color = colFaint
		label.MaxLines = 1
		label.Truncator = "…"
		return label.Layout(gtx)
	})

	paint.FillShape(gtx.Ops, colRule, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())

	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, name, value, def, env)
	})
}

// accountSection is the prototype's 账号 block: who the admin is, what the
// password currently means, and the reset action.
func (s *settingsPage) accountSection(u *launcherUI, gtx layout.Context) layout.Dimensions {
	label := func(gtx layout.Context) layout.Dimensions {
		heading := material.Body1(u.theme, launcher.Text.AccountSection)
		heading.TextSize = fsSection
		heading.Color = colText
		note := material.Body1(u.theme, launcher.Text.AccountNote)
		note.TextSize = fsSmall
		note.Color = colMuted
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, note.Layout)
			}),
		)
	}

	panel := func(gtx layout.Context) layout.Dimensions {
		return u.panel(gtx, unit.Dp(16), func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							who := material.Body1(u.theme, launcher.Text.AccountAdmin)
							who.TextSize = fsBody
							who.Color = colText
							return who.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							state := launcher.Text.PasswordInitial
							if u.ctrl.Status().CredState == launcher.CredUnknown {
								state = launcher.Text.PasswordUnknown
							}
							text := state + "。" + launcher.Text.AccountResetNote
							if launcher.Text.AccountResetNote == "" {
								text = state + "。"
							}
							note := material.Body1(u.theme, text)
							note.TextSize = fsSmall
							note.Color = colFaint
							return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, note.Layout)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.dangerButton(gtx, &s.resetAdmin, launcher.Text.ResetAdminPassword)
				}),
			)
		})
	}

	if u.compact(gtx) {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(label),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, panel)
			}),
		)
	}

	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{
				Min: image.Pt(gtx.Dp(unit.Dp(340)), 0),
				Max: image.Pt(gtx.Dp(unit.Dp(340)), gtx.Constraints.Max.Y),
			}
			return label(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(32)}.Layout),
		layout.Flexed(1, panel),
	)
}

// framedField is the .pval input: an exact 32 dp box around the editor. The
// editor is measured at its natural height and then centred inside the box, so
// the field is the same height as every other control and never stretches the
// row (padding above/below the editor used to be added on top of the box).
func (u *launcherUI) framedField(gtx layout.Context, editor layout.Widget) layout.Dimensions {
	height := gtx.Dp(controlHeight)

	natural := gtx
	natural.Constraints.Min.Y = 0

	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(natural, editor)
	call := macro.Stop()

	size := image.Pt(dims.Size.X, height)
	rect := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(radiusControl))
	paint.FillShape(gtx.Ops, colSurface, rect.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, colControl, clip.Stroke{Path: rect.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())

	offset := (height - dims.Size.Y) / 2
	if offset < 0 {
		offset = 0
	}
	translate := op.Offset(image.Pt(0, offset)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	translate.Pop()

	return layout.Dimensions{Size: size}
}

// dangerButton is the reset action: a quiet button in the negative colour
// (the prototype's "重置管理员密码" styling).
func (u *launcherUI) dangerButton(gtx layout.Context, btn *widget.Clickable, label string) layout.Dimensions {
	style := material.Button(u.theme, btn, label)
	style.TextSize = fsBody
	style.CornerRadius = radiusControl
	style.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}
	style.Background = color.NRGBA{}
	style.Color = colNegative
	return u.framedButton(gtx, btn, style.Layout, colSurface, colNegative)
}

// footer is .settings-foot: the reason on the left, the actions on the right.
func (s *settingsPage) footer(u *launcherUI, gtx layout.Context) layout.Dimensions {
	button := func(btn *widget.Clickable, label string, primary bool) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.button(gtx, btn, label, primary)
		})
	}

	why := func(gtx layout.Context) layout.Dimensions {
		if s.message == "" {
			return layout.Dimensions{}
		}
		label := material.Body1(u.theme, s.message)
		label.TextSize = fsSmall
		if s.messageOK {
			label.Color = colPositive
		} else {
			label.Color = colNegative
		}
		return label.Layout(gtx)
	}

	return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, colBorder, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
		return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, why),
				button(&s.testParse, launcher.Text.TestConfig, false),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				button(&s.save, launcher.Text.Save, true),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !s.saved {
						return layout.Dimensions{}
					}
					return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &s.restartApply, launcher.Text.RestartApply, false)
					})
				}),
			)
		})
	})
}
