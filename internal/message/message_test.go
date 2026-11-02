package message

import (
	"encoding/json"
	"testing"
)

func TestCreatedEventJSONContract(t *testing.T) {
	event := CreatedEvent{Type: CreatedType, Message: Message{
		MessageID: "message", RoomID: "room", Seq: "42",
		Sender:          Sender{UserID: "user", DisplayName: "Name"},
		ClientMessageID: "client", Text: "hello", CreatedAt: "time",
	}}
	got, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"message.created","message":{"message_id":"message","room_id":"room","seq":"42","sender":{"user_id":"user","display_name":"Name"},"client_message_id":"client","text":"hello","created_at":"time"}}`
	if string(got) != want {
		t.Fatalf("event JSON = %s, want %s", got, want)
	}
}
