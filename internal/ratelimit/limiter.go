// Package ratelimit provides atomic shared token buckets backed by Valkey.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/redis/go-redis/v9"
)

type Operation string

const (
	MessageSend        Operation = "message_send"
	APIRead            Operation = "api_read"
	RoomWrite          Operation = "room_write"
	WebSocketHandshake Operation = "websocket_handshake"
	WebSocketCommand   Operation = "websocket_command"
)

type Decision struct {
	Allowed           bool
	RetryAfterSeconds int
}

type policy struct {
	refillPerSecond float64
	capacity        int
	ttlMilliseconds int
}

type Limiter struct {
	client      redis.UniversalClient
	environment string
}

var policies = map[Operation]policy{
	MessageSend:        newPolicy(5, 10),
	APIRead:            newPolicy(20, 40),
	RoomWrite:          newPolicy(1, 10),
	WebSocketHandshake: newPolicy(10.0/60.0, 5),
	WebSocketCommand:   newPolicy(5, 10),
}

const tokenBucketScript = `
local capacity = tonumber(ARGV[1])
local refill_per_second = tonumber(ARGV[2])
local ttl_milliseconds = tonumber(ARGV[3])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) + tonumber(clock[2]) / 1000000
local values = redis.call('HMGET', KEYS[1], 'tokens', 'updated_at')
local tokens = tonumber(values[1])
local updated_at = tonumber(values[2])
if tokens == nil or updated_at == nil then
  tokens = capacity
  updated_at = now
end
local elapsed = math.max(0, now - updated_at)
tokens = math.min(capacity, tokens + elapsed * refill_per_second)
local allowed = 0
local retry_after_seconds = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_after_seconds = math.max(1, math.ceil((1 - tokens) / refill_per_second))
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'updated_at', tostring(now))
redis.call('PEXPIRE', KEYS[1], ttl_milliseconds)
return {allowed, retry_after_seconds}
`

func newPolicy(refillPerSecond float64, capacity int) policy {
	return policy{
		refillPerSecond: refillPerSecond,
		capacity:        capacity,
		ttlMilliseconds: int(math.Ceil(float64(capacity)/refillPerSecond*1000)) + 1000,
	}
}

// New creates a Valkey-backed limiter. The environment component is part of
// every key so independent deployments cannot consume each other's tokens.
func New(client redis.UniversalClient, environment string) (*Limiter, error) {
	environment = strings.TrimSpace(environment)
	if client == nil {
		return nil, errors.New("Valkey client is required")
	}
	if environment == "" || len(environment) > 64 {
		return nil, errors.New("rate-limit environment is invalid")
	}
	for _, character := range environment {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return nil, errors.New("rate-limit environment is invalid")
		}
	}
	return &Limiter{client: client, environment: environment}, nil
}

// Allow atomically consumes one token for an authenticated identity or a
// server-generated WebSocket connection scope. It has no process-local
// fallback: Valkey errors are returned to the caller for fail-closed handling.
func (l *Limiter) Allow(ctx context.Context, operation Operation, identity string) (Decision, error) {
	if l == nil || l.client == nil {
		return Decision{}, errors.New("rate limiter is unavailable")
	}
	selected, ok := policies[operation]
	if !ok {
		return Decision{}, fmt.Errorf("unknown rate-limit operation %q", operation)
	}
	if strings.TrimSpace(identity) == "" {
		return Decision{}, errors.New("rate-limit identity is required")
	}
	identityHash := sha256.Sum256([]byte(identity))
	key := fmt.Sprintf("chat:rate:v1:%s:%s:%x", l.environment, operation, identityHash[:])
	result, err := l.client.Eval(ctx, tokenBucketScript, []string{key}, selected.capacity, selected.refillPerSecond, selected.ttlMilliseconds).Result()
	if err != nil {
		return Decision{}, fmt.Errorf("Valkey rate-limit evaluation failed: %w", err)
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return Decision{}, errors.New("Valkey rate-limit response is invalid")
	}
	allowedValue, allowedOK := values[0].(int64)
	retryValue, retryOK := values[1].(int64)
	if !allowedOK || !retryOK || retryValue < 0 {
		return Decision{}, errors.New("Valkey rate-limit response is invalid")
	}
	return Decision{Allowed: allowedValue == 1, RetryAfterSeconds: int(retryValue)}, nil
}
