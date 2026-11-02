package search

import (
	"database/sql"
	"regexp"
	"strings"
)

// Spell correction constants, following Lucene's DirectSpellChecker
// (docs/srs/zh-search-and-retrieval.srs.md §6.5 B-3).
const (
	// SpellMaxEdits is the deepest edit distance still counted as a typo: a
	// transposition costs two edits, so one round at 1 and a second at 2
	// catches "recive" and "recieve" without opening the door to words that
	// merely look similar.
	SpellMaxEdits = 2
	// SpellPrefixLock is how many leading characters a candidate shares with
	// the query term, which keeps the candidate list small and the
	// suggestions recognisable.
	SpellPrefixLock = 1
	// MinTermFrequency is the number of documents a candidate must appear in
	// before it is offered.
	MinTermFrequency = 3
	// spellCandidateLimit caps how many vocabulary terms one suggestion looks
	// at, ordered by document frequency.
	spellCandidateLimit = 500
)

// CorrectTerms maps each ASCII term of one query that the corpus does not
// contain to the closest indexed term, in a single pass over the vocabulary.
// A term that is in the vocabulary is left out, however rare: it is a word of
// this wiki, not a typo (FR-302). CJK terms are never corrected: an
// edit-distance neighbour of a bigram is a different word (FR-305).
func (s *SQLiteIndex) CorrectTerms(terms []string) (map[string]string, error) {
	inputs := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || !isASCII(term) {
			continue
		}
		if _, dup := seen[term]; dup {
			continue
		}
		seen[term] = struct{}{}
		inputs = append(inputs, term)
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	var candidates []spellCandidate
	known := make(map[string]bool, len(inputs))
	err := s.withDBRead(func(db *sql.DB) error {
		var err error
		candidates, known, err = readVocabulary(db, inputs)
		return err
	})
	if err != nil {
		return nil, err
	}

	corrections := make(map[string]string)
	for _, term := range inputs {
		if known[term] {
			continue
		}
		for edits := 1; edits <= SpellMaxEdits; edits++ {
			if corrected, ok := closestTerm(term, candidates, edits); ok {
				corrections[term] = corrected
				break
			}
		}
	}
	return corrections, nil
}

// applyCorrections rewrites query with the corrections applied word by word,
// so the reader is told which words the results are actually for.
func applyCorrections(query string, corrections map[string]string) string {
	corrected := query
	for term, replacement := range corrections {
		corrected = replaceTerm(corrected, term, replacement)
	}
	if corrected == query {
		return ""
	}
	return corrected
}

// replaceTerm replaces every whole-word occurrence of term, keeping the
// surrounding text (spacing, operators) as the user typed it.
func replaceTerm(text, term, replacement string) string {
	pattern := "(?i)\\b" + regexp.QuoteMeta(term) + "\\b"
	return regexp.MustCompile(pattern).ReplaceAllString(text, replacement)
}

// SuggestTerm returns the corrected spelling for one term, or ok=false when
// the term is in the vocabulary or has no close neighbour.
func (s *SQLiteIndex) SuggestTerm(term string) (string, bool, error) {
	corrections, err := s.CorrectTerms([]string{term})
	if err != nil {
		return "", false, err
	}
	corrected, ok := corrections[strings.ToLower(strings.TrimSpace(term))]
	return corrected, ok, nil
}

// readVocabulary reads the terms one query needs in one pass: the candidates
// that share a prefix with an input term and occur often enough, plus any
// input term that is itself in the vocabulary however rare it is.
//
// ponytail: the scan is capped at spellCandidateLimit terms, ordered by
// document frequency, so a very large vocabulary could hide the right word
// behind more frequent ones. Raise the cap or index a term trie if
// suggestions start missing on real corpora.
func readVocabulary(db *sql.DB, inputs []string) ([]spellCandidate, map[string]bool, error) {
	prefixes := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, term := range inputs {
		prefix := prefixOf(term)
		if _, dup := seen[prefix]; dup {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}

	likeParts := make([]string, len(prefixes))
	args := make([]any, 0, len(prefixes)+len(inputs)+2)
	for i, prefix := range prefixes {
		likeParts[i] = "term LIKE ? ESCAPE '\\'"
		args = append(args, escapeLike(prefix)+"%")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(inputs)), ",")
	args = append(args, MinTermFrequency)
	for _, term := range inputs {
		args = append(args, term)
	}
	args = append(args, spellCandidateLimit)

	query := `SELECT term, doc FROM pages_vocab` +
		` WHERE (` + strings.Join(likeParts, ` OR `) + `)` +
		` AND (doc >= ? OR term IN (` + placeholders + `))` +
		` ORDER BY doc DESC, term ASC LIMIT ?`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	known := make(map[string]bool, len(inputs))
	var candidates []spellCandidate
	for rows.Next() {
		var c spellCandidate
		if err := rows.Scan(&c.term, &c.doc); err != nil {
			return nil, nil, err
		}
		if _, isInput := seenInput(inputs, c.term); isInput {
			known[c.term] = true
			continue
		}
		if !isSpellCandidate(c.term) {
			continue
		}
		candidates = append(candidates, c)
	}
	return candidates, known, rows.Err()
}

// isSpellCandidate reports whether a vocabulary term is a word worth
// suggesting. The tokenizer treats "-_/+#." as word characters, so a
// sentence-final "token." is one term; suggesting it for "token" is not a
// spelling correction.
func isSpellCandidate(term string) bool {
	return strings.TrimRight(term, searchTokenChars) == term
}

// seenInput reports whether term is one of the query's own terms.
func seenInput(inputs []string, term string) (int, bool) {
	for i, input := range inputs {
		if input == term {
			return i, true
		}
	}
	return 0, false
}

// prefixOf returns the leading runes a candidate must share with term.
func prefixOf(term string) string {
	runes := []rune(term)
	if len(runes) <= SpellPrefixLock {
		return term
	}
	return string(runes[:SpellPrefixLock])
}

type spellCandidate struct {
	term string
	doc  int
}

// closestTerm returns the best candidate at the given edit distance or closer,
// preferring the closest and then the most frequent one. Candidates that do
// not share the prefix lock are ignored.
func closestTerm(term string, candidates []spellCandidate, maxEdits int) (string, bool) {
	prefix := prefixOf(term)
	best := ""
	bestDoc := 0
	found := false
	for _, c := range candidates {
		if !strings.HasPrefix(c.term, prefix) {
			continue
		}
		if distance := editDistance(term, c.term, maxEdits); distance > maxEdits {
			continue
		}
		if !found || c.doc > bestDoc || (c.doc == bestDoc && c.term < best) {
			best, bestDoc, found = c.term, c.doc, true
		}
	}
	return best, found
}

// editDistance returns the Levenshtein distance between a and b in runes,
// giving up as soon as it is known to exceed limit.
func editDistance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if diff := len(ra) - len(rb); diff > limit || -diff > limit {
		return limit + 1
	}

	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < rowMin {
				rowMin = cur[j]
			}
		}
		if rowMin > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// isASCII reports whether s contains only ASCII bytes, the only terms the
// spell corrector touches.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// escapeLike makes s literal inside a LIKE pattern.
func escapeLike(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(s)
}
