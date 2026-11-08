package httpapi

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/requestid"
	"cloudchat/internal/uuid"
	"github.com/gin-gonic/gin"
)

func requireAuthentication(c *gin.Context) {
	if !requirePrincipal(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.Next()
}

func requirePrincipal(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := auth.PrincipalFromContext(r.Context()); !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return false
	}
	return true
}

// Bind validated Gin parameters for the net/http handlers.
func bindRoomPath(c *gin.Context) {
	roomID := c.Param("roomID")
	if !uuid.IsCanonical(roomID) {
		writeRoomNotFound(c.Writer, c.Request)
		c.Abort()
		return
	}
	c.Request.SetPathValue("roomID", roomID)
	c.Next()
}

func bindImagePath(c *gin.Context) {
	roomID, messageID := c.Param("roomID"), c.Param("messageID")
	if !uuid.IsCanonical(roomID) || !uuid.IsCanonical(messageID) {
		writeError(c.Writer, c.Request, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
		c.Abort()
		return
	}
	c.Request.SetPathValue("roomID", roomID)
	c.Request.SetPathValue("messageID", messageID)
	c.Next()
}

func requestMetadata(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		r = r.WithContext(requestid.WithContext(r.Context(), id))
		observed := &observedResponseWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(observed, r)
		if observed.status == 0 {
			observed.status = http.StatusOK
		}
		if responseID := w.Header().Get("X-Request-ID"); responseID != "" {
			id = responseID
		}
		logger.Info("http request",
			"event", "http.request",
			"service", "api",
			"request_id", id,
			"method", r.Method,
			"route", safeRouteName(r.URL.Path),
			"status", observed.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type observedResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *observedResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *observedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support connection hijacking")
	}
	w.status = http.StatusSwitchingProtocols
	return hijacker.Hijack()
}

func (w *observedResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func safeRouteName(path string) string {
	switch {
	case path == "/health/live":
		return "health.live"
	case path == "/health/ready":
		return "health.ready"
	case path == "/auth/callback":
		return "auth.callback"
	case strings.HasPrefix(path, "/auth/"):
		return "auth.endpoint"
	case path == "/api/v1/ws":
		return "api.websocket"
	case strings.Contains(path, "/messages/") && strings.HasSuffix(path, "/image"):
		return "api.message.image"
	case path == "/api/v1/rooms":
		return "api.rooms"
	case path == "/api/v1/users":
		return "api.users"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/members"):
		return "api.room.members"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/sync"):
		return "api.room.sync"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/messages"):
		return "api.room.messages"
	default:
		return "not_found"
	}
}

var fallbackRequestSequence atomic.Uint64

func requestID() string {
	if id, err := requestid.New(); err == nil {
		return id
	}
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), fallbackRequestSequence.Add(1))
}
