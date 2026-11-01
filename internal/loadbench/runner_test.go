package loadbench

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClientSubscribeUsesOnlyProtocolCommandFields(t *testing.T) {
	encoded, err := json.Marshal(clientCommand{Type: "subscribe", RequestID: "11111111-1111-4111-8111-111111111111", RoomID: "22222222-2222-4222-8222-222222222222"})
	if err != nil {
		t.Fatalf("encode subscribe command: %v", err)
	}
	if strings.Contains(string(encoded), "message") || string(encoded) != `{"type":"subscribe","request_id":"11111111-1111-4111-8111-111111111111","room_id":"22222222-2222-4222-8222-222222222222"}` {
		t.Fatalf("subscribe command = %s, want exact protocol envelope without server message fields", encoded)
	}
}

func TestTrackerCountsDuplicateDeliveryPerClient(t *testing.T) {
	tracker := &tracker{records: make(map[string]*sendRecord), httpStatuses: make(map[string]int)}
	tracker.begin(&sendRecord{ClientMessageID: "client-id", RoomID: "room-id", StartedAt: time.Now()})
	tracker.messageEvent(1, "client-id")
	tracker.messageEvent(1, "client-id")
	tracker.messageEvent(2, "client-id")

	if tracker.duplicates != 1 {
		t.Fatalf("duplicate frames = %d, want one repeated frame to the same connection", tracker.duplicates)
	}
	if got := tracker.records["client-id"].DeliveryCount; got != 3 {
		t.Fatalf("delivery count = %d, want three frames across two connections", got)
	}
}

func TestApplyFaultWindowReportsOutboxAndSyncRecovery(t *testing.T) {
	started := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Second)
	syncEnded := ended.Add(2 * time.Second)
	tracker := &tracker{records: map[string]*sendRecord{
		"accepted": {
			ClientMessageID: "accepted", RoomID: "group", MessageID: "message",
			StartedAt: started.Add(-time.Second), AcceptedAt: started.Add(-time.Millisecond),
		},
	}}
	sampler := &resourceSampler{backlog: []backlogSample{
		{At: started.Add(time.Second), Pending: 3, OldestAgeSeconds: 2},
		{At: ended.Add(time.Second), Pending: 0},
	}}
	var report Report
	fault := faultWindow{Service: "worker", StartedAt: started, EndedAt: ended}
	applyFaultWindow(&report, tracker, fault, map[string]map[string]struct{}{
		"group": {"message": {}},
	}, syncEnded, sampler)

	if report.Recovery.FaultService != "worker" || report.Recovery.FaultStartedAt == nil || report.Recovery.FaultEndedAt == nil {
		t.Fatalf("fault window was not copied into report: %+v", report.Recovery)
	}
	if report.Recovery.OutboxRecoverySeconds == nil || *report.Recovery.OutboxRecoverySeconds != 1 {
		t.Fatalf("outbox recovery duration = %v, want one second after fault end", report.Recovery.OutboxRecoverySeconds)
	}
	if report.Recovery.SyncRecoverySeconds == nil || *report.Recovery.SyncRecoverySeconds != 2 {
		t.Fatalf("sync recovery duration = %v, want two seconds after fault end", report.Recovery.SyncRecoverySeconds)
	}
}

func TestApplyFaultWindowLeavesOutboxRecoveryUnsetForAPIRestart(t *testing.T) {
	started := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Second)
	sampler := &resourceSampler{backlog: []backlogSample{
		{At: started.Add(time.Second), Pending: 2},
		{At: ended.Add(time.Second), Pending: 0},
	}}
	var report Report
	applyFaultWindow(&report, &tracker{records: map[string]*sendRecord{}}, faultWindow{
		Service: "api", StartedAt: started, EndedAt: ended,
	}, nil, ended.Add(2*time.Second), sampler)
	if report.Recovery.OutboxRecoverySeconds != nil {
		t.Fatalf("API restart should not be reported as outbox recovery: %+v", report.Recovery)
	}
}

func TestObservedSampleIntervalReportsActualAverage(t *testing.T) {
	started := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	samples := []backlogSample{
		{At: started},
		{At: started.Add(3200 * time.Millisecond)},
		{At: started.Add(6400 * time.Millisecond)},
	}
	if got := observedSampleInterval(samples); got != "requested 1s; observed average 3.2s" {
		t.Fatalf("observed interval = %q", got)
	}
}
