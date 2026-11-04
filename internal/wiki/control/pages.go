package control

import (
	"context"
)

// The write path's limits, in FR-141's numbers.
const (
	// MaxApplyItems bounds one page.apply batch.
	MaxApplyItems = 200
	// MaxApplyBytes bounds the whole batch.
	MaxApplyBytes = 8 << 20
	// MaxContentBytes bounds one item's content.
	MaxContentBytes = 1 << 20
)

// Caller is who is asking: the write path records it, and the capability matrix
// decides on operations that a whole-batch gate cannot judge (FR-115, FR-147).
type Caller struct {
	// Identity is the account id, or SystemUserID for an anonymous session.
	Identity string
	// Role is the account's role; empty for an anonymous session.
	Role string
}

// PageRef identifies one page for the read path (FR-131).
type PageRef struct {
	Path string `json:"path,omitempty"`
	ID   string `json:"id,omitempty"`
}

// PageView is what page.get returns (FR-132). Tags, Aliases and Properties are
// never null.
type PageView struct {
	Path       string            `json:"path"`
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Slug       string            `json:"slug"`
	Kind       string            `json:"kind"`
	Version    string            `json:"version"`
	Tags       []string          `json:"tags"`
	Aliases    []string          `json:"aliases"`
	Properties map[string]string `json:"properties"`
	Content    string            `json:"content"`
	// Vectors is the page's own vector state: indexed, stale, missing, queued,
	// failed or off (FR-132).
	Vectors string `json:"vectors"`
}

// ApplyItem is one entry of page.apply (FR-141). Optional fields are pointers so
// "leave it alone" and "set it to empty" stay distinguishable.
type ApplyItem struct {
	Path       string             `json:"path"`
	Create     bool               `json:"create,omitempty"`
	Delete     bool               `json:"delete,omitempty"`
	Title      *string            `json:"title,omitempty"`
	Tags       *[]string          `json:"tags,omitempty"`
	Aliases    *[]string          `json:"aliases,omitempty"`
	Properties *map[string]string `json:"properties,omitempty"`
	Content    *string            `json:"content,omitempty"`
	Recursive  bool               `json:"recursive,omitempty"`
}

// ApplyParams is the page.apply request (FR-141). Unknown fields are refused by
// the handler, which is how the P1 fields stay unimplemented (FR-142).
type ApplyParams struct {
	Items  []ApplyItem `json:"items"`
	DryRun bool        `json:"dry_run,omitempty"`
}

// ApplyResult is the page.apply answer. A partial batch still uses it: the
// failures live in the items, not in a top-level error (FR-148).
type ApplyResult struct {
	Results []ApplyItemResult `json:"results"`
}

// ApplyItemResult is one line of page.apply (FR-149).
type ApplyItemResult struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
	Version string `json:"version,omitempty"`
	// Vector is the wait result's starting point: queued, manual, off, paused or
	// unavailable. Never indexed without an explicit wait (FR-149, FR-163).
	Vector       string `json:"vector,omitempty"`
	Irreversible bool   `json:"irreversible,omitempty"`
	// Error marks the item that stopped the batch (FR-148).
	Error *ItemError `json:"error,omitempty"`
	// DryRun impact: what a delete would take with it (FR-147).
	Affected     []string `json:"affected,omitempty"`
	Assets       int      `json:"assets,omitempty"`
	Favorites    int      `json:"favorites,omitempty"`
	HasRevisions bool     `json:"has_revisions,omitempty"`
}

// ItemError is the per-item failure shape: a code, a kind and a hint.
type ItemError struct {
	Code int    `json:"code"`
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

// Pages is what the channel does with content; the composition root implements
// it with the wiki's own use cases.
type Pages interface {
	// PageGet reads one page (FR-131, FR-132).
	PageGet(ctx context.Context, caller Caller, ref PageRef) (PageView, *Error)
	// PageApply runs one batch: precheck everything, then write item by item
	// (FR-143 to FR-149).
	PageApply(ctx context.Context, caller Caller, params ApplyParams) (ApplyResult, *Error)
}
