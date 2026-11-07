package realtime

import (
	"context"
	"sync"
)

type messageQueue struct {
	mu        sync.Mutex
	frames    []outboundFrame
	bytes     int
	maxFrames int
	maxBytes  int
	wake      chan struct{}
	closed    bool
}

func newMessageQueue(maxFrames, maxBytes int) *messageQueue {
	return &messageQueue{
		maxFrames: maxFrames,
		maxBytes:  maxBytes,
		wake:      make(chan struct{}, 1),
	}
}

func (q *messageQueue) push(frame outboundFrame) bool {
	q.mu.Lock()
	if q.closed || len(q.frames) >= q.maxFrames || q.bytes+len(frame.payload) > q.maxBytes {
		q.mu.Unlock()
		return false
	}
	q.frames = append(q.frames, frame)
	q.bytes += len(frame.payload)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *messageQueue) pop(ctx context.Context) (outboundFrame, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return outboundFrame{}, false
		}
		if len(q.frames) > 0 {
			frame := q.frames[0]
			q.frames[0] = outboundFrame{}
			q.frames = q.frames[1:]
			q.bytes -= len(frame.payload)
			q.mu.Unlock()
			return frame, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return outboundFrame{}, false
		case <-q.wake:
		}
	}
}

func (q *messageQueue) dropMessageEvents(roomID string) {
	q.mu.Lock()
	filtered := q.frames[:0]
	for _, frame := range q.frames {
		if frame.message && frame.roomID == roomID {
			q.bytes -= len(frame.payload)
			continue
		}
		filtered = append(filtered, frame)
	}
	for index := len(filtered); index < len(q.frames); index++ {
		q.frames[index] = outboundFrame{}
	}
	q.frames = filtered
	q.mu.Unlock()
}

func (q *messageQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.frames = nil
	q.bytes = 0
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
