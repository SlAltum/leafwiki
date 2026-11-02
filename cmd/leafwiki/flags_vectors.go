package main

import (
	"fmt"

	"github.com/urfave/cli/v3"
)

// Semantic-search modes (docs/srs/zh-search-and-retrieval.srs.md §6.3).
const (
	vectorsModeOff    = "off"
	vectorsModeAuto   = "auto"
	vectorsModeManual = "manual"
)

// vectorsOptions holds the semantic-search options.
type vectorsOptions struct {
	vectors         string
	vectorsModelDir string
	vectorsLibrary  string
}

// Flags declares them: the switch, the model directory and their environment
// variables live in one place each.
func (o *vectorsOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "vectors",
			Destination: &o.vectors,
			Category:    catVectors,
			Usage:       "semantic search: off (no model, no vectors file), auto (index after saves) or manual (index only on request)",
			Value:       vectorsModeOff,
			Sources:     envVars("LEAFWIKI_VECTORS"),
			Config:      trimmed,
			Validator:   validateVectorsMode,
		},
		&cli.StringFlag{
			Name:        "vectors-library",
			Destination: &o.vectorsLibrary,
			Category:    catVectors,
			Usage:       "path to the libonnxruntime shared library the encoder loads",
			Sources:     envVars("LEAFWIKI_VECTORS_LIBRARY"),
			Config:      trimmed,
		},
		&cli.StringFlag{
			Name:        "vectors-model-dir",
			Destination: &o.vectorsModelDir,
			Category:    catVectors,
			Usage:       "directory holding the encoder model; defaults to <data-dir>/models",
			Sources:     envVars("LEAFWIKI_VECTORS_MODEL_DIR"),
			Config:      trimmed,
		},
	}
}

// validateVectorsMode rejects anything but the three documented values: a typo
// would otherwise start an instance that silently has no semantic search.
func validateVectorsMode(value string) error {
	switch value {
	case vectorsModeOff, vectorsModeAuto, vectorsModeManual:
		return nil
	default:
		return fmt.Errorf("expected one of %q, %q or %q, got %q", vectorsModeOff, vectorsModeAuto, vectorsModeManual, value)
	}
}

// enabled reports whether semantic search was switched on at all.
func (o *vectorsOptions) enabled() bool {
	return o.vectors != vectorsModeOff && o.vectors != ""
}
