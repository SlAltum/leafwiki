package markdown

import (
	"strings"
	"testing"
)

func TestParseFrontmatter_Aliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"block list", "---\nleafwiki_aliases:\n  - 移动电话\n  - mobile plan\n---\n\nbody", []string{"移动电话", "mobile plan"}},
		{"flow list", "---\nleafwiki_aliases: [移动电话, mobile plan]\n---\n\nbody", []string{"移动电话", "mobile plan"}},
		{"scalar", "---\nleafwiki_aliases: 移动电话\n---\n\nbody", []string{"移动电话"}},
		{"absent", "---\nleafwiki_id: p1\n---\n\nbody", nil},
		{"empty list", "---\nleafwiki_aliases: []\n---\n\nbody", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, _, has, err := ParseFrontmatter(tc.raw)
			if err != nil || !has {
				t.Fatalf("ParseFrontmatter has=%v err=%v", has, err)
			}
			if len(fm.LeafWikiAliases) != len(tc.want) {
				t.Fatalf("aliases = %v, want %v", fm.LeafWikiAliases, tc.want)
			}
			for i := range tc.want {
				if fm.LeafWikiAliases[i] != tc.want[i] {
					t.Fatalf("aliases = %v, want %v", fm.LeafWikiAliases, tc.want)
				}
			}
		})
	}
}

func TestBuildMarkdownWithFrontmatter_KeepsAliases(t *testing.T) {
	raw := "---\nleafwiki_id: p1\nleafwiki_aliases: [移动电话]\n---\n\nbody"
	fm, body, _, err := ParseFrontmatter(raw)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	rebuilt, err := BuildMarkdownWithFrontmatter(fm, body)
	if err != nil {
		t.Fatalf("BuildMarkdownWithFrontmatter: %v", err)
	}
	if !strings.Contains(rebuilt, "leafwiki_aliases") {
		t.Fatalf("rewrite dropped the aliases key: %q", rebuilt)
	}

	again, _, _, err := ParseFrontmatter(rebuilt)
	if err != nil {
		t.Fatalf("re-parsing the rewrite: %v", err)
	}
	if len(again.LeafWikiAliases) != 1 || again.LeafWikiAliases[0] != "移动电话" {
		t.Fatalf("aliases after rewrite = %v", again.LeafWikiAliases)
	}
}
