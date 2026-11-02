package markdown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixture lives beside the TypeScript test that imports it
// (ui/leafwiki-ui/src/features/preview/headingAnchorVectors.json), so the two
// implementations of the anchor normalization cannot drift apart (FR-502,
// FR-506). go:embed cannot reach outside the package, hence the relative read;
// go test runs with the package directory as its working directory.
const headingAnchorVectorsPath = "../../../ui/leafwiki-ui/src/features/preview/headingAnchorVectors.json"

type headingAnchorVectors struct {
	Cases []struct {
		Input string `json:"input"`
		Want  string `json:"want"`
	} `json:"cases"`
}

func loadHeadingAnchorVectors(t *testing.T) headingAnchorVectors {
	t.Helper()

	raw, err := os.ReadFile(filepath.FromSlash(headingAnchorVectorsPath))
	if err != nil {
		t.Fatalf("reading the shared vectors: %v", err)
	}
	var vectors headingAnchorVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parsing the shared vectors: %v", err)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("the shared vector file has no cases")
	}
	return vectors
}

func TestNormalizeHeadingID_SharedVectors(t *testing.T) {
	for _, tc := range loadHeadingAnchorVectors(t).Cases {
		t.Run(tc.Input, func(t *testing.T) {
			if got := NormalizeHeadingID(tc.Input); got != tc.Want {
				t.Fatalf("NormalizeHeadingID(%q) = %q, want %q", tc.Input, got, tc.Want)
			}
		})
	}
}
