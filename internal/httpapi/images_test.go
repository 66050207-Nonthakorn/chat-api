package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/imagestore"
)

type imageTestStore struct {
	get func() (imagestore.Object, error)
}

func (s imageTestStore) Put(context.Context, string, []byte, string) error      { return nil }
func (s imageTestStore) Delete(context.Context, string) error                   { return nil }
func (s imageTestStore) Get(context.Context, string) (imagestore.Object, error) { return s.get() }

func TestImageUploadRejectsBusyCapacityBeforeReadingBody(t *testing.T) {
	imageUploadSlots <- struct{}{}
	defer func() { <-imageUploadSlots }()
	h := handler{config: config.Config{ImageMessagesEnabled: true}, images: imageTestStore{}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("unread"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	w := httptest.NewRecorder()
	h.sendImageMessage(w, r, "unused", auth.Principal{})
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" || !r.Close {
		t.Fatalf("busy upload: status=%d headers=%v close=%v", w.Code, w.Header(), r.Close)
	}
}

func TestUploadDeadlineInterruptsAStalledMultipartBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 75*time.Millisecond)
		defer cancel()
		_, err := readImageSendRequest(ctx, w, r, "test")
		r.Close = true
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("body read error = %v, want timeout", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Type: multipart/form-data; boundary=test\r\nContent-Length: 1000\r\n\r\n--test\r\nContent-Disposition: form-data; name=\"image\"\r\n\r\npartial")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

type gatedImageBody struct {
	data      []byte
	gate      <-chan struct{}
	reads     int
	readBytes int
	closed    atomic.Bool
}

func (b *gatedImageBody) Read(p []byte) (int, error) {
	if b.readBytes >= 32*1024 {
		select {
		case <-b.gate:
		case <-time.After(3 * time.Second):
			return 0, errors.New("body was buffered before sending the first chunk")
		}
	}
	b.reads++
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.readBytes += n
	b.data = b.data[n:]
	return n, nil
}
func (b *gatedImageBody) Close() error { b.closed.Store(true); return nil }

func TestImageDownloadStreamsBeforeReadingTheRestOfTheObject(t *testing.T) {
	data := bytes.Repeat([]byte{0x42}, 256*1024)
	gate := make(chan struct{})
	body := &gatedImageBody{data: data, gate: gate}
	store := imageTestStore{get: func() (imagestore.Object, error) {
		return imagestore.Object{Body: body, Size: int64(len(data))}, nil
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamImage(r.Context(), w, r, store, "private-key", "image/png", len(data))
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 32*1024)
	_, err = io.ReadFull(response.Body, first)
	close(gate)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(first, rest...), data) {
		t.Fatal("streamed bytes differ")
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.ContentLength != int64(len(data)) {
		t.Fatalf("unsafe stream headers: %v", response.Header)
	}
}

func TestImageDownloadRejectsSizeMismatchAndAbortsTruncatedObjects(t *testing.T) {
	for _, mismatch := range []bool{true, false} {
		t.Run(fmt.Sprintf("metadataMismatch=%v", mismatch), func(t *testing.T) {
			const size = 128 * 1024
			body := &gatedImageBody{data: bytes.Repeat([]byte{1}, 32*1024), gate: closedImageGate()}
			objectSize := int64(size)
			if mismatch {
				objectSize++
			}
			store := imageTestStore{get: func() (imagestore.Object, error) { return imagestore.Object{Body: body, Size: objectSize}, nil }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				streamImage(r.Context(), w, r, store, "key", "image/png", size)
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			_, readErr := io.ReadAll(response.Body)
			if mismatch {
				if response.StatusCode != http.StatusServiceUnavailable || body.reads != 0 {
					t.Fatalf("mismatch status=%d reads=%d", response.StatusCode, body.reads)
				}
			} else if response.StatusCode != http.StatusOK || !errors.Is(readErr, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated response status=%d error=%v", response.StatusCode, readErr)
			}
			if !body.closed.Load() {
				t.Fatal("storage body not closed")
			}
		})
	}
}

func closedImageGate() <-chan struct{} { gate := make(chan struct{}); close(gate); return gate }
