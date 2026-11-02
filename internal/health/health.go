package health

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"cloudchat/internal/httpjson"
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

// NewHandler exposes health endpoints; the outer API layer adds request metadata.
func NewHandler(check ReadinessCheck) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, healthResponse{Status: "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if check == nil || check(r.Context()) != nil {
			httpjson.Write(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
		httpjson.Write(w, http.StatusOK, healthResponse{Status: "ok"})
	})

	return mux
}

type healthResponse struct {
	Status string `json:"status"`
}
