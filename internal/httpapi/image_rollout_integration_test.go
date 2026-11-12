package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/imagecleanup"
	"cloudchat/internal/imagestore"
	"cloudchat/internal/message"
	"cloudchat/internal/migrations"
	"cloudchat/internal/outbox"
	"cloudchat/internal/testutil"
	"cloudchat/internal/uuid"
	"github.com/redis/go-redis/v9"
)

func TestImageRolloutFromSchemaOneAndPrivateStreaming(t *testing.T) {
	databaseURL, valkeyURL, imageEndpoint := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_VALKEY_URL"), os.Getenv("TEST_IMAGE_ENDPOINT")
	if databaseURL == "" || valkeyURL == "" || imageEndpoint == "" {
		t.Skip("set TEST_DATABASE_URL, TEST_VALKEY_URL, TEST_IMAGE_ENDPOINT for rollout/storage integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := testutil.IsolatedDatabase(t, databaseURL, "image_rollout_")
	// This isolated schema contains only seeded rooms. Reconstruct schema 1 to
	// exercise the actual 0002 migration and a running compatibility release.
	_, err := pool.Exec(ctx, `
		DROP TABLE room_requests;
		ALTER TABLE messages DROP CONSTRAINT messages_text_or_image_check;
		ALTER TABLE messages DROP COLUMN image_id;
		DROP TABLE image_upload_candidates;
		ALTER TABLE messages ADD CONSTRAINT messages_text_check CHECK (octet_length(text) BETWEEN 1 AND 4096);
		DELETE FROM schema_migrations WHERE version IN (2, 3);`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Environment: config.EnvironmentLocal, EnableLocalTestIdentity: true,
		CursorHMACKey: "image-test-room-cursor-key-0123456789", ImageRegion: "us-east-1",
		ImageEndpoint: imageEndpoint, ImageBucket: "cloudchat-images", ImageAccessKey: "admin", ImageSecretKey: "secret"}
	store, err := imagestore.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(valkeyURL)
	if err != nil {
		t.Fatal(err)
	}
	broker := redis.NewClient(opts)
	t.Cleanup(func() { _ = broker.Close() })
	testID, err := uuid.New()
	if err != nil {
		t.Fatal(err)
	}
	eventEnvironment := config.Environment("image-rollout-" + testID)
	worker := outbox.NewWorker(pool, broker, eventEnvironment)
	subscriber := broker.Subscribe(ctx, message.EventChannel(string(eventEnvironment)))
	defer subscriber.Close()
	if _, err := subscriber.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	readiness := func(ctx context.Context) error {
		version, err := migrations.Version(ctx, pool)
		if err != nil {
			return err
		}
		return migrations.CheckVersion(version, false)
	}
	uploadSlots := make(chan struct{}, 1)
	api := httptest.NewServer(NewHandler(cfg, pool, Options{Readiness: readiness, Images: store, ImageUploadSlots: uploadSlots}))
	defer api.Close()
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	request := func(server *httptest.Server, method, path, user, contentType string, body io.Reader, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(auth.LocalTestUserHeader, user)
		req.Header.Set("Content-Type", contentType)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.StatusCode, want, data)
		}
		if strings.HasSuffix(path, "/image") && want == 200 && res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("private image cached")
		}
		return data
	}
	textBody := `{"client_message_id":"44444444-4444-4444-8444-444444444444","text":"before migration"}`
	request(api, "GET", "/health/ready", demo.UserAID, "", nil, 200)
	request(api, "POST", path, demo.UserAID, "application/json", strings.NewReader(textBody), 201)
	request(api, "POST", path, demo.UserAID, "application/json", strings.NewReader(textBody), 200)
	for _, readPath := range []string{path, "/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=0"} {
		if !bytes.Contains(request(api, "GET", readPath, demo.UserAID, "", nil, 200), []byte("before migration")) {
			t.Fatal("schema-1 message missing")
		}
	}
	if err := imagecleanup.ProcessBatch(ctx, pool, store); err != nil {
		t.Fatalf("schema-1 cleanup: %v", err)
	}
	if result, err := worker.ProcessBatch(ctx); err != nil || result.Published != 1 {
		t.Fatalf("schema-1 outbox: %+v %v", result, err)
	}
	if _, err := subscriber.ReceiveMessage(ctx); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	request(api, "GET", "/health/ready", demo.UserAID, "", nil, 200)
	request(api, "POST", path, demo.UserAID, "application/json", strings.NewReader(textBody), 200)

	imagesAPI := httptest.NewServer(NewHandler(cfg, pool, Options{Readiness: readiness, Images: store, ImageUploadSlots: uploadSlots}))
	defer imagesAPI.Close()
	t.Cleanup(func() {
		rows, err := pool.Query(context.Background(), "SELECT object_key FROM image_upload_candidates")
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				_ = store.Delete(context.Background(), key)
			}
		}
	})
	img := image.NewNRGBA(image.Rect(0, 0, 16, 8))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	upload := func(caption string, want int) []byte {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("client_message_id", "55555555-5555-4555-8555-555555555555")
		_ = form.WriteField("text", caption)
		part, err := form.CreateFormFile("image", "image.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(source.Bytes())
		_ = form.Close()
		return request(imagesAPI, "POST", path, demo.UserAID, form.FormDataContentType(), &body, want)
	}
	var sent struct{ Message message.Message }
	if err := json.Unmarshal(upload("caption", 201), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Message.Image == nil || sent.Message.Image.Width != 16 {
		t.Fatalf("image metadata: %+v", sent.Message)
	}
	upload("caption", 200)
	upload("changed caption", 409)
	imageURL := sent.Message.Image.URL
	data := request(imagesAPI, "GET", imageURL, demo.UserBID, "", nil, 200)
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, alpha := decoded.At(0, 0).RGBA()
	if alpha == 0xffff {
		t.Fatal("lost alpha")
	}
	// Sending can be disabled while existing images remain readable in sync.
	if !bytes.Contains(request(api, "GET", path, demo.UserAID, "", nil, 200), []byte(imageURL)) {
		t.Fatal("image lost with sends disabled")
	}
	if result, err := worker.ProcessBatch(ctx); err != nil || result.Published != 1 {
		t.Fatalf("schema-2 outbox: %+v %v", result, err)
	}
	event, err := subscriber.ReceiveMessage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.Payload, imageURL) {
		t.Fatalf("outbox dropped image: %s", event.Payload)
	}
	_, err = pool.Exec(ctx, "DELETE FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid", demo.GroupRoomID, demo.UserBID)
	if err != nil {
		t.Fatal(err)
	}
	request(imagesAPI, "GET", imageURL, demo.UserBID, "", nil, 404)
}

func TestImageUploadAdmissionSharedAcrossHandlers(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for shared upload admission integration")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "image_admission_")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	enteredStorage := make(chan struct{})
	releaseStorage := make(chan struct{})
	var puts atomic.Int32
	store := imageTestStore{put: func(ctx context.Context) error {
		if puts.Add(1) == 1 {
			close(enteredStorage)
			select {
			case <-releaseStorage:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.New("test storage failure")
	}}
	cfg := config.Config{Environment: config.EnvironmentLocal, EnableLocalTestIdentity: true}
	newServer := func(slots chan struct{}) *httptest.Server {
		server := httptest.NewServer(NewHandler(cfg, pool, Options{Images: store, ImageUploadSlots: slots}))
		t.Cleanup(server.Close)
		return server
	}
	sharedSlots := make(chan struct{}, 1)
	first := newServer(sharedSlots)
	second := newServer(sharedSlots)
	otherProcess := newServer(make(chan struct{}, 1))

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("client_message_id", "88888888-8888-4888-8888-888888888888")
	part, err := form.CreateFormFile("image", "image.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(part, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	post := func(server *httptest.Server) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/rooms/"+demo.GroupRoomID+"/messages", bytes.NewReader(body.Bytes()))
		if err != nil {
			return 0, err
		}
		req.Header.Set(auth.LocalTestUserHeader, demo.UserAID)
		req.Header.Set("Content-Type", form.FormDataContentType())
		response, err := server.Client().Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		return response.StatusCode, err
	}
	type result struct {
		status int
		err    error
	}
	completed := make(chan result, 1)
	go func() {
		status, err := post(first)
		completed <- result{status, err}
	}()
	select {
	case <-enteredStorage:
	case got := <-completed:
		t.Fatalf("first upload stopped before storage: %+v", got)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if status, err := post(second); err != nil || status != http.StatusTooManyRequests {
		t.Fatalf("shared gate status=%d error=%v, want 429 while first upload stores its image", status, err)
	}
	if status, err := post(otherProcess); err != nil || status != http.StatusServiceUnavailable || puts.Load() != 2 {
		t.Fatalf("independent gate status=%d error=%v puts=%d, want its own storage attempt", status, err, puts.Load())
	}
	close(releaseStorage)
	select {
	case got := <-completed:
		if got.err != nil || got.status != http.StatusServiceUnavailable {
			t.Fatalf("first upload result=%+v, want storage failure", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if status, err := post(second); err != nil || status != http.StatusServiceUnavailable || puts.Load() != 3 {
		t.Fatalf("released gate status=%d error=%v puts=%d, want another storage attempt", status, err, puts.Load())
	}
}
