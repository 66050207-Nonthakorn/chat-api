package httpapi

import (
	"net/http"
	"strings"

	"cloudchat/internal/uuid"
	"github.com/gin-gonic/gin"
)

func (h *handler) routes(health http.Handler) http.Handler {
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false

	probes := router.Group("/health")
	probes.GET("/live", gin.WrapH(health))
	probes.GET("/ready", gin.WrapH(health))
	probes.HEAD("/live", gin.WrapH(health))
	probes.HEAD("/ready", gin.WrapH(health))

	v1 := router.Group("/api/v1")
	v1.GET("/ws", gin.WrapF(h.serveWebSocket))

	protected := v1.Group("", requireAuthentication)
	protected.GET("/users", gin.WrapF(h.searchUsers))

	rooms := protected.Group("/rooms")
	rooms.GET("", gin.WrapF(h.listRooms))
	rooms.POST("", gin.WrapF(h.createRoom))

	room := rooms.Group("/:roomID", bindRoomPath)
	room.PATCH("", gin.WrapF(h.renameGroup))
	room.DELETE("/members/:userID", func(c *gin.Context) { c.Request.SetPathValue("userID", c.Param("userID")); c.Next() }, gin.WrapF(h.removeGroupMember))
	room.GET("/members", gin.WrapF(h.listMembers))
	room.POST("/members", gin.WrapF(h.addMembers))
	room.GET("/messages", gin.WrapF(h.messageHistory))
	room.POST("/messages", gin.WrapF(h.sendMessage))
	room.GET("/sync", gin.WrapF(h.syncMessages))

	// Image paths use the same not-found response for either malformed ID.
	rooms.GET("/:roomID/messages/:messageID/image", bindImagePath, gin.WrapF(h.readMessageImage))

	router.NoRoute(gin.WrapF(func(w http.ResponseWriter, r *http.Request) {
		h.serveUnmatched(w, r, health)
	}))
	return router
}

func (h *handler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.websocket == nil {
		dependencyUnavailable(w, r)
		return
	}
	h.websocket.ServeHTTP(w, r)
}

// serveUnmatched delegates method checks to the health and auth handlers and
// preserves API errors for unsupported methods and unknown room paths.
func (h *handler) serveUnmatched(w http.ResponseWriter, r *http.Request, health http.Handler) {
	path := r.URL.Path
	switch {
	case path == "/health/live" || path == "/health/ready":
		health.ServeHTTP(w, r)
	case strings.HasPrefix(path, "/auth/") || path == "/api/v1/session":
		if h.auth == nil {
			dependencyUnavailable(w, r)
			return
		}
		h.auth.ServeHTTP(w, r)
	case path == "/api/v1/ws" || path == "/api/v1/rooms" || path == "/api/v1/users":
		writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
	case strings.HasPrefix(path, "/api/v1/rooms/"):
		unmatchedRoomRoute(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	}
}

func unmatchedRoomRoute(w http.ResponseWriter, r *http.Request) {
	if !requirePrincipal(w, r) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/rooms/"), "/")
	if len(parts) == 4 && parts[1] == "messages" && parts[3] == "image" {
		if uuid.IsCanonical(parts[0]) && uuid.IsCanonical(parts[2]) {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
		} else {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
		}
		return
	}
	if len(parts) != 2 || !uuid.IsCanonical(parts[0]) {
		writeRoomNotFound(w, r)
		return
	}
	switch parts[1] {
	case "members", "messages", "sync":
		writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	}
}
