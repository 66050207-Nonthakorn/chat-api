package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"cloudchat/internal/auth"
)

type room struct {
	RoomID         string `json:"room_id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	MembershipRole string `json:"membership_role"`
	LastSeq        string `json:"last_seq"`
}

func (h *handler) listRooms(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	query, ok := parseQuery(w, r)
	if !ok {
		return
	}
	for key := range query {
		if key != "limit" && key != "cursor" {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Unsupported query parameter", false, []errorDetail{{Field: key, Code: "unknown"}})
			return
		}
		if len(query[key]) != 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Query parameter must appear once", false, []errorDetail{{Field: key, Code: "duplicate"}})
			return
		}
	}
	limit := 50
	if values, exists := query["limit"]; exists {
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != values[0] {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid page size", false, []errorDetail{{Field: "limit", Code: "invalid"}})
			return
		}
		limit = parsed
	}
	var afterRoomID *string
	if token := query.Get("cursor"); token != "" {
		roomID, err := h.decodeRoomCursor(token, principal.UserID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The room cursor is invalid", false, nil)
			return
		}
		afterRoomID = &roomID
	} else if _, exists := query["cursor"]; exists {
		writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The room cursor is invalid", false, nil)
		return
	}

	ctx, cancel := queryContext(r)
	defer cancel()
	rows, err := h.pool.Query(ctx, `
		SELECT r.room_id::text, r.kind, r.title,
		       CASE WHEN r.kind = 'dm' THEN 'member'
		            WHEN r.owner_user_id = $1::uuid THEN 'owner'
		            ELSE rm.member_role END,
		       r.last_seq::text
		FROM rooms r
		JOIN room_memberships rm ON rm.room_id = r.room_id AND rm.user_id = $1::uuid
		WHERE ($2::uuid IS NULL OR r.room_id > $2::uuid)
		ORDER BY r.room_id ASC
		LIMIT $3`, principal.UserID, afterRoomID, limit+1)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]room, 0, limit+1)
	for rows.Next() {
		var item room
		if err := rows.Scan(&item.RoomID, &item.Kind, &item.Title, &item.MembershipRole, &item.LastSeq); err != nil {
			dependencyUnavailable(w, r)
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		dependencyUnavailable(w, r)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		token, err := h.encodeRoomCursor(principal.UserID, items[len(items)-1].RoomID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "The request could not be completed", true, nil)
			return
		}
		nextCursor = &token
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []room  `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}{Items: items, NextCursor: nextCursor})
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
