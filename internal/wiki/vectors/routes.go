package vectors

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	coreauth "github.com/perber/wiki/internal/core/auth"
	httpinternal "github.com/perber/wiki/internal/http"
	authmw "github.com/perber/wiki/internal/http/middleware/auth"
)

// Routes is the RouteRegistrar for the semantic index admin endpoints. It lives
// in this slice because the endpoints only talk to the vector service; the
// search slice may not import another slice (docs/development/
// architecture rules).
type Routes struct {
	service     *Service
	authService *coreauth.AuthService
}

// NewRoutes constructs the vectors RouteRegistrar. service may be nil, which is
// how an instance with the feature off registers: every endpoint answers 503.
func NewRoutes(service *Service, authService *coreauth.AuthService) *Routes {
	return &Routes{service: service, authService: authService}
}

// RegisterRoutes implements RouteRegistrar.
func (r *Routes) RegisterRoutes(ctx httpinternal.RouterContext) {
	adminGroup := ctx.APIAuthGroup(r.authService).Group("/admin")
	adminGroup.Use(authmw.RequireAdmin(ctx.Opts.AuthDisabled))

	adminGroup.POST("/search/vectors/rebuild", r.handleRebuild)
	adminGroup.GET("/search/vectors/pages/:id", r.handlePageStatus)
	adminGroup.POST("/search/vectors/pages/:id/rebuild", r.handlePageRebuild)
	adminGroup.POST("/search/vectors/pause", r.handlePause)
	adminGroup.DELETE("/search/vectors", r.handleDelete)
	adminGroup.GET("/search/vectors/status", r.handleStatus)
}

// respondDisabled answers every endpoint of a switched-off feature.
func (r *Routes) respondDisabled(c *gin.Context) bool {
	if r.service != nil {
		return false
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "state": string(StateOff)})
	return true
}

// handleRebuild starts a full rebuild in the background.
func (r *Routes) handleRebuild(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}
	switch err := r.service.StartRebuild(); {
	case err == nil:
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	case errors.Is(err, ErrBuildRunning):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "state": string(StateRunning)})
	case errors.Is(err, ErrEncoderUnavailable), errors.Is(err, errNoPageSource):
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "state": string(StateOff), "error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
	}
}

// handlePageStatus reports one page's vectorisation, so a single page can be
// tracked without looking at the whole index.
func (r *Routes) handlePageStatus(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}
	pageID := c.Param("id")
	if pageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "page id is required"})
		return
	}

	status, err := r.service.PageStatus(pageID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, status)
	case errors.Is(err, ErrEncoderUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "state": string(StateOff), "error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
	}
}

// handlePageRebuild re-embeds one page in place, leaving every other page's
// chunks untouched.
func (r *Routes) handlePageRebuild(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}
	pageID := c.Param("id")
	if pageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "page id is required"})
		return
	}

	switch err := r.service.IndexPage(pageID); {
	case err == nil:
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	case errors.Is(err, ErrBuildRunning):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "state": string(StateRunning)})
	case errors.Is(err, ErrEncoderUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "state": string(StateOff), "error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
	}
}

// handlePause toggles automatic indexing.
func (r *Routes) handlePause(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}

	var body struct {
		Paused *bool `json:"paused"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Paused == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "body must carry a paused boolean"})
		return
	}

	if err := r.service.SetPaused(*body.Paused); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "paused": *body.Paused})
}

// handleDelete throws the index away and rebuilds it, which is also how a stale
// running marker is cleared.
func (r *Routes) handleDelete(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}
	switch err := r.service.ClearAndRebuild(); {
	case err == nil:
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	case errors.Is(err, ErrBuildRunning):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "state": string(StateRunning)})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
	}
}

// handleStatus returns the detailed admin view.
func (r *Routes) handleStatus(c *gin.Context) {
	if r.respondDisabled(c) {
		return
	}
	status, err := r.service.Status(true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}
