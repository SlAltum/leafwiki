package search

import (
	"strings"
	"testing"
)

// The hand-written corpus is the honest one but it is small. These cases are
// derived from the pages themselves, which is free and repeatable but measures
// something narrower - say which, not "quality":
//
//   - spans: exact substrings taken from a page. They measure the tokenizer and
//     the index, nothing about meaning. Ground truth is certain.
//   - absent: words no page contains. They measure false positives.
//   - substitutes: a word pair like 回滚/退回, applied where the page uses the
//     first. They measure one real gap (the same meaning under other words) and
//     only that one; a person's paraphrase is still worth more.

// substitutePairs are word pairs a person would call interchangeable in this
// library. Extending this table is the cheapest way to widen the semantic probe.
var substitutePairs = [][2]string{
	{"回滚", "退回"},
	{"部署", "上线"},
	{"撤销", "取消"},
	{"备份", "存档"},
	{"恢复", "还原"},
	{"告警", "报警"},
	{"监控", "观测"},
	{"容量", "余量"},
	{"扩容", "加机器"},
	{"快照", "存档副本"},
	{"阈值", "门限"},
	{"灰度", "分批"},
}

// absentWords must not appear in the corpus: a false positive test needs a
// question the library truly cannot answer.
var absentWords = []string{
	"量子退火", "光合作用", "猫粮配方", "烘焙温度", "足球战术",
	"乐谱编配", "天文望远镜", "园艺修剪", "陶艺拉坯", "滑雪装备",
}

// spanLength is how many runes an exact-span query covers: long enough to be
// specific, short enough to stay a substring search.
const spanLength = 8

// generatedCases derives the mechanical cases from the corpus pages.
func generatedCases(t *testing.T, corpus evalCorpus, ids map[string]string) (spans, absent, substitutes []evalCase) {
	t.Helper()

	for _, page := range corpus.Pages {
		body := page.Body
		if span, ok := longestSpan(body); ok {
			spans = append(spans, evalCase{Query: span, Kind: evalSubstring, Relevant: []string{ids[page.Slug]}})
		}
	}

	for _, word := range absentWords {
		if corpusMentions(corpus, word) {
			// A corpus the fixture was not written for; that word is not a
			// negative case here, so it is skipped rather than asserted.
			continue
		}
		absent = append(absent, evalCase{Query: word, Kind: evalNegative})
	}

	for _, page := range corpus.Pages {
		for _, pair := range substitutePairs {
			if !strings.Contains(page.Body, pair[0]) {
				continue
			}
			// The substitute has to be absent from the whole library, otherwise
			// the keyword path could answer it and the case would measure
			// nothing about meaning.
			if corpusMentions(corpus, pair[1]) {
				continue
			}
			// The query is the word the page does not use: that is the gap.
			substitutes = append(substitutes, evalCase{
				Query:    pair[1],
				Kind:     evalParaphrase,
				Relevant: []string{ids[page.Slug]},
			})
		}
	}
	return spans, absent, substitutes
}

// longestSpan returns a fixed-length substring from the longest line of the
// body: a heading would be too easy, a mid-paragraph run is what people quote.
func longestSpan(body string) (string, bool) {
	var best string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if len([]rune(line)) > len([]rune(best)) {
			best = line
		}
	}
	if len([]rune(best)) < spanLength {
		return "", false
	}
	runes := []rune(best)
	start := (len(runes) - spanLength) / 2
	return string(runes[start : start+spanLength]), true
}

// corpusMentions reports whether any page contains the word.
func corpusMentions(corpus evalCorpus, word string) bool {
	for _, page := range corpus.Pages {
		if strings.Contains(page.Body, word) || strings.Contains(page.Title, word) {
			return true
		}
	}
	return false
}
