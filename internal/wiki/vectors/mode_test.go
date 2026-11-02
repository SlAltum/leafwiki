package vectors

import "testing"

func TestParseMode(t *testing.T) {
	cases := []struct {
		value   string
		want    Mode
		enabled bool
		auto    bool
	}{
		{"", ModeOff, false, false},
		{"off", ModeOff, false, false},
		{"auto", ModeAuto, true, true},
		{"manual", ModeManual, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			mode, err := ParseMode(tc.value)
			if err != nil {
				t.Fatalf("ParseMode(%q) = %v", tc.value, err)
			}
			if mode != tc.want || mode.Enabled() != tc.enabled || mode.AutoIndex() != tc.auto {
				t.Fatalf("ParseMode(%q) = %v (enabled %v, auto %v)", tc.value, mode, mode.Enabled(), mode.AutoIndex())
			}
		})
	}

	if _, err := ParseMode("on"); err == nil {
		t.Fatal("ParseMode accepted an unknown mode")
	}
}
