package loadbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloudchat/internal/demo"
	"cloudchat/internal/uuid"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	localIdentityHeader = "X-Local-Test-User-ID"
	webSocketProtocol   = "chat.v1"
)

// RunOptions configures the public API load run and optional local Docker
// observations. Docker observation is read-only and scoped to the named
// Compose project.
type RunOptions struct {
	Workload        Config
	OutputPath      string
	ComposeFile     string
	ComposeProject  string
	FaultWindowFile string
	ReadyFile       string
	SettleDuration  time.Duration
}

// Report contains workload settings, measured client behavior, optional
// container samples, and sync evidence. It intentionally makes no capacity
// or SLA claim.
type Report struct {
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  time.Time       `json:"completed_at"`
	Environment  string          `json:"environment"`
	Workload     WorkloadReport  `json:"workload"`
	Acceptance   Summary         `json:"acceptance_latency"`
	LiveDelivery Summary         `json:"submit_to_subscribed_client_live_frame_latency"`
	Traffic      TrafficReport   `json:"traffic"`
	WebSockets   WebSocketReport `json:"websockets"`
	Errors       ErrorReport     `json:"errors"`
	Recovery     RecoveryReport  `json:"recovery"`
	Resources    ResourceReport  `json:"resources"`
	Limitations  []string        `json:"limitations"`
}

type WorkloadReport struct {
	TargetConnections         int     `json:"target_connections"`
	TargetMessagesPerSecond   int     `json:"target_messages_per_second"`
	ConfiguredDuration        string  `json:"configured_duration"`
	ConnectionRamp            string  `json:"connection_ramp"`
	PayloadBytes              int     `json:"payload_bytes"`
	TrafficMix                string  `json:"traffic_mix"`
	GroupRoomSize             int     `json:"group_room_size"`
	PrivateRoomSize           int     `json:"private_room_size"`
	ObservedConnections       int     `json:"observed_connections"`
	AcceptedMessages          int     `json:"accepted_messages"`
	AcceptedMessagesPerSecond float64 `json:"accepted_messages_per_second"`
	AttemptedMessages         int     `json:"attempted_messages"`
	Elapsed                   string  `json:"elapsed"`
}

type TrafficReport struct {
	GroupMessages               int `json:"group_messages"`
	PrivateMessages             int `json:"private_messages"`
	LiveUniqueMessages          int `json:"live_unique_messages"`
	LiveMissingMessages         int `json:"live_missing_messages"`
	LiveClientDeliveries        int `json:"live_client_deliveries"`
	LiveMissingClientDeliveries int `json:"live_missing_client_deliveries"`
	DuplicateFrames             int `json:"duplicate_frames"`
	RecoveredBySync             int `json:"recovered_by_sync"`
	NotFoundBySync              int `json:"not_found_by_sync"`
}

type WebSocketReport struct {
	SubscribeAcks      int            `json:"subscribe_acks"`
	Disconnects        int64          `json:"disconnects"`
	Reconnects         int64          `json:"reconnects"`
	DialFailures       int64          `json:"dial_failures"`
	DialFailureReasons map[string]int `json:"dial_failure_reasons"`
	ReadFailures       int64          `json:"read_failures"`
}

type ErrorReport struct {
	HTTPStatusCounts  map[string]int `json:"http_status_counts"`
	SyncErrors        int            `json:"sync_errors"`
	DockerProbeErrors int            `json:"docker_probe_errors"`
}

type RecoveryReport struct {
	FaultService            string     `json:"fault_service,omitempty"`
	FaultStartedAt          *time.Time `json:"fault_started_at,omitempty"`
	FaultEndedAt            *time.Time `json:"fault_ended_at,omitempty"`
	OutboxPendingPeak       int        `json:"outbox_pending_peak"`
	OutboxPendingFinal      int        `json:"outbox_pending_final"`
	OutboxOldestAgePeakSec  float64    `json:"outbox_oldest_age_peak_seconds"`
	OutboxRecoverySeconds   *float64   `json:"outbox_recovery_seconds,omitempty"`
	DeliveryRecoverySeconds *float64   `json:"delivery_recovery_seconds,omitempty"`
	SyncRecoverySeconds     *float64   `json:"sync_recovery_seconds,omitempty"`
}

type ResourceReport struct {
	SamplingInterval string           `json:"sampling_interval"`
	Samples          []ResourceSample `json:"samples"`
	ProbeErrors      int              `json:"probe_errors"`
	LastProbeError   string           `json:"last_probe_error,omitempty"`
}

type ResourceSample struct {
	At            time.Time `json:"at"`
	Container     string    `json:"container"`
	CPUPercent    string    `json:"cpu_percent"`
	Memory        string    `json:"memory"`
	MemoryPercent string    `json:"memory_percent"`
}

type backlogSample struct {
	At               time.Time
	Pending          int
	OldestAgeSeconds float64
}

type faultWindow struct {
	Service   string    `json:"service"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

type sendRecord struct {
	ClientMessageID string
	RoomID          string
	StartedAt       time.Time
	AcceptedAt      time.Time
	MessageID       string
	ByClient        map[int]int
	FirstDeliveryAt map[int]time.Time
}

type tracker struct {
	mu                 sync.Mutex
	records            map[string]*sendRecord
	httpStatuses       map[string]int
	dialFailureReasons map[string]int
	expectedClients    map[string][]int
	acceptance         []time.Duration
	groupAccepted      int
	privateAccepted    int
	duplicates         int
	wsAcks             int
	syncErrors         int
}

type socketMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id"`
	Message   struct {
		MessageID       string `json:"message_id"`
		RoomID          string `json:"room_id"`
		ClientMessageID string `json:"client_message_id"`
	} `json:"message"`
}

type clientCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id,omitempty"`
}

type syncResponse struct {
	Items []struct {
		MessageID string `json:"message_id"`
		Seq       string `json:"seq"`
	} `json:"items"`
	ThroughSeq   string `json:"through_seq"`
	NextAfterSeq string `json:"next_after_seq"`
	HasMore      bool   `json:"has_more"`
}

type sendResponse struct {
	Message struct {
		MessageID       string `json:"message_id"`
		RoomID          string `json:"room_id"`
		ClientMessageID string `json:"client_message_id"`
		CreatedAt       string `json:"created_at"`
	} `json:"message"`
}

type apiError struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

type socketClient struct {
	id           int
	userID       string
	roomID       string
	baseURL      *url.URL
	http         *http.Client
	tracker      *tracker
	ready        chan struct{}
	readyOnce    sync.Once
	disconnects  atomic.Int64
	reconnects   atomic.Int64
	dialFailures atomic.Int64
	readFailures atomic.Int64
}

type resourceSampler struct {
	project        string
	compose        string
	mu             sync.Mutex
	resources      []ResourceSample
	backlog        []backlogSample
	probeErrors    int
	lastProbeError string
}

// Run drives public HTTP and WebSocket endpoints, then reconciles accepted
// messages against the public sync endpoint. It does not inspect application
// memory or call private server methods.
func Run(ctx context.Context, options RunOptions) (Report, error) {
	cfg := options.Workload
	if err := cfg.Validate(); err != nil {
		return Report{}, err
	}
	baseURL, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" || (baseURL.Path != "" && baseURL.Path != "/") || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return Report{}, errors.New("base URL must be an absolute HTTP or HTTPS origin")
	}
	if options.SettleDuration <= 0 {
		options.SettleDuration = 3 * time.Second
	}
	if options.ComposeProject != "" && options.ComposeFile == "" {
		return Report{}, errors.New("a Compose file is required when Docker sampling is enabled")
	}
	if options.ComposeProject != "" && options.ComposeProject != "chat-ticket08-benchmark" {
		return Report{}, errors.New("Docker sampling is restricted to project chat-ticket08-benchmark")
	}
	if options.ComposeFile != "" {
		absolute, err := filepath.Abs(options.ComposeFile)
		if err != nil {
			return Report{}, fmt.Errorf("resolve Compose file: %w", err)
		}
		options.ComposeFile = absolute
	}

	benchmarkStart := time.Now()
	startedAt := benchmarkStart.UTC()
	tracker := &tracker{records: make(map[string]*sendRecord), httpStatuses: make(map[string]int), dialFailureReasons: make(map[string]int), expectedClients: make(map[string][]int)}
	client := &http.Client{Timeout: 15 * time.Second}
	baseline, err := readBaselines(ctx, client, baseURL, cfg)
	if err != nil {
		return Report{}, fmt.Errorf("read initial sync snapshot: %w", err)
	}
	clients := make([]*socketClient, cfg.Connections)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var clientWG sync.WaitGroup
	for index := range clients {
		userID := cfg.UserAID
		if index%2 != 0 {
			userID = cfg.UserBID
		}
		roomID := cfg.PrivateRoomID
		if index < cfg.Connections/2 {
			roomID = cfg.GroupRoomID
		}
		clients[index] = &socketClient{id: index, userID: userID, roomID: roomID, baseURL: baseURL, http: client, tracker: tracker, ready: make(chan struct{})}
		tracker.expectedClients[roomID] = append(tracker.expectedClients[roomID], index)
		clientWG.Add(1)
		go func(c *socketClient) {
			defer clientWG.Done()
			c.run(runCtx)
		}(clients[index])
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 45*time.Second)
	defer readyCancel()
	for _, socket := range clients {
		select {
		case <-socket.ready:
		case <-readyCtx.Done():
			cancel()
			clientWG.Wait()
			return Report{}, fmt.Errorf("WebSocket clients did not all subscribe: %w", readyCtx.Err())
		}
	}
	if options.ReadyFile != "" {
		if err := os.MkdirAll(filepath.Dir(options.ReadyFile), 0o755); err != nil {
			cancel()
			clientWG.Wait()
			return Report{}, fmt.Errorf("create load-ready marker directory: %w", err)
		}
		if err := os.WriteFile(options.ReadyFile, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
			cancel()
			clientWG.Wait()
			return Report{}, fmt.Errorf("write load-ready marker: %w", err)
		}
	}

	sampler := (*resourceSampler)(nil)
	var samplerCancel context.CancelFunc
	var samplerWG sync.WaitGroup
	if options.ComposeProject != "" {
		sampler = &resourceSampler{project: options.ComposeProject, compose: options.ComposeFile}
		sampleCtx, stopSampling := context.WithCancel(runCtx)
		samplerCancel = stopSampling
		samplerWG.Add(1)
		go func() {
			defer samplerWG.Done()
			sampler.run(sampleCtx)
		}()
	}

	durationStart := time.Now()
	connectionRamp := durationStart.Sub(benchmarkStart)
	deadline := durationStart.Add(cfg.Duration)
	interval := time.Second / time.Duration(cfg.MessagesPerSecond)
	if interval < time.Nanosecond {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	var sendWG sync.WaitGroup
	attempted := 0
	launchMessage := func(index int) {
		roomID := cfg.PrivateRoomID
		if index%100 < cfg.GroupPercent {
			roomID = cfg.GroupRoomID
		}
		userID := cfg.UserAID
		if index%2 != 0 {
			userID = cfg.UserBID
		}
		sendWG.Add(1)
		go func() {
			defer sendWG.Done()
			sendMessage(runCtx, client, baseURL, tracker, roomID, userID, cfg.PayloadBytes)
		}()
	}
	launchMessage(attempted)
	attempted++
	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			cancel()
			clientWG.Wait()
			if samplerCancel != nil {
				samplerCancel()
				samplerWG.Wait()
			}
			return Report{}, ctx.Err()
		case tick := <-ticker.C:
			if !tick.Before(deadline) {
				ticker.Stop()
				goto sent
			}
			launchMessage(attempted)
			attempted++
		}
	}

sent:
	sendWG.Wait()
	workloadElapsed := time.Since(durationStart)
	time.Sleep(options.SettleDuration)

	synced, syncErrors, syncErr := reconcile(ctx, client, baseURL, cfg, baseline, tracker)
	syncEnd := time.Now()
	if sampler != nil {
		waitForOutboxDrain(ctx, sampler, 20*time.Second)
	}
	if samplerCancel != nil {
		samplerCancel()
		samplerWG.Wait()
	}
	completedAt := time.Now().UTC()
	cancel()
	clientWG.Wait()

	report := buildReport(startedAt, completedAt, workloadElapsed, connectionRamp, cfg, attempted, tracker, clients, synced, syncErrors, sampler)
	if syncErr != nil {
		report.Limitations = append(report.Limitations, "Final public HTTP sync encountered an error; inspect sync_errors and not_found_by_sync.")
	}
	if options.FaultWindowFile != "" {
		window, err := readFaultWindow(options.FaultWindowFile)
		if err != nil {
			report.Limitations = append(report.Limitations, "Fault-window evidence could not be loaded: "+err.Error())
		} else {
			applyFaultWindow(&report, tracker, window, synced, syncEnd, sampler)
		}
	}
	if report.Traffic.LiveMissingMessages > 0 {
		report.Limitations = append(report.Limitations, "At least one accepted message was not observed on an active live socket; see HTTP sync recovery counts.")
	}
	if report.Workload.AcceptedMessagesPerSecond < float64(cfg.MessagesPerSecond)*0.95 {
		report.Limitations = append(report.Limitations, "Measured accepted throughput was below 95% of the configured target; no target-capacity claim is made.")
	}
	if options.OutputPath != "" {
		if err := writeReport(options.OutputPath, report); err != nil {
			return report, fmt.Errorf("write report: %w", err)
		}
	}
	return report, syncErr
}

func (c *socketClient) run(ctx context.Context) {
	wsURL := *c.baseURL
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path = "/api/v1/ws"
	wsURL.RawQuery = ""
	wsURL.Fragment = ""
	header := http.Header{}
	header.Set(localIdentityHeader, c.userID)
	header.Set("Origin", c.baseURL.Scheme+"://"+c.baseURL.Host)
	delay := 250 * time.Millisecond
	connectedOnce := false
	for ctx.Err() == nil {
		connection, response, err := websocket.Dial(ctx, wsURL.String(), &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{webSocketProtocol}})
		if err != nil {
			c.dialFailures.Add(1)
			reason := "transport_error"
			if response != nil && response.Body != nil {
				reason = fmt.Sprintf("http_%d", response.StatusCode)
				_ = response.Body.Close()
			}
			c.tracker.countDialFailure(reason)
			if !waitContext(ctx, delay) {
				return
			}
			delay = min(delay*2, 3*time.Second)
			continue
		}
		if err = wsjson.Write(ctx, connection, clientCommand{Type: "subscribe", RequestID: mustUUID(), RoomID: c.roomID}); err != nil {
			_ = connection.Close(websocket.StatusGoingAway, "subscribe failed")
			c.readFailures.Add(1)
			continue
		}
		_, raw, err := connection.Read(ctx)
		if err != nil {
			_ = connection.Close(websocket.StatusGoingAway, "subscribe acknowledgement missing")
			c.readFailures.Add(1)
			if !waitContext(ctx, delay) {
				return
			}
			continue
		}
		var ack socketMessage
		if json.Unmarshal(raw, &ack) != nil || ack.Type != "subscribed" || ack.RoomID != c.roomID {
			_ = connection.Close(websocket.StatusPolicyViolation, "invalid subscribe acknowledgement")
			c.readFailures.Add(1)
			if !waitContext(ctx, delay) {
				return
			}
			continue
		}
		c.tracker.mu.Lock()
		c.tracker.wsAcks++
		c.tracker.mu.Unlock()
		c.readyOnce.Do(func() { close(c.ready) })
		if connectedOnce {
			c.reconnects.Add(1)
		}
		connectedOnce = true
		c.pump(ctx, connection)
		_ = connection.Close(websocket.StatusGoingAway, "reconnecting")
		if ctx.Err() != nil {
			return
		}
		c.disconnects.Add(1)
		if !waitContext(ctx, delay) {
			return
		}
		delay = min(delay*2, 3*time.Second)
	}
}

func (c *socketClient) pump(ctx context.Context, connection *websocket.Conn) {
	pumpCtx, stopPings := context.WithCancel(ctx)
	defer stopPings()
	var writeMu sync.Mutex
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pumpCtx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				err := wsjson.Write(pumpCtx, connection, clientCommand{Type: "ping", RequestID: mustUUID()})
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
	defer func() {
		stopPings()
		<-pingDone
	}()
	for {
		_, raw, err := connection.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.readFailures.Add(1)
			}
			return
		}
		var event socketMessage
		if json.Unmarshal(raw, &event) != nil {
			continue
		}
		if event.Type == "message.created" && event.Message.ClientMessageID != "" {
			c.tracker.messageEvent(c.id, event.Message.ClientMessageID)
		}
	}
}

func (t *tracker) begin(record *sendRecord) {
	t.mu.Lock()
	t.records[record.ClientMessageID] = record
	t.mu.Unlock()
}

func (t *tracker) messageEvent(clientID int, clientMessageID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record := t.records[clientMessageID]
	if record == nil {
		return
	}
	now := time.Now()
	if record.ByClient == nil {
		record.ByClient = make(map[int]int)
	}
	record.ByClient[clientID]++
	if record.FirstDeliveryAt == nil {
		record.FirstDeliveryAt = make(map[int]time.Time)
	}
	if _, received := record.FirstDeliveryAt[clientID]; !received {
		record.FirstDeliveryAt[clientID] = now
	}
	if record.ByClient[clientID] > 1 {
		t.duplicates++
	}
}

func sendMessage(ctx context.Context, client *http.Client, baseURL *url.URL, tracker *tracker, roomID, userID string, payloadBytes int) {
	clientMessageID := mustUUID()
	startedAt := time.Now()
	record := &sendRecord{ClientMessageID: clientMessageID, RoomID: roomID, StartedAt: startedAt}
	tracker.begin(record)
	body, _ := json.Marshal(struct {
		ClientMessageID string `json:"client_message_id"`
		Text            string `json:"text"`
	}{clientMessageID, strings.Repeat("x", payloadBytes)})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL.String()+"/api/v1/rooms/"+roomID+"/messages", strings.NewReader(string(body)))
	if err != nil {
		tracker.countHTTP("request_error")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localIdentityHeader, userID)
	response, err := client.Do(request)
	if err != nil {
		tracker.countHTTP("transport_error")
		return
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		code := ""
		var decoded apiError
		if json.Unmarshal(responseBody, &decoded) == nil {
			code = decoded.Error.Code
		}
		key := fmt.Sprintf("%d", response.StatusCode)
		if code != "" {
			key += "_" + code
		}
		tracker.countHTTP(key)
		return
	}
	var created sendResponse
	if err := json.Unmarshal(responseBody, &created); err != nil || created.Message.MessageID == "" {
		tracker.countHTTP("invalid_success_response")
		return
	}
	acceptedAt := time.Now()
	tracker.mu.Lock()
	record.AcceptedAt = acceptedAt
	record.MessageID = created.Message.MessageID
	tracker.acceptance = append(tracker.acceptance, acceptedAt.Sub(startedAt))
	if roomID == demo.GroupRoomID {
		tracker.groupAccepted++
	} else {
		tracker.privateAccepted++
	}
	tracker.mu.Unlock()
}

func (t *tracker) countHTTP(status string) {
	t.mu.Lock()
	t.httpStatuses[status]++
	t.mu.Unlock()
}

func (t *tracker) countDialFailure(reason string) {
	t.mu.Lock()
	t.dialFailureReasons[reason]++
	t.mu.Unlock()
}

func readBaselines(ctx context.Context, client *http.Client, baseURL *url.URL, cfg Config) (map[string]string, error) {
	baselines := make(map[string]string, 2)
	for _, roomID := range []string{cfg.GroupRoomID, cfg.PrivateRoomID} {
		response, err := getSync(ctx, client, baseURL, cfg.UserAID, roomID, "0", "", 1)
		if err != nil {
			return nil, err
		}
		baselines[roomID] = response.ThroughSeq
	}
	return baselines, nil
}

func reconcile(ctx context.Context, client *http.Client, baseURL *url.URL, cfg Config, baseline map[string]string, tracker *tracker) (map[string]map[string]struct{}, int, error) {
	result := make(map[string]map[string]struct{}, 2)
	var syncErrors int
	for _, roomID := range []string{cfg.GroupRoomID, cfg.PrivateRoomID} {
		cursor := baseline[roomID]
		through := ""
		roomMessages := make(map[string]struct{})
		for {
			page, err := getSync(ctx, client, baseURL, cfg.UserAID, roomID, cursor, through, 200)
			if err != nil {
				syncErrors++
				tracker.mu.Lock()
				tracker.syncErrors++
				tracker.mu.Unlock()
				return result, syncErrors, fmt.Errorf("sync room %s after %s: %w", roomID, cursor, err)
			}
			through = page.ThroughSeq
			for _, message := range page.Items {
				roomMessages[message.MessageID] = struct{}{}
			}
			if !page.HasMore {
				break
			}
			if page.NextAfterSeq == cursor {
				syncErrors++
				tracker.mu.Lock()
				tracker.syncErrors++
				tracker.mu.Unlock()
				return result, syncErrors, fmt.Errorf("sync cursor did not advance for room %s", roomID)
			}
			cursor = page.NextAfterSeq
		}
		result[roomID] = roomMessages
	}
	return result, syncErrors, nil
}

func getSync(ctx context.Context, client *http.Client, baseURL *url.URL, userID, roomID, after, through string, limit int) (syncResponse, error) {
	path := fmt.Sprintf("%s/api/v1/rooms/%s/sync?after_seq=%s&limit=%d", baseURL.String(), roomID, url.QueryEscape(after), limit)
	if through != "" {
		path += "&through_seq=" + url.QueryEscape(through)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return syncResponse{}, err
	}
	request.Header.Set(localIdentityHeader, userID)
	response, err := client.Do(request)
	if err != nil {
		return syncResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return syncResponse{}, fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded syncResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return syncResponse{}, err
	}
	if decoded.ThroughSeq == "" {
		return syncResponse{}, errors.New("sync response has no through_seq")
	}
	return decoded, nil
}

func buildReport(start, completed time.Time, elapsed, connectionRamp time.Duration, cfg Config, attempted int, tracker *tracker, clients []*socketClient, synced map[string]map[string]struct{}, syncErrors int, sampler *resourceSampler) Report {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	report := Report{
		StartedAt:   start,
		CompletedAt: completed,
		Environment: "local isolated Docker Compose; public HTTP/WebSocket clients; PostgreSQL and Valkey",
		Workload: WorkloadReport{
			TargetConnections:       cfg.Connections,
			TargetMessagesPerSecond: cfg.MessagesPerSecond,
			ConfiguredDuration:      cfg.Duration.String(),
			ConnectionRamp:          connectionRamp.String(),
			PayloadBytes:            cfg.PayloadBytes,
			TrafficMix:              fmt.Sprintf("%d%% group / %d%% private", cfg.GroupPercent, 100-cfg.GroupPercent),
			GroupRoomSize:           cfg.GroupRoomSize,
			PrivateRoomSize:         cfg.PrivateRoomSize,
			ObservedConnections:     len(clients),
			AttemptedMessages:       attempted,
			Elapsed:                 elapsed.String(),
		},
		WebSockets: WebSocketReport{SubscribeAcks: tracker.wsAcks, DialFailureReasons: cloneMap(tracker.dialFailureReasons)},
		Errors:     ErrorReport{HTTPStatusCounts: cloneMap(tracker.httpStatuses), SyncErrors: syncErrors},
		Traffic:    TrafficReport{GroupMessages: tracker.groupAccepted, PrivateMessages: tracker.privateAccepted, DuplicateFrames: tracker.duplicates},
		Limitations: []string{
			"This result is local-container evidence only; AWS RDS failover and ECS autoscaling were not exercised.",
			"The harness's reconnect loop is benchmark-client behavior and is not a claim about the separately owned frontend implementation.",
			"No stretch-target capacity is implied by this run.",
		},
	}
	accepted := 0
	var acceptance []time.Duration
	var delivery []time.Duration
	for _, record := range tracker.records {
		if record.AcceptedAt.IsZero() {
			continue
		}
		accepted++
		if len(record.FirstDeliveryAt) > 0 {
			report.Traffic.LiveUniqueMessages++
		} else {
			report.Traffic.LiveMissingMessages++
		}
		for _, clientID := range tracker.expectedClients[record.RoomID] {
			if deliveredAt, found := record.FirstDeliveryAt[clientID]; found {
				delivery = append(delivery, deliveredAt.Sub(record.StartedAt))
				report.Traffic.LiveClientDeliveries++
			} else {
				report.Traffic.LiveMissingClientDeliveries++
			}
		}
		if _, found := synced[record.RoomID][record.MessageID]; found {
			if len(record.FirstDeliveryAt) == 0 {
				report.Traffic.RecoveredBySync++
			}
		} else {
			report.Traffic.NotFoundBySync++
		}
	}
	acceptance = append(acceptance, tracker.acceptance...)
	report.Workload.AcceptedMessages = accepted
	if elapsed > 0 {
		report.Workload.AcceptedMessagesPerSecond = float64(accepted) / elapsed.Seconds()
	}
	report.Acceptance = Summarize(acceptance)
	report.LiveDelivery = Summarize(delivery)
	for _, client := range clients {
		report.WebSockets.Disconnects += client.disconnects.Load()
		report.WebSockets.Reconnects += client.reconnects.Load()
		report.WebSockets.DialFailures += client.dialFailures.Load()
		report.WebSockets.ReadFailures += client.readFailures.Load()
	}
	if sampler != nil {
		resources, backlog, probeErrors, lastProbeError := sampler.snapshot()
		interval := observedSampleInterval(backlog)
		report.Resources = ResourceReport{SamplingInterval: interval, Samples: resources, ProbeErrors: probeErrors, LastProbeError: lastProbeError}
		report.Errors.DockerProbeErrors = probeErrors
		for _, sample := range backlog {
			if sample.Pending > report.Recovery.OutboxPendingPeak {
				report.Recovery.OutboxPendingPeak = sample.Pending
			}
			if sample.OldestAgeSeconds > report.Recovery.OutboxOldestAgePeakSec {
				report.Recovery.OutboxOldestAgePeakSec = sample.OldestAgeSeconds
			}
			report.Recovery.OutboxPendingFinal = sample.Pending
		}
	}
	return report
}

func applyFaultWindow(report *Report, tracker *tracker, fault faultWindow, synced map[string]map[string]struct{}, syncEnd time.Time, sampler *resourceSampler) {
	report.Recovery.FaultService = fault.Service
	start, end := fault.StartedAt.UTC(), fault.EndedAt.UTC()
	report.Recovery.FaultStartedAt = &start
	report.Recovery.FaultEndedAt = &end
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	var duringFault []time.Time
	var missedBeforeOrDuringFault bool
	for _, record := range tracker.records {
		if record.AcceptedAt.IsZero() {
			continue
		}
		if !record.StartedAt.Before(fault.StartedAt) && !record.StartedAt.After(fault.EndedAt) {
			for _, clientID := range tracker.expectedClients[record.RoomID] {
				if deliveredAt, found := record.FirstDeliveryAt[clientID]; found {
					if deliveredAt.After(fault.EndedAt) {
						duringFault = append(duringFault, deliveredAt)
					}
				} else {
					missedBeforeOrDuringFault = true
				}
			}
		}
		if !record.StartedAt.After(fault.EndedAt) {
			if _, recovered := synced[record.RoomID][record.MessageID]; recovered {
				for _, clientID := range tracker.expectedClients[record.RoomID] {
					if _, found := record.FirstDeliveryAt[clientID]; !found {
						missedBeforeOrDuringFault = true
						break
					}
				}
			}
		}
	}
	if len(duringFault) > 0 {
		last := end
		for _, deliveryAt := range duringFault {
			if deliveryAt.After(last) {
				last = deliveryAt
			}
		}
		duration := last.Sub(end).Seconds()
		if duration < 0 {
			duration = 0
		}
		report.Recovery.DeliveryRecoverySeconds = &duration
	}
	if missedBeforeOrDuringFault {
		duration := syncEnd.Sub(end).Seconds()
		if duration < 0 {
			duration = 0
		}
		report.Recovery.SyncRecoverySeconds = &duration
	}
	if sampler != nil && (fault.Service == "worker" || fault.Service == "valkey") {
		_, backlog, _, _ := sampler.snapshot()
		observedPendingDuringFault := false
		for _, sample := range backlog {
			if !sample.At.Before(fault.StartedAt) && !sample.At.After(fault.EndedAt) && sample.Pending > 0 {
				observedPendingDuringFault = true
			}
		}
		for _, sample := range backlog {
			if observedPendingDuringFault && !sample.At.Before(fault.EndedAt) && sample.Pending == 0 {
				duration := sample.At.Sub(fault.EndedAt).Seconds()
				if duration < 0 {
					duration = 0
				}
				report.Recovery.OutboxRecoverySeconds = &duration
				break
			}
		}
	}
}

func readFaultWindow(path string) (faultWindow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return faultWindow{}, err
	}
	var window faultWindow
	if err := json.Unmarshal(data, &window); err != nil {
		return faultWindow{}, err
	}
	if window.Service == "" || window.StartedAt.IsZero() || window.EndedAt.IsZero() || window.EndedAt.Before(window.StartedAt) {
		return faultWindow{}, errors.New("fault window must contain service and ordered UTC timestamps")
	}
	return window, nil
}

func (s *resourceSampler) run(ctx context.Context) {
	s.sample(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

func (s *resourceSampler) sample(ctx context.Context) {
	now := time.Now().UTC()
	idsOutput, err := s.composeCommand(ctx, "ps", "-q")
	if err == nil {
		ids := strings.Fields(string(idsOutput))
		if len(ids) > 0 {
			args := []string{"stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}"}
			args = append(args, ids...)
			output, statsErr := exec.CommandContext(ctx, "docker", args...).Output()
			if statsErr != nil {
				err = statsErr
			} else {
				for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
					parts := strings.Split(line, "|")
					if len(parts) == 4 {
						s.mu.Lock()
						s.resources = append(s.resources, ResourceSample{At: now, Container: strings.TrimSpace(parts[0]), CPUPercent: strings.TrimSpace(parts[1]), Memory: strings.TrimSpace(parts[2]), MemoryPercent: strings.TrimSpace(parts[3])})
						s.mu.Unlock()
					}
				}
			}
		}
	}
	if err != nil && ctx.Err() == nil {
		s.probeError(err)
	}
	query := "SELECT count(*), COALESCE(EXTRACT(epoch FROM clock_timestamp() - min(created_at)), 0)::text FROM message_outbox WHERE published_at IS NULL"
	output, err := s.composeCommand(ctx, "exec", "-T", "postgres", "psql", "-U", "chat", "-d", "chat", "-At", "-F", "|", "-c", query)
	if err != nil {
		if ctx.Err() == nil {
			s.probeError(err)
		}
		return
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(parts) != 2 {
		s.probeError(errors.New("unexpected PostgreSQL backlog probe response"))
		return
	}
	var pending int
	var age float64
	if _, err := fmt.Sscan(parts[0], &pending); err != nil {
		s.probeError(err)
		return
	}
	if _, err := fmt.Sscan(parts[1], &age); err != nil {
		s.probeError(err)
		return
	}
	s.mu.Lock()
	s.backlog = append(s.backlog, backlogSample{At: now, Pending: pending, OldestAgeSeconds: age})
	s.mu.Unlock()
}

func (s *resourceSampler) composeCommand(ctx context.Context, args ...string) ([]byte, error) {
	base := []string{"compose", "--project-name", s.project, "--file", s.compose}
	return exec.CommandContext(ctx, "docker", append(base, args...)...).Output()
}

func (s *resourceSampler) probeError(err error) {
	s.mu.Lock()
	s.probeErrors++
	s.lastProbeError = err.Error()
	s.mu.Unlock()
}

func (s *resourceSampler) snapshot() ([]ResourceSample, []backlogSample, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ResourceSample(nil), s.resources...), append([]backlogSample(nil), s.backlog...), s.probeErrors, s.lastProbeError
}

func observedSampleInterval(samples []backlogSample) string {
	if len(samples) < 2 {
		return "requested 1s; insufficient samples to estimate observed interval"
	}
	span := samples[len(samples)-1].At.Sub(samples[0].At)
	average := span / time.Duration(len(samples)-1)
	return fmt.Sprintf("requested 1s; observed average %s", average.Round(100*time.Millisecond))
}

func waitForOutboxDrain(ctx context.Context, sampler *resourceSampler, timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, backlog, _, _ := sampler.snapshot()
		if len(backlog) > 0 && backlog[len(backlog)-1].Pending == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func writeReport(path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func mustUUID() string {
	value, err := uuid.New()
	if err != nil {
		panic("cryptographic random source unavailable: " + err.Error())
	}
	return value
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func cloneMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
