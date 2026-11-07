package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/strictjson"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
)

type createRoomRequest struct {
	Kind      string   `json:"kind"`
	UserID    string   `json:"user_id"`
	Title     string   `json:"title"`
	MemberIDs []string `json:"member_ids"`
	RequestID string   `json:"request_id"`
}
type addMembersRequest struct {
	MemberIDs []string `json:"member_ids"`
	RequestID string   `json:"request_id"`
}
type roomResult struct {
	RoomID   string `json:"room_id"`
	Replayed bool   `json:"replayed"`
}

func decodeManagement(w http.ResponseWriter, r *http.Request, target any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil || !utf8.Valid(b) || strictjson.Decode(b, target) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body", false, nil)
		return false
	}
	return true
}

func normalizeMembers(ids []string) ([]string, bool) {
	if len(ids) > 100 {
		return nil, false
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if !uuid.IsCanonical(id) {
			return nil, false
		}
		unique[id] = true
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, true
}

func lockActive(ctx context.Context, tx pgx.Tx, ids []string) (bool, error) {
	for _, id := range ids {
		var active bool
		err := tx.QueryRow(ctx, `SELECT is_active FROM app_users WHERE user_id=$1::uuid FOR SHARE`, id).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !active {
			return false, nil
		}
	}
	return true, nil
}

func unavailableUser(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusUnprocessableEntity, "USER_UNAVAILABLE", "User unavailable", false, nil)
}
func conflict(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusConflict, "REQUEST_CONFLICT", "Request ID was used for different contents", false, nil)
}
func requestHash(v any) []byte { b, _ := json.Marshal(v); h := sha256.Sum256(b); return h[:] }

// requestReplay serializes requests by actor, operation and request ID, including first attempts.
func requestReplay(ctx context.Context, tx pgx.Tx, user, op, id string, hash []byte) (string, bool, error) {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, user+":"+op+":"+id)
	if err != nil {
		return "", false, err
	}
	var roomID string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT room_id::text, request_hash FROM room_requests WHERE user_id=$1::uuid AND operation=$2 AND request_id=$3::uuid`, user, op, id).Scan(&roomID, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !bytes.Equal(hash, previous) {
		return "", true, nil
	}
	return roomID, true, nil
}

func (h *handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if !decodeManagement(w, r, &req) {
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	ids, ok := normalizeMembers(req.MemberIDs)
	if !ok {
		writeValidationError(w, r, "member_ids", "invalid")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Kind == "dm" {
		if !uuid.IsCanonical(req.UserID) || req.UserID == p.UserID || req.Title != "" || len(ids) != 0 || req.RequestID != "" {
			writeValidationError(w, r, "kind", "invalid")
			return
		}
		h.createDM(w, r, p.UserID, req.UserID)
		return
	}
	if req.Kind != "group" || req.UserID != "" || !uuid.IsCanonical(req.RequestID) || req.Title == "" || strings.ContainsRune(req.Title, 0) || utf8.RuneCountInString(req.Title) > 200 {
		writeValidationError(w, r, "kind", "invalid")
		return
	}
	members := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != p.UserID {
			members = append(members, id)
		}
	}
	hash := requestHash(struct {
		Title   string
		Members []string
	}{req.Title, members})
	ctx, cancel := queryContext(r)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(context.Background())
	roomID, replayed, err := requestReplay(ctx, tx, p.UserID, "create_group", req.RequestID, hash)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if replayed {
		if roomID == "" {
			conflict(w, r)
			return
		}
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid)`, roomID, p.UserID).Scan(&exists)
		if err != nil {
			dependencyUnavailable(w, r)
			return
		}
		if !exists {
			writeRoomNotFound(w, r)
			return
		}
		httpjson.Write(w, http.StatusOK, roomResult{roomID, true})
		return
	}
	if len(members)+1 > 20 {
		writeError(w, r, http.StatusUnprocessableEntity, "GROUP_FULL", "Group member limit exceeded", false, nil)
		return
	}
	all := append(append([]string{}, members...), p.UserID)
	sort.Strings(all)
	active, err := lockActive(ctx, tx, all)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !active {
		unavailableUser(w, r)
		return
	}
	roomID, err = uuid.New()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO rooms(room_id,kind,title,owner_user_id) VALUES($1::uuid,'group',$2,$3::uuid)`, roomID, req.Title, p.UserID)
	if err == nil {
		for _, id := range all {
			_, err = tx.Exec(ctx, `INSERT INTO room_memberships(room_id,user_id) VALUES($1::uuid,$2::uuid)`, roomID, id)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO room_requests(user_id,operation,request_id,request_hash,room_id) VALUES($1::uuid,'create_group',$2::uuid,$3,$4::uuid)`, p.UserID, req.RequestID, hash, roomID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	httpjson.Write(w, http.StatusCreated, roomResult{roomID, false})
}

func (h *handler) createDM(w http.ResponseWriter, r *http.Request, user, peer string) {
	ctx, cancel := queryContext(r)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(context.Background())
	ids := []string{user, peer}
	sort.Strings(ids)
	active, err := lockActive(ctx, tx, ids)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !active {
		unavailableUser(w, r)
		return
	}
	roomID, err := uuid.New()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var created string
	err = tx.QueryRow(ctx, `INSERT INTO rooms(room_id,kind,title,dm_user_low,dm_user_high) VALUES($1::uuid,'dm','DM',$2::uuid,$3::uuid) ON CONFLICT (dm_user_low,dm_user_high) WHERE kind='dm' DO NOTHING RETURNING room_id::text`, roomID, ids[0], ids[1]).Scan(&created)
	replay := errors.Is(err, pgx.ErrNoRows)
	if replay {
		err = tx.QueryRow(ctx, `SELECT room_id::text FROM rooms WHERE kind='dm' AND dm_user_low=$1::uuid AND dm_user_high=$2::uuid`, ids[0], ids[1]).Scan(&created)
	} else if err == nil {
		for _, id := range ids {
			_, err = tx.Exec(ctx, `INSERT INTO room_memberships(room_id,user_id) VALUES($1::uuid,$2::uuid)`, created, id)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	httpjson.Write(w, status, roomResult{created, replay})
}

func (h *handler) addMembers(w http.ResponseWriter, r *http.Request, roomID string) {
	var req addMembersRequest
	if !decodeManagement(w, r, &req) {
		return
	}
	ids, ok := normalizeMembers(req.MemberIDs)
	if !ok || len(ids) == 0 || !uuid.IsCanonical(req.RequestID) {
		writeValidationError(w, r, "member_ids", "invalid")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	hash := requestHash(struct {
		Room    string
		Members []string
	}{roomID, ids})
	ctx, cancel := queryContext(r)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(context.Background())
	previous, replayed, err := requestReplay(ctx, tx, p.UserID, "add_members", req.RequestID, hash)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	// Lock users before the room, matching message and image transactions.
	all := append(append([]string{}, ids...), p.UserID)
	sort.Strings(all)
	active, err := lockActive(ctx, tx, all)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT owner_user_id::text FROM rooms WHERE room_id=$1::uuid AND kind='group' FOR UPDATE`, roomID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT member_role FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid`, roomID, p.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != p.UserID && role != "admin") {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if replayed && previous == "" {
		conflict(w, r)
		return
	}
	if replayed {
		httpjson.Write(w, http.StatusOK, roomResult{roomID, true})
		return
	}
	if !active {
		unavailableUser(w, r)
		return
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM room_memberships WHERE room_id=$1::uuid`, roomID).Scan(&count)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	for _, id := range ids {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid)`, roomID, id).Scan(&exists)
		if err != nil {
			dependencyUnavailable(w, r)
			return
		}
		if !exists {
			count++
		}
	}
	if count > 20 {
		writeError(w, r, http.StatusUnprocessableEntity, "GROUP_FULL", "Group member limit exceeded", false, nil)
		return
	}
	for _, id := range ids {
		_, err = tx.Exec(ctx, `INSERT INTO room_memberships(room_id,user_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, roomID, id)
		if err != nil {
			dependencyUnavailable(w, r)
			return
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO room_requests(user_id,operation,request_id,request_hash,room_id) VALUES($1::uuid,'add_members',$2::uuid,$3,$4::uuid)`, p.UserID, req.RequestID, hash, roomID)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	httpjson.Write(w, http.StatusOK, roomResult{roomID, false})
}

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
			writeError(w, r, 400, "INVALID_CURSOR", "Invalid cursor", false, nil)
			return
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[1])
		mac := hmac.New(sha256.New, []byte(h.config.CursorHMACKey))
		mac.Write([]byte(parts[0]))
		b, e := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil || e != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(b, &c) != nil || c.User != p.UserID || c.Query != term || !uuid.IsCanonical(c.ID) {
			writeError(w, r, 400, "INVALID_CURSOR", "Invalid cursor", false, nil)
			return
		}
	}
	ctx, cancel := queryContext(r)
	defer cancel()
	rows, err := h.pool.Query(ctx, `SELECT user_id::text,display_name FROM app_users WHERE is_active AND user_id<>$1::uuid AND position(lower($2) in lower(display_name))>0 AND ($3='' OR (lower(display_name),user_id)>(lower($3),$4::uuid)) ORDER BY lower(display_name),user_id LIMIT $5`, p.UserID, term, c.Name, func() any {
		if c.ID == "" {
			return nil
		}
		return c.ID
	}(), limit+1)
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

type groupMember struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Active      bool   `json:"is_active"`
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request, roomID string) {
	p, _ := auth.PrincipalFromContext(r.Context())
	ctx, cancel := queryContext(r)
	defer cancel()
	rows, err := h.pool.Query(ctx, `SELECT u.user_id::text,u.display_name,CASE WHEN r.owner_user_id=u.user_id THEN 'owner' ELSE rm.member_role END,u.is_active FROM rooms r JOIN room_memberships viewer ON viewer.room_id=r.room_id AND viewer.user_id=$2::uuid JOIN room_memberships rm ON rm.room_id=r.room_id JOIN app_users u ON u.user_id=rm.user_id WHERE r.room_id=$1::uuid AND r.kind='group' ORDER BY lower(u.display_name),u.user_id`, roomID, p.UserID)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := []groupMember{}
	for rows.Next() {
		var m groupMember
		if rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.Active) != nil {
			dependencyUnavailable(w, r)
			return
		}
		items = append(items, m)
	}
	if rows.Err() != nil {
		dependencyUnavailable(w, r)
		return
	}
	if len(items) == 0 {
		writeRoomNotFound(w, r)
		return
	}
	httpjson.Write(w, http.StatusOK, struct {
		Items []groupMember `json:"items"`
	}{items})
}
