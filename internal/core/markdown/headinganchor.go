package markdown

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeHeadingID turns a heading's text into the id the reader uses to link
// to that section. It is the exact counterpart of slugifyHeadline in
// ui/leafwiki-ui/src/features/preview/rehypeLineNumber.ts: both sides read the
// same vectors from testdata/heading_anchor_vectors.json, so a change on one
// side fails the other's test (FR-502, FR-506).
//
// The sequence: NFKD, ß/ẞ to ss, lowercase, drop combining marks, trim, keep
// only letters, numbers, whitespace and "-", collapse whitespace and "-" runs
// to one "-", trim the dashes at both ends. An underscore is dropped rather
// than turned into a dash: the reader's existing anchors have always spelled
// it that way. CJK characters are letters, so they survive (FR-505).
//
// Two differences to the TypeScript side are deliberate: Go trims U+0085 and
// JavaScript does not, and JavaScript trims U+FEFF and Go does not. Neither
// appears in a heading that came from markdown.
func NormalizeHeadingID(text string) string {
	decomposed := norm.NFKD.String(text)
	decomposed = strings.NewReplacer("ß", "ss", "ẞ", "ss").Replace(decomposed)
	decomposed = strings.ToLower(decomposed)

	var withoutMarks strings.Builder
	withoutMarks.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
			continue
		}
		withoutMarks.WriteRune(r)
	}

	var kept strings.Builder
	kept.Grow(withoutMarks.Len())
	collapsing := false
	for _, r := range strings.TrimSpace(withoutMarks.String()) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			collapsing = false
			kept.WriteRune(r)
		case unicode.IsSpace(r) || r == '-':
			if !collapsing {
				collapsing = true
				kept.WriteRune('-')
			}
		}
	}

	return strings.Trim(kept.String(), "-")
}
