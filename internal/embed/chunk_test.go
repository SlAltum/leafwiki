package embed

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitPage_IntroductionOnly(t *testing.T) {
	passages := SplitPage("Just some text without headings.\n\nSecond paragraph.")

	if len(passages) != 1 {
		t.Fatalf("SplitPage returned %d passages, want 1: %#v", len(passages), passages)
	}
	if passages[0].Heading != "" || passages[0].Anchor != "" || passages[0].HeadingPath != "" {
		t.Fatalf("introduction carries a heading: %#v", passages[0])
	}
	if !strings.Contains(passages[0].Text, "Second paragraph.") {
		t.Fatalf("introduction text = %q", passages[0].Text)
	}
}

func TestSplitPage_IntroductionAndSections(t *testing.T) {
	body := "intro line\n\n## 部署流程\n\nrun deploy\n\n## Rollback\n\ngo back\n"

	passages := SplitPage(body)

	if len(passages) != 3 {
		t.Fatalf("SplitPage returned %d passages, want 3: %#v", len(passages), passages)
	}
	if passages[0].Heading != "" || !strings.Contains(passages[0].Text, "intro line") {
		t.Fatalf("first passage = %#v, want the introduction", passages[0])
	}
	if passages[1].Heading != "部署流程" || passages[1].Anchor != "部署流程" {
		t.Fatalf("second passage = %#v", passages[1])
	}
	if !strings.Contains(passages[1].Text, "run deploy") || !strings.Contains(passages[1].Text, "## 部署流程") {
		t.Fatalf("second passage text = %q, want the heading and its body", passages[1].Text)
	}
	if passages[2].Heading != "Rollback" || passages[2].Anchor != "rollback" {
		t.Fatalf("third passage = %#v", passages[2])
	}
}

func TestSplitPage_NestedHeadingPath(t *testing.T) {
	body := "## Deploy\n\nstep one\n\n### Rollback details\n\ndetails\n\n## Other\n\nother\n"

	passages := SplitPage(body)

	want := []string{"deploy", "deploy" + headingPathSeparator + "rollback-details", "other"}
	var got []string
	for _, passage := range passages {
		got = append(got, passage.Anchor+"|"+passage.HeadingPath)
	}
	wantPaths := []string{"deploy|Deploy", "rollback-details|Deploy" + headingPathSeparator + "Rollback details", "other|Other"}
	if len(got) != len(wantPaths) {
		t.Fatalf("got %v, want %v (anchors %v)", got, wantPaths, want)
	}
	for i := range wantPaths {
		if got[i] != wantPaths[i] {
			t.Fatalf("passage %d = %q, want %q", i, got[i], wantPaths[i])
		}
	}
}

func TestSplitPage_DuplicateHeadingsGetNumberedAnchors(t *testing.T) {
	body := "## Repeat\n\nfirst\n\n## Repeat\n\nsecond\n"

	passages := SplitPage(body)

	if len(passages) != 2 {
		t.Fatalf("SplitPage returned %d passages, want 2: %#v", len(passages), passages)
	}
	if passages[0].Anchor != "repeat" || passages[1].Anchor != "repeat-1" {
		t.Fatalf("anchors = %q, %q; want repeat, repeat-1", passages[0].Anchor, passages[1].Anchor)
	}
}

func TestSplitPage_LongSectionSharesItsAnchor(t *testing.T) {
	body := "## Long\n\n" + strings.Repeat("abcde fghij\n", 100)

	passages := SplitPage(body)

	if len(passages) < 2 {
		t.Fatalf("SplitPage returned %d passages, want the long section split: %#v", len(passages), passages)
	}
	for _, passage := range passages {
		if passage.Anchor != "long" {
			t.Fatalf("piece lost the anchor: %#v", passage)
		}
		if runes := utf8.RuneCountInString(passage.Text); runes > ChunkMaxChars+ChunkMinChars {
			t.Fatalf("piece has %d runes, want at most %d", runes, ChunkMaxChars+ChunkMinChars)
		}
	}
}

func TestSplitPage_ShortTailIsFoldedIntoThePreviousPiece(t *testing.T) {
	// A body that leaves a handful of characters after the first cut.
	body := "## Long\n\n" + strings.Repeat("x", ChunkMaxChars) + "\n\ntail"

	passages := SplitPage(body)

	if len(passages) != 1 {
		t.Fatalf("SplitPage returned %d passages, want the short tail folded: %#v", len(passages), passages)
	}
	if !strings.HasSuffix(passages[0].Text, "tail") {
		t.Fatalf("folded text = %q", passages[0].Text)
	}
}

func TestSplitPage_FencedCodeDoesNotStartASection(t *testing.T) {
	body := "## Real\n\n```sh\n## not a heading\n```\n\nafter\n"

	passages := SplitPage(body)

	if len(passages) != 1 {
		t.Fatalf("SplitPage returned %d passages, want 1: %#v", len(passages), passages)
	}
	if !strings.Contains(passages[0].Text, "## not a heading") {
		t.Fatalf("the fenced line was dropped: %q", passages[0].Text)
	}
}

func TestSplitPage_CJKSplitsOnRuneBoundaries(t *testing.T) {
	body := "## 中文\n\n" + strings.Repeat("这是一段中文文本。", 100)

	passages := SplitPage(body)

	if len(passages) < 2 {
		t.Fatalf("SplitPage returned %d passages, want the section split", len(passages))
	}
	for _, passage := range passages {
		if !utf8.ValidString(passage.Text) {
			t.Fatalf("piece is not valid UTF-8: %q", passage.Text)
		}
		if strings.ContainsRune(passage.Text, utf8.RuneError) {
			t.Fatalf("piece holds a broken rune: %q", passage.Text)
		}
	}
}

func TestSplitPage_EmptyBody(t *testing.T) {
	if passages := SplitPage(""); len(passages) != 0 {
		t.Fatalf("SplitPage(\"\") = %#v, want none", passages)
	}
}
