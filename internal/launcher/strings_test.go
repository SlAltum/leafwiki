package launcher

import (
	"reflect"
	"testing"
	"time"
)

func TestText_HasNoEmptyFields(t *testing.T) {
	value := reflect.ValueOf(Text)
	kind := value.Type()
	for i := 0; i < kind.NumField(); i++ {
		if value.Field(i).String() == "" {
			t.Errorf("Text.%s is empty; the window would show a blank label", kind.Field(i).Name)
		}
	}
}

func TestStatusWord_CoversEveryState(t *testing.T) {
	for name, want := range map[string]string{
		"stopped":  Text.StatusStopped,
		"starting": Text.StatusStarting,
		"running":  Text.StatusRunning,
		"stopping": Text.StatusStopping,
		"closing":  Text.StatusClosing,
		"failed":   Text.StatusFailed,
	} {
		if got := StatusWord(name); got != want {
			t.Errorf("StatusWord(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestUpTimeAndUpdatedAt_ReadAsSentences(t *testing.T) {
	if got, want := UpTime(30*time.Second), Text.UpTimePrefix+" 不到 1 分钟"; got != want {
		t.Errorf("UpTime(30s) = %q, want %q", got, want)
	}
	if got, want := UpTime(14*time.Minute), Text.UpTimePrefix+" 14 分钟"; got != want {
		t.Errorf("UpTime(14m) = %q, want %q", got, want)
	}
	if got, want := UpTime(2*time.Hour+14*time.Minute), Text.UpTimePrefix+" 2 小时 14 分"; got != want {
		t.Errorf("UpTime(2h14m) = %q, want %q", got, want)
	}

	at := time.Date(2026, 11, 3, 9, 5, 0, 0, time.Local)
	if got, want := UpdatedAt(at), Text.RefreshDoneAt+" 09:05"; got != want {
		t.Errorf("UpdatedAt() = %q, want %q", got, want)
	}
}
