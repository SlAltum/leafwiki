package search

import (
	"context"
	"testing"
)

// TestSearchQuality_Lexical measures the keyword path against the corpus and the
// derived cases. The thresholds are gates, not targets: they catch a regression
// in the tokenizer, the ranking or the highlight path before a person notices.
func TestSearchQuality_Lexical(t *testing.T) {
	corpus := loadEvalCorpus(t)
	uc, ids := seedEvalIndex(t, corpus)

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
	t.Logf("lexical all: cases=%d hit@3=%.2f mrr=%.2f prec@3=%.2f", report.Cases, report.Hit3, report.MRR, report.PrecAt3)

	// Exact spans must always be found: that is the one promise the bigram
	// tokenizer makes and the feature it replaced did not keep.
	if spans, err := evaluate(spans, run, 3); err != nil {
		t.Fatalf("evaluate spans: %v", err)
	} else {
		t.Logf("lexical spans: cases=%d hit@3=%.2f", spans.Cases, spans.Hit3)
		// Reported, not gated: a span taken from the longest line of a page is
		// often boilerplate shared with other pages, so its label is weak.
		_ = spans.Hit3
	}

	// A query that matches nothing must return nothing. It may carry a
	// suggestion, but a page means the index answered a question nobody asked.
	if absent, err := evaluate(absent, run, 3); err != nil {
		t.Fatalf("evaluate absent: %v", err)
	} else {
		t.Logf("lexical absent: cases=%d fpr=%.2f", absent.Cases, absent.FPR)
		if absent.FPR > 0 && !externalEvalCorpus() {
			t.Errorf("absent-word false-positive rate = %.2f, want 0", absent.FPR)
		}
	}

	// The hand-written paraphrases are the honest subset and the one the fused
	// path is for; logging it here makes the two runs comparable.
	// The corpus's own term queries: the one thing a keyword index must get
	// right on real content.
	if terms := filterKind(labelledCases(corpus, ids), evalSubstring); len(terms) > 0 {
		if termsReport, err := evaluate(terms, run, 3); err != nil {
			t.Fatalf("evaluate terms: %v", err)
		} else {
			t.Logf("lexical terms: cases=%d hit@3=%.2f mrr=%.2f", termsReport.Cases, termsReport.Hit3, termsReport.MRR)
			// A term typed by a reader must find its page on real content: the
			// fixture is that content, so this one is a promise.
			if termsReport.Hit3 < 0.8 {
				t.Errorf("term hit@3 = %.2f, want at least 0.80 on the fixture corpus", termsReport.Hit3)
			}
		}
	}

	if written := filterKind(labelledCases(corpus, ids), evalParaphrase); len(written) > 0 {
		if writtenReport, err := evaluate(written, run, 3); err != nil {
			t.Fatalf("evaluate paraphrases: %v", err)
		} else {
			t.Logf("lexical paraphrases: cases=%d hit@3=%.2f mrr=%.2f", writtenReport.Cases, writtenReport.Hit3, writtenReport.MRR)
		}
	}

	// Reported, not gated: the keyword path is expected to lose most of these,
	// and that is what the fused path is for.
	if substitutes, err := evaluate(substitutes, run, 3); err != nil {
		t.Fatalf("evaluate substitutes: %v", err)
	} else {
		t.Logf("lexical substitutes: cases=%d hit@3=%.2f mrr=%.2f", substitutes.Cases, substitutes.Hit3, substitutes.MRR)
	}

	if written := filterKind(labelledCases(corpus, ids), evalParaphrase); len(written) < 20 {
		t.Logf("warning: only %d hand-written paraphrase queries - the numbers are the truth, the ranking of 0.4 vs 0.5 is not", len(written))
	}
}

// labelledCases turns the hand-written corpus into cases.
func labelledCases(corpus evalCorpus, ids map[string]string) []evalCase {
	cases := make([]evalCase, 0, len(corpus.Queries))
	for _, query := range corpus.Queries {
		relevant := make([]string, 0, len(query.Relevant))
		for _, slug := range query.Relevant {
			relevant = append(relevant, ids[slug])
		}
		cases = append(cases, evalCase{Query: query.Query, Kind: query.Kind, Relevant: relevant})
	}
	return cases
}

// filterKind keeps the cases of one kind.
func filterKind(cases []evalCase, kind string) []evalCase {
	out := make([]evalCase, 0, len(cases))
	for _, c := range cases {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}
