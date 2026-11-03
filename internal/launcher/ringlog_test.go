package launcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRing_KeepsTheNewestLines(t *testing.T) {
	ring := NewRing(3)
	for _, line := range []string{"one", "two", "three", "four"} {
		ring.Add(line)
	}

	got := ring.Lines(10)
	want := []string{"two", "three", "four"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Lines(10) = %v, want %v", got, want)
	}
	if last := ring.Lines(1); len(last) != 1 || last[0] != "four" {
		t.Fatalf("Lines(1) = %v, want [four]", last)
	}
}

// FR-606: "<time> <LEVEL> <msg> <k=v ...>".
var linePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} INFO hello username=admin nested\.k=v$`)

func TestRingHandler_RendersTheLineFormat(t *testing.T) {
	handler := NewHandler(nil, 10, nil)
	log := slog.New(handler).With("username", "admin").WithGroup("nested")

	log.Info("hello", "k", "v")

	lines := handler.Ring().Lines(1)
	if len(lines) != 1 {
		t.Fatalf("ring holds %d lines, want 1", len(lines))
	}
	if !linePattern.MatchString(lines[0]) {
		t.Fatalf("line = %q, want to match %q", lines[0], linePattern.String())
	}
}

func TestRingHandler_QuotesValuesWithSpaces(t *testing.T) {
	handler := NewHandler(nil, 10, nil)
	slog.New(handler).Info("msg", "path", "C:\\my data\\wiki")

	line := handler.Ring().Lines(1)[0]
	if want := "path=" + strconv.Quote(`C:\my data\wiki`); !strings.Contains(line, want) {
		t.Fatalf("line = %q, want it to contain %q", line, want)
	}
}

// The ring keeps receiving records no matter what level the process filter
// uses, so the window shows the full story (FR-605).
func TestRingHandler_AcceptsEveryLevel(t *testing.T) {
	handler := NewHandler(nil, 10, nil)
	if !handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Enabled(debug) = false, want true")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Enabled(error) = false, want true")
	}
}

func TestFileSink_RotatesAndKeepsOneBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)

	sink, err := openSink(path)
	if err != nil {
		t.Fatalf("openSink() error = %v", err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	sink.maxBytes = 40

	for i := 0; i < 12; i++ {
		if err := sink.WriteLine("012345678"); err != nil {
			t.Fatalf("WriteLine() error = %v", err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() > sink.maxBytes {
		t.Fatalf("current log is %d bytes, want at most %d", info.Size(), sink.maxBytes)
	}
	backup, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if backup.Size() == 0 {
		t.Fatal("rotated log is empty")
	}
}

func TestOpenFileSink_FallsBackToTempDirectory(t *testing.T) {
	// A file where the data directory should be: the preferred directory
	// cannot be created, which is FR-603's case.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	sink, err := OpenFileSink(notADir)
	if err != nil {
		t.Fatalf("OpenFileSink() error = %v, want the temp fallback", err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		if err := os.Remove(sink.Path()); err != nil {
			t.Errorf("remove fallback log: %v", err)
		}
	}()

	if want := os.TempDir(); !strings.HasPrefix(sink.Path(), want) {
		t.Fatalf("Path() = %q, want a file under %q", sink.Path(), want)
	}
}
