package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// testVocab is a miniature BERT vocabulary: enough word pieces to exercise
// continuation matching, an unknown token and the special tokens.
const testVocab = `{
  "normalizer": {"type": "BertNormalizer", "clean_text": true, "handle_chinese_chars": true, "strip_accents": null, "lowercase": true},
  "pre_tokenizer": {"type": "BertPreTokenizer"},
  "model": {
    "type": "WordPiece",
    "unk_token": "[UNK]",
    "continuing_subword_prefix": "##",
    "max_input_chars_per_word": 100,
    "vocab": {
      "[PAD]": 0, "[UNK]": 100, "[CLS]": 101, "[SEP]": 102,
      "hello": 1, "world": 2, "##s": 3, "un": 4, "##aff": 5, "##able": 6,
      "cafe": 7, "中": 8, "文": 9, ",": 10, "the": 11
    }
  }
}`

func loadTestTokenizer(t *testing.T) *WordPieceTokenizer {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tokenizer.json")
	if err := os.WriteFile(path, []byte(testVocab), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tokenizer, err := LoadWordPieceTokenizer(path)
	if err != nil {
		t.Fatalf("LoadWordPieceTokenizer: %v", err)
	}
	return tokenizer
}

func TestWordPieceTokenizer_Encode(t *testing.T) {
	tokenizer := loadTestTokenizer(t)

	cases := []struct {
		name string
		text string
		want []int32
	}{
		{"simple words", "hello world", []int32{101, 1, 2, 102}},
		{"continuation pieces", "worlds", []int32{101, 2, 3, 102}},
		{"several continuations", "unaffable", []int32{101, 4, 5, 6, 102}},
		{"case folded", "HELLO", []int32{101, 1, 102}},
		{"punctuation split", "hello,world", []int32{101, 1, 10, 2, 102}},
		{"unknown word", "zzzz", []int32{101, 100, 102}},
		{"chinese per character", "中文", []int32{101, 8, 9, 102}},
		{"empty text", "", []int32{101, 102}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoding := tokenizer.Encode(tc.text, 0)
			if len(encoding.IDs) != len(tc.want) {
				t.Fatalf("Encode(%q) = %v, want %v", tc.text, encoding.IDs, tc.want)
			}
			for i := range tc.want {
				if encoding.IDs[i] != tc.want[i] {
					t.Fatalf("Encode(%q) = %v, want %v", tc.text, encoding.IDs, tc.want)
				}
			}
			for i, mask := range encoding.AttentionMask {
				if mask != 1 {
					t.Fatalf("attention mask[%d] = %d, want 1", i, mask)
				}
			}
		})
	}
}

func TestWordPieceTokenizer_Truncates(t *testing.T) {
	tokenizer := loadTestTokenizer(t)

	encoding := tokenizer.Encode("hello world hello world", 4)
	if len(encoding.IDs) != 4 {
		t.Fatalf("Encode with maxTokens = %v, want 4 ids", encoding.IDs)
	}
	if encoding.IDs[0] != 101 || encoding.IDs[3] != 102 {
		t.Fatalf("Encode with maxTokens = %v, want [CLS] first and [SEP] last", encoding.IDs)
	}
}

func TestWordPieceTokenizer_StripsAccents(t *testing.T) {
	tokenizer := loadTestTokenizer(t)

	// strip_accents defaults to the lowercase setting, as in HuggingFace.
	if encoding := tokenizer.Encode("café", 0); len(encoding.IDs) != 3 || encoding.IDs[1] != 7 {
		t.Fatalf("Encode(café) = %v, want the accent folded onto cafe", encoding.IDs)
	}
}

func TestLoadWordPieceTokenizer_RejectsOtherShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not wordpiece", `{"model": {"type": "BPE", "vocab": {"a": 1}}}`},
		{"unsupported pre-tokenizer", `{"pre_tokenizer": {"type": "Whitespace"}, "model": {"type": "WordPiece", "unk_token": "[UNK]", "vocab": {"[UNK]": 1, "[CLS]": 2, "[SEP]": 3}}}`},
		{"missing unknown token", `{"model": {"type": "WordPiece", "unk_token": "[UNK]", "vocab": {"[CLS]": 2, "[SEP]": 3}}}`},
		{"empty vocabulary", `{"model": {"type": "WordPiece", "unk_token": "[UNK]", "vocab": {}}}`},
		{"malformed json", `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokenizer.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := LoadWordPieceTokenizer(path); err == nil {
				t.Fatal("LoadWordPieceTokenizer accepted an unusable tokenizer")
			}
		})
	}
}
