package markdown

import (
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var sectionParser = goldmark.New()

// Section is one heading of a markdown body: the line it starts on, its level,
// the reader-visible title (inline markup stripped) and the anchor the reader
// gives that heading.
type Section struct {
	Line   int
	Level  int
	Title  string
	Anchor string
}

// Sections returns the headings of body in document order, with the anchors the
// reader assigns. A fenced code block never produces a section, repeated
// titles are numbered the way the reader numbers them (anchor, anchor-1, …),
// and a heading whose text normalizes to nothing gets no anchor at all, exactly
// as the reader leaves it without an id (FR-502, FR-505).
func Sections(body string) []Section {
	src := []byte(body)
	doc := sectionParser.Parser().Parse(text.NewReader(src))

	counts := make(map[string]int)
	var sections []Section

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		heading, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}

		title := strings.TrimSpace(headingText(src, heading))
		anchor := NormalizeHeadingID(title)
		if anchor != "" {
			count := counts[anchor]
			counts[anchor] = count + 1
			if count > 0 {
				anchor = anchor + "-" + strconv.Itoa(count)
			}
		}

		sections = append(sections, Section{
			Line:   lineOf(src, heading),
			Level:  heading.Level,
			Title:  title,
			Anchor: anchor,
		})
		return ast.WalkSkipChildren, nil
	})

	return sections
}

// headingText collects the visible text of one heading, so inline markup
// ("**Bold**", `code`, links) does not leak into the title or the anchor.
func headingText(src []byte, heading *ast.Heading) string {
	var text strings.Builder
	_ = ast.Walk(heading, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || child == heading {
			return ast.WalkContinue, nil
		}
		if node, ok := child.(*ast.Text); ok {
			// The segment already carries the spaces between inline nodes; adding
			// one would turn "**Deploy** steps" into "Deploy  steps".
			text.Write(node.Segment.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return text.String()
}

// lineOf returns the 1-based line the heading starts on.
func lineOf(src []byte, heading *ast.Heading) int {
	lines := heading.Lines()
	if lines.Len() == 0 {
		return 1
	}
	offset := lines.At(0).Start
	if offset > len(src) {
		offset = len(src)
	}
	return strings.Count(string(src[:offset]), "\n") + 1
}
