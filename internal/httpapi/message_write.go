package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/httpjson"
	"cloudchat/internal/imageproc"
	chatmessage "cloudchat/internal/message"
	"cloudchat/internal/migrations"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	errMessageUnauthenticated   = errors.New("message session is not active")
	errMessageAccountNotAllowed = errors.New("message sender is not active")
	errMessageRoomNotFound      = errors.New("message room is unavailable")
	errMessageConflict          = errors.New("message idempotency conflict")
)

type messageImageCandidate struct {
	id           string
	sourceDigest [32]byte
	sourceBytes  int
	sourceType   string
	processed    imageproc.Result
}

type messageWriteRequest struct {
	roomID          string
	principal       auth.Principal
	clientMessageID string
	text            string
	image           *messageImageCandidate
}

type messageWriteResult struct {
	Message  chatmessage.Message `json:"message"`
	Replayed bool                `json:"replayed"`
}

// writeMessage commits the message, its room sequence, and its outbox event
// together. Image candidates have already been uploaded outside the transaction.
func (h *handler) writeMessage(parent context.Context, request messageWriteRequest) (messageWriteResult, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return messageWriteResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	validSession, err := auth.LockActiveSession(ctx, tx, request.principal)
	if err != nil {
		return messageWriteResult{}, err
	}
	if !validSession {
		return messageWriteResult{}, errMessageUnauthenticated
	}
	var displayName string
	err = tx.QueryRow(ctx, `SELECT display_name FROM app_users WHERE user_id = $1::uuid AND is_active FOR SHARE`, request.principal.UserID).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return messageWriteResult{}, errMessageAccountNotAllowed
	}
	if err != nil {
		return messageWriteResult{}, err
	}
	var previousSeq int64
	err = tx.QueryRow(ctx, `SELECT last_seq FROM rooms WHERE room_id = $1::uuid FOR UPDATE`, request.roomID).Scan(&previousSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return messageWriteResult{}, errMessageRoomNotFound
	}
	if err != nil {
		return messageWriteResult{}, err
	}
	var isMember bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM room_memberships WHERE room_id = $1::uuid AND user_id = $2::uuid)`, request.roomID, request.principal.UserID).Scan(&isMember)
	if err != nil {
		return messageWriteResult{}, err
	}
	if !isMember {
		return messageWriteResult{}, errMessageRoomNotFound
	}

	imageJoin := ` LEFT JOIN image_upload_candidates images ON images.image_id = m.image_id `
	if request.image == nil {
		imageJoin, err = migrations.MessageImageJoin(ctx, tx)
		if err != nil {
			return messageWriteResult{}, err
		}
	}
	existing, err := scanMessage(tx.QueryRow(ctx, messageSelect+imageJoin+`
        WHERE m.sender_user_id = $1::uuid AND m.room_id = $2::uuid AND m.client_message_id = $3::uuid`,
		request.principal.UserID, request.roomID, request.clientMessageID))
	if err == nil {
		if existing.Text != request.text || (existing.Image == nil) != (request.image == nil) {
			return messageWriteResult{}, errMessageConflict
		}
		if request.image != nil {
			var digest []byte
			var sourceBytes int
			var sourceType string
			err = tx.QueryRow(ctx, `SELECT source_sha256, source_bytes, source_content_type FROM image_upload_candidates WHERE image_id = (SELECT image_id FROM messages WHERE message_id = $1::uuid)`, existing.MessageID).Scan(&digest, &sourceBytes, &sourceType)
			if err != nil {
				return messageWriteResult{}, err
			}
			if sourceBytes != request.image.sourceBytes || sourceType != request.image.sourceType || !bytes.Equal(digest, request.image.sourceDigest[:]) {
				return messageWriteResult{}, errMessageConflict
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return messageWriteResult{}, err
		}
		return messageWriteResult{Message: existing, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return messageWriteResult{}, err
	}

	var seq int64
	err = tx.QueryRow(ctx, `UPDATE rooms SET last_seq = last_seq + 1 WHERE room_id = $1::uuid AND last_seq < 9223372036854775807 RETURNING last_seq`, request.roomID).Scan(&seq)
	if err != nil {
		return messageWriteResult{}, err
	}
	messageID, err := uuid.New()
	if err != nil {
		return messageWriteResult{}, err
	}
	outboxID, err := uuid.New()
	if err != nil {
		return messageWriteResult{}, err
	}

	var createdAt time.Time
	if request.image == nil {
		err = tx.QueryRow(ctx, `INSERT INTO messages (message_id, room_id, seq, sender_user_id, sender_display_name, client_message_id, text)
            VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6::uuid, $7) RETURNING created_at`,
			messageID, request.roomID, seq, request.principal.UserID, displayName, request.clientMessageID, request.text).Scan(&createdAt)
	} else {
		tag, attachErr := tx.Exec(ctx, `UPDATE image_upload_candidates SET state = 'attached' WHERE image_id = $1::uuid AND room_id = $2::uuid AND sender_user_id = $3::uuid AND client_message_id = $4::uuid AND state = 'candidate'`, request.image.id, request.roomID, request.principal.UserID, request.clientMessageID)
		if attachErr != nil {
			return messageWriteResult{}, attachErr
		}
		if tag.RowsAffected() != 1 {
			return messageWriteResult{}, errors.New("image candidate is unavailable")
		}
		err = tx.QueryRow(ctx, `INSERT INTO messages (message_id, room_id, seq, sender_user_id, sender_display_name, client_message_id, text, image_id)
            VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6::uuid, $7, $8::uuid) RETURNING created_at`,
			messageID, request.roomID, seq, request.principal.UserID, displayName, request.clientMessageID, request.text, request.image.id).Scan(&createdAt)
	}
	if err != nil {
		return messageWriteResult{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO message_outbox (outbox_id, message_id) VALUES ($1::uuid, $2::uuid)`, outboxID, messageID)
	if err != nil {
		return messageWriteResult{}, err
	}
	if request.image != nil && tag.RowsAffected() != 1 {
		return messageWriteResult{}, errors.New("outbox insertion failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return messageWriteResult{}, err
	}

	stored := chatmessage.Message{MessageID: messageID, RoomID: request.roomID, Seq: strconv.FormatInt(seq, 10),
		Sender:          chatmessage.Sender{UserID: request.principal.UserID, DisplayName: displayName},
		ClientMessageID: request.clientMessageID, Text: request.text, CreatedAt: createdAt.UTC().Format(time.RFC3339Nano)}
	if request.image != nil {
		processed := request.image.processed
		stored.Image = &chatmessage.Image{URL: "/api/v1/rooms/" + request.roomID + "/messages/" + messageID + "/image", ContentType: processed.ContentType, ByteSize: len(processed.Bytes), Width: processed.Width, Height: processed.Height}
	}
	return messageWriteResult{Message: stored}, nil
}

func writeMessageResponse(w http.ResponseWriter, r *http.Request, result messageWriteResult, err error) {
	switch {
	case errors.Is(err, errMessageUnauthenticated):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
	case errors.Is(err, errMessageAccountNotAllowed):
		writeError(w, r, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "Account is not allowed", false, nil)
	case errors.Is(err, errMessageRoomNotFound):
		writeRoomNotFound(w, r)
	case errors.Is(err, errMessageConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Request key already used with different content", false, nil)
	case err != nil:
		dependencyUnavailable(w, r)
	case result.Replayed:
		httpjson.Write(w, http.StatusOK, result)
	default:
		httpjson.Write(w, http.StatusCreated, result)
	}
}
