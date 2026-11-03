package launcher

// Focus is the keyboard ring over the controls a frame lays out. It holds no
// widgets: the view registers each control in layout order and asks whether it
// is the focused one.
type Focus struct {
	index   int
	visible bool
	// count is the number of controls registered so far this frame.
	count int
	// total is the number of controls the previous frame had, so a shrink can
	// drop a ring that would otherwise point at nothing.
	total int
}

// Begin starts a frame.
func (f *Focus) Begin() {
	f.count = 0
}

// Register records one control and reports its position and whether it holds
// the ring.
func (f *Focus) Register() (index int, focused bool) {
	index = f.count
	f.count++
	return index, f.visible && index == f.index
}

// End closes a frame: when the control set shrank, an index past the end drops
// the ring instead of pointing at nothing.
func (f *Focus) End() {
	f.total = f.count
	if f.visible && (f.total == 0 || f.index >= f.total) {
		f.visible = false
		f.index = 0
	}
}

// Step moves the ring and reports the new position and whether it is visible.
// It wraps at both ends.
func (f *Focus) Step(reverse bool) (index int, visible bool) {
	if f.total == 0 {
		return f.index, f.visible
	}
	if !f.visible {
		// The first Tab lands on the first control, Shift+Tab on the last.
		f.visible = true
		if reverse {
			f.index = f.total - 1
		} else {
			f.index = 0
		}
		return f.index, true
	}
	if reverse {
		f.index = (f.index - 1 + f.total) % f.total
	} else {
		f.index = (f.index + 1) % f.total
	}
	return f.index, f.visible
}

// Current reports the ring's position and whether it is visible.
func (f *Focus) Current() (index int, visible bool) {
	return f.index, f.visible
}

// Hide drops the ring.
func (f *Focus) Hide() {
	f.visible = false
}
