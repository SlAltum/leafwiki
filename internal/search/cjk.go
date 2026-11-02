package search

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

// UnigramEnabled also indexes single CJK characters next to their bigrams.
// It is off because a two-character word is already findable through its
// bigram, so unigrams only add postings (index size) for one extra query
// class: a lone character. See docs/srs/zh-search-and-retrieval.srs.md
// FR-103.
const UnigramEnabled = false

// foldWidthRune maps a fullwidth ASCII rune (U+FF01–U+FF5E) to its ASCII form
// and a halfwidth katakana rune (U+FF61–U+FF9F) to its fullwidth form, so
// "ＡＢＣ" and "ABC", "ｶﾀｶﾅ" and "カタカナ" reach the same postings. Every
// other rune is returned unchanged.
func foldWidthRune(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E:
		return r - 0xFEE0
	case r >= 0xFF61 && r <= 0xFF9F:
		if full := width.LookupRune(r).Wide(); full != 0 {
			return full
		}
	}
	return r
}

// isCJK reports whether r belongs to a script unicode61 keeps whole: a run of
// such runes is one token, which is why each run is replaced by bigrams
// instead.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// cjkTokenize splits text into the tokens a pages column should contain:
// every maximal non-CJK run verbatim (the tokenizer splits it into words as
// it always did), and every maximal CJK run as its adjacent bigrams, plus
// its unigrams when UnigramEnabled, plus the run itself when it is a single
// character, which has no bigram and would otherwise not be indexed at all.
// Width folding runs first, so the tokens and the query terms agree.
//
// The index and the query side both tokenize through here, which is what
// keeps them one implementation (FR-108).
func cjkTokenize(text string) []string {
	return cjkTokenizeWithUnigrams(text, UnigramEnabled)
}

// cjkTokenizeWithUnigrams is cjkTokenize with the unigram decision passed in,
// so both states are reachable from a test while production code keeps
// reading the single constant above.
func cjkTokenizeWithUnigrams(text string, unigrams bool) []string {
	var tokens []string
	run := make([]rune, 0, 8)
	var plain strings.Builder

	flushRun := func() {
		if len(run) == 0 {
			return
		}
		if unigrams || len(run) == 1 {
			for _, r := range run {
				tokens = append(tokens, string(r))
			}
		}
		for i := 0; i+1 < len(run); i++ {
			tokens = append(tokens, string(run[i:i+2]))
		}
		run = run[:0]
	}

	flushPlain := func() {
		// A segment of pure separators (commas, dashes, spaces) contributes no word, so
		// it is dropped instead of becoming a token of its own.
		if seg := strings.TrimSpace(plain.String()); len(splitIntoIndexWords(seg)) > 0 {
			tokens = append(tokens, seg)
		}
		plain.Reset()
	}

	for _, r := range text {
		r = foldWidthRune(r)
		if isCJK(r) {
			flushPlain()
			run = append(run, r)
			continue
		}
		flushRun()
		plain.WriteRune(r)
	}
	flushPlain()
	flushRun()

	return tokens
}

// cjkAnalyze turns text into the analysed string a pages column stores: the
// tokens from cjkTokenize, space separated. Space is the tokenizer's own
// separator, so the inserted ones neither add nor drop a token.
func cjkAnalyze(text string) string {
	return strings.Join(cjkTokenize(text), " ")
}

// QueryTerms returns the words a query is matched by, for highlighting the
// original text: the tokens cjkTokenize yields, with every ASCII run split
// into the words the tokenizer would produce.
func QueryTerms(query string) []string {
	var terms []string
	for _, token := range cjkTokenize(query) {
		terms = append(terms, splitIntoIndexWords(token)...)
	}
	return terms
}
