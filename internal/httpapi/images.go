package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/imageproc"
	"cloudchat/internal/imagestore"
	chatmessage "cloudchat/internal/message"
	"cloudchat/internal/migrations"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxImageRequestBytes = 6 * 1024 * 1024
	maxImageBytes        = 5 * 1024 * 1024
	maxImageCaptionBytes = 4096
	imageUploadTimeout   = 45 * time.Second
)

type imageStore interface {
	Put(context.Context, string, []byte, string) error
	Get(context.Context, string) (imagestore.Object, error)
}

type imageSendRequest struct {
	clientMessageID string
	text            string
	image           []byte
}

func (h *handler) sendImageMessage(w http.ResponseWriter, r *http.Request, roomID string, principal auth.Principal) {
	// Rejections before consuming the body must not trigger an unbounded drain
	// of a stalled client. Successful reads restore normal keep-alive behavior.
	clientClose := r.Close
	r.Close = true
	w.Header().Set("Connection", "close")
	if !h.config.ImageMessagesEnabled {
		writeError(w, r, http.StatusServiceUnavailable, "IMAGE_MESSAGES_DISABLED", "Image sending is not enabled yet", true, nil)
		return
	}
	if h.images == nil || h.imageUploadSlots == nil {
		dependencyUnavailable(w, r)
		return
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid multipart request", false, nil)
		return
	}
	if r.ContentLength > maxImageRequestBytes {
		writeImageTooLarge(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), imageUploadTimeout)
	defer cancel()
	select {
	case h.imageUploadSlots <- struct{}{}:
		defer func() { <-h.imageUploadSlots }()
	default:
		r.Close = true
		w.Header().Set("Connection", "close")
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusTooManyRequests, "IMAGE_UPLOAD_BUSY", "Image upload capacity is busy", true, nil)
		return
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	allowed, err := checkRoomMembership(checkCtx, h.pool, roomID, principal.UserID)
	checkCancel()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !allowed {
		writeRoomNotFound(w, r)
		return
	}
	version, err := migrations.Version(ctx, h.pool)
	if err != nil || migrations.CheckVersion(version, true) != nil {
		dependencyUnavailable(w, r)
		return
	}
	request, err := readImageSendRequest(ctx, w, r, params["boundary"])
	if err != nil {
		r.Close = true // Do not drain an incomplete, possibly stalled body.
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			writeError(w, r, http.StatusRequestTimeout, "UPLOAD_TIMEOUT", "Image upload timed out", true, nil)
			return
		}
		if errors.Is(err, http.ErrNotSupported) {
			dependencyUnavailable(w, r)
			return
		}
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) || errors.Is(err, errImageTooLarge) {
			writeImageTooLarge(w, r)
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid multipart image message", false, nil)
		return
	}
	r.Close = clientClose
	if !clientClose {
		w.Header().Del("Connection")
	}
	if !uuid.IsCanonical(request.clientMessageID) {
		writeValidationError(w, r, "client_message_id", "invalid_uuid")
		return
	}
	if len(request.image) == 0 {
		writeValidationError(w, r, "image", "required")
		return
	}
	if len(request.text) > maxImageCaptionBytes || !utf8.ValidString(request.text) || bytes.IndexByte([]byte(request.text), 0) >= 0 {
		writeValidationError(w, r, "text", "invalid_text")
		return
	}

	processed, sourceType, err := imageproc.Normalize(ctx, request.image)
	if err != nil {
		writeValidationError(w, r, "image", "invalid_or_unsupported_image")
		return
	}
	sourceDigest := sha256.Sum256(request.image)
	imageID, err := uuid.New()
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	objectKey := "images/" + roomID + "/" + imageID
	_, err = h.pool.Exec(ctx, `
		INSERT INTO image_upload_candidates (
			image_id, room_id, sender_user_id, client_message_id, source_sha256,
			source_bytes, source_content_type, object_key, content_type,
			byte_size, width, height
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12)`,
		imageID, roomID, principal.UserID, request.clientMessageID, sourceDigest[:], len(request.image), sourceType,
		objectKey, processed.ContentType, len(processed.Bytes), processed.Width, processed.Height)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if err := h.images.Put(ctx, objectKey, processed.Bytes, processed.ContentType); err != nil {
		markImageDeleting(h.pool, imageID)
		dependencyUnavailable(w, r)
		return
	}

	h.persistImageMessage(w, r, ctx, roomID, principal, request, imageID, sourceDigest, len(request.image), sourceType, processed)
}

var errImageTooLarge = errors.New("image request exceeds the size limit")

func readImageSendRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, boundary string) (imageSendRequest, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return imageSendRequest{}, errors.New("upload requires a read deadline")
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return imageSendRequest{}, err
	}
	complete := false
	defer func() {
		if complete {
			_ = controller.SetReadDeadline(time.Time{})
		} else {
			// Keep the deadline on failed reads. Clearing it can let net/http
			// block while draining the unread body before writing the error.
			r.Close = true
			w.Header().Set("Connection", "close")
		}
	}()
	reader := multipart.NewReader(http.MaxBytesReader(w, r.Body, maxImageRequestBytes), boundary)
	var request imageSendRequest
	seen := make(map[string]bool, 3)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return imageSendRequest{}, err
		}
		name := part.FormName()
		if seen[name] || name != "client_message_id" && name != "text" && name != "image" {
			_ = part.Close()
			return imageSendRequest{}, errors.New("unexpected or duplicate multipart field")
		}
		seen[name] = true
		limit := int64(36)
		if name == "text" {
			limit = maxImageCaptionBytes
		}
		if name == "image" {
			limit = maxImageBytes
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		_ = part.Close()
		if err != nil {
			return imageSendRequest{}, err
		}
		if int64(len(data)) > limit {
			return imageSendRequest{}, errImageTooLarge
		}
		switch name {
		case "client_message_id":
			request.clientMessageID = string(data)
		case "text":
			request.text = string(data)
		case "image":
			request.image = data
		}
	}
	if !seen["client_message_id"] || !seen["image"] {
		return imageSendRequest{}, errors.New("client_message_id and image are required")
	}
	if !utf8.ValidString(request.clientMessageID) || !utf8.ValidString(request.text) {
		return imageSendRequest{}, errors.New("invalid UTF-8")
	}
	complete = true
	return request, nil
}

func (h *handler) persistImageMessage(w http.ResponseWriter, r *http.Request, ctx context.Context, roomID string, principal auth.Principal, request imageSendRequest, imageID string, sourceDigest [32]byte, sourceBytes int, sourceType string, processed imageproc.Result) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
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
	err = transaction.QueryRow(ctx, `SELECT display_name FROM app_users WHERE user_id = $1::uuid AND is_active FOR SHARE`, principal.UserID).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "Account is not allowed", false, nil)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var seq int64
	err = transaction.QueryRow(ctx, `SELECT last_seq FROM rooms WHERE room_id = $1::uuid FOR UPDATE`, roomID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	var isMember bool
	err = transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM room_memberships WHERE room_id = $1::uuid AND user_id = $2::uuid)`, roomID, principal.UserID).Scan(&isMember)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if !isMember {
		writeRoomNotFound(w, r)
		return
	}

	existing, err := scanMessage(transaction.QueryRow(ctx, `
		SELECT m.message_id::text, m.room_id::text, m.seq::text, m.sender_user_id::text,
		       m.sender_display_name, m.client_message_id::text, m.text, m.created_at,
		       images.image_id::text, images.content_type, images.byte_size, images.width, images.height
		FROM messages m LEFT JOIN image_upload_candidates images ON images.image_id = m.image_id
		WHERE m.sender_user_id = $1::uuid AND m.room_id = $2::uuid AND m.client_message_id = $3::uuid`,
		principal.UserID, roomID, request.clientMessageID))
	if err == nil {
		if existing.Text != request.text || existing.Image == nil {
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Request key already used with different content", false, nil)
			return
		}
		var existingDigest []byte
		var existingBytes int
		var existingType string
		err = transaction.QueryRow(ctx, `SELECT source_sha256, source_bytes, source_content_type FROM image_upload_candidates WHERE image_id = (SELECT image_id FROM messages WHERE message_id = $1::uuid)`, existing.MessageID).Scan(&existingDigest, &existingBytes, &existingType)
		if err != nil {
			dependencyUnavailable(w, r)
			return
		}
		if existing.Text != request.text || existingBytes != sourceBytes || existingType != sourceType || !bytes.Equal(existingDigest, sourceDigest[:]) {
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Request key already used with different content", false, nil)
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
	if seq >= 9223372036854775807 {
		dependencyUnavailable(w, r)
		return
	}
	seq++
	if _, err := transaction.Exec(ctx, `UPDATE rooms SET last_seq = $2 WHERE room_id = $1::uuid`, roomID, seq); err != nil {
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
	imageTag, err := transaction.Exec(ctx, `UPDATE image_upload_candidates SET state = 'attached' WHERE image_id = $1::uuid AND room_id = $2::uuid AND sender_user_id = $3::uuid AND client_message_id = $4::uuid AND state = 'candidate'`, imageID, roomID, principal.UserID, request.clientMessageID)
	if err != nil || imageTag.RowsAffected() != 1 {
		dependencyUnavailable(w, r)
		return
	}
	var createdAt time.Time
	err = transaction.QueryRow(ctx, `
		INSERT INTO messages (message_id, room_id, seq, sender_user_id, sender_display_name, client_message_id, text, image_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6::uuid, $7, $8::uuid)
		RETURNING created_at`, messageID, roomID, seq, principal.UserID, displayName, request.clientMessageID, request.text, imageID).Scan(&createdAt)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	if tag, err := transaction.Exec(ctx, `INSERT INTO message_outbox (outbox_id, message_id) VALUES ($1::uuid, $2::uuid)`, outboxID, messageID); err != nil || tag.RowsAffected() != 1 {
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
		MessageID: messageID, RoomID: roomID, Seq: strconv.FormatInt(seq, 10),
		Sender:          chatmessage.Sender{UserID: principal.UserID, DisplayName: displayName},
		ClientMessageID: request.clientMessageID, Text: request.text,
		Image:     &chatmessage.Image{URL: "/api/v1/rooms/" + roomID + "/messages/" + messageID + "/image", ContentType: processed.ContentType, ByteSize: len(processed.Bytes), Width: processed.Width, Height: processed.Height},
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}, Replayed: false})
}

func (h *handler) readMessageImage(w http.ResponseWriter, r *http.Request, roomID, messageID string) {
	if h.images == nil {
		dependencyUnavailable(w, r)
		return
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	version, err := migrations.Version(ctx, h.pool)
	if err != nil || migrations.CheckVersion(version, false) != nil {
		dependencyUnavailable(w, r)
		return
	}
	if version < 2 {
		writeRoomNotFound(w, r)
		return
	}
	var objectKey, contentType string
	var byteSize int
	err = h.pool.QueryRow(ctx, `
		SELECT images.object_key, images.content_type, images.byte_size
		FROM messages m
		JOIN room_memberships rm ON rm.room_id = m.room_id AND rm.user_id = $3::uuid
		JOIN image_upload_candidates images ON images.image_id = m.image_id AND images.state = 'attached'
		WHERE m.room_id = $1::uuid AND m.message_id = $2::uuid`, roomID, messageID, principal.UserID).Scan(&objectKey, &contentType, &byteSize)
	if errors.Is(err, pgx.ErrNoRows) {
		writeRoomNotFound(w, r)
		return
	}
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	streamImage(ctx, w, r, h.images, objectKey, contentType, byteSize)
}

func streamImage(ctx context.Context, w http.ResponseWriter, r *http.Request, store imageStore, key, contentType string, byteSize int) {
	if byteSize < 1 || byteSize > maxImageBytes || (contentType != "image/jpeg" && contentType != "image/png") {
		dependencyUnavailable(w, r)
		return
	}
	object, err := store.Get(ctx, key)
	if err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer func() { _ = object.Body.Close() }()
	if object.Size != int64(byteSize) {
		dependencyUnavailable(w, r)
		return
	}
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(10 * time.Second)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		dependencyUnavailable(w, r)
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(byteSize))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, object.Body, int64(byteSize)); err != nil {
		// Headers are already sent: abort the response so clients see a failed
		// transfer and retry the read, rather than receiving JSON as image data.
		panic(http.ErrAbortHandler)
	}
}

func markImageDeleting(pool *pgxpool.Pool, imageID string) {
	deleteCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = pool.Exec(deleteCtx, `UPDATE image_upload_candidates SET state = 'deleting' WHERE image_id = $1::uuid AND state = 'candidate'`, imageID)
}

func writeImageTooLarge(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Image request exceeds the 5 MiB image limit", false, nil)
}
