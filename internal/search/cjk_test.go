package search

import (
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/test_utils"
)

func TestFoldWidthRune(t *testing.T) {
	cases := []struct {
		name string
		in   rune
		want rune
	}{
		{"fullwidth letter", 'Ａ', 'A'},
		{"fullwidth digit", '１', '1'},
		{"fullwidth punctuation", '！', '!'},
		{"halfwidth katakana", 'ｶ', 'カ'},
		{"halfwidth katakana small", 'ｯ', 'ッ'},
		{"ascii untouched", 'a', 'a'},
		{"cjk untouched", '中', '中'},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foldWidthRune(tc.in); got != tc.want {
				t.Errorf("foldWidthRune(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCJKTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "bigrams cover every adjacent pair",
			in:   "中文开发指南",
			want: []string{"中文", "文开", "开发", "发指", "指南"},
		},
		{
			name: "single character stays searchable",
			in:   "枪",
			want: []string{"枪"},
		},
		{
			name: "latin and cjk mixed",
			in:   "9mm 手枪",
			want: []string{"9mm", "手枪"},
		},
		{
			name: "width folded before tokenizing",
			in:   "ＡＢＣ 全形",
			want: []string{"ABC", "全形"},
		},
		{
			name: "halfwidth katakana folded",
			in:   "ｶﾀｶﾅ",
			want: []string{"カタ", "タカ", "カナ"},
		},
		{
			name: "latin words keep their separators",
			in:   "Hello 世界 world",
			want: []string{"Hello", "世界", "world"},
		},
		{
			name: "separator-only segment dropped",
			in:   "中文，开发",
			want: []string{"中文", "开发"},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cjkTokenize(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("cjkTokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("cjkTokenize(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestCJKTokenizeUnigrams(t *testing.T) {
	// FR-103: with unigrams on, a lone character of a longer run is indexed;
	// with them off, only the bigrams are.
	got := cjkTokenizeWithUnigrams("9mm 手枪", true)
	want := []string{"9mm", "手", "枪", "手枪"}
	if len(got) != len(want) {
		t.Fatalf("cjkTokenizeWithUnigrams(%q, true) = %v, want %v", "9mm 手枪", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("cjkTokenizeWithUnigrams(%q, true) = %v, want %v", "9mm 手枪", got, want)
		}
	}

	off := cjkTokenizeWithUnigrams("9mm 手枪", false)
	if len(off) != 2 || off[1] != "手枪" {
		t.Fatalf("cjkTokenizeWithUnigrams(%q, false) = %v, want [9mm 手枪]", "9mm 手枪", off)
	}
}

func TestSQLiteIndex_Search_ChineseSubstringMatches(t *testing.T) {
	index, err := NewSQLiteIndex(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create SQLiteIndex: %v", err)
	}
	defer test_utils.WrapCloseWithErrorCheck(index.Close, t)

	inputs := []IndexPageInput{
		{Path: "docs/guide.md", FilePath: "docs/guide.md", PageID: "guide", Title: "中文开发指南", Kind: tree.NodeKindPage, Raw: "这是一段中文文本，介绍部署流程。"},
		{Path: "docs/other.md", FilePath: "docs/other.md", PageID: "other", Title: "Other", Kind: tree.NodeKindPage, Raw: "Nothing relevant here."},
	}
	if _, err := index.IndexPages(inputs); err != nil {
		t.Fatalf("indexing failed: %v", err)
	}

	for _, query := range []string{"指南", "开发", "中文文本", "部署"} {
		t.Run(query, func(t *testing.T) {
			result, err := index.Search(query, nil, 0, 10)
			if err != nil {
				t.Fatalf("search %q failed: %v", query, err)
			}
			if len(result.Items) != 1 || result.Items[0].PageID != "guide" {
				t.Fatalf("search %q returned %v, want the guide page", query, result.Items)
			}
		})
	}

	// A single character degrades to a prefix match: it finds a word that
	// starts with it, not one that merely contains it (FR-104).
	single, err := index.Search("中", nil, 0, 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(single.Items) != 1 || single.Items[0].PageID != "guide" {
		t.Fatalf("expected the guide page for a single-character prefix, got %v", single.Items)
	}

	// A word that is not in the text must not match, even though the page is
	// full of Chinese.
	result, err := index.Search("数据库", nil, 0, 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("expected no match for an absent word, got %v", result.Items)
	}
}
