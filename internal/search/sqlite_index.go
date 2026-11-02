package search

import (
	"bytes"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/perber/wiki/internal/core/excerpt"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/core/shared/sqliteutil"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	_ "modernc.org/sqlite"
)

type SQLiteIndex struct {
	mu         sync.RWMutex
	storageDir string
	filename   string
	db         *sql.DB
}

func searchIndexDatabasePath(storageDir string, filename string) string {
	normalizedStorageDir := filepath.FromSlash(strings.ReplaceAll(storageDir, `\`, `/`))
	return filepath.Join(normalizedStorageDir, filename)
}

var headingParser = goldmark.New()

func extractHeadings(src string) string {
	srcBytes := []byte(src)
	reader := text.NewReader(srcBytes)
	doc := headingParser.Parser().Parse(reader)

	var buf bytes.Buffer

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, ok := n.(*ast.Heading); !ok {
			return ast.WalkContinue, nil
		}

		var headingText bytes.Buffer
		_ = ast.Walk(n, func(child ast.Node, childEntering bool) (ast.WalkStatus, error) {
			if !childEntering || child == n {
				return ast.WalkContinue, nil
			}
			if t, ok := child.(*ast.Text); ok {
				headingText.Write(t.Segment.Value(srcBytes))
				headingText.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		})

		headingStr := strings.TrimSpace(headingText.String())
		if headingStr != "" {
			buf.WriteString(headingStr)
			buf.WriteByte('\n')
		}

		return ast.WalkSkipChildren, nil
	})

	return excerpt.PlainTextFromMarkdown(buf.String())
}

// buildFuzzyQuery turns raw user search input into an FTS5 MATCH argument.
//
// It classifies each whitespace-separated field independently rather than
// making one all-or-nothing trust decision for the whole query string. The
// earlier whole-query approach (github.com/perber/leafwiki/issues/1577)
// trusted the *entire* query as deliberate FTS5 syntax the moment it saw an
// operator character (*, (, ), :, AND, OR) anywhere in it — so a query like
// `foo* bar,baz` or `it's* thing` still reached SQLite with an unescaped
// comma or apostrophe in the other field and failed with an FTS5 syntax
// error. Classifying field-by-field means one field's operator syntax can
// no longer vouch for a different field's stray punctuation.
// termSuggester returns a corrected spelling for a term the corpus does not
// contain. Query construction stays a pure function and takes the corrections
// as a lookup; nil means "match the query as typed".
type termSuggester func(term string) (string, bool)

func buildFuzzyQuery(q string, suggest termSuggester) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return q
	}

	// A NEAR(...) proximity call is the one FTS5 construct whose syntax
	// genuinely spans multiple whitespace-separated fields tied together
	// by punctuation (NEAR(term1 term2, distance)) — per-field
	// classification below would treat "term2," and "distance)" as two
	// unrelated fields and strip the comma the call's own grammar requires,
	// silently breaking it instead of erroring. Trusting the whole call
	// verbatim when it's shaped like one preserves this narrow,
	// already-supported case exactly as it worked before this function
	// existed.
	if isNearFunctionCall(q) {
		return q
	}

	fields := splitFTS5AwareFields(q)

	parts := make([]string, 0, len(fields))
	literal := make([]bool, 0, len(fields))
	for i := range fields {
		fragment, isLiteral := sanitizeFTS5Field(fields, i, suggest)
		if fragment == "" {
			continue
		}
		parts = append(parts, fragment)
		literal = append(literal, isLiteral)
	}

	// Two literal fields are joined with an explicit AND: a corrected term is
	// a parenthesised group, and FTS5 rejects a group that merely follows a
	// phrase. Operator syntax keeps its plain spacing.
	var b strings.Builder
	for i, fragment := range parts {
		if i > 0 {
			if literal[i] && literal[i-1] {
				b.WriteString(" AND ")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString(fragment)
	}
	return b.String()
}

// isNearFunctionCall reports whether q, trimmed, is shaped like a complete
// FTS5 NEAR(...) proximity call ("NEAR" then "(", ending in ")") — not a
// syntax guarantee, just enough to single out this specific construct from
// ordinary text before per-field classification would otherwise take it
// apart.
func isNearFunctionCall(q string) bool {
	upper := strings.ToUpper(q)
	if !strings.HasPrefix(upper, "NEAR") {
		return false
	}
	rest := strings.TrimSpace(upper[len("NEAR"):])
	return strings.HasPrefix(rest, "(") && strings.HasSuffix(q, ")")
}

// splitFTS5AwareFields splits q on whitespace like strings.Fields, except
// whitespace inside a "double-quoted span" doesn't split the field, so a
// quoted phrase (balanced or not) reaches sanitizeFTS5Field as one field to
// classify as a whole instead of being cut apart mid-phrase.
func splitFTS5AwareFields(q string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for _, r := range q {
		switch {
		case r == '"':
			cur.WriteRune(r)
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
}

// sanitizeFTS5Field decides whether fields[i] is trustworthy, deliberate
// FTS5 syntax (an operator, a boolean keyword used between two real
// operands, a column filter naming a real column, an already-quoted
// phrase) or literal search text that needs quoting so it can't be misread
// as syntax.
//
// A comma or apostrophe — never valid FTS5 syntax on their own — and an
// unbalanced double quote always disqualify a field from the "trust it"
// path, even if the same field also contains an operator character like *
// or : (that combination is exactly what #1577's follow-up reports found:
// "sometimes # and \"" alongside other punctuation).
func sanitizeFTS5Field(fields []string, i int, suggest termSuggester) (fragment string, literal bool) {
	f := fields[i]
	if f == "" {
		return f, false
	}

	if isBalancedQuotedField(f) {
		return f, false
	}

	// A column filter is always rewritten (never trusted verbatim) into a
	// safe, per-word-quoted form scoped to that column — see
	// rewriteColumnFilterField for why: the raw value can contain anything
	// (a pasted URL's second ":", an apostrophe, ...) without needing its
	// own escaping rules, because it's built from the same safe
	// splitIntoIndexWords primitive as the plain-literal-text path below.
	if rewritten, ok := rewriteColumnFilterField(f); ok {
		return rewritten, false
	}

	// An operator field ("foo*", "(foo", "bar)") is only trusted when it is
	// well-formed FTS5 syntax on its own and the query's parentheses balance
	// overall. Anything else ("foo-bar*", "report.v2*", "(foo" without a
	// partner, a bare "*") goes through the literal path below.
	if strings.ContainsAny(f, "*()") {
		if isWellFormedOperatorField(f) && (!strings.ContainsAny(f, "()") || operatorParensBalanced(fields)) {
			return f, false
		}
	} else if isFTS5Keyword(f) && hasRealOperandNeighbors(fields, i) {
		return f, false
	}

	// Literal text: split it into the same "words" the pages table's
	// tokenizer would (see splitIntoIndexWords) and AND together a
	// quoted-prefix match for each one, rather than quoting the whole field
	// as a single phrase. A single quoted phrase would need FTS5 to treat
	// the words on either side of the dropped punctuation as consecutive
	// token positions, which does not hold for every separator character —
	// SQLite's unicode61 tokenizer does not leave "it" and "s" (from "it's")
	// at adjacent positions the way it does for, say, a comma, so a
	// `"it's"` phrase query fails to match content containing "it's" even
	// though both tokenize the same way. AND-ing independent terms doesn't
	// depend on that adjacency at all.
	// CJK runs become bigrams here exactly as cjkAnalyze wrote them into the
	// index, so a query and the text it should find agree on their tokens
	// (FR-104, FR-108).
	return quoteFieldTerms(QueryTerms(f), suggest), true
}

// isBarewordRune reports whether r may appear in an unquoted FTS5 bareword.
// The tokenizer's searchTokenChars (- . / # +) are deliberately not
// included: they are word characters for indexing but not valid query
// syntax.
func isBarewordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isWellFormedOperatorField reports whether f consists only of barewords,
// prefix markers and parentheses in a shape FTS5 accepts: "(" only at the
// start or after another "(", "*" only directly after a word, ")" only after
// a word, "*" or ")", and nothing following ")" except another ")".
func isWellFormedOperatorField(f string) bool {
	var prev rune // 0 = start of field
	for _, r := range f {
		switch {
		case r == '*':
			if !isBarewordRune(prev) {
				return false
			}
		case r == '(':
			if prev != 0 && prev != '(' {
				return false
			}
		case r == ')':
			if !isBarewordRune(prev) && prev != '*' && prev != ')' {
				return false
			}
		case isBarewordRune(r):
			if prev == '*' || prev == ')' {
				return false
			}
		default:
			return false
		}
		prev = r
	}
	return true
}

// operatorParensBalanced reports whether the parentheses in the query's
// well-formed operator fields pair up, so a group may span several fields
// ("(apple OR cherry) AND banana") but a lone "(foo" is not trusted.
func operatorParensBalanced(fields []string) bool {
	depth := 0
	for _, f := range fields {
		if !isWellFormedOperatorField(f) {
			continue
		}
		for _, r := range f {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
				if depth < 0 {
					return false
				}
			}
		}
	}
	return depth == 0
}

// quoteFieldTerms builds the MATCH fragment for one literal field: every term
// quoted as a prefix match, joined by FTS5's implicit AND. A term the corpus
// does not contain is matched by both its typed spelling and its correction,
// so a page carrying either is found (FR-303). FTS5 has no per-term weight, so
// the typed spelling is one of two alternatives rather than boosted above the
// correction; a page carrying both still scores higher.
func quoteFieldTerms(terms []string, suggest termSuggester) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted := strconv.Quote(term) + "*"
		if suggest != nil {
			if corrected, ok := suggest(term); ok && corrected != term {
				parts = append(parts, "("+quoted+" OR "+strconv.Quote(corrected)+"*"+")")
				continue
			}
		}
		parts = append(parts, quoted)
	}
	// An explicit AND: FTS5 accepts a parenthesised group after a phrase only
	// when an operator joins them, and a correction turns one term into one.
	return strings.Join(parts, " AND ")
}

// quoteWordsAsPrefixTerms wraps each word as its own quoted-phrase-prefix
// match ("word"*) and joins them with a space (FTS5's implicit AND). Since
// each word came from splitIntoIndexWords, none of them can contain a
// quote, comma, or apostrophe, so no escaping is needed here.
func quoteWordsAsPrefixTerms(words []string) string {
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return strings.Join(words, " ")
}

// hasRealOperandNeighbors reports whether fields[i] sits strictly between
// two other fields that aren't themselves boolean keywords. FTS5's
// AND/OR/NOT are binary operators: a dangling "AND"/"OR"/"NOT" with no
// operand on one side (or with another keyword standing in for the
// operand, e.g. a doubled "foo AND AND bar") is itself a syntax error, not
// valid-but-unusual syntax.
func hasRealOperandNeighbors(fields []string, i int) bool {
	if i <= 0 || i >= len(fields)-1 {
		return false
	}
	return !isFTS5Keyword(fields[i-1]) && !isFTS5Keyword(fields[i+1])
}

// searchTokenChars is the extra tokenchars given to the pages table's
// unicode61 tokenizer (see ensureSchema) — the punctuation that's part of a
// word rather than a separator (so "report-final.v2" indexes as one token,
// not three). splitIntoIndexWords uses the same alphabet so a literal
// search field is split into exactly the "words" the tokenizer would
// itself produce; both must stay in sync with the schema's tokenize
// argument or search can silently miss content that's plainly there.
const searchTokenChars = "-_/+#."

// splitIntoIndexWords splits a raw field into the same run-of-word-chars
// units the pages table's tokenizer (see searchTokenChars) would treat as
// tokens, dropping any other character (comma, apostrophe, a stray ") as a
// separator instead of embedding it as literal phrase content — since
// split words can never contain a quote, comma, or apostrophe, none of
// them need escaping.
func splitIntoIndexWords(f string) []string {
	var words []string
	var cur strings.Builder
	for _, r := range f {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(searchTokenChars, r) {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// isFTS5Keyword reports whether f is a boolean-operator keyword. Callers
// must also confirm it sits between two real operands (see
// hasRealOperandNeighbors) before trusting it — FTS5's AND/OR/NOT are binary
// operators, so a dangling "AND"/"OR"/"NOT" with no left or right neighbor
// is itself a syntax error, not valid-but-unusual syntax.
func isFTS5Keyword(f string) bool {
	switch strings.ToUpper(f) {
	case "AND", "OR", "NOT":
		return true
	default:
		return false
	}
}

// searchColumnNames lists the pages FTS5 table's columns in declaration
// order. It is the single source for the bm25 weights and for the column
// filters rewriteColumnFilterField accepts.
var searchColumnNames = []string{
	"path",
	"filepath",
	"pageID",
	"kind",
	"title",
	"headings",
	"content",
	"aliases",
}

// searchColumnWeights is the bm25 weight of each column, in
// searchColumnNames order: a hit in the title or a declared alias says more
// about a page's subject than a hit in the body. bm25() pads a short weight
// list with 1.0 and ignores a long one without failing, so the list is
// generated from these and the length is checked below; a column that lost
// its weight would otherwise rank as if it were body text (FR-106).
var searchColumnWeights = []float64{
	0,  // path
	0,  // filepath
	0,  // pageID
	0,  // kind
	20, // title
	5,  // headings
	1,  // content
	10, // aliases
}

// searchColumns are the pages FTS5 table's real column names, lowercased,
// against which a field's "col:" prefix is validated before trusting it as
// an FTS5 column filter (see rewriteColumnFilterField). FTS5 column-name
// matching is case-insensitive, so column names are compared lowercased.
var searchColumns = map[string]bool{}

func init() {
	if err := checkSearchColumnWeights(searchColumnNames, searchColumnWeights); err != nil {
		panic(err)
	}
	for _, name := range searchColumnNames {
		searchColumns[strings.ToLower(name)] = true
	}
}

// checkSearchColumnWeights reports whether every pages column has exactly
// one bm25 weight.
func checkSearchColumnWeights(names []string, weights []float64) error {
	if len(names) != len(weights) {
		return fmt.Errorf("search: %d bm25 weights for %d pages columns", len(weights), len(names))
	}
	return nil
}

// rewriteColumnFilterField checks whether f looks like a deliberate FTS5
// column filter (e.g. "content:search") naming one of the pages table's
// real columns, as opposed to incidental punctuation with a colon in it —
// a pasted URL ("http://example.com"), a Windows path ("c:\Users\foo"), or
// a time ("10:30") all contain ":" but aren't column filters, and trusting
// any colon-bearing field unconditionally (the pre-#1577 behavior) sends
// SQLite a "no such column" error for all three.
//
// When the prefix does name a real column, the *value* after the colon is
// never trusted verbatim either — it's rewritten into a parenthesized,
// per-word quoted-prefix group scoped to that column
// (`col:("word1"* "word2"*)`), built with the same splitIntoIndexWords
// primitive as the plain-literal-text path. That handles a value
// containing anything (another ":" from a pasted URL, an apostrophe, ...)
// safely without a separate "is this value simple enough to trust"
// predicate, and — unlike simply falling back to an unscoped literal
// search — still honors the column scope the user asked for instead of
// silently searching every column.
func rewriteColumnFilterField(f string) (string, bool) {
	idx := strings.Index(f, ":")
	if idx <= 0 {
		return "", false
	}
	col := f[:idx]
	if !searchColumns[strings.ToLower(col)] {
		return "", false
	}
	words := QueryTerms(f[idx+1:])
	if len(words) == 0 {
		return "", false
	}
	return col + ":(" + quoteWordsAsPrefixTerms(words) + ")", true
}

// isBalancedQuotedField reports whether f is a complete, balanced quoted
// FTS5 phrase — e.g. `"foo bar"` or `"foo bar"*` — as opposed to a
// lone/unbalanced " that only looks like the start of one.
func isBalancedQuotedField(f string) bool {
	body := strings.TrimSuffix(f, "*")
	if len(body) < 2 || !strings.HasPrefix(body, `"`) || !strings.HasSuffix(body, `"`) {
		return false
	}
	inner := body[1 : len(body)-1]
	return strings.Count(inner, `"`)%2 == 0
}

func NewSQLiteIndex(storageDir string) (*SQLiteIndex, error) {
	s := &SQLiteIndex{
		storageDir: storageDir,
		filename:   "search.db",
	}

	err := sqliteutil.OpenWithPolicy(searchIndexDatabasePath(s.storageDir, s.filename), sqliteutil.RebuildOnCorruption, func() error {
		if err := s.ensureSchema(); err != nil {
			if closeErr := s.Close(); closeErr != nil {
				slog.Default().Warn("failed to close corrupt search database before recovery", "error", closeErr)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s, nil
}

// connect returns the open *sql.DB, opening it on first use. Safe for
// concurrent callers on both the read and write paths: the common case
// (already open) only takes the cheap RLock; opening the connection is
// the one operation that mutates s.db itself, so it's re-checked under
// the exclusive Lock (standard double-checked locking) rather than
// requiring every caller — including read-only ones — to hold the
// exclusive lock just to be safe against a lazy-init race that almost
// never happens after the first call.
func (s *SQLiteIndex) connect() (*sql.DB, error) {
	s.mu.RLock()
	if s.db != nil {
		db := s.db
		s.mu.RUnlock()
		return db, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		db, err := sql.Open("sqlite", searchIndexDatabasePath(s.storageDir, s.filename)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		if err != nil {
			return nil, err
		}
		s.db = db
	}
	return s.db, nil
}

// withDB runs fn under the exclusive lock — use for anything that writes.
func (s *SQLiteIndex) withDB(fn func(db *sql.DB) error) error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(db)
}

// withDBRead runs fn under a shared read lock — use for read-only
// queries (Search, SearchPageIDs, Ping) so concurrent readers don't
// serialize behind each other the way withDB's writers must. Load-tested:
// this is what fixed Search's reader-self-contention (see
// loadtest/k6/search-only.js results), the same fix already applied to
// internal/links/links_store.go for the identical symptom.
func (s *SQLiteIndex) withDBRead(fn func(db *sql.DB) error) error {
	db, err := s.connect()
	if err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(db)
}

func (s *SQLiteIndex) ensureSchema() error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`
			DROP TABLE IF EXISTS pages_vocab;
			DROP TABLE IF EXISTS pages;
			CREATE VIRTUAL TABLE IF NOT EXISTS pages USING fts5(
				path UNINDEXED,
				filepath UNINDEXED,
				pageID,
				kind UNINDEXED,
				title,
				headings,
				content,
				aliases,
				tokenize = "unicode61 tokenchars '` + searchTokenChars + `'"
			);
			-- The term vocabulary is a view over the index itself, so it needs no
			-- separate rebuild path and cannot drift from the postings (FR-301).
			CREATE VIRTUAL TABLE IF NOT EXISTS pages_vocab USING fts5vocab(pages, 'row');
        `)
		return err
	})
}

func (s *SQLiteIndex) Clear() error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM pages`)
		return err
	})
}

func (s *SQLiteIndex) Ping() error {
	return s.withDBRead(func(db *sql.DB) error {
		return db.Ping()
	})
}

func (s *SQLiteIndex) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}

// IndexPageInput bundles one page's worth of IndexPages input.
type IndexPageInput struct {
	Path     string
	FilePath string
	PageID   string
	Title    string
	Kind     tree.NodeKind
	Raw      string
}

// IndexFailure pairs a failed input with why it failed, returned by
// IndexPages for inputs skipped due to a per-page problem (e.g.
// unparsable frontmatter) rather than a transaction-level failure.
type IndexFailure struct {
	PageID string
	Err    error
}

// IndexPages indexes multiple pages in a single transaction — used for
// subtree operations (recursive delete healing/move) where a naive
// one-IndexPage-call-per-page loop would pay for one commit per page.
// Load-tested: for a 200-page subtree this cut the search side effect's
// share of total Move/Delete cost from ~75-93% to a small fraction (see
// the Delete/Move/Rename load-test results).
//
// Inputs whose frontmatter fails to parse are skipped and reported via
// the returned []IndexFailure rather than aborting the whole batch — the
// rest still commit together. Only a transaction-level failure (e.g. the
// commit itself failing) returns a non-nil error, in which case none of
// the inputs were indexed. This preserves IndexPage's per-page fault
// isolation for the most likely failure mode (bad content) while still
// getting the throughput win for the common case (everything valid).
func (s *SQLiteIndex) IndexPages(inputs []IndexPageInput) ([]IndexFailure, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	type prepared struct {
		path, filePath, pageID, title string
		kind                          tree.NodeKind
		headings, sanitizedBody       string
		aliases                       string
	}

	var failures []IndexFailure
	prepped := make([]prepared, 0, len(inputs))
	for _, in := range inputs {
		fm, content, _, err := markdown.ParseFrontmatter(in.Raw)
		if err != nil {
			failures = append(failures, IndexFailure{PageID: in.PageID, Err: err})
			continue
		}
		content = excerpt.NormalizeMarkdownBody(content)
		prepped = append(prepped, prepared{
			path:          in.Path,
			filePath:      in.FilePath,
			pageID:        in.PageID,
			title:         cjkAnalyze(in.Title),
			kind:          in.Kind,
			headings:      cjkAnalyze(extractHeadings(content)),
			sanitizedBody: cjkAnalyze(excerpt.PlainTextForSearch(content)),
			aliases:       cjkAnalyze(strings.Join(fm.LeafWikiAliases, "\n")),
		})
	}

	if len(prepped) == 0 {
		return failures, nil
	}

	err := s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		deleteStmt, err := tx.Prepare(`DELETE FROM pages WHERE pageID = ?`)
		if err != nil {
			return err
		}
		defer func() {
			_ = deleteStmt.Close()
		}()

		insertStmt, err := tx.Prepare(`
			INSERT INTO pages (path, filepath, pageID, kind, title, headings, content, aliases)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?);
		`)
		if err != nil {
			return err
		}
		defer func() {
			_ = insertStmt.Close()
		}()

		for _, p := range prepped {
			if _, err := deleteStmt.Exec(p.pageID); err != nil {
				return err
			}
			if _, err := insertStmt.Exec(p.path, p.filePath, p.pageID, string(p.kind), p.title, p.headings, p.sanitizedBody, p.aliases); err != nil {
				return err
			}
		}

		return tx.Commit()
	})

	return failures, err
}

func (s *SQLiteIndex) IndexPage(path string, filePath string, pageID string, title string, kind tree.NodeKind, raw string) error {
	failures, err := s.IndexPages([]IndexPageInput{{Path: path, FilePath: filePath, PageID: pageID, Title: title, Kind: kind, Raw: raw}})
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failures[0].Err
	}
	return nil
}

// RemovePages removes multiple pages from the index in a single
// transaction — the batch counterpart to RemovePage, used for subtree
// deletes for the same reason IndexPages exists (one commit instead of
// one per page).
func (s *SQLiteIndex) RemovePages(pageIDs []string) error {
	if len(pageIDs) == 0 {
		return nil
	}
	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback()
		}()

		stmt, err := tx.Prepare(`DELETE FROM pages WHERE pageID = ?`)
		if err != nil {
			return err
		}
		defer func() {
			_ = stmt.Close()
		}()

		for _, pageID := range pageIDs {
			if _, err := stmt.Exec(pageID); err != nil {
				return err
			}
		}

		return tx.Commit()
	})
}

func (s *SQLiteIndex) RemovePage(pageID string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM pages WHERE pageID = ?`, pageID)
		return err
	})
}

func (s *SQLiteIndex) RemovePageByFilePath(filePath string) (int64, error) {
	var rows int64
	err := s.withDB(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM pages WHERE filepath = ?`, filePath)
		if err != nil {
			return err
		}
		r, err := res.RowsAffected()
		if err != nil {
			return err
		}
		rows = r
		return nil
	})
	return rows, err
}

func (s *SQLiteIndex) Search(query string, pageIDs []string, offset, limit int) (*SearchResult, error) {
	query = strings.TrimSpace(query)
	// ftsQuery, not the raw query, decides whether there's anything to MATCH
	// on: a query built entirely from characters outside the tokenizer's
	// word alphabet (e.g. "," or "'" alone) sanitizes to "" even though the
	// raw query wasn't empty, and `pages MATCH ''` is itself an FTS5 syntax
	// error — the same crash class #1577 reported, for the simplest
	// possible input.
	suggestion, suggest := s.correctionsFor(query)
	ftsQuery := buildFuzzyQuery(query, suggest)

	if len(pageIDs) == 0 && pageIDs != nil {
		return &SearchResult{
			Count:     0,
			Items:     []SearchResultItem{},
			Offset:    offset,
			Limit:     limit,
			TagFacets: []SearchTagFacet{},
		}, nil
	}

	if ftsQuery == "" && len(pageIDs) == 0 {
		return &SearchResult{
			Count:     0,
			Items:     []SearchResultItem{},
			Offset:    offset,
			Limit:     limit,
			TagFacets: []SearchTagFacet{},
		}, nil
	}

	sr := &SearchResult{TagFacets: []SearchTagFacet{}, Suggestion: suggestion}

	err := s.withDBRead(func(db *sql.DB) error {
		var total int
		whereClause, whereArgs := buildSearchWhereClause(ftsQuery, pageIDs)

		countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM pages WHERE %s;`, whereClause)
		if err := db.QueryRow(countQuery, whereArgs...).Scan(&total); err != nil {
			return err
		}
		sr.Count = total

		searchQuery := fmt.Sprintf(`
		SELECT 
			pageID,
			path,
			kind,
			%s AS bm25_score
		FROM pages
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?;
	`,
			searchRankExpr(ftsQuery != ""),
			whereClause,
			searchOrderByExpr(ftsQuery != ""),
		)

		queryArgs := append(append([]interface{}{}, whereArgs...), limit, offset)
		rows, err := db.Query(searchQuery, queryArgs...)
		if err != nil {
			return err
		}
		defer func() {
			if err := rows.Close(); err != nil {
				slog.Default().Error("could not close rows", "error", err)
			}
		}()

		var results []SearchResultItem
		for rows.Next() {
			var r SearchResultItem
			var bm25Score float64

			if err := rows.Scan(&r.PageID, &r.Path, &r.Kind, &bm25Score); err != nil {
				return err
			}

			if ftsQuery == "" {
				r.Rank = 1
			} else {
				// Convert bm25 score to a rank (lower score = higher rank)
				if bm25Score < 0 {
					bm25Score = 0
				}
				r.Rank = 1.0 / (1.0 + bm25Score)
			}

			results = append(results, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		sr.Items = results
		sr.Offset = offset
		sr.Limit = limit
		return nil
	})

	return sr, err
}

func (s *SQLiteIndex) SearchPageIDs(query string, pageIDs []string) ([]string, error) {
	query = strings.TrimSpace(query)
	_, suggest := s.correctionsFor(query)
	ftsQuery := buildFuzzyQuery(query, suggest)

	if len(pageIDs) == 0 && pageIDs != nil {
		return []string{}, nil
	}

	if ftsQuery == "" && len(pageIDs) == 0 {
		return []string{}, nil
	}

	var result []string

	err := s.withDBRead(func(db *sql.DB) error {
		whereClause, whereArgs := buildSearchWhereClause(ftsQuery, pageIDs)
		searchQuery := fmt.Sprintf(`
		SELECT pageID, %s AS bm25_score
		FROM pages
		WHERE %s
		ORDER BY %s;
	`, searchRankExpr(ftsQuery != ""), whereClause, searchOrderByExpr(ftsQuery != ""))

		rows, err := db.Query(searchQuery, whereArgs...)
		if err != nil {
			return err
		}
		defer func() {
			if err := rows.Close(); err != nil {
				slog.Default().Error("could not close rows", "error", err)
			}
		}()

		for rows.Next() {
			var pageID string
			var bm25Score float64
			if err := rows.Scan(&pageID, &bm25Score); err != nil {
				return err
			}
			result = append(result, pageID)
		}

		return rows.Err()
	})

	return result, err
}

// correctionsFor resolves the corrections for one query in a single
// vocabulary read: the suggester the query constructor uses, and the query
// with the corrections applied, for the hint the response carries. A failed
// vocabulary read must not fail the search, so the query is matched as typed.
func (s *SQLiteIndex) correctionsFor(query string) (string, termSuggester) {
	corrections, err := s.CorrectTerms(QueryTerms(query))
	if err != nil {
		slog.Default().Warn("search: could not read the term vocabulary, matching the query as typed", "error", err)
		return "", nil
	}
	if len(corrections) == 0 {
		return "", nil
	}
	suggest := func(term string) (string, bool) {
		corrected, ok := corrections[term]
		return corrected, ok
	}
	return applyCorrections(query, corrections), suggest
}

func buildSearchWhereClause(ftsQuery string, pageIDs []string) (string, []interface{}) {
	clauses := make([]string, 0, 2)
	args := make([]interface{}, 0, 1+len(pageIDs))

	if ftsQuery != "" {
		clauses = append(clauses, "pages MATCH ?")
		args = append(args, ftsQuery)
	}

	if len(pageIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pageIDs)), ",")
		clauses = append(clauses, fmt.Sprintf("pageID IN (%s)", placeholders))
		for _, pageID := range pageIDs {
			args = append(args, pageID)
		}
	}

	return strings.Join(clauses, " AND "), args
}

func searchRankExpr(hasQuery bool) string {
	if !hasQuery {
		return "0.0"
	}
	weights := make([]string, len(searchColumnWeights))
	for i, w := range searchColumnWeights {
		weights[i] = strconv.FormatFloat(w, 'f', -1, 64)
	}
	return "bm25(pages, " + strings.Join(weights, ", ") + ")"
}

func searchOrderByExpr(hasQuery bool) string {
	if hasQuery {
		return "bm25_score ASC"
	}
	return "title COLLATE NOCASE ASC, path COLLATE NOCASE ASC"
}
