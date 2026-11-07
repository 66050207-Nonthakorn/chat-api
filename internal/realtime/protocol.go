package realtime

import (
	"strconv"

	chatmessage "cloudchat/internal/message"
	"cloudchat/internal/uuid"
)

type clientCommand struct {
	Type      string  `json:"type"`
	RequestID *string `json:"request_id"`
	RoomID    string  `json:"room_id,omitempty"`
}

type outboundFrame struct {
	payload []byte
	roomID  string
	private bool
	message bool
	value   any
}

func validMessage(m chatmessage.Message) bool {
	if !uuid.IsCanonical(m.MessageID) || !uuid.IsCanonical(m.RoomID) || !uuid.IsCanonical(m.Sender.UserID) || !uuid.IsCanonical(m.ClientMessageID) || (m.Text == "" && m.Image == nil) || m.CreatedAt == "" {
		return false
	}
	if image := m.Image; image != nil {
		if image.URL != "/api/v1/rooms/"+m.RoomID+"/messages/"+m.MessageID+"/image" || image.ByteSize < 1 || image.ByteSize > 5*1024*1024 || image.Width < 1 || image.Width > 2560 || image.Height < 1 || image.Height > 2560 || (image.ContentType != "image/jpeg" && image.ContentType != "image/png") {
			return false
		}
	}
	sequence, err := strconv.ParseInt(m.Seq, 10, 64)
	return err == nil && sequence > 0 && strconv.FormatInt(sequence, 10) == m.Seq
}

type protocolErrorFrame struct {
	Type      string        `json:"type"`
	RequestID *string       `json:"request_id"`
	Error     protocolError `json:"error"`
}

type protocolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
