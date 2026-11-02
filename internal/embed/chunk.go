package embed

import (
	"strings"

	"github.com/perber/wiki/internal/core/markdown"
)

// Chunking constants (docs/srs/zh-search-and-retrieval.srs.md §6.5).
const (
	// ChunkMaxChars caps one passage. A longer section is split again; every
	// piece keeps the section's anchor, so a hit still links to the heading it
	// came from (FR-405).
	ChunkMaxChars = 800
	// ChunkMinChars is the shortest trailing piece worth its own chunk: a
	// smaller remainder is folded back into the previous piece instead of
	// being indexed as a fragment.
	ChunkMinChars = 40
)

// headingPathSeparator joins the headings above a passage.
const headingPathSeparator = " > "

// Passage is one indexing unit before it is embedded: a run of text with the
// heading it belongs to. The page introduction is a passage without a heading.
type Passage struct {
	Heading     string
	HeadingPath string
	Anchor      string
	Text        string
}

// SplitPage splits a page body into the passages the index embeds (FR-405):
// the introduction before the first section, then one group per heading of
// level two or deeper, each split further when it is too long. Chunk numbers
// are assigned by the caller from the order of the returned passages, which is
// what makes page + heading path + number unique.
func SplitPage(body string) []Passage {
	sections := markdown.Sections(body)
	lines := strings.Split(body, "\n")

	var passages []Passage

	introEnd := len(lines)
	if len(sections) > 0 {
		introEnd = sections[0].Line - 1
	}
	if intro := strings.TrimSpace(strings.Join(lines[:clamp(introEnd, 0, len(lines))], "\n")); intro != "" {
		passages = append(passages, Passage{Text: intro})
	}

	var stack []markdown.Section
	for i, section := range sections {
		start := section.Line - 1
		if start < 0 || start >= len(lines) {
			continue
		}
		end := len(lines)
		if i+1 < len(sections) {
			end = sections[i+1].Line - 1
		}
		text := strings.TrimSpace(strings.Join(lines[start:clamp(end, start, len(lines))], "\n"))
		if text == "" {
			continue
		}

		for len(stack) > 0 && stack[len(stack)-1].Level >= section.Level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, section)

		path := headingPath(stack)
		for _, piece := range splitLongText(text) {
			passages = append(passages, Passage{
				Heading:     section.Title,
				HeadingPath: path,
				Anchor:      section.Anchor,
				Text:        piece,
			})
		}
	}

	return passages
}

// headingPath renders the heading chain of a passage, "Deploy > Rollback".
func headingPath(stack []markdown.Section) string {
	titles := make([]string, len(stack))
	for i, section := range stack {
		titles[i] = section.Title
	}
	return strings.Join(titles, headingPathSeparator)
}

// splitLongText splits one section's text into pieces of at most ChunkMaxChars
// runes, preferring a line break and then a space as the cut. A final piece
// shorter than ChunkMinChars is folded into the previous one rather than
// indexed as a fragment.
func splitLongText(text string) []string {
	runes := []rune(text)
	if len(runes) <= ChunkMaxChars {
		return []string{text}
	}

	var pieces []string
	for len(runes) > 0 {
		if len(runes) <= ChunkMaxChars {
			if piece := strings.TrimSpace(string(runes)); piece != "" {
				pieces = append(pieces, piece)
			}
			break
		}

		cut := cutPoint(runes)
		if piece := strings.TrimSpace(string(runes[:cut])); piece != "" {
			pieces = append(pieces, piece)
		}
		runes = []rune(strings.TrimLeft(string(runes[cut:]), " \t\n"))
	}

	if len(pieces) > 1 && len([]rune(pieces[len(pieces)-1])) < ChunkMinChars {
		last := len(pieces) - 1
		pieces[last-1] = pieces[last-1] + " " + pieces[last]
		pieces = pieces[:last]
	}

	return pieces
}

// cutPoint returns where to cut a run longer than ChunkMaxChars: after the last
// newline in the window, else after the last space, else at the limit, but
// never before ChunkMinChars, so a pathological run of one long word still
// makes progress.
func cutPoint(runes []rune) int {
	window := runes[:ChunkMaxChars]
	for i := len(window) - 1; i >= ChunkMinChars; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := len(window) - 1; i >= ChunkMinChars; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	return ChunkMaxChars
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
