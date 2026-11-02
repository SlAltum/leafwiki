package wiki

import (
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/embed"
	wikisearch "github.com/perber/wiki/internal/wiki/search"
	"github.com/perber/wiki/internal/wiki/vectors"
)

func TestSemanticStateAdapter(t *testing.T) {
	if got := (semanticBridge{}).SemanticState(); got != wikisearch.SemanticOff {
		t.Fatalf("a wiki without the feature reports %q, want off", got)
	}

	store, err := embed.OpenStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	service := vectors.NewService(store, nil, nil, "minilm", vectors.TokenizerVersion, vectors.ModeAuto, nil)
	adapter := semanticBridge{service: service}

	// No build and no encoder: the feature is on but cannot answer.
	if got := adapter.SemanticState(); got != wikisearch.SemanticUnavailable {
		t.Fatalf("state = %q, want unavailable before any build", got)
	}
}
