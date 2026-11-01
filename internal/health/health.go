package health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// ReadinessCheck reports whether this process can safely receive new work.
// It should check only dependencies needed to serve requests, without returning
// internal errors to the caller.
type ReadinessCheck func(context.Context) error

// NewHandler exposes the public liveness and readiness endpoints.
func NewHandler(check ReadinessCheck) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if check == nil || check(r.Context()) != nil {
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeStatus(w, http.StatusOK, "ok")
	})

	return requestMetadata(mux)
}

type healthResponse struct {
	Status string `json:"status"`
}

func writeStatus(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: value})
}

var requestSequence atomic.Uint64

func requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestID())
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}

	// Request IDs are correlation data rather than credentials. Keep returning
	// one even if the operating system random source is temporarily unavailable.
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), requestSequence.Add(1))
}
