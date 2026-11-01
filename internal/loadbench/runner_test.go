package loadbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/demo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TestRunMeasuresDeliveryForEverySubscribedClient exercises the harness
// through its public HTTP and WebSocket endpoints. The fake endpoint drops a
// notification for one subscribed client and repeats it on another so the
// resulting report proves partial delivery loss and duplicate-frame counting
// are visible to operators.
func TestRunMeasuresDeliveryForEverySubscribedClient(t *testing.T) {
	api := newPublicTestAPI(true)
	server := httptest.NewServer(api)
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL, GroupRoomID: demo.GroupRoomID, PrivateRoomID: demo.DirectRoomID,
		UserAID: demo.UserAID, UserBID: demo.UserBID,
		Connections: 4, MessagesPerSecond: 6, Duration: 350 * time.Millisecond,
		PayloadBytes: 32, GroupPercent: 50, GroupRoomSize: 2, PrivateRoomSize: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := Run(ctx, RunOptions{Workload: cfg, SettleDuration: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("run public load harness: %v", err)
	}

	if got.Workload.AcceptedMessages < 2 {
		t.Fatalf("accepted messages = %d, want at least two HTTP successes", got.Workload.AcceptedMessages)
	}
	if got.WebSockets.SubscribeAcks != cfg.Connections {
		t.Fatalf("subscribe acknowledgements = %d, want %d", got.WebSockets.SubscribeAcks, cfg.Connections)
	}
	if got.Traffic.LiveMissingClientDeliveries != 1 {
		t.Fatalf("missing client deliveries = %d, want the one deliberately dropped group frame", got.Traffic.LiveMissingClientDeliveries)
	}
	if got.Traffic.LiveMissingMessages != 0 {
		t.Fatalf("fully missing messages = %d, want zero because another room client received the dropped message", got.Traffic.LiveMissingMessages)
	}
	if got.Traffic.DuplicateFrames != 1 {
		t.Fatalf("duplicate frames = %d, want one repeated frame on a single socket", got.Traffic.DuplicateFrames)
	}
	if got.Traffic.LiveClientDeliveries+got.Traffic.LiveMissingClientDeliveries != got.Workload.AcceptedMessages*2 {
		t.Fatalf("observed plus missing client deliveries = %d + %d for %d accepted messages; want two expected recipients per message", got.Traffic.LiveClientDeliveries, got.Traffic.LiveMissingClientDeliveries, got.Workload.AcceptedMessages)
	}
	if got.LiveDelivery.Count != got.Traffic.LiveClientDeliveries || got.LiveDelivery.Count <= got.Workload.AcceptedMessages {
		t.Fatalf("client delivery latency sample count = %d for %d client deliveries and %d messages", got.LiveDelivery.Count, got.Traffic.LiveClientDeliveries, got.Workload.AcceptedMessages)
	}
	if got.Traffic.RecoveredBySync != 0 || got.Traffic.NotFoundBySync != 0 {
		t.Fatalf("HTTP sync results = recovered %d, not found %d; partial delivery must remain durable and all accepted IDs should sync", got.Traffic.RecoveredBySync, got.Traffic.NotFoundBySync)
	}
}

type publicTestMessage struct {
	MessageID       string `json:"message_id"`
	RoomID          string `json:"room_id"`
	ClientMessageID string `json:"client_message_id"`
	Seq             string `json:"seq"`
}

type publicSubscribeCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id"`
}

type publicTestSocket struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

type publicTestAPI struct {
	mu            sync.Mutex
	messages      map[string][]publicTestMessage
	sockets       map[string][]*publicTestSocket
	injectFault   bool
	faultInjected bool
}

func newPublicTestAPI(injectFault bool) *publicTestAPI {
	return &publicTestAPI{
		messages:    make(map[string][]publicTestMessage),
		sockets:     make(map[string][]*publicTestSocket),
		injectFault: injectFault,
	}
}

func (api *publicTestAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "rooms" && parts[4] == "sync" && r.Method == http.MethodGet {
		api.sync(w, r, parts[3])
		return
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "rooms" && parts[4] == "messages" && r.Method == http.MethodPost {
		api.createMessage(w, r, parts[3])
		return
	}
	if r.URL.Path == "/api/v1/ws" && r.Method == http.MethodGet {
		api.webSocket(w, r)
		return
	}
	http.NotFound(w, r)
}

func (api *publicTestAPI) sync(w http.ResponseWriter, r *http.Request, roomID string) {
	api.mu.Lock()
	items := append([]publicTestMessage(nil), api.messages[roomID]...)
	api.mu.Unlock()
	through := "0"
	if len(items) > 0 {
		through = items[len(items)-1].Seq
	}
	writePublicJSON(w, http.StatusOK, map[string]any{
		"items": items, "through_seq": through, "next_after_seq": through, "has_more": false,
	})
}

func (api *publicTestAPI) createMessage(w http.ResponseWriter, r *http.Request, roomID string) {
	var request struct {
		ClientMessageID string `json:"client_message_id"`
		Text            string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ClientMessageID == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	api.mu.Lock()
	item := publicTestMessage{
		MessageID: fmt.Sprintf("message-%d-%s", len(api.messages[roomID])+1, request.ClientMessageID),
		RoomID:    roomID, ClientMessageID: request.ClientMessageID,
		Seq: fmt.Sprint(len(api.messages[roomID]) + 1),
	}
	api.messages[roomID] = append(api.messages[roomID], item)
	targets := append([]*publicTestSocket(nil), api.sockets[roomID]...)
	dropID, duplicateID := -1, -1
	if api.injectFault && !api.faultInjected && roomID == demo.GroupRoomID && len(targets) >= 2 {
		api.faultInjected = true
		dropID, duplicateID = 0, 1
	}
	api.mu.Unlock()

	event := map[string]any{
		"type": "message.created",
		"message": map[string]string{
			"message_id": item.MessageID, "room_id": item.RoomID,
			"client_message_id": item.ClientMessageID,
		},
	}
	for index, target := range targets {
		if index == dropID {
			continue
		}
		if err := target.write(r.Context(), event); err != nil {
			continue
		}
		if index == duplicateID {
			_ = target.write(r.Context(), event)
		}
	}
	writePublicJSON(w, http.StatusCreated, map[string]any{"message": item})
}

func (api *publicTestAPI) webSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"chat.v1"}})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "test socket closed")
	var command publicSubscribeCommand
	if err := wsjson.Read(r.Context(), conn, &command); err != nil || command.Type != "subscribe" || command.RoomID == "" {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid subscribe")
		return
	}
	socket := &publicTestSocket{conn: conn}
	api.mu.Lock()
	api.sockets[command.RoomID] = append(api.sockets[command.RoomID], socket)
	api.mu.Unlock()
	if err := wsjson.Write(r.Context(), conn, map[string]string{
		"type": "subscribed", "request_id": command.RequestID, "room_id": command.RoomID,
	}); err != nil {
		return
	}
	for {
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
	}
}

func (socket *publicTestSocket) write(ctx context.Context, value any) error {
	socket.writeMu.Lock()
	defer socket.writeMu.Unlock()
	return wsjson.Write(ctx, socket.conn, value)
}

func writePublicJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
