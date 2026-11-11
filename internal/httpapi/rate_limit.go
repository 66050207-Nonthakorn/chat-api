package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"cloudchat/internal/auth"
	"cloudchat/internal/ratelimit"
)

func (h *handler) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation, limited := requestRateLimitOperation(r)
		if !limited {
			next.ServeHTTP(w, r)
			return
		}
		principal, authenticated := auth.PrincipalFromContext(r.Context())
		if !authenticated || principal.UserID == "" {
			next.ServeHTTP(w, r)
			return
		}
		var decision ratelimit.Decision
		var err error
		if h.rateLimit == nil {
			err = errors.New("production rate limiter is not configured")
		} else {
			decision, err = h.rateLimit.Allow(r.Context(), operation, principal.UserID)
		}
		if err != nil {
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
			return
		}
		if !decision.Allowed {
			retryAfter := decision.RetryAfterSeconds
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Request rate limit exceeded", true, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestRateLimitOperation(r *http.Request) (ratelimit.Operation, bool) {
	if r.URL.Path == "/api/v1/ws" && r.Method == http.MethodGet {
		return ratelimit.WebSocketHandshake, true
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/rooms/") && strings.HasSuffix(r.URL.Path, "/messages") {
		return ratelimit.MessageSend, true
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/rooms" || (strings.HasPrefix(r.URL.Path, "/api/v1/rooms/") && strings.HasSuffix(r.URL.Path, "/members"))) {
		return ratelimit.RoomWrite, true
	}
	if (r.Method == http.MethodPatch || r.Method == http.MethodDelete) && strings.HasPrefix(r.URL.Path, "/api/v1/rooms/") {
		return ratelimit.RoomWrite, true
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return ratelimit.APIRead, true
	}
	return "", false
}
