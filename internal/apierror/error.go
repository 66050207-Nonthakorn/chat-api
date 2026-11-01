package apierror

import (
	"encoding/json"
	"net/http"
)

// Detail describes one field-level validation error in the API contract.
type Detail struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type response struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id"`
	Retryable bool     `json:"retryable"`
	Details   []Detail `json:"details"`
}

// Write emits the shared HTTP error envelope used by HTTP and WebSocket
// handshakes. Request IDs come from the request metadata middleware.
func Write(w http.ResponseWriter, status int, code, message string, retryable bool, details []Detail) {
	if details == nil {
		details = []Detail{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Error: errorBody{
		Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID"),
		Retryable: retryable, Details: details,
	}})
}
