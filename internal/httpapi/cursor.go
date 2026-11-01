package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"cloudchat/internal/uuid"
)

type roomCursor struct {
	Version int    `json:"v"`
	UserID  string `json:"u"`
	RoomID  string `json:"r"`
}

func (h *handler) encodeRoomCursor(userID, roomID string) (string, error) {
	payload, err := json.Marshal(roomCursor{Version: 1, UserID: userID, RoomID: roomID})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(h.config.CursorHMACKey))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (h *handler) decodeRoomCursor(token, userID string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("invalid cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, []byte(h.config.CursorHMACKey))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", errors.New("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("invalid cursor")
	}
	var cursor roomCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.UserID != userID || !uuid.IsCanonical(cursor.RoomID) {
		return "", errors.New("invalid cursor")
	}
	return cursor.RoomID, nil
}
