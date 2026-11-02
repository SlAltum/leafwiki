package search

import (
	"math"
	"testing"
)

// TestEvaluate covers the metric arithmetic itself: a metric that is quietly
// wrong makes every later number meaningless.
func TestEvaluate(t *testing.T) {
	answers := map[string][]string{
		"first": {"a", "b", "c", "d"},
		"later": {"x", "y", "z", "a"},
		"mixed": {"b", "a", "c"},
		"empty": {},
	}
	search := func(query string) ([]string, error) { return answers[query], nil }

	report, err := evaluate([]evalCase{
		{Query: "first", Kind: evalSubstring, Relevant: []string{"a"}},
		{Query: "later", Kind: evalParaphrase, Relevant: []string{"a"}},
		{Query: "mixed", Kind: evalParaphrase, Relevant: []string{"a", "b"}},
		{Query: "empty", Kind: evalNegative},
	}, search, 3)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if report.Cases != 4 {
		t.Fatalf("cases = %d, want 4", report.Cases)
	}
	// Two of three positives found a relevant page in the top 3.
	if diff := math.Abs(report.Hit3 - 2.0/3.0); diff > 1e-9 {
		t.Fatalf("hit@3 = %f, want 2/3", report.Hit3)
	}
	// Reciprocal ranks: 1 ("first"), 1/4 ("later", first relevant at rank
	// 4), 1 ("mixed", b is rank 1 even though the query also expects a).
	wantMRR := (1.0 + 0.25 + 1.0) / 3
	if diff := math.Abs(report.MRR - wantMRR); diff > 1e-9 {
		t.Fatalf("mrr = %f, want %f", report.MRR, wantMRR)
	}
	// Precision: 1/3, 0, 2/3.
	wantPrec := (1.0/3 + 0 + 2.0/3) / 3
	if diff := math.Abs(report.PrecAt3 - wantPrec); diff > 1e-9 {
		t.Fatalf("prec@3 = %f, want %f", report.PrecAt3, wantPrec)
	}
	if report.FPR != 0 {
		t.Fatalf("fpr = %f, want 0: the negative query returned nothing", report.FPR)
	}
}

func TestEvaluate_NegativeQueriesThatReturnPages(t *testing.T) {
	search := func(string) ([]string, error) { return []string{"anything"}, nil }

	report, err := evaluate([]evalCase{
		{Query: "gibberish", Kind: evalNegative},
		{Query: "cat", Kind: evalNegative},
	}, search, 3)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.FPR != 1 {
		t.Fatalf("fpr = %f, want 1 when every unanswerable query returns pages", report.FPR)
	}
	// No positive query means hit/mrr stay zero rather than dividing by zero.
	if report.Hit3 != 0 || report.MRR != 0 || report.PrecAt3 != 0 {
		t.Fatalf("positive metrics = %f/%f/%f, want zero", report.Hit3, report.MRR, report.PrecAt3)
	}
}

func TestRankOfFirst(t *testing.T) {
	wanted := map[string]bool{"b": true}
	if got := rankOfFirst([]string{"a", "b"}, wanted); got != 2 {
		t.Fatalf("rank = %d, want 2", got)
	}
	if got := rankOfFirst([]string{"a"}, wanted); got != 0 {
		t.Fatalf("rank = %d, want 0 when nothing matches", got)
	}
}
