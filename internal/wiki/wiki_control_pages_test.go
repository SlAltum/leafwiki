package wiki

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/wiki/control"
)

// controlHarness is a running channel wired to a real wiki, plus a client.
type controlHarness struct {
	t      *testing.T
	conn   net.Conn
	dec    *json.Decoder
	wiki   *Wiki
	nextID int
}

func startControlHarness(t *testing.T) *controlHarness {
	t.Helper()

	w := createWikiTestInstance(t)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close wiki: %v", err)
		}
	})

	// A short path: an AF_UNIX address holds about 108 bytes (FR-104).
	socketDir, err := os.MkdirTemp("", "lw")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(socketDir)
	})

	service, err := control.New(control.Config{
		DataDir:      socketDir,
		Source:       w,
		Pages:        w.ControlPages(),
		AuthMode:     control.AuthOptional,
		Capabilities: []string{"page.get", "page.apply", "auth.login", "vectors.wait"},
	})
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Skipf("this platform cannot bind the control socket here: %v", err)
	}
	t.Cleanup(service.Stop)

	conn, err := net.Dial("unix", service.SocketPath())
	if err != nil {
		t.Skipf("this platform cannot dial a unix socket here: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return &controlHarness{t: t, conn: conn, dec: json.NewDecoder(conn), wiki: w}
}

// call sends one request and returns the raw result plus the error object.
func (h *controlHarness) call(method string, params any) (json.RawMessage, *control.Error) {
	h.t.Helper()

	h.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": h.nextID, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := json.NewEncoder(h.conn).Encode(request); err != nil {
		h.t.Fatalf("encode %s: %v", method, err)
	}
	if err := h.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		h.t.Fatalf("read deadline: %v", err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *control.Error  `json:"error"`
	}
	if err := h.dec.Decode(&response); err != nil {
		h.t.Fatalf("decode %s response: %v", method, err)
	}
	return response.Result, response.Error
}

// getPage reads one page through the channel and decodes the view.
func (h *controlHarness) getPage(params map[string]any) (control.PageView, *control.Error) {
	h.t.Helper()

	raw, apiErr := h.call("page.get", params)
	if apiErr != nil {
		return control.PageView{}, apiErr
	}
	var view control.PageView
	if err := json.Unmarshal(raw, &view); err != nil {
		h.t.Fatalf("decode page view %s: %v", raw, err)
	}
	return view, nil
}

// apply runs one batch and decodes the results.
func (h *controlHarness) apply(params control.ApplyParams) (control.ApplyResult, *control.Error) {
	h.t.Helper()

	raw, apiErr := h.call("page.apply", params)
	if apiErr != nil {
		return control.ApplyResult{}, apiErr
	}
	var result control.ApplyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		h.t.Fatalf("decode apply result %s: %v", raw, err)
	}
	return result, nil
}

func itemText(value string) *string { return &value }

// login authenticates the harness connection, which delete paths need (FR-115).
func (h *controlHarness) login(user, password string) {
	h.t.Helper()

	_, apiErr := h.call("auth.login", map[string]any{"user": user, "password": password})
	if apiErr != nil {
		h.t.Fatalf("auth.login: %#v", apiErr)
	}
}

func itemTags(values ...string) *[]string { return &values }

func itemProperties(values map[string]string) *map[string]string { return &values }

// page.get returns the SRS's fields and refuses an unknown page with its own kind
// (FR-131 to FR-133).
func TestControlPageGet(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path:       "platform/deploy",
		Create:     true,
		Title:      itemText("部署流程"),
		Content:    itemText("# 部署流程 body"),
		Tags:       itemTags("ops"),
		Properties: itemProperties(map[string]string{"env": "prod"}),
	}}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	view, apiErr := h.getPage(map[string]any{"path": "platform/deploy"})
	if apiErr != nil {
		t.Fatalf("page.get: %#v", apiErr)
	}
	if view.Path != "platform/deploy" || view.Title != "部署流程" || view.Kind != string(tree.NodeKindPage) {
		t.Fatalf("view = %#v", view)
	}
	if view.Content != "# 部署流程 body" {
		t.Fatalf("content = %q", view.Content)
	}
	if view.Version == "" {
		t.Fatal("the view has no version")
	}
	if len(view.Tags) != 1 || view.Tags[0] != "ops" {
		t.Fatalf("tags = %v", view.Tags)
	}
	if view.Properties["env"] != "prod" {
		t.Fatalf("properties = %v", view.Properties)
	}
	if view.Vectors != "off" {
		t.Fatalf("vectors = %q, want off without the feature", view.Vectors)
	}

	byID, apiErr := h.getPage(map[string]any{"id": view.ID})
	if apiErr != nil || byID.ID != view.ID {
		t.Fatalf("page.get by id = %#v, %#v", byID, apiErr)
	}

	_, apiErr = h.getPage(map[string]any{"path": "nope"})
	if apiErr == nil || apiErr.Code != control.CodePageNotFound {
		t.Fatalf("page.get(unknown) = %#v, want page_not_found", apiErr)
	}
	if apiErr.Data == nil || apiErr.Data.Kind != control.KindPageNotFound || apiErr.Data.Hint == "" {
		t.Fatalf("error data = %#v", apiErr.Data)
	}

	if _, apiErr := h.getPage(map[string]any{}); apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("page.get without a reference = %#v, want invalid params", apiErr)
	}
}

// A second identical write is unchanged and does not touch the version (FR-145).
func TestControlPageApply_UnchangedWrite(t *testing.T) {
	h := startControlHarness(t)

	first, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path:    "notes",
		Create:  true,
		Title:   itemText("Notes"),
		Content: itemText("body"),
	}}})
	if apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	if first.Results[0].Action != "create" || !first.Results[0].Changed {
		t.Fatalf("first result = %#v", first.Results[0])
	}

	second, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path:    "notes",
		Title:   itemText("Notes"),
		Content: itemText("body"),
	}}})
	if apiErr != nil {
		t.Fatalf("second apply: %#v", apiErr)
	}
	if second.Results[0].Action != "unchanged" || second.Results[0].Changed {
		t.Fatalf("second result = %#v, want unchanged", second.Results[0])
	}
	if second.Results[0].Version != first.Results[0].Version {
		t.Fatalf("version changed from %q to %q on an unchanged write", first.Results[0].Version, second.Results[0].Version)
	}
}

// A real update writes and moves the version on.
func TestControlPageApply_Update(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "notes", Create: true, Title: itemText("Notes"), Content: itemText("body"),
	}}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	before, _ := h.getPage(map[string]any{"path": "notes"})

	result, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "notes", Content: itemText("new body"),
	}}})
	if apiErr != nil {
		t.Fatalf("update: %#v", apiErr)
	}
	if result.Results[0].Action != "update" || !result.Results[0].Changed {
		t.Fatalf("result = %#v", result.Results[0])
	}
	after, _ := h.getPage(map[string]any{"path": "notes"})
	if after.Content != "new body" {
		t.Fatalf("content = %q", after.Content)
	}
	if after.Version == before.Version {
		t.Fatal("the version did not move after a real update")
	}
}

// Deleting a page with children needs recursive; dry_run writes nothing and
// reports the impact (FR-146, FR-147).
func TestControlPageApply_DeleteAndDryRun(t *testing.T) {
	h := startControlHarness(t)

	// One item creates the section and the page below it: an intermediate node
	// is a section by construction.
	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "handbook/deploy", Create: true, Title: itemText("Deploy"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	// Deleting needs an editor or an admin (FR-115).
	h.login("admin", "adminpassword")

	_, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{Path: "handbook", Delete: true}}})
	if apiErr == nil || apiErr.Data == nil || apiErr.Data.Kind != control.KindPageHasChildren {
		t.Fatalf("delete with children = %#v, want page_has_children", apiErr)
	}

	before := hashWikiTree(t, h.wiki)
	dry, apiErr := h.apply(control.ApplyParams{DryRun: true, Items: []control.ApplyItem{{Path: "handbook", Delete: true}}})
	if apiErr != nil {
		t.Fatalf("dry run: %#v", apiErr)
	}
	if after := hashWikiTree(t, h.wiki); before != after {
		t.Fatal("a dry run changed the tree")
	}
	if !dry.Results[0].Irreversible || !dry.Results[0].Changed {
		t.Fatalf("dry run result = %#v", dry.Results[0])
	}
	if len(dry.Results[0].Affected) != 2 {
		t.Fatalf("affected = %v, want the page and its child", dry.Results[0].Affected)
	}

	result, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{Path: "handbook", Delete: true, Recursive: true}}})
	if apiErr != nil {
		t.Fatalf("recursive delete: %#v", apiErr)
	}
	if !result.Results[0].Irreversible || result.Results[0].Action != "delete" {
		t.Fatalf("delete result = %#v", result.Results[0])
	}
	if _, apiErr := h.getPage(map[string]any{"path": "handbook"}); apiErr == nil {
		t.Fatal("the page survived its delete")
	}
}

// The precheck refuses a batch without writing anything (FR-143).
func TestControlPageApply_Precheck(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "keep", Create: true, Title: itemText("Keep"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	before := hashWikiTree(t, h.wiki)

	tests := []struct {
		name  string
		items []control.ApplyItem
	}{
		{name: "create without title", items: []control.ApplyItem{{Path: "new", Create: true}}},
		{name: "duplicate path", items: []control.ApplyItem{
			{Path: "dup", Create: true, Title: itemText("Dup"), Content: itemText("a")},
			{Path: "dup", Create: true, Title: itemText("Dup"), Content: itemText("b")},
		}},
		{name: "reserved segment", items: []control.ApplyItem{{Path: "edit", Create: true, Title: itemText("Edit")}}},
		{name: "invalid segment", items: []control.ApplyItem{{Path: "bad_name", Create: true, Title: itemText("Bad")}}},
		{name: "windows device name", items: []control.ApplyItem{{Path: "con", Create: true, Title: itemText("Con")}}},
		{name: "metadata without content", items: []control.ApplyItem{{Path: "keep", Tags: itemTags("x")}}},
		{name: "system property", items: []control.ApplyItem{{Path: "keep", Content: itemText("x"), Properties: itemProperties(map[string]string{"leafwiki_id": "x"})}}},
		{name: "update of a missing page", items: []control.ApplyItem{{Path: "ghost", Content: itemText("x")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, apiErr := h.apply(control.ApplyParams{Items: test.items})
			if apiErr == nil {
				t.Fatal("the batch was accepted")
			}
			if apiErr.Code != control.CodeInvalidParams && apiErr.Code != control.CodePageNotFound {
				t.Fatalf("code = %d, want a precheck refusal", apiErr.Code)
			}
		})
	}

	if after := hashWikiTree(t, h.wiki); before != after {
		t.Fatal("a refused batch wrote something")
	}
}

// The wire refuses P1 fields and oversized batches (FR-141, FR-142).
func TestControlPageApply_WireLimits(t *testing.T) {
	h := startControlHarness(t)

	_, apiErr := h.call("page.apply", map[string]any{"items": []map[string]any{{"path": "a", "create": true, "title": "A", "slug": "a"}}})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("a P1 field was accepted: %#v", apiErr)
	}

	_, apiErr = h.call("page.apply", map[string]any{"items": []map[string]any{{"path": "a", "create": true, "title": "A", "content_file": "/etc/passwd"}}})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("content_file was accepted: %#v", apiErr)
	}

	items := make([]map[string]any, 0, control.MaxApplyItems+1)
	for i := 0; i <= control.MaxApplyItems; i++ {
		items = append(items, map[string]any{"path": "p" + itoaForTest(i), "create": true, "title": "P"})
	}
	_, apiErr = h.call("page.apply", map[string]any{"items": items})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("an oversized batch was accepted: %#v", apiErr)
	}
}

// An empty batch is a success with no results (FR-151).
func TestControlPageApply_EmptyBatch(t *testing.T) {
	h := startControlHarness(t)

	result, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{}})
	if apiErr != nil {
		t.Fatalf("empty batch: %#v", apiErr)
	}
	if len(result.Results) != 0 {
		t.Fatalf("results = %v, want none", result.Results)
	}
}

// An anonymous caller may create and update but not delete (FR-115, FR-147).
func TestControlPageApply_AnonymousCannotDelete(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "doomed", Create: true, Title: itemText("Doomed"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	_, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{Path: "doomed", Delete: true}}})
	if apiErr == nil || apiErr.Code != control.CodeForbidden {
		t.Fatalf("anonymous delete = %#v, want permission_denied", apiErr)
	}
	if view, apiErr := h.getPage(map[string]any{"path": "doomed"}); apiErr != nil || view.ID == "" {
		t.Fatal("the page was deleted by an anonymous caller")
	}

	// The precheck refuses the whole batch, so the create before the delete is
	// not written either (FR-143).
	_, apiErr = h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "second", Create: true, Title: itemText("Second"), Content: itemText("body")},
		{Path: "doomed", Delete: true},
	}})
	if apiErr == nil || apiErr.Code != control.CodeForbidden {
		t.Fatalf("batch with an anonymous delete = %#v, want the whole batch refused", apiErr)
	}
	if _, apiErr := h.getPage(map[string]any{"path": "second"}); apiErr == nil {
		t.Fatal("the refused batch wrote its first item")
	}
}

// hashWikiTree fingerprints the content directory, including modification times.
func hashWikiTree(t *testing.T, w *Wiki) string {
	t.Helper()

	sum := sha256.New()
	rootDir := filepath.Join(w.GetStorageDir(), "root")
	var paths []string
	err := filepath.WalkDir(rootDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		_, _ = io.WriteString(sum, path+"|"+info.ModTime().UTC().String()+"|")
		_, _ = sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func itoaForTest(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// A default instance has no revision service: the dry run must still work
// (FR-147 on a stock server).
func TestControlPageApply_DryRunWithoutRevisions(t *testing.T) {
	w, err := NewWiki(&WikiOptions{
		StorageDir:          t.TempDir(),
		AdminPassword:       "adminpassword",
		JWTSecret:           "secretkey",
		AccessTokenTimeout:  15 * time.Minute,
		RefreshTokenTimeout: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewWiki: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := w.Close(); closeErr != nil {
			t.Errorf("close wiki: %v", closeErr)
		}
	})
	if w.revision != nil {
		t.Fatal("this fixture was meant to run without the revision feature")
	}

	pages := w.ControlPages()
	if _, apiErr := pages.PageApply(context.Background(), control.Caller{Identity: "admin", Role: "admin"}, control.ApplyParams{Items: []control.ApplyItem{
		{Path: "docs", Create: true, Title: itemText("Docs"), Content: itemText("section")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	result, apiErr := pages.PageApply(context.Background(), control.Caller{Identity: "admin", Role: "admin"}, control.ApplyParams{DryRun: true, Items: []control.ApplyItem{{Path: "docs", Delete: true}}})
	if apiErr != nil {
		t.Fatalf("dry run: %#v", apiErr)
	}
	if result.Results[0].HasRevisions {
		t.Fatal("a revision-less instance reported revisions")
	}
}

// An update that writes metadata keeps the properties the item did not mention
// (the metadata write replaces the whole map).
func TestControlPageApply_MetadataUpdateKeepsProperties(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "props", Create: true, Title: itemText("Props"), Content: itemText("body"),
		Properties: itemProperties(map[string]string{"env": "prod"}),
	}}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	result, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "props", Content: itemText("new body"), Tags: itemTags("ops"),
	}}})
	if apiErr != nil {
		t.Fatalf("update: %#v", apiErr)
	}
	if result.Results[0].Action != "update" {
		t.Fatalf("result = %#v", result.Results[0])
	}
	view, _ := h.getPage(map[string]any{"path": "props"})
	if view.Properties["env"] != "prod" {
		t.Fatalf("properties = %v; the update dropped one the item never mentioned", view.Properties)
	}
	if len(view.Tags) != 1 || view.Tags[0] != "ops" {
		t.Fatalf("tags = %v", view.Tags)
	}
}

// Tags are normalized on write, so the same content twice is unchanged (FR-145).
func TestControlPageApply_TagNormalizationIsIdempotent(t *testing.T) {
	h := startControlHarness(t)

	first, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "tags", Create: true, Title: itemText("Tags"), Content: itemText("body"), Tags: itemTags("Ops", " ops ", "OPS"),
	}}})
	if apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	second, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "tags", Content: itemText("body"), Tags: itemTags("ops"),
	}}})
	if apiErr != nil {
		t.Fatalf("second apply: %#v", apiErr)
	}
	if second.Results[0].Action != "unchanged" {
		t.Fatalf("result = %#v, want unchanged", second.Results[0])
	}
	if second.Results[0].Version != first.Results[0].Version {
		t.Fatal("an identical apply moved the version")
	}
}

// The capability is decided before the page's shape (FR-115, FR-147).
func TestControlPageApply_CapabilityBeforeShape(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{
		{Path: "section/child", Create: true, Title: itemText("Child"), Content: itemText("body")},
	}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}

	_, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{Path: "section", Delete: true}}})
	if apiErr == nil || apiErr.Code != control.CodeForbidden {
		t.Fatalf("anonymous delete of a page with children = %#v, want permission_denied", apiErr)
	}
}

// A create that would restructure an existing page is refused (FR-144).
func TestControlPageApply_ParentMustBeASection(t *testing.T) {
	h := startControlHarness(t)

	if _, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "leaf", Create: true, Title: itemText("Leaf"), Content: itemText("body"),
	}}}); apiErr != nil {
		t.Fatalf("create: %#v", apiErr)
	}
	_, apiErr := h.apply(control.ApplyParams{Items: []control.ApplyItem{{
		Path: "leaf/below", Create: true, Title: itemText("Below"), Content: itemText("body"),
	}}})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("create below a page = %#v, want invalid params", apiErr)
	}
}

// An item over 1 MiB is refused at the wire (FR-141).
func TestControlPageApply_ContentLimit(t *testing.T) {
	h := startControlHarness(t)

	big := strings.Repeat("x", control.MaxContentBytes+1)
	_, apiErr := h.call("page.apply", map[string]any{"items": []map[string]any{{"path": "big", "create": true, "title": "Big", "content": big}}})
	if apiErr == nil || apiErr.Code != control.CodeInvalidParams {
		t.Fatalf("an oversized content was accepted: %#v", apiErr)
	}
}

var _ = context.Background
var _ = errors.Is
