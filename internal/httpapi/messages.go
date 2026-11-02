package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
	chatmessage "cloudchat/internal/message"
	"cloudchat/internal/strictjson"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
)

const maxMessageRequestBytes = 16 * 1024

const memberMessagesQuery = `
	SELECT m.message_id::text, m.room_id::text, m.seq::text, m.sender_user_id::text,
	       m.sender_display_name, m.client_message_id::text, m.text, m.created_at
	FROM messages m
	JOIN room_memberships rm ON rm.room_id = m.room_id AND rm.user_id = $2::uuid
	WHERE m.room_id = $1::uuid`

type sendMessageRequest struct {
	ClientMessageID string `json:"client_message_id"`
	Text            string `json:"text"`
}

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request, roomID string) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType == "" {
		writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Use application/json", false, nil)
		return
	} else if mediaType, _, err := mime.ParseMediaType(contentType); err != nil || mediaType != "application/json" {
		writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Use application/json", false, nil)
		return
	}
	request, err := decodeSendRequest(w, r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body is too large", false, nil)
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body must be valid JSON", false, nil)
		return
	}
	if !uuid.IsCanonical(request.ClientMessageID) {
		writeValidationError(w, r, "client_message_id", "invalid_uuid")
		return
	}
	if len([]byte(request.Text)) == 0 || len([]byte(request.Text)) > 4096 || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, '\x00') || isAllWhitespace(request.Text) {
		writeValidationError(w, r, "text", "invalid_text")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	transaction, err := h.pool.Begin(ctx)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	validSession, err := auth.LockActiveSession(ctx, transaction, principal)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !validSession {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}

	var displayName string
	err = transaction.QueryRow(ctx, `
		SELECT display_name FROM app_users
		WHERE user_id = $1::uuid AND is_active
		FOR SHARE`, principal.UserID).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "Account is not allowed", false, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	err = transaction.QueryRow(ctx, `SELECT last_seq FROM rooms WHERE room_id = $1::uuid FOR UPDATE`, roomID).Scan(new(int64))
	if errors.Is(err, pgx.ErrNoRows) {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var isMember bool
	err = transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM room_memberships
			WHERE room_id = $1::uuid AND user_id = $2::uuid
		)`, roomID, principal.UserID).Scan(&isMember)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !isMember {
		writeRoomNotFound(w, r)
		return
	}

	existing, err := scanMessage(transaction.QueryRow(ctx, `
		SELECT message_id::text, room_id::text, seq::text, sender_user_id::text,
		       sender_display_name, client_message_id::text, text, created_at
		FROM messages
		WHERE sender_user_id = $1::uuid AND room_id = $2::uuid AND client_message_id = $3::uuid`,
		principal.UserID, roomID, request.ClientMessageID))
	if err == nil {
		if existing.Text != request.Text {
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Request key already used with different text", false, nil)
			return
		}
		if err := transaction.Commit(ctx); err != nil {
			dependencyUnavailable(w, r)
			return
		}
		httpjson.Write(w, http.StatusOK, struct {
			Message  chatmessage.Message `json:"message"`
			Replayed bool                `json:"replayed"`
		}{Message: existing, Replayed: true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		dependencyUnavailable(w, r)
		return
	}

	var seq int64
	err = transaction.QueryRow(ctx, `
		UPDATE rooms SET last_seq = last_seq + 1
		WHERE room_id = $1::uuid AND last_seq < 9223372036854775807
		RETURNING last_seq`, roomID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	messageID, err := uuid.New()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	outboxID, err := uuid.New()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var createdAt time.Time
	err = transaction.QueryRow(ctx, `
		INSERT INTO messages (
			message_id, room_id, seq, sender_user_id, sender_display_name,
			client_message_id, text
		) VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6::uuid, $7)
		RETURNING created_at`, messageID, roomID, seq, principal.UserID,
		displayName, request.ClientMessageID, request.Text).Scan(&createdAt)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO message_outbox (outbox_id, message_id) VALUES ($1::uuid, $2::uuid)`, outboxID, messageID); err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if err := transaction.Commit(ctx); err != nil {
		dependencyUnavailable(w, r)
		return
	}
	httpjson.Write(w, http.StatusCreated, struct {
		Message  chatmessage.Message `json:"message"`
		Replayed bool                `json:"replayed"`
	}{Message: chatmessage.Message{
		MessageID:       messageID,
		RoomID:          roomID,
		Seq:             strconv.FormatInt(seq, 10),
		Sender:          chatmessage.Sender{UserID: principal.UserID, DisplayName: displayName},
		ClientMessageID: request.ClientMessageID,
		Text:            request.Text,
		CreatedAt:       createdAt.UTC().Format(time.RFC3339Nano),
	}, Replayed: false})
}

func decodeSendRequest(w http.ResponseWriter, r *http.Request) (sendMessageRequest, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageRequestBytes))
	if err != nil {
		return sendMessageRequest{}, err
	}
	if !utf8.Valid(body) {
		return sendMessageRequest{}, errors.New("invalid utf-8")
	}
	var request sendMessageRequest
	if err := strictjson.Decode(body, &request); err != nil {
		return sendMessageRequest{}, err
	}
	return request, nil
}

func isAllWhitespace(value string) bool {
	for _, character := range value {
		if !unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func writeValidationError(w http.ResponseWriter, r *http.Request, field, code string) {
	writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Request validation failed", false, []errorDetail{{Field: field, Code: code}})
}

func writeRoomNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "ROOM_NOT_FOUND", "Room not found", false, nil)
}

func scanMessage(row interface{ Scan(...any) error }) (chatmessage.Message, error) {
	var result chatmessage.Message
	var senderID string
	var createdAt time.Time
	err := row.Scan(
		&result.MessageID, &result.RoomID, &result.Seq, &senderID,
		&result.Sender.DisplayName, &result.ClientMessageID, &result.Text, &createdAt,
	)
	if err != nil {
		return chatmessage.Message{}, err
	}
	result.Sender.UserID = senderID
	result.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return result, nil
}

func parseDecimalSequence(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("invalid decimal sequence")
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, errors.New("invalid decimal sequence")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("invalid decimal sequence")
	}
	return parsed, nil
}

func checkRoomMembership(ctx context.Context, poolQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, roomID, userID string) (bool, error) {
	var allowed bool
	err := poolQueryer.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM rooms r
			JOIN room_memberships rm ON rm.room_id = r.room_id
			WHERE r.room_id = $1::uuid AND rm.user_id = $2::uuid
		)`, roomID, userID).Scan(&allowed)
	return allowed, err
}

func invalidSequenceQuery(w http.ResponseWriter, r *http.Request, field string) {
	writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "The sequence cursor is invalid", false, []errorDetail{{Field: field, Code: "invalid"}})
}

func (h *handler) messageHistory(w http.ResponseWriter, r *http.Request, roomID string) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	query, ok := parseQuery(w, r)
	if !ok {
		return
	}
	if !validateQueryKeys(w, r, query, map[string]bool{"limit": true, "before_seq": true}) {
		return
	}
	limit, ok := parseLimit(w, r, query, 50, 100)
	if !ok {
		return
	}
	var beforeSeq *int64
	if value, exists := query["before_seq"]; exists {
		parsed, err := parseDecimalSequence(value[0])
		if err != nil {
			invalidSequenceQuery(w, r, "before_seq")
			return
		}
		beforeSeq = &parsed
	}

	ctx, cancel := queryContext(r)
	defer cancel()
	allowed, err := checkRoomMembership(ctx, h.pool, roomID, principal.UserID)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !allowed {
		writeRoomNotFound(w, r)
		return
	}
	rows, err := h.pool.Query(ctx, memberMessagesQuery+`
		AND ($3::bigint IS NULL OR m.seq < $3::bigint)
		ORDER BY m.seq DESC
		LIMIT $4`, roomID, principal.UserID, beforeSeq, limit+1)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]chatmessage.Message, 0, limit+1)
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
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
	var nextBeforeSeq *string
	if hasMore && len(items) > 0 {
		value := items[len(items)-1].Seq
		nextBeforeSeq = &value
	}
	httpjson.Write(w, http.StatusOK, struct {
		Items         []chatmessage.Message `json:"items"`
		NextBeforeSeq *string               `json:"next_before_seq"`
		HasMore       bool                  `json:"has_more"`
	}{Items: items, NextBeforeSeq: nextBeforeSeq, HasMore: hasMore})
}

func (h *handler) syncMessages(w http.ResponseWriter, r *http.Request, roomID string) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	query, ok := parseQuery(w, r)
	if !ok {
		return
	}
	if !validateQueryKeys(w, r, query, map[string]bool{"after_seq": true, "limit": true, "through_seq": true}) {
		return
	}
	afterValue, exists := query["after_seq"]
	if !exists {
		invalidSequenceQuery(w, r, "after_seq")
		return
	}
	afterSeq, err := parseDecimalSequence(afterValue[0])
	if err != nil {
		invalidSequenceQuery(w, r, "after_seq")
		return
	}
	limit, ok := parseLimit(w, r, query, 100, 200)
	if !ok {
		return
	}
	var requestedThrough *int64
	if value, exists := query["through_seq"]; exists {
		parsed, err := parseDecimalSequence(value[0])
		if err != nil {
			invalidSequenceQuery(w, r, "through_seq")
			return
		}
		requestedThrough = &parsed
	}

	ctx, cancel := queryContext(r)
	defer cancel()
	var lastSeq int64
	err = h.pool.QueryRow(ctx, `
		SELECT r.last_seq
		FROM rooms r
		JOIN room_memberships rm ON rm.room_id = r.room_id AND rm.user_id = $2::uuid
		WHERE r.room_id = $1::uuid`, roomID, principal.UserID).Scan(&lastSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	throughSeq := lastSeq
	if requestedThrough != nil {
		throughSeq = *requestedThrough
	}
	if afterSeq > lastSeq || throughSeq > lastSeq || afterSeq > throughSeq {
		invalidSequenceQuery(w, r, "after_seq")
		return
	}
	rows, err := h.pool.Query(ctx, memberMessagesQuery+`
		AND m.seq > $3::bigint AND m.seq <= $4::bigint
		ORDER BY m.seq ASC
		LIMIT $5`, roomID, principal.UserID, afterSeq, throughSeq, limit+1)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]chatmessage.Message, 0, limit+1)
	expected := afterSeq + 1
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			dependencyUnavailable(w, r)
			return
		}
		seq, err := parseDecimalSequence(item.Seq)
		if err != nil || seq != expected {
			writeError(w, r, http.StatusServiceUnavailable, "SYNC_INCONSISTENT", "The room history is inconsistent", true, nil)
			return
		}
		items = append(items, item)
		expected++
	}
	if rows.Err() != nil {
		dependencyUnavailable(w, r)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	} else if afterSeq < throughSeq && (len(items) == 0 || items[len(items)-1].Seq != strconv.FormatInt(throughSeq, 10)) {
		writeError(w, r, http.StatusServiceUnavailable, "SYNC_INCONSISTENT", "The room history is inconsistent", true, nil)
		return
	}
	nextAfterSeq := strconv.FormatInt(afterSeq, 10)
	if len(items) > 0 {
		nextAfterSeq = items[len(items)-1].Seq
	}
	httpjson.Write(w, http.StatusOK, struct {
		Items        []chatmessage.Message `json:"items"`
		ThroughSeq   string                `json:"through_seq"`
		NextAfterSeq string                `json:"next_after_seq"`
		HasMore      bool                  `json:"has_more"`
	}{Items: items, ThroughSeq: strconv.FormatInt(throughSeq, 10), NextAfterSeq: nextAfterSeq, HasMore: hasMore})
}

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
