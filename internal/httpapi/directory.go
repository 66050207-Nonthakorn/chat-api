package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/uuid"
)

type directoryCursor struct {
	User  string `json:"u"`
	Query string `json:"q"`
	Name  string `json:"n"`
	ID    string `json:"i"`
}
type directoryUser struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

func (h *handler) searchUsers(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFromContext(r.Context())
	q, ok := parseQuery(w, r)
	if !ok || !validateQueryKeys(w, r, q, map[string]bool{"q": true, "limit": true, "cursor": true}) {
		return
	}
	term := strings.TrimSpace(q.Get("q"))
	if term == "" || len(term) > 400 || !utf8.ValidString(term) || strings.ContainsRune(term, 0) {
		writeValidationError(w, r, "q", "invalid")
		return
	}
	limit, ok := parseLimit(w, r, q, 20, 20)
	if !ok {
		return
	}
	var c directoryCursor
	if token, exists := q["cursor"]; exists {
		parts := strings.Split(token[0], ".")
		if len(parts) != 2 || len(token[0]) > 2048 {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "Invalid cursor", false, nil)
			return
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[1])
		mac := hmac.New(sha256.New, []byte(h.config.CursorHMACKey))
		mac.Write([]byte(parts[0]))
		b, e := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil || e != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(b, &c) != nil || c.User != p.UserID || c.Query != term || !uuid.IsCanonical(c.ID) {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "Invalid cursor", false, nil)
			return
		}
	}
	var afterID any
	if c.ID != "" {
		afterID = c.ID
	}
	ctx, cancel := queryContext(r)
	defer cancel()
	rows, err := h.pool.Query(ctx, `SELECT user_id::text,display_name FROM app_users WHERE is_active AND user_id<>$1::uuid AND position(lower($2) in lower(display_name))>0 AND ($3='' OR (lower(display_name),user_id)>(lower($3),$4::uuid)) ORDER BY lower(display_name),user_id LIMIT $5`, p.UserID, term, c.Name, afterID, limit+1)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]directoryUser, 0, limit+1)
	for rows.Next() {
		var u directoryUser
		if rows.Scan(&u.UserID, &u.DisplayName) != nil {
			dependencyUnavailable(w, r)
			return
		}
		items = append(items, u)
	}
	if rows.Err() != nil {
		dependencyUnavailable(w, r)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		b, _ := json.Marshal(directoryCursor{p.UserID, term, last.DisplayName, last.UserID})
		encoded := base64.RawURLEncoding.EncodeToString(b)
		mac := hmac.New(sha256.New, []byte(h.config.CursorHMACKey))
		mac.Write([]byte(encoded))
		token := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		next = &token
	}
	httpjson.Write(w, http.StatusOK, struct {
		Items []directoryUser `json:"items"`
		Next  *string         `json:"next_cursor"`
	}{items, next})
}
