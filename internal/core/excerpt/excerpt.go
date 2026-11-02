package excerpt

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/yuin/goldmark"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

const MaxRunes = 180

var mdRenderer = goldmark.New(
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

var (
	sanitize = bluemonday.StrictPolicy()

	fencedCodePattern    = regexp.MustCompile("(?s)```.*?```")
	mermaidCodePattern   = regexp.MustCompile("(?s)```mermaid.*?```")
	imagePattern         = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	linkPattern          = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	htmlPattern          = regexp.MustCompile(`<[^>]+>`)
	linePrefixPattern    = regexp.MustCompile(`(?m)^\s{0,3}(#{1,6}\s*|[-*+]\s+|\d+\.\s+|>\s?)`)
	wikiImagePattern     = regexp.MustCompile(`!\[\[[^\]\n]*\]\]`)
	wikiLinkAliasPattern = regexp.MustCompile(`\[\[[^\]|\n]+\|([^\]\n]+)\]\]`)
	wikiLinkPattern      = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)

	shoutoutOpenPattern  = regexp.MustCompile(`^ {0,3}:::\s*(?P<type>[A-Za-z][\w-]*)\s*$`)
	shoutoutClosePattern = regexp.MustCompile(`^ {0,3}:::\s*$`)
	fencePattern         = regexp.MustCompile(`^ {0,3}(?P<marker>` + "`{3,}|~{3,}" + `).*$`)

	shoutoutTypeIdx  = shoutoutOpenPattern.SubexpIndex("type")
	fenceMarkerIdx   = fencePattern.SubexpIndex("marker")
	blockQuotePrefix = regexp.MustCompile(`^(> ?)+`)
)

type fenceState struct {
	markerChar   byte
	markerLength int
}

// FromContent parses markdown, skips frontmatter, and returns a short plain-text excerpt.
func FromContent(raw string) string {
	_, body, _, err := markdown.ParseFrontmatter(raw)
	if err != nil {
		body = raw
	}
	return FromBody(body)
}

// FromBody converts markdown or HTML-ish body content into a short plain-text excerpt.
func FromBody(body string) string {
	text := PlainTextFromMarkdown(body)

	if text == "" {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= MaxRunes {
		return text
	}

	truncated := strings.TrimSpace(string(runes[:MaxRunes]))
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace >= 120 {
		truncated = truncated[:lastSpace]
	}
	return strings.TrimSpace(truncated) + "..."
}

// NormalizeMarkdownBody removes non-content markdown constructs that should not leak into excerpts or search text.
func NormalizeMarkdownBody(body string) string {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	output := make([]string, 0, len(lines))
	var outerFence *fenceState

	for _, line := range lines {
		if outerFence != nil {
			output = append(output, line)
			outerFence = getFenceState(line, outerFence)
			continue
		}

		if match := shoutoutOpenPattern.FindStringSubmatch(line); match != nil {
			output = append(output, match[shoutoutTypeIdx])
			continue
		}

		if shoutoutClosePattern.MatchString(line) {
			continue
		}

		output = append(output, line)
		outerFence = getFenceState(line, outerFence)
	}

	return strings.Join(output, "\n")
}

// PlainTextFromMarkdown converts markdown body content into plain text for display excerpts.
// Fenced code blocks are stripped so excerpts show prose rather than code tokens.
func PlainTextFromMarkdown(body string) string {
	return plainText(body, false)
}

// PlainTextForSearch converts markdown body content into plain text for full-text search indexing.
// Fenced code block content is preserved so searches can find terms inside code blocks.
func PlainTextForSearch(body string) string {
	return plainText(body, true)
}

// applyInlinePatternsOutsideFences applies wiki-link and image patterns only to
// lines outside fenced code blocks. Lines inside fences are passed through
// unchanged so their content remains intact for search indexing.
func applyInlinePatternsOutsideFences(text string) string {
	lines := strings.Split(text, "\n")
	var fence *fenceState
	for i, line := range lines {
		// Strip block-quote prefixes ("> ") before fence detection so that fenced
		// code blocks inside block-quotes are correctly recognised by fencePattern,
		// which only allows up to 3 leading spaces (not "> ").
		stripped := blockQuotePrefix.ReplaceAllString(line, "")
		nextFence := getFenceState(stripped, fence)
		// Only apply patterns when staying outside a fence (not on the opening/closing
		// fence line itself — fence==nil && nextFence!=nil means we're entering a fence).
		if fence == nil && nextFence == nil {
			line = imagePattern.ReplaceAllString(line, "$1")
			line = wikiImagePattern.ReplaceAllString(line, " ")
			line = wikiLinkAliasPattern.ReplaceAllString(line, "$1")
			line = wikiLinkPattern.ReplaceAllStringFunc(line, func(m string) string {
				subs := wikiLinkPattern.FindStringSubmatch(m)
				if len(subs) < 2 {
					return ""
				}
				target := subs[1]
				if idx := strings.LastIndex(target, "/"); idx >= 0 {
					return target[idx+1:]
				}
				return target
			})
			lines[i] = line
		}
		fence = nextFence
	}
	return strings.Join(lines, "\n")
}

func plainText(body string, includeCode bool) string {
	normalized := NormalizeMarkdownBody(body)
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if !includeCode {
		normalized = fencedCodePattern.ReplaceAllString(normalized, " ")
	} else {
		// Mermaid diagram blocks contain structural syntax tokens (graph TD, -->, classDef)
		// that are not meaningful search terms, so strip them even in search mode.
		normalized = mermaidCodePattern.ReplaceAllString(normalized, " ")
	}
	normalized = applyInlinePatternsOutsideFences(normalized)
	var htmlBuf bytes.Buffer
	if err := mdRenderer.Convert([]byte(normalized), &htmlBuf); err != nil {
		htmlBuf.Reset()
		htmlBuf.Write([]byte(normalized))
	}
	text := sanitize.Sanitize(htmlBuf.String())
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = fencedCodePattern.ReplaceAllString(text, " ")
	text = imagePattern.ReplaceAllString(text, "$1")
	text = linkPattern.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "`", "")
	text = htmlPattern.ReplaceAllString(text, " ")
	text = linePrefixPattern.ReplaceAllString(text, "")
	return strings.Join(strings.Fields(text), " ")
}

func getFenceState(line string, currentFence *fenceState) *fenceState {
	match := fencePattern.FindStringSubmatch(line)
	if match == nil {
		return currentFence
	}

	marker := match[fenceMarkerIdx]
	if marker == "" {
		return currentFence
	}

	if currentFence == nil {
		return &fenceState{
			markerChar:   marker[0],
			markerLength: len(marker),
		}
	}

	if marker[0] == currentFence.markerChar && len(marker) >= currentFence.markerLength {
		return nil
	}

	return currentFence
}

// HighlightTerms wraps every case-insensitive occurrence of any term in text
// with open and close, leaving the rest of the text untouched. Matching folds
// ASCII letters only, so the byte offsets used to splice stay valid for every
// other script.
func HighlightTerms(text string, terms []string, open, close string) string {
	folded := foldASCII(text)
	type span struct{ start, end int }
	var spans []span
	for _, term := range terms {
		t := foldASCII(term)
		if t == "" {
			continue
		}
		for off := 0; off < len(folded); {
			idx := strings.Index(folded[off:], t)
			if idx < 0 {
				break
			}
			start := off + idx
			spans = append(spans, span{start, start + len(t)})
			off = start + len(t)
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var b strings.Builder
	b.Grow(len(text) + len(spans)*(len(open)+len(close)))
	pos := 0
	for _, s := range spans {
		if s.start < pos {
			// Inside a span this term overlaps; the first match already covers it.
			continue
		}
		b.WriteString(text[pos:s.start])
		b.WriteString(open)
		b.WriteString(text[s.start:s.end])
		b.WriteString(close)
		pos = s.end
	}
	b.WriteString(text[pos:])
	return b.String()
}

// ContainsTerm reports whether text contains any of the terms, with the same
// case folding HighlightTerms uses.
func ContainsTerm(text string, terms []string) bool {
	folded := foldASCII(text)
	for _, term := range terms {
		t := foldASCII(term)
		if t != "" && strings.Contains(folded, t) {
			return true
		}
	}
	return false
}

// HighlightedExcerpt returns at most MaxRunes of plain text taken around the
// first term occurrence, with the occurrences inside that window wrapped in
// <b></b>, the same tags the index used to inject, so the client renders the
// same HTML. Text that contains no term falls back to the opening window of
// FromBody.
func HighlightedExcerpt(body string, terms []string) string {
	plain := PlainTextFromMarkdown(body)
	if plain == "" {
		return ""
	}

	runes := []rune(plain)
	start := 0
	if idx := firstTermRuneIndex(plain, terms); idx > MaxRunes/3 {
		// A third of a window of lead-in, so the match is not at the edge.
		start = idx - MaxRunes/3
	}
	end := start + MaxRunes
	if end > len(runes) {
		end = len(runes)
		if start = end - MaxRunes; start < 0 {
			start = 0
		}
	}
	if start >= end {
		return ""
	}

	highlighted := HighlightTerms(strings.TrimSpace(string(runes[start:end])), terms, "<b>", "</b>")
	if start > 0 {
		highlighted = "..." + highlighted
	}
	if end < len(runes) {
		highlighted += "..."
	}
	return highlighted
}

// firstTermRuneIndex returns the rune index of the earliest term occurrence in
// text, or -1 when no term occurs.
func firstTermRuneIndex(text string, terms []string) int {
	folded := foldASCII(text)
	best := -1
	for _, term := range terms {
		t := foldASCII(term)
		if t == "" {
			continue
		}
		idx := strings.Index(folded, t)
		if idx < 0 {
			continue
		}
		if runeIdx := utf8.RuneCountInString(folded[:idx]); best < 0 || runeIdx < best {
			best = runeIdx
		}
	}
	return best
}

// foldASCII lowercases A-Z and leaves every other byte alone, so the result has
// the same length as its input and byte offsets into it address the original.
func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
