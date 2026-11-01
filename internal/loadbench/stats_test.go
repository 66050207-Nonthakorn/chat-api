package loadbench

import (
	"testing"
	"time"

	"cloudchat/internal/demo"
)

func TestSummarizeUsesNearestRankPercentiles(t *testing.T) {
	samples := make([]time.Duration, 100)
	for index := range samples {
		samples[index] = time.Duration(index+1) * time.Millisecond
	}
	got := Summarize(samples)
	if got.Count != 100 || got.P50MS != 50 || got.P95MS != 95 || got.P99MS != 99 || got.MaxMS != 100 {
		t.Fatalf("summary = %+v, want count 100 and nearest-rank 50/95/99/100ms", got)
	}
}

func TestSummarizeEmptyAndSingleSample(t *testing.T) {
	if got := Summarize(nil); got.Count != 0 {
		t.Fatalf("empty summary = %+v, want count 0", got)
	}
	got := Summarize([]time.Duration{17 * time.Millisecond})
	if got.Count != 1 || got.P50MS != 17 || got.P95MS != 17 || got.P99MS != 17 || got.MaxMS != 17 {
		t.Fatalf("single sample summary = %+v, want 17ms for every percentile", got)
	}
}

func TestConfigValidateEnforcesBaselineBounds(t *testing.T) {
	valid := Config{
		BaseURL: "http://127.0.0.1:18080", Connections: 100, MessagesPerSecond: 10,
		Duration: time.Minute, PayloadBytes: 256, GroupPercent: 50,
		GroupRoomSize: 20, PrivateRoomSize: 2,
		GroupRoomID: demo.GroupRoomID, PrivateRoomID: demo.DirectRoomID,
		UserAID: demo.UserAID, UserBID: demo.UserBID,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid baseline workload rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*Config)
	}{
		{name: "no websocket connections", edit: func(c *Config) { c.Connections = 0 }},
		{name: "zero message rate", edit: func(c *Config) { c.MessagesPerSecond = 0 }},
		{name: "payload exceeds API limit", edit: func(c *Config) { c.PayloadBytes = 4097 }},
		{name: "group exceeds baseline room ceiling", edit: func(c *Config) { c.GroupRoomSize = 21 }},
		{name: "invalid traffic mix", edit: func(c *Config) { c.GroupPercent = 101 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.edit(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid workload was accepted")
			}
		})
	}
}
