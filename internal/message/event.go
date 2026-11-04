package message

// CreatedType identifies an event for a committed message.
const CreatedType = "message.created"

// CreatedEvent carries a message to realtime subscribers.
type CreatedEvent struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}

// EventChannel returns the single Pub/Sub channel shared by all replicas in
// one environment.
func EventChannel(environment string) string {
	return "cloudchat:" + environment + ":events"
}
