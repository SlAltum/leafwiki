package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	coresearch "github.com/perber/wiki/internal/search"
	coretags "github.com/perber/wiki/internal/tags"
	"github.com/perber/wiki/internal/test_utils"
)

// The evaluation corpus is one file: the pages that make up a small library and
// the queries with the pages a person decided are the right answers
// (docs/srs/zh-search-and-retrieval.srs.md §7). It is hand-labelled, because a
// generated corpus measures the generator, not the search.
type evalPage struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type evalQuery struct {
	Query    string   `json:"query"`
	Kind     string   `json:"kind"` // substring, paraphrase or negative
	Relevant []string `json:"relevant"`
	Note     string   `json:"note"`
}

type evalCorpus struct {
	Pages   []evalPage  `json:"pages"`
	Queries []evalQuery `json:"queries"`
}

const (
	evalSubstring  = "substring"
	evalParaphrase = "paraphrase"
	evalNegative   = "negative"
)

// evalCorpusPath is the built-in corpus unless LEAFWIKI_EVAL_CORPUS names
// another file. A real wiki's content makes a far better corpus than an invented
// one, but it belongs to whoever runs the wiki, so it is read from outside the
// repository.
func evalCorpusPath() string {
	if path := os.Getenv("LEAFWIKI_EVAL_CORPUS"); path != "" {
		return path
	}
	return filepath.Join("testdata", "eval", "corpus.json")
}

// externalEvalCorpus reports whether the numbers come from a corpus outside the
// repository. Those are reported, not gated: a stranger's corpus must not fail
// the build.
func externalEvalCorpus() bool {
	return os.Getenv("LEAFWIKI_EVAL_CORPUS") != ""
}

// loadEvalCorpus reads the corpus and reports why it cannot be used, so a broken
// fixture fails loudly instead of quietly measuring nothing.
func loadEvalCorpus(t *testing.T) evalCorpus {
	t.Helper()

	raw, err := os.ReadFile(evalCorpusPath())
	if err != nil {
		t.Fatalf("read the evaluation corpus: %v", err)
	}
	var corpus evalCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse the evaluation corpus: %v", err)
	}

	if len(corpus.Pages) == 0 {
		t.Fatal("the evaluation corpus has no pages")
	}
	if len(corpus.Queries) == 0 {
		t.Fatal("the evaluation corpus has no queries")
	}

	known := make(map[string]bool, len(corpus.Pages))
	for _, page := range corpus.Pages {
		if page.Slug == "" || page.Title == "" {
			t.Fatalf("an evaluation page has no slug or title: %#v", page)
		}
		if known[page.Slug] {
			t.Fatalf("duplicate page slug %q in the evaluation corpus", page.Slug)
		}
		known[page.Slug] = true
	}

	for _, query := range corpus.Queries {
		if query.Query == "" {
			t.Fatal("an evaluation query is empty")
		}
		switch query.Kind {
		case evalSubstring, evalParaphrase, evalNegative:
		default:
			t.Fatalf("query %q has unknown kind %q", query.Query, query.Kind)
		}
		if query.Kind != evalNegative && len(query.Relevant) == 0 {
			t.Fatalf("query %q is %s but names no relevant page", query.Query, query.Kind)
		}
		for _, slug := range query.Relevant {
			if !known[slug] {
				t.Fatalf("query %q expects unknown page %q", query.Query, slug)
			}
		}
	}
	return corpus
}

// seedEvalIndex builds a search index and a tree from the corpus pages, and
// returns the slug-to-page-id mapping the queries are labelled with.
func seedEvalIndex(t *testing.T, corpus evalCorpus) (*SearchUseCase, map[string]string) {
	uc, ids, _ := seedEvalIndexWithTree(t, corpus)
	return uc, ids
}

// seedEvalIndexWithTree also hands back the tree, which the semantic path needs
// as its page source.
func seedEvalIndexWithTree(t *testing.T, corpus evalCorpus) (*SearchUseCase, map[string]string, *tree.TreeService) {
	t.Helper()

	dir := t.TempDir()
	treeSvc := tree.NewTreeService(dir)
	if err := treeSvc.LoadTree(); err != nil {
		t.Fatalf("LoadTree: %v", err)
	}
	index, err := coresearch.NewSQLiteIndex(dir)
	if err != nil {
		t.Fatalf("NewSQLiteIndex: %v", err)
	}
	t.Cleanup(func() { test_utils.WrapCloseWithErrorCheck(index.Close, t) })

	tagsStore, err := coretags.NewTagsStore(dir)
	if err != nil {
		t.Fatalf("NewTagsStore: %v", err)
	}
	t.Cleanup(func() { test_utils.WrapCloseWithErrorCheck(tagsStore.Close, t) })

	uc := NewSearchUseCase(index, coretags.NewTagsService(tagsStore), treeSvc, nil)
	ids := make(map[string]string, len(corpus.Pages))
	for _, page := range corpus.Pages {
		ids[page.Slug] = createIndexedPage(t, uc, treeSvc, page.Title, page.Slug, page.Body)
	}
	return uc, ids, treeSvc
}

// evalCase is one query with the page ids that answer it, in the order a person
// ranked them.
type evalCase struct {
	Query    string
	Kind     string
	Relevant []string
}

// evalReport is what one run measured. Empty slices stay empty, so a metric that
// was not applicable reads as such instead of as zero.
type evalReport struct {
	Cases   int
	Hit3    float64 // at least one relevant page in the top 3
	MRR     float64 // mean reciprocal rank of the first relevant page
	PrecAt3 float64 // relevant pages among the top 3
	FPR     float64 // negative queries that returned anything
}

// evaluate runs every case through search and computes the metrics. It is
// deliberately independent of the index: the same function serves the keyword
// path and the fused path, so both are measured the same way.
func evaluate(cases []evalCase, search func(query string) ([]string, error), k int) (evalReport, error) {
	report := evalReport{Cases: len(cases)}
	var hit, reciprocal, precision float64
	var positives, negatives, falsePositives int

	for _, evalCase := range cases {
		ids, err := search(evalCase.Query)
		if err != nil {
			return report, err
		}
		top := ids
		if len(top) > k {
			top = top[:k]
		}

		if evalCase.Kind == evalNegative {
			negatives++
			if len(ids) > 0 {
				falsePositives++
			}
			continue
		}

		positives++
		wanted := make(map[string]bool, len(evalCase.Relevant))
		for _, id := range evalCase.Relevant {
			wanted[id] = true
		}
		found := 0
		for _, id := range top {
			if wanted[id] {
				found++
			}
		}
		precision += float64(found) / float64(k)
		if found > 0 {
			hit++
		}
		if rank := rankOfFirst(ids, wanted); rank > 0 {
			reciprocal += 1 / float64(rank)
		}
	}

	if positives > 0 {
		report.Hit3 = hit / float64(positives)
		report.MRR = reciprocal / float64(positives)
		report.PrecAt3 = precision / float64(positives)
	}
	if negatives > 0 {
		report.FPR = float64(falsePositives) / float64(negatives)
	}
	return report, nil
}

// rankOfFirst returns the 1-based position of the first relevant page, or 0.
func rankOfFirst(ids []string, wanted map[string]bool) int {
	for i, id := range ids {
		if wanted[id] {
			return i + 1
		}
	}
	return 0
}
