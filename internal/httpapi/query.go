package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
)

func parseQuery(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Malformed query string", false, nil)
		return nil, false
	}
	return query, true
}

func validateQueryKeys(w http.ResponseWriter, r *http.Request, query url.Values, allowed map[string]bool) bool {
	for key, values := range query {
		if !allowed[key] {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Unsupported query parameter", false, []errorDetail{{Field: key, Code: "unknown"}})
			return false
		}
		if len(values) != 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Query parameter must appear once", false, []errorDetail{{Field: key, Code: "duplicate"}})
			return false
		}
	}
	return true
}

func parseLimit(w http.ResponseWriter, r *http.Request, query url.Values, defaultValue, maximum int) (int, bool) {
	values, exists := query["limit"]
	if !exists {
		return defaultValue, true
	}
	value := values[0]
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > maximum || strconv.Itoa(parsed) != value {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid page size", false, []errorDetail{{Field: "limit", Code: "invalid"}})
		return 0, false
	}
	return parsed, true
}
