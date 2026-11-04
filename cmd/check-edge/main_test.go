package main

import (
	"os"
	"strings"
	"testing"
)

func TestSharedEdgeConfigurationAndUnsafeChanges(t *testing.T) {
	data, err := os.ReadFile("../../infra/shared-edge/distribution.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(data, "chat-alb"); err != nil {
		t.Fatal(err)
	}
	if err := validate(append(append([]byte(`{"DistributionConfig":`), data...), '}'), "chat-alb"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(string) string{
		"caches-private-data":   func(s string) string { return strings.Replace(s, cachingDisabled, "cache-enabled", 1) },
		"drops-session-or-csrf": func(s string) string { return strings.Replace(s, allViewer, "forward-none", 1) },
		"upload-method-denied":  func(s string) string { return strings.Replace(s, `"POST"`, `"TRACE"`, 1) },
		"origin-http": func(s string) string {
			return strings.Replace(s, `"OriginProtocolPolicy": "https-only"`, `"OriginProtocolPolicy": "http-only"`, 1)
		},
		"origin-timeout": func(s string) string {
			return strings.Replace(s, `"OriginReadTimeout": 60`, `"OriginReadTimeout": 30`, 1)
		},
		"shadowing-behavior": func(s string) string {
			return strings.Replace(s, `"PathPattern": "/api/*"`, `"PathPattern": "*"`, 1) + " "
		},
		"spa-rewrites-api-errors": func(s string) string {
			return strings.Replace(s, `"CustomErrorResponses": {"Quantity": 0}`, `"CustomErrorResponses": {"Items":[{"ErrorCode":404,"ResponsePagePath":"/index.html"}]}`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := change(string(data))
			if name == "shadowing-behavior" {
				changed = strings.Replace(changed, cachingDisabled, "cache-enabled", 1)
			}
			if err := validate([]byte(changed), "chat-alb"); err == nil {
				t.Fatal("accepted unsafe distribution")
			}
		})
	}
}
