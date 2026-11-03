package launcher

import "testing"

func TestFocusWrapsBothWays(t *testing.T) {
	focus := Focus{}
	registerThree := func() {
		focus.Begin()
		for i := 0; i < 3; i++ {
			focus.Register()
		}
		focus.End()
	}
	registerThree()

	if index, visible := focus.Step(false); index != 0 || !visible {
		t.Fatalf("first Tab: got index %d visible %v, want 0 true", index, visible)
	}
	if index, _ := focus.Step(false); index != 1 {
		t.Fatalf("second Tab: got index %d, want 1", index)
	}
	if index, _ := focus.Step(false); index != 2 {
		t.Fatalf("third Tab: got index %d, want 2", index)
	}
	if index, _ := focus.Step(false); index != 0 {
		t.Fatalf("forward wrap: got index %d, want 0", index)
	}
	if index, _ := focus.Step(true); index != 2 {
		t.Fatalf("backward wrap: got index %d, want 2", index)
	}
}

func TestFocusReportsOnlyTheRingedControl(t *testing.T) {
	focus := Focus{}
	focus.Begin()
	for i := 0; i < 2; i++ {
		focus.Register()
	}
	focus.End()

	focus.Step(false) // ring on 0
	focus.Begin()
	first, firstFocused := focus.Register()
	second, secondFocused := focus.Register()
	focus.End()

	if first != 0 || !firstFocused {
		t.Fatalf("control 0: got %d focused %v, want 0 true", first, firstFocused)
	}
	if second != 1 || secondFocused {
		t.Fatalf("control 1: got %d focused %v, want 1 false", second, secondFocused)
	}
}

func TestFocusDropsWhenControlsDisappear(t *testing.T) {
	focus := Focus{}
	focus.Begin()
	for i := 0; i < 3; i++ {
		focus.Register()
	}
	focus.End()

	focus.Step(false)
	focus.Step(false)
	focus.Step(false) // ring on 2

	// The page changed and now lays out a single control.
	focus.Begin()
	focus.Register()
	focus.End()

	if index, visible := focus.Current(); visible || index != 0 {
		t.Fatalf("after shrink: got index %d visible %v, want 0 false", index, visible)
	}

	// A later Tab starts from the first control.
	focus.Begin()
	focus.Register()
	focus.End()
	if index, visible := focus.Step(false); index != 0 || !visible {
		t.Fatalf("first Tab after shrink: got index %d visible %v, want 0 true", index, visible)
	}
}

func TestFocusIgnoresTabWithoutControls(t *testing.T) {
	focus := Focus{}
	focus.Begin()
	focus.End()

	if index, visible := focus.Step(false); index != 0 || visible {
		t.Fatalf("no controls: got index %d visible %v, want 0 false", index, visible)
	}
}
