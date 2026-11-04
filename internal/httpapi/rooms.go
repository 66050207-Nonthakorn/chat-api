package httpapi

import (
	"net/http"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
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
	if !validateQueryKeys(w, r, query, map[string]bool{"limit": true, "cursor": true}) {
		return
	}
	limit, ok := parseLimit(w, r, query, 50, 100)
	if !ok {
		return
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
	httpjson.Write(w, http.StatusOK, struct {
		Items      []room  `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}{Items: items, NextCursor: nextCursor})
}
