// Package httpjson writes JSON HTTP responses.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// Write encodes value as a JSON response with the given status.
func Write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
