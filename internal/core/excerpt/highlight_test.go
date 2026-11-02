package excerpt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHighlightTerms(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		terms []string
		want  string
	}{
		{"single occurrence", "a searchterm here", []string{"searchterm"}, "a <b>searchterm</b> here"},
		{"every occurrence", "go go go", []string{"go"}, "<b>go</b> <b>go</b> <b>go</b>"},
		{"case insensitive", "Search SEARCH search", []string{"search"}, "<b>Search</b> <b>SEARCH</b> <b>search</b>"},
		{"no match", "nothing to see", []string{"absent"}, "nothing to see"},
		{"cjk", "介绍部署流程", []string{"部署"}, "介绍<b>部署</b>流程"},
		{"overlapping terms keep one span", "中文文本", []string{"中文文本", "中文"}, "<b>中文文本</b>"},
		{"empty terms", "unchanged", nil, "unchanged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HighlightTerms(tc.text, tc.terms, "<b>", "</b>"); got != tc.want {
				t.Fatalf("HighlightTerms(%q, %v) = %q, want %q", tc.text, tc.terms, got, tc.want)
			}
		})
	}
}

func TestHighlightedExcerpt(t *testing.T) {
	body := "开头文字。" + strings.Repeat("填充内容。", 60) + "说明部署流程的最后一步。"

	got := HighlightedExcerpt(body, []string{"部署"})
	if !strings.Contains(got, "<b>部署</b>") {
		t.Fatalf("expected the match to be highlighted, got %q", got)
	}
	if runes := utf8.RuneCountInString(strings.Trim(got, ".")); runes > MaxRunes+len("<b></b>") {
		t.Fatalf("excerpt has %d runes, want at most %d", runes, MaxRunes)
	}
	if !strings.HasPrefix(got, "...") {
		t.Fatalf("expected the window to start mid-body, got %q", got)
	}

	// No term: the opening window, like FromBody.
	plain := HighlightedExcerpt("Short body text.", []string{"absent"})
	if plain != "Short body text." {
		t.Fatalf("fallback excerpt = %q", plain)
	}

	if HighlightedExcerpt("", []string{"部署"}) != "" {
		t.Fatal("expected an empty excerpt for empty body")
	}

	// The markdown is stripped before the excerpt is built.
	if md := HighlightedExcerpt("## 标题\n\n**部署**流程说明。", []string{"部署"}); !strings.Contains(md, "<b>部署</b>") || strings.Contains(md, "**") || strings.Contains(md, "##") {
		t.Fatalf("expected plain text with highlight, got %q", md)
	}
}

func TestFirstTermRuneIndex(t *testing.T) {
	cases := []struct {
		text  string
		terms []string
		want  int
	}{
		{"abc abc", []string{"abc"}, 0},
		{"中文abc", []string{"abc"}, 2},
		{"中文介绍部署", []string{"部署", "中文"}, 0},
		{"absent", []string{"zzz"}, -1},
	}
	for _, tc := range cases {
		if got := firstTermRuneIndex(tc.text, tc.terms); got != tc.want {
			t.Errorf("firstTermRuneIndex(%q, %v) = %d, want %d", tc.text, tc.terms, got, tc.want)
		}
	}
}
