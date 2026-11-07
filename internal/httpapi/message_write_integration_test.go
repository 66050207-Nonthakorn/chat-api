package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"

	"cloudchat/internal/auth"
	"cloudchat/internal/demo"
	"cloudchat/internal/imageproc"
	"cloudchat/internal/testutil"
)

func TestImageMessageTransactionRollsBackAndPreservesReplayRules(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for message transaction integration")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "message_write_")
	ctx := context.Background()
	h := &handler{pool: pool}
	digest := sha256.Sum256([]byte("source"))
	image := &messageImageCandidate{id: "66666666-6666-4666-8666-666666666666", sourceDigest: digest, sourceBytes: 6, sourceType: "image/png", processed: imageproc.Result{Bytes: []byte("image"), ContentType: "image/png", Width: 1, Height: 1}}
	request := messageWriteRequest{roomID: demo.GroupRoomID, principal: auth.Principal{UserID: demo.UserAID}, clientMessageID: "77777777-7777-4777-8777-777777777777", text: "caption", image: image}
	_, err := pool.Exec(ctx, `INSERT INTO image_upload_candidates (image_id, room_id, sender_user_id, client_message_id, source_sha256, source_bytes, source_content_type, object_key, content_type, byte_size, width, height)
        VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 6, 'image/png', 'test-image', 'image/png', 5, 1, 1)`, image.id, request.roomID, request.principal.UserID, request.clientMessageID, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_message_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$;
        CREATE TRIGGER fail_message_outbox BEFORE INSERT ON message_outbox FOR EACH ROW EXECUTE FUNCTION fail_message_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.writeMessage(ctx, request); err == nil {
		t.Fatal("expected outbox failure")
	}
	var seq, messages, outbox int
	var state string
	err = pool.QueryRow(ctx, `SELECT last_seq, (SELECT count(*) FROM messages), (SELECT count(*) FROM message_outbox), (SELECT state FROM image_upload_candidates WHERE image_id=$2::uuid) FROM rooms WHERE room_id=$1::uuid`, request.roomID, image.id).Scan(&seq, &messages, &outbox, &state)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 0 || messages != 0 || outbox != 0 || state != "candidate" {
		t.Fatalf("rollback: seq=%d messages=%d outbox=%d state=%s", seq, messages, outbox, state)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_message_outbox ON message_outbox; DROP FUNCTION fail_message_outbox()`); err != nil {
		t.Fatal(err)
	}
	created, err := h.writeMessage(ctx, request)
	if err != nil || created.Replayed || created.Message.Seq != "1" || created.Message.Image == nil {
		t.Fatalf("create: %+v, %v", created, err)
	}
	replay, err := h.writeMessage(ctx, request)
	if err != nil || !replay.Replayed || replay.Message.MessageID != created.Message.MessageID {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	textRequest := request
	textRequest.image = nil
	if _, err := h.writeMessage(ctx, textRequest); !errors.Is(err, errMessageConflict) {
		t.Fatalf("image-to-text conflict: %v", err)
	}
	changedImage := *image
	changedImage.sourceDigest = sha256.Sum256([]byte("different"))
	request.image = &changedImage
	if _, err := h.writeMessage(ctx, request); !errors.Is(err, errMessageConflict) {
		t.Fatalf("image fingerprint conflict: %v", err)
	}
}

func TestConcurrentTextAndImageSameKeyOnlyOneCommits(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for message transaction integration")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "message_race_")
	ctx := context.Background()
	digest := sha256.Sum256([]byte("source"))
	image := &messageImageCandidate{id: "88888888-8888-4888-8888-888888888888", sourceDigest: digest, sourceBytes: 6, sourceType: "image/png", processed: imageproc.Result{Bytes: []byte("image"), ContentType: "image/png", Width: 1, Height: 1}}
	request := messageWriteRequest{roomID: demo.GroupRoomID, principal: auth.Principal{UserID: demo.UserAID}, clientMessageID: "99999999-9999-4999-8999-999999999999", text: "same", image: image}
	_, err := pool.Exec(ctx, `INSERT INTO image_upload_candidates (image_id, room_id, sender_user_id, client_message_id, source_sha256, source_bytes, source_content_type, object_key, content_type, byte_size, width, height)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 6, 'image/png', 'race-image', 'image/png', 5, 1, 1)`, image.id, request.roomID, request.principal.UserID, request.clientMessageID, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	textRequest := request
	textRequest.image = nil
	h := &handler{pool: pool}
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, input := range []messageWriteRequest{request, textRequest} {
		go func(input messageWriteRequest) {
			ready.Done()
			<-start
			_, err := h.writeMessage(ctx, input)
			results <- err
		}(input)
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if !(first == nil && errors.Is(second, errMessageConflict) || second == nil && errors.Is(first, errMessageConflict)) {
		t.Fatalf("concurrent results: %v, %v", first, second)
	}
	var sequence, messages, outbox int
	if err := pool.QueryRow(ctx, `SELECT last_seq, (SELECT count(*) FROM messages), (SELECT count(*) FROM message_outbox) FROM rooms WHERE room_id=$1::uuid`, request.roomID).Scan(&sequence, &messages, &outbox); err != nil {
		t.Fatal(err)
	}
	if sequence != 1 || messages != 1 || outbox != 1 {
		t.Fatalf("sequence=%d messages=%d outbox=%d", sequence, messages, outbox)
	}
}
