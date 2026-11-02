//go:build vectors

package search

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// TestSearchQuality_Hybrid runs the same cases through the fused path, so the
// two runs differ in nothing but the ranking. It needs the model and the
// runtime, and skips without them:
//
//	LEAFWIKI_VECTORS_TEST_MODELS_DIR  the directory holding the model directory
//	LEAFWIKI_VECTORS_TEST_LIBRARY     path to libonnxruntime
func TestSearchQuality_Hybrid(t *testing.T) {
	modelsDir := os.Getenv("LEAFWIKI_VECTORS_TEST_MODELS_DIR")
	library := os.Getenv("LEAFWIKI_VECTORS_TEST_LIBRARY")
	if modelsDir == "" || library == "" {
		t.Skip("set LEAFWIKI_VECTORS_TEST_MODELS_DIR and LEAFWIKI_VECTORS_TEST_LIBRARY to measure the fused path")
	}

	corpus := loadEvalCorpus(t)
	uc, ids, treeSvc := seedEvalIndexWithTree(t, corpus)

	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})

	model, err := embed.Open(modelsDir)
	if err != nil {
		t.Fatalf("embed.Open: %v", err)
	}
	encoder, err := embed.NewEncoder(model, embed.EncoderOptions{SharedLibraryPath: library, IntraOpThreads: 1})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := encoder.Close(); closeErr != nil {
			t.Errorf("close encoder: %v", closeErr)
		}
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := vectors.NewService(store, encoder, treeSvc, model.Manifest.ModelID, vectors.TokenizerVersion, vectors.ModeManual, logger)
	if _, err := service.Rebuild(nil); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	uc.WithSemanticSearcher(hybridSearcher{service: service})

	cases := labelledCases(corpus, ids)
	spans, absent, substitutes := generatedCases(t, corpus, ids)
	cases = append(cases, spans...)
	cases = append(cases, absent...)
	cases = append(cases, substitutes...)

	run := func(query string) ([]string, error) {
		out, err := uc.Execute(context.Background(), SearchInput{Query: query, Limit: 3})
		if err != nil {
			return nil, err
		}
		found := make([]string, 0, len(out.Result.Items))
		for _, item := range out.Result.Items {
			found = append(found, item.PageID)
		}
		return found, nil
	}

	report, err := evaluate(cases, run, 3)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	t.Logf("hybrid all: cases=%d hit@3=%.2f mrr=%.2f prec@3=%.2f", report.Cases, report.Hit3, report.MRR, report.PrecAt3)

	// The hand-written paraphrases are the honest subset: whatever they say is
	// the closest thing to a real user this corpus has.
	if written := filterKind(labelledCases(corpus, ids), evalParaphrase); len(written) > 0 {
		if writtenReport, err := evaluate(written, run, 3); err != nil {
			t.Fatalf("evaluate paraphrases: %v", err)
		} else {
			t.Logf("hybrid paraphrases: cases=%d hit@3=%.2f mrr=%.2f", writtenReport.Cases, writtenReport.Hit3, writtenReport.MRR)
		}
	}

	// Fusion must not lose what the keyword path already found.
	if spans, err := evaluate(spans, run, 3); err != nil {
		t.Fatalf("evaluate spans: %v", err)
	} else {
		t.Logf("hybrid spans: cases=%d hit@3=%.2f", spans.Cases, spans.Hit3)
		if spans.Hit3 < 1 {
			t.Errorf("span hit@3 = %.2f, want every exact span still found", spans.Hit3)
		}
	}

	// This is the number the whole feature exists for.
	if substitutes, err := evaluate(substitutes, run, 3); err != nil {
		t.Fatalf("evaluate substitutes: %v", err)
	} else {
		t.Logf("hybrid substitutes: cases=%d hit@3=%.2f mrr=%.2f", substitutes.Cases, substitutes.Hit3, substitutes.MRR)
		// Reported, not gated: the substitute probe is crude (a two-character
		// word carries little signal), so its absolute value says less than its
		// distance from the keyword path's zero. It must beat that zero, though.
		if substitutes.Hit3 <= 0 {
			t.Errorf("substitute hit@3 = %.2f, want semantic recall to find at least some; "+
				"first check the model's vocabulary covers the corpus language - an English-only "+
				"model turns Chinese into [UNK] and every query then scores alike", substitutes.Hit3)
		}
	}

	// An unanswerable query must stay unanswered: recall above the similarity
	// floor is exactly how semantic search invents results.
	if absent, err := evaluate(absent, run, 3); err != nil {
		t.Fatalf("evaluate absent: %v", err)
	} else {
		t.Logf("hybrid absent: cases=%d fpr=%.2f", absent.Cases, absent.FPR)
		if absent.FPR > 0 {
			t.Errorf("absent-word false-positive rate = %.2f, want 0: semantic recall must not answer "+
				"a question the library cannot; raise SemanticMinSimilarity or check the model's language", absent.FPR)
		}
	}
}

// hybridSearcher adapts the vectors service to the port this slice declares.
type hybridSearcher struct {
	service *vectors.Service
}

func (h hybridSearcher) SearchPages(query string, limit int) ([]SemanticHit, error) {
	hits, err := h.service.SearchPages(query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SemanticHit, len(hits))
	for i, hit := range hits {
		out[i] = SemanticHit{PageID: hit.PageID, Anchor: hit.Anchor, Section: hit.Section}
	}
	return out, nil
}
