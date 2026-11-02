//go:build vectors

package search

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// Calibration probe: prints the raw cosine of the best hits for unanswerable and
// substitute queries, so the similarity floor comes from data.
func TestProbeSimilarities(t *testing.T) {
	modelsDir := os.Getenv("LEAFWIKI_VECTORS_TEST_MODELS_DIR")
	library := os.Getenv("LEAFWIKI_VECTORS_TEST_LIBRARY")
	if modelsDir == "" || library == "" {
		t.Skip("needs the model")
	}

	corpus := loadEvalCorpus(t)
	_, ids, treeSvc := seedEvalIndexWithTree(t, corpus)
	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	model, err := embed.Open(modelsDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	encoder, err := embed.NewEncoder(model, embed.EncoderOptions{SharedLibraryPath: library, IntraOpThreads: 1})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	t.Cleanup(func() { _ = encoder.Close() })

	service := vectors.NewService(store, encoder, treeSvc, model.Manifest.ModelID, vectors.TokenizerVersion, vectors.ModeManual,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := service.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	current, err := store.CurrentBuild()
	if err != nil || current == nil {
		t.Fatalf("CurrentBuild")
	}

	probe := func(kind, query, want string) {
		vec, err := encoder.EncodeQuery(query)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		hits, err := store.SearchVector(current.ID, vec, -1, 2)
		if err != nil {
			t.Fatalf("SearchVector: %v", err)
		}
		line := fmt.Sprintf("%-11s %-12q", kind, query)
		for i, hit := range hits {
			mark := " "
			if hit.PageID == want {
				mark = "*"
			}
			line += fmt.Sprintf(" %d:%s%.3f(%s)", i+1, mark, hit.Score, shortID(hit.PageID, ids))
		}
		fmt.Println(line)
	}

	_, absent, substitutes := generatedCases(t, corpus, ids)
	for _, c := range absent {
		probe("absent", c.Query, "")
	}
	for _, c := range substitutes {
		probe("substitute", c.Query, c.Relevant[0])
	}
}

func shortID(id string, ids map[string]string) string {
	for slug, pageID := range ids {
		if pageID == id {
			return slug
		}
	}
	return id
}
