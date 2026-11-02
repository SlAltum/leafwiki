package markdown

import "testing"

func TestSections(t *testing.T) {
	body := "# 回滚流程\n\nintro text\n\n## **Deploy** steps\n\nfirst\n\n## Deploy steps\n\nsecond\n\n```sh\n## not a heading\n```\n\n## !!!\n\ntail\n"

	sections := Sections(body)

	want := []Section{
		{Line: 1, Level: 1, Title: "回滚流程", Anchor: "回滚流程"},
		{Line: 5, Level: 2, Title: "Deploy steps", Anchor: "deploy-steps"},
		{Line: 9, Level: 2, Title: "Deploy steps", Anchor: "deploy-steps-1"},
		{Line: 17, Level: 2, Title: "!!!", Anchor: ""},
	}
	if len(sections) != len(want) {
		t.Fatalf("Sections returned %d sections, want %d: %#v", len(sections), len(want), sections)
	}
	for i := range want {
		if sections[i] != want[i] {
			t.Fatalf("section %d = %#v, want %#v", i, sections[i], want[i])
		}
	}
}

func TestSections_EmptyBody(t *testing.T) {
	if sections := Sections(""); len(sections) != 0 {
		t.Fatalf("Sections(\"\") = %#v, want none", sections)
	}
}
