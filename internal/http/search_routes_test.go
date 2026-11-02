package http_test

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/perber/wiki/internal/test_utils"
)

// searchRoutes is the whole public search surface: path, method and the
// handler that must serve it.
var searchRoutes = []struct {
	key     string
	handler string
}{
	{"GET /api/search", "search.(*Routes).handleSearch"},
	{"GET /api/search/status", "search.(*Routes).handleGetIndexingStatus"},
}

// TestSearchRoutes_StayOnThePublicReadGroup is the route-level half of C-8.
// TestArchitecture_ImportBoundaries only sees package imports, not how a route
// is mounted, and ADR-0015's scoped read layer answers with a subset of the
// wiki; a search served from inside it would leak pages the scope excludes.
//
// The guard works on the mounted shape: the search API is exactly these two
// routes, each served by the search slice's own handler. A scoped group cannot
// reuse /api/search (gin panics on a duplicate route), so a scoped search would
// have to appear as an additional path, which this catches; a swap of the
// handler shows up as an unexpected handler name.
func TestSearchRoutes_StayOnThePublicReadGroup(t *testing.T) {
	w := createWikiTestInstance(t)
	defer test_utils.WrapCloseWithErrorCheck(w.Close, t)
	engine := createRouterTestInstance(w, t)

	for _, problem := range searchRouteProblems(engine.Routes()) {
		t.Error(problem)
	}
}

// TestSearchRouteProblems_RejectsAScopedDuplicate checks the checker: a second
// search entry point under a scoped path, or a search route served by another
// handler, must both be reported.
func TestSearchRouteProblems_RejectsAScopedDuplicate(t *testing.T) {
	clean := []gin.RouteInfo{
		{Method: "GET", Path: "/api/search", Handler: "search.(*Routes).handleSearch"},
		{Method: "GET", Path: "/api/search/status", Handler: "search.(*Routes).handleGetIndexingStatus"},
	}
	if problems := searchRouteProblems(clean); len(problems) != 0 {
		t.Fatalf("clean routes reported %v", problems)
	}

	withScopedDuplicate := append([]gin.RouteInfo{}, clean...)
	withScopedDuplicate = append(withScopedDuplicate, gin.RouteInfo{
		Method:  "GET",
		Path:    "/api/shares/:token/search",
		Handler: "scopedread.(*Routes).handleScopedSearch-fm",
	})
	if problems := searchRouteProblems(withScopedDuplicate); len(problems) != 1 {
		t.Fatalf("scoped duplicate reported %v, want one problem", problems)
	}

	swapped := append([]gin.RouteInfo{}, clean...)
	swapped[0].Handler = "scopedread.(*Routes).handleScopedSearch-fm"
	if problems := searchRouteProblems(swapped); len(problems) != 1 {
		t.Fatalf("swapped handler reported %v, want one problem", problems)
	}

	missing := []gin.RouteInfo{clean[0]}
	if problems := searchRouteProblems(missing); len(problems) != 1 {
		t.Fatalf("missing route reported %v, want one problem", problems)
	}
}

// searchRouteProblems lists what is wrong with the mounted search routes: an
// extra entry point, a handler that is not the search slice's, or a missing
// route.
func searchRouteProblems(routes []gin.RouteInfo) []string {
	seen := make(map[string]bool, len(searchRoutes))
	var problems []string

	for _, route := range routes {
		// Only the search reads are guarded here. Admin paths such as
		// /api/admin/search/vectors/rebuild are a different surface and never
		// a scoped page read.
		if !strings.HasSuffix(route.Path, "/search") && !strings.HasSuffix(route.Path, "/search/status") {
			continue
		}
		key := route.Method + " " + route.Path
		var expected string
		for _, want := range searchRoutes {
			if want.key == key {
				expected = want.handler
				break
			}
		}
		if expected == "" {
			problems = append(problems, "unexpected search route "+key+": the search API lives only at /api/search, never behind a scoped read group")
			continue
		}
		if !strings.Contains(route.Handler, expected) {
			problems = append(problems, key+" is served by "+route.Handler+", want "+expected)
		}
		seen[key] = true
	}

	for _, want := range searchRoutes {
		if !seen[want.key] {
			problems = append(problems, "missing search route "+want.key)
		}
	}
	return problems
}
