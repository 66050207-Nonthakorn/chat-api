// Package message defines the shared v1 message and event payloads.
package message

// CreatedType identifies an event for a committed message.
const CreatedType = "message.created"

// Message is the v1 HTTP and realtime message payload.
type Message struct {
	MessageID       string `json:"message_id"`
	RoomID          string `json:"room_id"`
	Seq             string `json:"seq"`
	Sender          Sender `json:"sender"`
	ClientMessageID string `json:"client_message_id"`
	Text            string `json:"text"`
	Image           *Image `json:"image,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type Image struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	ByteSize    int    `json:"byte_size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// Sender identifies the author recorded when a message was sent.
type Sender struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// CreatedEvent carries a message to realtime subscribers.
type CreatedEvent struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}
