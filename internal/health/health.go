package health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// ReadinessCheck reports whether this process can safely receive new work.
// It should check only dependencies needed to serve requests, without returning
// internal errors to the caller.
type ReadinessCheck func(context.Context) error

// DrainController makes readiness fail before a process stops accepting work.
// Liveness remains healthy so an orchestrator can drain the task instead of
// restarting it while its sockets and requests close.
type DrainController struct {
	check    ReadinessCheck
	draining atomic.Bool
}

// NewDrainController wraps dependency readiness with process drain state.
func NewDrainController(check ReadinessCheck) *DrainController {
	return &DrainController{check: check}
}

// BeginDrain makes subsequent readiness checks fail.
func (d *DrainController) BeginDrain() {
	d.draining.Store(true)
}

// Check reports unavailable when dependencies are unhealthy or the process
// has begun shutting down.
func (d *DrainController) Check(ctx context.Context) error {
	if d == nil || d.draining.Load() {
		return errors.New("process is draining")
	}
	if d.check == nil {
		return errors.New("readiness check is not configured")
	}
	return d.check(ctx)
}

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
