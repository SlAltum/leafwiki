package main

import "testing"

func TestValidateVectorsMode(t *testing.T) {
	for _, mode := range []string{vectorsModeOff, vectorsModeAuto, vectorsModeManual} {
		if err := validateVectorsMode(mode); err != nil {
			t.Errorf("validateVectorsMode(%q) = %v, want nil", mode, err)
		}
	}

	for _, mode := range []string{"", "on", "AUTO", " true", "auto "} {
		if err := validateVectorsMode(mode); err == nil {
			t.Errorf("validateVectorsMode(%q) accepted an unknown mode", mode)
		}
	}
}

func TestVectorsOptions_DefaultIsOff(t *testing.T) {
	options := &vectorsOptions{vectors: vectorsModeOff}
	if options.enabled() {
		t.Fatal("the default configuration reports semantic search as enabled")
	}

	options.vectors = vectorsModeAuto
	if !options.enabled() {
		t.Fatal("auto does not report semantic search as enabled")
	}
}
