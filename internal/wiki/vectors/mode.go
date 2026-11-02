package vectors

import "fmt"

// TokenizerVersion identifies the tokenizer implementation that produced the
// vectors. It is part of a build's identity, so a change here invalidates the
// current build (FR-408).
const TokenizerVersion = "wordpiece-1"

// Mode is how semantic indexing is driven (docs/srs/zh-search-and-retrieval.srs.md §6.3).
type Mode string

const (
	// ModeOff keeps the feature off: no model, no vectors database, no queue.
	ModeOff Mode = "off"
	// ModeAuto indexes after every save.
	ModeAuto Mode = "auto"
	// ModeManual only indexes when an operator asks for it.
	ModeManual Mode = "manual"
)

// ParseMode reads the configured mode. An empty value means off, so an
// instance without the option keeps the feature off.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case "":
		return ModeOff, nil
	case ModeOff, ModeAuto, ModeManual:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("vectors: expected one of %q, %q or %q, got %q", ModeOff, ModeAuto, ModeManual, value)
	}
}

// Enabled reports whether semantic search is switched on at all.
func (m Mode) Enabled() bool {
	return m == ModeAuto || m == ModeManual
}

// AutoIndex reports whether a save should queue the page.
func (m Mode) AutoIndex() bool {
	return m == ModeAuto
}
