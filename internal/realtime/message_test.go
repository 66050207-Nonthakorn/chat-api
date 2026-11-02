package realtime

import (
	"testing"

	"cloudchat/internal/message"
)

func TestValidMessage(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	valid := message.Message{MessageID: id, RoomID: id, Seq: "1", Sender: message.Sender{UserID: id}, ClientMessageID: id, Text: "hello", CreatedAt: "time"}
	if !validMessage(valid) {
		t.Fatal("expected valid message")
	}
	for _, seq := range []string{"0", "01", "-1", "not-a-number"} {
		invalid := valid
		invalid.Seq = seq
		if validMessage(invalid) {
			t.Errorf("accepted sequence %q", seq)
		}
	}
	invalid := valid
	invalid.Sender.UserID = "invalid"
	if validMessage(invalid) {
		t.Error("accepted invalid sender ID")
	}
}
