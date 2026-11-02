//go:build !vectors

package embed

import "errors"

// ErrEncoderNotBuilt is returned by a build compiled without the vectors tag:
// the default binary carries no inference runtime at all.
var ErrEncoderNotBuilt = errors.New("embed: this build was compiled without the vectors tag")

// EncoderOptions has the same shape in both builds so a caller compiles either
// way; the untagged build ignores it.
type EncoderOptions struct {
	SharedLibraryPath string
	InputNames        []string
	OutputName        string
	MaxTokens         int
	IntraOpThreads    int
}

// EncoderAvailable reports whether this build can run the model at all.
func EncoderAvailable() bool { return false }

// NewEncoder always fails in a build without the vectors tag.
func NewEncoder(_ *Model, _ EncoderOptions) (*Encoder, error) {
	return nil, ErrEncoderNotBuilt
}

// Encoder keeps callers compiling in the untagged build.
type Encoder struct{}

// Close satisfies the same surface as the tagged build.
func (e *Encoder) Close() error { return nil }

// Encode always fails in a build without the vectors tag.
func (e *Encoder) Encode(_ []string) ([][]float32, error) {
	return nil, ErrEncoderNotBuilt
}
