package wiki

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/perber/wiki/internal/core/assets"
	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/core/revision"
	sharederrors "github.com/perber/wiki/internal/core/shared/errors"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/favorites"
	httpmetrics "github.com/perber/wiki/internal/http/metrics"
	"github.com/perber/wiki/internal/restore"
	"github.com/perber/wiki/internal/wiki/control"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
	"github.com/perber/wiki/internal/wiki/pagesave"
	"github.com/perber/wiki/internal/wiki/vectors"
)

// pageOps is what the channel's page methods touch. The running server and the
// offline path fill it differently, so both run the same code and the same use
// cases (FR-172).
type pageOps struct {
	tree        *tree.TreeService
	slug        *tree.SlugService
	newPipeline func() *pagesave.PageSaveOrchestrator
	revision    *revision.Service
	assets      *assets.AssetService
	favorites   *favorites.FavoritesStore
	vectors     *vectors.Service
	gate        *restore.WriteGate
	writeMu     *sync.Mutex
	log         *slog.Logger
	metrics     *httpmetrics.HTTPMetrics
}

// controlPages implements control.Pages with the wiki's own use cases: the
// channel gets no private write path (docs/srs/agent-operations.srs.md FR-131 to
// FR-149).
type controlPages struct {
	ops pageOps
}

// newControlPages wires the page methods to an instance's services.
func newControlPages(ops pageOps) *controlPages {
	return &controlPages{ops: ops}
}

// ControlPages hands the channel the page methods.
func (w *Wiki) ControlPages() control.Pages {
	return newControlPages(pageOps{
		tree:        w.tree,
		slug:        w.slug,
		newPipeline: w.newPageOrchestrator,
		revision:    w.revision,
		assets:      w.asset,
		favorites:   w.favorites,
		vectors:     w.vectorService,
		gate:        w.writeGate,
		writeMu:     &w.controlWriteMu,
		log:         w.log,
		metrics:     w.metrics,
	})
}

// PageGet reads one page and its metadata (FR-131, FR-132).
func (p *controlPages) PageGet(ctx context.Context, _ control.Caller, ref control.PageRef) (control.PageView, *control.Error) {
	view, apiErr := p.resolve(ctx, ref)
	if apiErr != nil {
		return control.PageView{}, apiErr
	}
	page := view.Page
	return control.PageView{
		Path:       routePath(page),
		ID:         page.ID,
		Title:      page.Title,
		Slug:       page.Slug,
		Kind:       string(page.Kind),
		Version:    page.Version(),
		Tags:       view.Tags,
		Aliases:    view.Aliases,
		Properties: view.Properties,
		Content:    page.Content,
		Vectors:    p.vectorState(page.ID),
	}, nil
}

// routePath is the path an API client uses: no leading slash.
func routePath(page *tree.Page) string {
	if page == nil || page.PageNode == nil {
		return ""
	}
	return strings.TrimPrefix(page.CalculatePath(), "/")
}

// resolve reads a page by id or route path. A page that is not there is the only
// thing that maps to page_not_found: everything else is the server's problem.
func (p *controlPages) resolve(ctx context.Context, ref control.PageRef) (*wikipages.PageView, *control.Error) {
	if ref.ID != "" {
		view, err := wikipages.NewGetPageViewByIDUseCase(p.ops.tree).Execute(ctx, wikipages.GetPageViewByIDInput{ID: ref.ID})
		if err != nil {
			if errors.Is(err, tree.ErrPageNotFound) {
				return nil, control.ErrPageNotFound(ref.ID)
			}
			return nil, control.ErrInternal(err)
		}
		return view, nil
	}

	path := strings.Trim(strings.TrimSpace(ref.Path), "/")
	view, err := wikipages.NewGetPageViewByPathUseCase(p.ops.tree).Execute(ctx, wikipages.GetPageViewByPathInput{RoutePath: path})
	if err != nil {
		if errors.Is(err, tree.ErrPageNotFound) {
			return nil, control.ErrPageNotFound(ref.Path)
		}
		return nil, control.ErrInternal(err)
	}
	return view, nil
}

// vectorState reports the page's own vector state (FR-132, FR-165b). Without the
// feature it is off; without an encoder the comparison still works.
func (p *controlPages) vectorState(pageID string) string {
	service := p.ops.vectors
	if service == nil {
		return string(vectors.StateOff)
	}
	if !service.EncoderAvailable() {
		status, err := service.PageDiffStatus(pageID)
		if err != nil {
			p.ops.log.Warn("could not read a page's vector state", "pageID", pageID, "error", err)
			return string(vectors.PageMissing)
		}
		return string(status.State)
	}
	status, err := service.PageStatus(pageID)
	if err != nil {
		p.ops.log.Warn("could not read a page's vector state", "pageID", pageID, "error", err)
		return string(vectors.PageMissing)
	}
	return string(status.State)
}

// applyPlan is one prechecked item.
type applyPlan struct {
	item   control.ApplyItem
	path   string
	action string
	page   *tree.Page
}

// bufferedOrchestrator holds the side effects back until the item has finished
// writing. A create writes twice (the node, then its content) and a delete
// removes assets after its event, so releasing on the first event would let a
// restore swap the files while the item is still writing (FR-150, C-9).
type bufferedOrchestrator struct {
	inner  *pagesave.PageSaveOrchestrator
	events []pagesave.PageSaveEvent
}

// Apply records the event instead of running it.
func (b *bufferedOrchestrator) Apply(event pagesave.PageSaveEvent) {
	b.events = append(b.events, event)
}

// replay runs the recorded events through the real pipeline. The caller has
// released the write gate by now.
func (b *bufferedOrchestrator) replay() {
	for _, event := range b.events {
		b.inner.Run(event)
	}
	b.events = nil
}

// newBufferedOrchestrator wraps the wiki's pipeline for one item.
func (p *controlPages) newBufferedOrchestrator() (*pagesave.PageSaveOrchestrator, *bufferedOrchestrator) {
	buffer := &bufferedOrchestrator{inner: p.ops.newPipeline()}
	return pagesave.NewPageSaveOrchestrator(p.ops.metrics, buffer), buffer
}

// enterWriteGate admits one item. A restore in progress refuses it (FR-150).
func (p *controlPages) enterWriteGate() (func(), *control.Error) {
	if p.ops.gate == nil {
		return func() {}, nil
	}
	leave, ok := p.ops.gate.TryEnter()
	if !ok {
		return nil, control.ErrRestoreInProgress()
	}
	var once sync.Once
	return func() { once.Do(leave) }, nil
}

// PageApply checks the whole batch first, then writes item by item (FR-143 to
// FR-149).
func (p *controlPages) PageApply(ctx context.Context, caller control.Caller, params control.ApplyParams) (control.ApplyResult, *control.Error) {
	empty := control.ApplyResult{Results: []control.ApplyItemResult{}}
	if caller.Identity == "" {
		// Every write is attributed: an empty identity would land in the
		// frontmatter as an authorless page (FR-113, FR-114).
		return control.ApplyResult{}, control.ErrInternal(errors.New("the caller has no identity"))
	}
	if len(params.Items) == 0 {
		// An empty batch is not an error (FR-151).
		return empty, nil
	}

	// One batch at a time: the side effects run outside the tree lock, so two
	// batches would interleave their revisions and queue rows (NFR-7).
	if !params.DryRun {
		p.ops.writeMu.Lock()
		defer p.ops.writeMu.Unlock()
	}

	plans, apiErr := p.precheck(params.Items, caller, params.DryRun)
	if apiErr != nil {
		return control.ApplyResult{}, apiErr
	}

	if params.DryRun {
		results := make([]control.ApplyItemResult, 0, len(plans))
		for _, plan := range plans {
			results = append(results, p.dryRunResult(plan))
		}
		return control.ApplyResult{Results: results}, nil
	}

	results := make([]control.ApplyItemResult, 0, len(plans))
	for _, plan := range plans {
		result := p.execute(ctx, caller, plan)
		results = append(results, result)
		if result.Error != nil {
			if len(results) == 1 {
				// Nothing was written, so the batch itself failed (FR-148).
				return control.ApplyResult{}, &control.Error{Code: result.Error.Code, Message: result.Error.Hint, Data: &control.ErrorData{Kind: result.Error.Kind, Hint: result.Error.Hint}}
			}
			// The items before it succeeded: the caller must see them (FR-148).
			return control.ApplyResult{Results: results}, nil
		}
	}
	return control.ApplyResult{Results: results}, nil
}

// precheck refuses a batch before anything is written (FR-143).
func (p *controlPages) precheck(items []control.ApplyItem, caller control.Caller, dryRun bool) ([]applyPlan, *control.Error) {
	plans := make([]applyPlan, 0, len(items))
	seen := make(map[string]bool, len(items))
	// Paths this batch creates as pages: a later item cannot make one of them a
	// section, because that would restructure a page (FR-144).
	createdPages := make(map[string]bool, len(items))

	for index, item := range items {
		path := strings.Trim(strings.TrimSpace(item.Path), "/")
		if path == "" {
			return nil, control.ErrInvalidItem("item "+itoa(index)+" has no path", "every item needs the route path of the page")
		}
		if seen[strings.ToLower(path)] {
			return nil, control.ErrInvalidItem("the batch names "+path+" twice", "send one item per page")
		}
		seen[strings.ToLower(path)] = true

		if item.Create && item.Delete {
			return nil, control.ErrInvalidItem("item "+path+" says create and delete", "send one of the two")
		}
		if apiErr := p.validatePath(path); apiErr != nil {
			return nil, apiErr
		}

		plan := applyPlan{item: item, path: path}
		page, err := p.ops.tree.FindPageByRoutePath(path)
		switch {
		case err == nil:
			plan.page = page
		case errors.Is(err, tree.ErrPageNotFound):
			plan.page = nil
		default:
			return nil, control.ErrInternal(err)
		}

		if item.Delete {
			// The capability comes first: an anonymous caller must learn that,
			// not how the page beside it happens to be shaped (FR-115, FR-147).
			if !auth.Can(caller.Role, auth.OpPageDelete) {
				return nil, control.ErrPermissionDenied()
			}
			if plan.page == nil {
				return nil, control.ErrPageNotFound(path)
			}
			// A dry run reports what the subtree would take with it, so it is
			// the place to learn that recursive is needed (FR-147).
			if plan.page.HasChildren() && !item.Recursive && !dryRun {
				return nil, control.ErrPageHasChildren(path)
			}
			plan.action = "delete"
			plans = append(plans, plan)
			continue
		}

		if plan.page == nil && !item.Create {
			return nil, control.ErrPageNotFound(path)
		}
		if item.Title != nil && strings.TrimSpace(*item.Title) == "" {
			return nil, control.ErrInvalidItem("item "+path+" has an empty title", "a page needs a title")
		}
		if plan.page == nil && item.Title == nil {
			return nil, control.ErrInvalidItem("create "+path+" has no title", "a new page needs \"title\"")
		}
		// A create below a leaf would turn that leaf into a section (FR-144).
		if apiErr := p.checkParentIsASection(path); apiErr != nil {
			return nil, apiErr
		}
		if apiErr := checkParentNotCreatedAsPage(path, createdPages); apiErr != nil {
			return nil, apiErr
		}
		if item.Content == nil && (item.Tags != nil || item.Aliases != nil || item.Properties != nil) {
			return nil, control.ErrInvalidItem("metadata without content at "+path, "tags, aliases and properties are written with the content: send \"content\" too")
		}
		if item.Properties != nil {
			for key := range *item.Properties {
				if markdown.IsSystemKey(key) {
					return nil, control.ErrInvalidItem("property "+key+" is reserved at "+path, "pick another name; names starting with leafwiki_ and tag keys belong to the wiki")
				}
			}
		}
		if plan.page == nil {
			plan.action = "create"
			createdPages[path] = true
		} else {
			plan.action = "update"
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// checkParentNotCreatedAsPage refuses a batch that first creates a path as a
// page and then needs it to be a section (FR-144: no restructuring).
func checkParentNotCreatedAsPage(path string, createdPages map[string]bool) *control.Error {
	segments := strings.Split(path, "/")
	for i := 1; i < len(segments); i++ {
		prefix := strings.Join(segments[:i], "/")
		if createdPages[prefix] {
			return control.ErrInvalidItem("the batch creates "+prefix+" as a page and then needs it as a section", "create the section first in its own batch, or create only the child path")
		}
	}
	return nil
}

// checkParentIsASection refuses a create that would restructure an existing
// page: writing "a/b" under a leaf "a" would turn it into a section (FR-144).
func (p *controlPages) checkParentIsASection(path string) *control.Error {
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return nil
	}
	parentPath := strings.Join(segments[:len(segments)-1], "/")
	parent, err := p.ops.tree.FindPageByRoutePath(parentPath)
	if err != nil {
		// A missing parent is fine: EnsurePath creates the intermediates.
		return nil
	}
	if parent.Kind == tree.NodeKindPage {
		return control.ErrInvalidItem("the parent "+parentPath+" is a page, not a section", "creating a page below it would change its structure; create the section first")
	}
	return nil
}

// validatePath refuses a route path the wiki cannot store (FR-143).
func (p *controlPages) validatePath(path string) *control.Error {
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return control.ErrInvalidItem("the path "+path+" has an empty or relative segment", "use the slug segments only, for example platform/deploy")
		}
		if isWindowsDeviceName(segment) {
			return control.ErrInvalidItem("the path "+path+" uses the reserved name "+segment, "rename the segment; Windows cannot store it")
		}
		if err := p.ops.slug.IsValidSlug(segment); err != nil {
			return control.ErrInvalidItem("the path "+path+" is not usable: "+err.Error(), "use letters, digits and hyphens per segment, and avoid reserved words")
		}
	}
	return nil
}

// isWindowsDeviceName reports the names Windows refuses as files.
func isWindowsDeviceName(segment string) bool {
	name := strings.ToUpper(strings.TrimSpace(segment))
	switch name {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

// itoa keeps error messages short without pulling fmt into every call.
func itoa(value int) string {
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

// execute applies one prechecked item.
func (p *controlPages) execute(ctx context.Context, caller control.Caller, plan applyPlan) control.ApplyItemResult {
	result := control.ApplyItemResult{Path: plan.path, Action: plan.action}

	// The gate covers every file write of this item; the side effects run after
	// it is released, so a restore never waits for the indexing (FR-150).
	leave, gateErr := p.enterWriteGate()
	if gateErr != nil {
		return failedItem(plan, restoreInProgressError{})
	}
	orchestrator, buffer := p.newBufferedOrchestrator()
	defer func() {
		leave()
		// The earlier legs may have written pages before the failing one, so the
		// recorded events run either way.
		buffer.replay()
	}()

	switch plan.action {
	case "delete":
		useCase := wikipages.NewDeletePageUseCase(p.ops.tree, p.ops.revision, p.ops.assets, p.ops.favorites, orchestrator, p.ops.log, p.ops.metrics)
		if err := useCase.Execute(ctx, wikipages.DeletePageInput{
			UserID:    caller.Identity,
			ID:        plan.page.ID,
			Version:   plan.page.Version(),
			Recursive: plan.item.Recursive,
		}); err != nil {
			return failedItem(plan, err)
		}
		// Nothing is queued for a delete: the side effect only forgets the page.
		result.Action = "delete"
		result.Changed = true
		result.Irreversible = true
		return result

	case "create":
		ensure := wikipages.NewEnsurePathUseCase(p.ops.tree, p.ops.slug, orchestrator, p.ops.log)
		created, err := ensure.Execute(ctx, wikipages.EnsurePathInput{
			UserID:      caller.Identity,
			TargetPath:  plan.path,
			TargetTitle: strings.TrimSpace(*plan.item.Title),
			Kind:        kindPtr(tree.NodeKindPage),
		})
		if err != nil {
			return failedItem(plan, err)
		}
		result.Action = "create"
		result.Changed = true
		page := created.Page
		if plan.item.Content != nil || plan.item.Tags != nil || plan.item.Aliases != nil || plan.item.Properties != nil {
			if updateErr := p.write(ctx, caller, page, plan.item, orchestrator); updateErr != nil {
				result.Error = itemErrorOf(plan, updateErr)
				return result
			}
		}
		return p.finalize(result, plan, page.ID)

	default:
		if p.sameAsStored(plan) {
			// Nothing to write: no revision, no queue entry (FR-145).
			result.Action = "unchanged"
			result.Changed = false
			result.Version = plan.page.Version()
			return result
		}
		if err := p.write(ctx, caller, plan.page, plan.item, orchestrator); err != nil {
			return failedItem(plan, err)
		}
		result.Action = "update"
		result.Changed = true
		return p.finalize(result, plan, plan.page.ID)
	}
}

// controlWriteProbe, when set by a test, runs immediately before the item's
// second file write. Production leaves it nil.
var controlWriteProbe func()

// write updates one page through the normal save path.
func (p *controlPages) write(ctx context.Context, caller control.Caller, page *tree.Page, item control.ApplyItem, orchestrator *pagesave.PageSaveOrchestrator) error {
	input := wikipages.UpdatePageInput{
		UserID:  caller.Identity,
		ID:      page.ID,
		Version: page.Version(),
		Title:   page.Title,
		Slug:    page.Slug,
		Kind:    kindPtr(page.Kind),
	}
	if item.Title != nil {
		input.Title = strings.TrimSpace(*item.Title)
	}
	if item.Content != nil {
		input.Content = item.Content
	}
	// Writing tags or aliases rewrites the metadata block, and that write
	// replaces every string property with the map it is given. An item that does
	// not mention properties keeps the ones the page has (FR-143 guards only the
	// other direction).
	if item.Tags != nil || item.Aliases != nil {
		_, _, current := p.currentMetadata(page.ID)
		input.Properties = current
	}
	if item.Tags != nil {
		// The readers normalize, so the writer must too, or the same content
		// looks different on the next apply (FR-145).
		input.Tags = wikipages.NormalizeTags(*item.Tags)
	}
	if item.Aliases != nil {
		input.Aliases = wikipages.NormalizeAliases(*item.Aliases)
	}
	if item.Properties != nil {
		input.Properties = *item.Properties
	}
	if controlWriteProbe != nil {
		controlWriteProbe()
	}
	useCase := wikipages.NewUpdatePageUseCase(p.ops.tree, p.ops.slug, orchestrator, p.ops.log, p.ops.metrics)
	_, err := useCase.Execute(ctx, input)
	return err
}

// finalize reads the written page back and fills version and vector state.
func (p *controlPages) finalize(result control.ApplyItemResult, plan applyPlan, pageID string) control.ApplyItemResult {
	page, err := p.ops.tree.GetPage(pageID)
	if err != nil {
		return result
	}
	result.Version = page.Version()
	result.Vector = p.pendingVectorState()
	return result
}

// currentMetadata reads the page's tags, aliases and properties the way the
// readers see them.
func (p *controlPages) currentMetadata(pageID string) ([]string, []string, map[string]string) {
	view, err := wikipages.NewGetPageViewByIDUseCase(p.ops.tree).Execute(context.Background(), wikipages.GetPageViewByIDInput{ID: pageID})
	if err != nil {
		return []string{}, []string{}, map[string]string{}
	}
	return view.Tags, view.Aliases, view.Properties
}

// sameAsStored reports whether the item would change nothing (FR-145).
func (p *controlPages) sameAsStored(plan applyPlan) bool {
	if plan.page == nil {
		return false
	}
	tags, aliases, properties := p.currentMetadata(plan.page.ID)
	item := plan.item
	if item.Title != nil && strings.TrimSpace(*item.Title) != plan.page.Title {
		return false
	}
	if item.Content != nil && *item.Content != plan.page.Content {
		return false
	}
	if item.Tags != nil && !sameStrings(wikipages.NormalizeTags(*item.Tags), tags) {
		return false
	}
	if item.Aliases != nil && !sameStrings(wikipages.NormalizeAliases(*item.Aliases), aliases) {
		return false
	}
	if item.Properties != nil && !sameProperties(*item.Properties, properties) {
		return false
	}
	return true
}

// sameStrings compares two string lists as sets, which is how tags and aliases
// are stored.
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	sortedLeft := append([]string{}, left...)
	sortedRight := append([]string{}, right...)
	sort.Strings(sortedLeft)
	sort.Strings(sortedRight)
	for i := range sortedLeft {
		if sortedLeft[i] != sortedRight[i] {
			return false
		}
	}
	return true
}

func sameProperties(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// dryRunResult reports what one item would do and what a delete would take with
// it (FR-147).
func (p *controlPages) dryRunResult(plan applyPlan) control.ApplyItemResult {
	result := control.ApplyItemResult{Path: plan.path, Action: plan.action}
	if plan.page == nil {
		result.Changed = plan.action == "create"
		return result
	}
	result.Version = plan.page.Version()
	if plan.action == "delete" {
		result.Changed = true
		result.Irreversible = true
		// A delete takes the whole subtree with it, so the impact is summed over
		// every page below it (FR-147, FR-146).
		for _, node := range subtreeNodes(plan.page.PageNode) {
			result.Affected = append(result.Affected, routePath(&tree.Page{PageNode: node}))
			if p.ops.assets != nil {
				if assets, err := p.ops.assets.ListAssetsForPage(node); err == nil {
					result.Assets += len(assets)
				}
			}
			if p.ops.favorites != nil {
				if favorites, err := p.ops.favorites.CountForPage(node.ID); err == nil {
					result.Favorites += favorites
				}
			}
			// Revisions only exist when the feature is on: the service is nil
			// otherwise (FR-147 must work on a default instance).
			if p.ops.revision != nil {
				if revisions, err := p.ops.revision.ListRevisions(node.ID); err == nil && len(revisions) > 0 {
					result.HasRevisions = true
				}
			}
		}
		sort.Strings(result.Affected)
		return result
	}
	result.Changed = !p.sameAsStored(plan)
	return result
}

// subtreeNodes lists the page and everything below it, for a delete's impact.
func subtreeNodes(node *tree.PageNode) []*tree.PageNode {
	if node == nil {
		return nil
	}
	nodes := []*tree.PageNode{node}
	for _, child := range node.Children {
		nodes = append(nodes, subtreeNodes(child)...)
	}
	return nodes
}

// pendingVectorState is the wait result before any wait happened (FR-149): a
// write never reports indexed on its own.
func (p *controlPages) pendingVectorState() string {
	service := p.ops.vectors
	if service == nil {
		return string(vectors.StateOff)
	}
	if !service.EncoderAvailable() {
		return "unavailable"
	}
	if service.Mode() == vectors.ModeManual {
		return "manual"
	}
	paused, err := service.Paused()
	if err != nil {
		p.ops.log.Warn("could not read the semantic index pause flag", "error", err)
	} else if paused {
		return "paused"
	}
	return "queued"
}

// failedItem is the item that stopped the batch (FR-148, §4.5): action "none",
// nothing changed, and the code the CLI maps to an exit code.
func failedItem(plan applyPlan, err error) control.ApplyItemResult {
	return control.ApplyItemResult{
		Path:   plan.path,
		Action: "none",
		Error:  itemErrorOf(plan, err),
	}
}

// restoreInProgressError carries the refusal through the normal error mapping.
type restoreInProgressError struct{}

func (restoreInProgressError) Error() string { return "a restore is running" }

// itemErrorOf maps a use case failure onto the SRS's codes (FR-196).
func itemErrorOf(plan applyPlan, err error) *control.ItemError {
	var validation *sharederrors.ValidationErrors
	switch {
	case errors.Is(err, tree.ErrPageNotFound):
		return &control.ItemError{Code: control.CodePageNotFound, Kind: control.KindPageNotFound, Hint: "add \"create\": true to create it"}
	case errors.Is(err, tree.ErrVersionConflict), errors.Is(err, tree.ErrVersionRequired):
		return &control.ItemError{Code: control.CodePageNotFound, Kind: control.KindPageVersionConflict, Hint: "read the page again and retry with the current version"}
	case errors.Is(err, tree.ErrPageAlreadyExists):
		return &control.ItemError{Code: control.CodePageNotFound, Kind: control.KindPageSlugConflict, Hint: "pick another path, or delete the other page first"}
	case errors.As(err, &restoreInProgressError{}):
		return &control.ItemError{Code: control.CodeRestoreInProgress, Kind: control.KindRestoreInProgress, Hint: "wait for the restore to finish and send the batch again"}
	case errors.As(err, &validation):
		return &control.ItemError{Code: control.CodeInvalidParams, Kind: control.KindInvalidParams, Hint: validation.Error()}
	default:
		return &control.ItemError{Code: control.CodeInternal, Kind: control.KindInternal, Hint: err.Error()}
	}
}

func kindPtr(kind tree.NodeKind) *tree.NodeKind { return &kind }
