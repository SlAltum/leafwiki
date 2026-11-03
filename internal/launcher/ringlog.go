package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Ring keeps the last capacity rendered log lines for the window (FR-605).
type Ring struct {
	mu       sync.Mutex
	capacity int
	lines    []string
}

// NewRing returns a ring that holds capacity lines.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1
	}
	return &Ring{capacity: capacity}
}

// Add appends one line, dropping the oldest beyond capacity.
func (r *Ring) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.lines) == r.capacity {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.capacity-1]
	}
	r.lines = append(r.lines, line)
}

// Lines returns the newest n lines, oldest first.
func (r *Ring) Lines(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// Sink receives every rendered line besides the ring: the log file (FR-602).
type Sink interface {
	WriteLine(line string) error
}

// lineTimeLayout is the window/file timestamp format of FR-606.
const lineTimeLayout = "2006-01-02 15:04:05"

// NewHandler returns a handler that renders each record once, keeps it in the
// ring, writes it to sink, and forwards it to next (nil means nowhere: the
// process stream is already served by the handler this one is attached next to).
//
// Every level is accepted: the window and the file are for diagnosis, so they
// follow the process's own LEAFWIKI_LOG_LEVEL filter rather than tightening it.
func NewHandler(next slog.Handler, capacity int, sink Sink) *RingHandler {
	return &RingHandler{next: next, ring: NewRing(capacity), sink: sink}
}

// RingHandler implements slog.Handler (FR-601/606).
type RingHandler struct {
	next slog.Handler
	ring *Ring
	sink Sink

	mu sync.Mutex
	// prefix holds the attributes added through WithAttrs, already rendered
	// with the group path in force when they were added: a group introduced
	// later must not re-nest them (slog appends, it does not rewrite).
	prefix string
	groups []string
}

// Ring exposes the buffered lines to the view.
func (h *RingHandler) Ring() *Ring { return h.ring }

func (h *RingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *RingHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mu.Lock()
	prefix := h.prefix
	groups := append([]string(nil), h.groups...)
	h.mu.Unlock()

	line := renderLine(record, prefix, groups)
	h.ring.Add(line)
	if h.sink != nil {
		_ = h.sink.WriteLine(line)
	}
	if h.next != nil {
		return h.next.Handle(ctx, record.Clone())
	}
	return nil
}

func (h *RingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()

	var rendered strings.Builder
	for _, attr := range attrs {
		writeAttr(&rendered, h.groups, attr)
	}

	clone := &RingHandler{
		next:   h.next,
		ring:   h.ring,
		sink:   h.sink,
		prefix: h.prefix + rendered.String(),
		groups: append([]string(nil), h.groups...),
	}
	if h.next != nil {
		clone.next = h.next.WithAttrs(attrs)
	}
	return clone
}

func (h *RingHandler) WithGroup(name string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()

	clone := &RingHandler{
		next:   h.next,
		ring:   h.ring,
		sink:   h.sink,
		prefix: h.prefix,
		groups: append(append([]string(nil), h.groups...), name),
	}
	if h.next != nil {
		clone.next = h.next.WithGroup(name)
	}
	return clone
}

// renderLine builds "<time> <LEVEL> <msg> <k=v ...>" (FR-606).
func renderLine(record slog.Record, prefix string, groups []string) string {
	var b strings.Builder

	b.WriteString(record.Time.Local().Format(lineTimeLayout))
	b.WriteByte(' ')
	b.WriteString(levelName(record.Level))
	b.WriteByte(' ')
	b.WriteString(record.Message)
	b.WriteString(prefix)

	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(&b, groups, attr)
		return true
	})
	return b.String()
}

func writeAttr(b *strings.Builder, groups []string, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) {
		return
	}
	// Groups are flattened into dotted keys; the line format has no nesting.
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		for _, sub := range value.Group() {
			writeAttr(b, append(groups, attr.Key), sub)
		}
		return
	}

	b.WriteByte(' ')
	b.WriteString(strings.Join(append(append([]string(nil), groups...), attr.Key), "."))
	b.WriteByte('=')
	b.WriteString(renderValue(value))
}

func renderValue(value slog.Value) string {
	if value.Kind() == slog.KindString {
		text := value.String()
		if strings.ContainsAny(text, " =\"") || text == "" {
			return strconv.Quote(text)
		}
		return text
	}
	return value.String()
}

func levelName(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// FileSink appends rendered lines to one file and rotates it (FR-602). When the
// preferred directory is not writable it falls back to the system temp
// directory, and Path reports where the lines actually go (FR-603).
type FileSink struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	size     int64
}

const (
	defaultLogMaxBytes = 1 << 20 // 1 MiB, FR-602
	logFileName        = "launcher.log"
	logDirName         = "logs"
)

// OpenFileSink opens <data-dir>/logs/launcher.log, falling back to
// <temp>/leafwiki-launcher.log. The returned path is the one in use.
func OpenFileSink(dataDir string) (*FileSink, error) {
	preferred := filepath.Join(dataDir, logDirName)
	if err := os.MkdirAll(preferred, 0o700); err == nil {
		if sink, err := openSink(filepath.Join(preferred, logFileName)); err == nil {
			return sink, nil
		}
	}

	fallback := filepath.Join(os.TempDir(), "leafwiki-"+logFileName)
	return openSink(fallback)
}

func openSink(path string) (*FileSink, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	sink := &FileSink{path: path, maxBytes: defaultLogMaxBytes, file: file, size: info.Size()}
	if sink.size > sink.maxBytes {
		// A leftover oversized file (crash before rotation) starts fresh.
		if err := sink.rotate(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return sink, nil
}

// Path is the file the sink writes to.
func (s *FileSink) Path() string { return s.path }

// WriteLine appends one line, rotating first when the file would exceed its
// limit. Lines are written whole so the file stays greppable.
func (s *FileSink) WriteLine(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return nil
	}
	if s.size+int64(len(line))+1 > s.maxBytes {
		if err := s.rotate(); err != nil {
			return err
		}
	}

	written, err := s.file.WriteString(line + "\n")
	s.size += int64(written)
	return err
}

// Close releases the file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *FileSink) rotate() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}

	backup := s.path + ".1"
	if err := os.Rename(s.path, backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate %s: %w", s.path, err)
	}

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", s.path, err)
	}
	s.file = file
	s.size = 0
	return nil
}
