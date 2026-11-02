package vectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRoutes wires the real handlers, so a change to any of them is
// exercised here.
func newTestRoutes(service *Service) *gin.Engine {
	r := gin.New()
	routes := NewRoutes(service, nil)
	r.POST("/search/vectors/rebuild", routes.handleRebuild)
	r.GET("/search/vectors/pages/:id", routes.handlePageStatus)
	r.POST("/search/vectors/pages/:id/rebuild", routes.handlePageRebuild)
	r.POST("/search/vectors/pause", routes.handlePause)
	r.DELETE("/search/vectors", routes.handleDelete)
	r.GET("/search/vectors/status", routes.handleStatus)
	return r
}

func do(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(rec, req)
	return rec
}

func TestRoutes_FeatureOffAnswers503(t *testing.T) {
	router := newTestRoutes(nil)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/search/vectors/rebuild", ""},
		{http.MethodPost, "/search/vectors/pause", `{"paused":true}`},
		{http.MethodDelete, "/search/vectors", ""},
		{http.MethodGet, "/search/vectors/status", ""},
	} {
		rec := do(router, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503 while the feature is off", tc.method, tc.path, rec.Code)
		}
	}
}

func TestRoutes_RebuildAcceptsAndReportsRunning(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	// Hold the build slot: the endpoint must answer 409, not start a second
	// build (FR-411).
	if err := svc.claimBuild(); err != nil {
		t.Fatalf("claimBuild: %v", err)
	}
	defer svc.releaseBuild()

	rec := do(router, http.MethodPost, "/search/vectors/rebuild", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("rebuild while running = %d, want 409", rec.Code)
	}
	var conflict map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if conflict["ok"] != false || conflict["state"] != string(StateRunning) {
		t.Fatalf("conflict body = %v, want ok=false and state=running", conflict)
	}
}

func TestRoutes_RebuildAccepts(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	rec := do(router, http.MethodPost, "/search/vectors/rebuild", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rebuild = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	// The build runs in the background; wait for it to leave the running state.
	deadline := 200
	for i := 0; i < deadline; i++ {
		status, err := svc.Status(true)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.State == StateFresh {
			if current, err := store.CurrentBuild(); err != nil || current == nil {
				t.Fatalf("CurrentBuild = %#v, %v", current, err)
			}
			return
		}
	}
	t.Fatal("the background rebuild never finished")
}

func TestRoutes_Pause(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	if rec := do(router, http.MethodPost, "/search/vectors/pause", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("pause without a body = %d, want 400", rec.Code)
	}

	rec := do(router, http.MethodPost, "/search/vectors/pause", `{"paused":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if paused, err := svc.Paused(); err != nil || !paused {
		t.Fatalf("Paused = %v, %v; want true", paused, err)
	}

	if rec := do(router, http.MethodPost, "/search/vectors/pause", `{"paused":false}`); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d, want 200", rec.Code)
	}
	if paused, err := svc.Paused(); err != nil || paused {
		t.Fatalf("Paused = %v, %v; want false", paused, err)
	}
}

func TestRoutes_DeleteClearsAndRebuilds(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before, err := store.CurrentBuild()
	if err != nil || before == nil {
		t.Fatalf("CurrentBuild = %#v, %v", before, err)
	}

	rec := do(router, http.MethodDelete, "/search/vectors", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	// The old generation is gone immediately; the new one is built in the
	// background.
	current, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if current != nil && current.ID == before.ID {
		t.Fatalf("delete left the old generation current")
	}

	// The rebuild is asynchronous: let it settle before the test's temp
	// directory goes away, otherwise the store is still open on Windows.
	for i := 0; i < 200; i++ {
		status, err := svc.Status(true)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.State != StateRunning {
			return
		}
	}
	t.Fatal("the background rebuild never finished")
}

func TestRoutes_PageStatusAndRebuild(t *testing.T) {
	svc, _, _, pages := newTestService(t)
	router := newTestRoutes(svc)

	rec := do(router, http.MethodGet, "/search/vectors/pages/p1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var status PageStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if status.State != PageMissing {
		t.Fatalf("state = %q, want missing before any build", status.State)
	}

	if _, err := svc.Rebuild([]string{"p1"}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// One page can be rebuilt on its own, without a full rebuild.
	pages.pages["p1"].Content = "改过的内容。"
	rec = do(router, http.MethodPost, "/search/vectors/pages/p1/rebuild", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("page rebuild = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	rec = do(router, http.MethodGet, "/search/vectors/pages/p1", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if status.State != PageIndexed {
		t.Fatalf("state = %q, want indexed after its own rebuild", status.State)
	}
}

func TestRoutes_PageStatusWithoutAnIDIsRejected(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	rec := do(router, http.MethodGet, "/search/vectors/pages/", "")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Fatalf("status without an id = %d, want 400 or 404", rec.Code)
	}
}

func TestRoutes_Status(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	router := newTestRoutes(svc)

	rec := do(router, http.MethodGet, "/search/vectors/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if status["state"] != string(StateMissing) {
		t.Fatalf("state = %v, want missing", status["state"])
	}
	if _, ok := status["paused"]; !ok {
		t.Fatal("the admin status must carry the paused flag")
	}
}
