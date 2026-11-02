package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// WordPieceTokenizer runs the tokenizer a BERT-family model expects: the
// tokenizer.json that ships next to the model, read here rather than through a
// third-party pipeline, so a page's tokens are exactly what the model was
// trained on. Only the BERT shape is accepted (BertNormalizer, BertPreTokenizer,
// WordPiece, [CLS]/[SEP]); anything else is refused instead of quietly
// producing the wrong tokens.
type WordPieceTokenizer struct {
	vocab        map[string]int32
	unkID        int32
	clsID        int32
	sepID        int32
	lowercase    bool
	stripAccents bool
	handleCJK    bool
	cleanText    bool
	continuing   string
	maxWordRunes int
}

// Encoding is one text ready for the model.
type Encoding struct {
	IDs           []int32
	AttentionMask []int32
	TypeIDs       []int32
}

type tokenizerJSON struct {
	Normalizer *struct {
		Type               string `json:"type"`
		CleanText          *bool  `json:"clean_text"`
		HandleChineseChars *bool  `json:"handle_chinese_chars"`
		StripAccents       *bool  `json:"strip_accents"`
		Lowercase          *bool  `json:"lowercase"`
	} `json:"normalizer"`
	PreTokenizer *struct {
		Type string `json:"type"`
	} `json:"pre_tokenizer"`
	Model *struct {
		Type                    string           `json:"type"`
		UnkToken                string           `json:"unk_token"`
		ContinuingSubwordPrefix string           `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int              `json:"max_input_chars_per_word"`
		Vocab                   map[string]int32 `json:"vocab"`
	} `json:"model"`
}

// LoadWordPieceTokenizer reads a HuggingFace tokenizer.json.
func LoadWordPieceTokenizer(path string) (*WordPieceTokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed tokenizerJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embed: reading %s: %w", path, err)
	}
	if parsed.Model == nil || parsed.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("embed: %s is not a WordPiece tokenizer", path)
	}
	if parsed.PreTokenizer != nil && parsed.PreTokenizer.Type != "BertPreTokenizer" {
		return nil, fmt.Errorf("embed: %s uses the unsupported pre-tokenizer %q", path, parsed.PreTokenizer.Type)
	}
	if len(parsed.Model.Vocab) == 0 {
		return nil, fmt.Errorf("embed: %s has an empty vocabulary", path)
	}

	tokenizer := &WordPieceTokenizer{
		vocab:        parsed.Model.Vocab,
		continuing:   parsed.Model.ContinuingSubwordPrefix,
		maxWordRunes: parsed.Model.MaxInputCharsPerWord,
		handleCJK:    true,
		cleanText:    true,
	}
	if tokenizer.continuing == "" {
		tokenizer.continuing = "##"
	}
	if tokenizer.maxWordRunes <= 0 {
		tokenizer.maxWordRunes = 100
	}

	if parsed.Normalizer != nil {
		switch parsed.Normalizer.Type {
		case "BertNormalizer", "":
			if parsed.Normalizer.Lowercase != nil {
				tokenizer.lowercase = *parsed.Normalizer.Lowercase
			}
			if parsed.Normalizer.StripAccents != nil {
				tokenizer.stripAccents = *parsed.Normalizer.StripAccents
			} else {
				// HuggingFace reads a null strip_accents as "follow lowercase".
				tokenizer.stripAccents = tokenizer.lowercase
			}
			if parsed.Normalizer.HandleChineseChars != nil {
				tokenizer.handleCJK = *parsed.Normalizer.HandleChineseChars
			}
			if parsed.Normalizer.CleanText != nil {
				tokenizer.cleanText = *parsed.Normalizer.CleanText
			}
		default:
			return nil, fmt.Errorf("embed: %s uses the unsupported normalizer %q", path, parsed.Normalizer.Type)
		}
	}

	for _, special := range []struct {
		token string
		field *int32
	}{
		{parsed.Model.UnkToken, &tokenizer.unkID},
		{"[CLS]", &tokenizer.clsID},
		{"[SEP]", &tokenizer.sepID},
	} {
		id, ok := tokenizer.vocab[special.token]
		if !ok {
			return nil, fmt.Errorf("embed: %s has no %q token", path, special.token)
		}
		*special.field = id
	}

	return tokenizer, nil
}

// Encode tokenizes text into model input. maxTokens caps the sequence
// including the two special tokens; 0 means no cap.
func (t *WordPieceTokenizer) Encode(text string, maxTokens int) Encoding {
	words := t.basicTokenize(text)

	ids := make([]int32, 0, len(words)+2)
	ids = append(ids, t.clsID)
	for _, word := range words {
		for _, id := range t.wordPiece(word) {
			ids = append(ids, id)
		}
	}
	ids = append(ids, t.sepID)

	if maxTokens > 0 && len(ids) > maxTokens {
		ids = ids[:maxTokens]
		// A truncated sequence must still end the way the model expects.
		ids[maxTokens-1] = t.sepID
	}

	encoding := Encoding{
		IDs:           ids,
		AttentionMask: make([]int32, len(ids)),
		TypeIDs:       make([]int32, len(ids)),
	}
	for i := range encoding.AttentionMask {
		encoding.AttentionMask[i] = 1
	}
	return encoding
}

// basicTokenize splits text the way BertPreTokenizer does: clean, split CJK,
// lowercase, strip accents, then split on whitespace and punctuation.
func (t *WordPieceTokenizer) basicTokenize(text string) []string {
	if t.cleanText {
		text = cleanText(text)
	}
	if t.handleCJK {
		text = spaceOutCJK(text)
	}

	var words []string
	for _, chunk := range strings.Fields(text) {
		chunk = strings.ToLower(chunk)
		if t.stripAccents {
			chunk = stripAccents(chunk)
		}
		words = append(words, splitPunctuation(chunk)...)
	}
	return words
}

// wordPiece applies greedy longest-match-first to one word.
func (t *WordPieceTokenizer) wordPiece(word string) []int32 {
	runes := []rune(word)
	if len(runes) > t.maxWordRunes {
		return []int32{t.unkID}
	}

	var ids []int32
	start := 0
	for start < len(runes) {
		end := len(runes)
		var id int32
		found := false
		for end > start {
			piece := string(runes[start:end])
			if start > 0 {
				piece = t.continuing + piece
			}
			if candidate, ok := t.vocab[piece]; ok {
				id = candidate
				found = true
				break
			}
			end--
		}
		if !found {
			return []int32{t.unkID}
		}
		ids = append(ids, id)
		start = end
	}
	return ids
}

// cleanText drops control characters and turns whitespace into plain spaces.
func cleanText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == 0 || r == 0xFFFD:
			continue
		case unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r':
			continue
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// spaceOutCJK puts a space around every CJK ideograph, which is what makes a
// Chinese sentence tokenize character by character instead of as one blob.
func spaceOutCJK(text string) string {
	var b strings.Builder
	b.Grow(len(text) + len(text)/4)
	for _, r := range text {
		if isCJKIdeograph(r) {
			b.WriteRune(' ')
			b.WriteRune(r)
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isCJKIdeograph reports whether r is a Han character, the block BERT splits.
func isCJKIdeograph(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

// stripAccents removes combining marks after a compatibility decomposition.
func stripAccents(text string) string {
	decomposed := norm.NFD.String(text)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// splitPunctuation splits a chunk on punctuation, keeping each mark as its own
// piece, the way BertPreTokenizer treats "hello,world".
func splitPunctuation(text string) []string {
	var pieces []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			pieces = append(pieces, current.String())
			current.Reset()
		}
	}
	for _, r := range text {
		if isPunctuation(r) {
			flush()
			pieces = append(pieces, string(r))
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return pieces
}

// isPunctuation matches BERT's definition: the ASCII punctuation ranges plus
// every Unicode punctuation category.
func isPunctuation(r rune) bool {
	switch {
	case r >= 33 && r <= 47, r >= 58 && r <= 64, r >= 91 && r <= 96, r >= 123 && r <= 126:
		return true
	default:
		return unicode.IsPunct(r)
	}
}
