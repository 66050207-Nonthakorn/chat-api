package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
)

func TestRoomManagementLifecycle(t *testing.T) {
	s := newTestServer(t, httpapi.Options{})
	call := func(method, path, user, body string, want int) map[string]any {
		t.Helper()
		response, data := s.request(t, method, path, user, body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: status %d, body %s", method, path, response.StatusCode, data)
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	dm := call(http.MethodPost, "/api/v1/rooms", demo.UserAID, `{"kind":"dm","user_id":"`+demo.UserBID+`"}`, http.StatusOK)
	if dm["room_id"] != demo.DirectRoomID || dm["replayed"] != true {
		t.Fatalf("existing DM: %v", dm)
	}
	groupBody := `{"kind":"group","title":"  Team  ","member_ids":["` + demo.UserBID + `"],"request_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}`
	created := call(http.MethodPost, "/api/v1/rooms", demo.UserAID, groupBody, http.StatusCreated)
	room := created["room_id"].(string)
	replay := call(http.MethodPost, "/api/v1/rooms", demo.UserAID, groupBody, http.StatusOK)
	if replay["room_id"] != room || replay["replayed"] != true {
		t.Fatalf("creation replay: %v", replay)
	}
	call(http.MethodPost, "/api/v1/rooms", demo.UserAID, strings.Replace(groupBody, "Team", "Different", 1), http.StatusConflict)
	call(http.MethodGet, "/api/v1/rooms/"+room+"/members", demo.UserBID, "", http.StatusOK)
	addBody := `{"member_ids":["` + demo.UserBID + `"],"request_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`
	call(http.MethodPost, "/api/v1/rooms/"+room+"/members", demo.UserAID, addBody, http.StatusOK)
	if _, err := s.pool.Exec(context.Background(), `DELETE FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid`, room, demo.UserBID); err != nil {
		t.Fatal(err)
	}
	replay = call(http.MethodPost, "/api/v1/rooms/"+room+"/members", demo.UserAID, addBody, http.StatusOK)
	if replay["replayed"] != true {
		t.Fatalf("add replay: %v", replay)
	}
	call(http.MethodGet, "/api/v1/rooms/"+room+"/members", demo.UserBID, "", http.StatusNotFound)
	var count int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM room_memberships WHERE room_id=$1::uuid AND user_id=$2::uuid`, room, demo.UserBID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replay restored membership: %d %v", count, err)
	}
	call(http.MethodGet, "/api/v1/users?q=demo", demo.UserAID, "", http.StatusOK)
}

func TestConcurrentGroupAdditionsRespectLimit(t *testing.T) {
	s := newTestServer(t, httpapi.Options{})
	ctx := context.Background()
	// The seeded group begins with two members; add 17 to leave one place.
	for i := 0; i < 19; i++ {
		id := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", i)
		if _, err := s.pool.Exec(ctx, `INSERT INTO app_users(user_id,display_name) VALUES($1::uuid,$2)`, id, fmt.Sprintf("User %d", i)); err != nil {
			t.Fatal(err)
		}
		if i < 17 {
			if _, err := s.pool.Exec(ctx, `INSERT INTO room_memberships(room_id,user_id) VALUES($1::uuid,$2::uuid)`, demo.GroupRoomID, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	for i := 17; i < 19; i++ {
		go func(i int) {
			<-start
			body := fmt.Sprintf(`{"member_ids":["aaaaaaaa-aaaa-4aaa-8aaa-%012d"],"request_id":"bbbbbbbb-bbbb-4bbb-8bbb-%012d"}`, i, i)
			req, err := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/rooms/"+demo.GroupRoomID+"/members", strings.NewReader(body))
			if err != nil {
				results <- 0
				return
			}
			req.Header.Set("X-Local-Test-User-ID", demo.UserAID)
			res, err := s.server.Client().Do(req)
			if err != nil {
				results <- 0
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			results <- res.StatusCode
		}(i)
	}
	close(start)
	a, b := <-results, <-results
	if !((a == http.StatusOK && b == http.StatusUnprocessableEntity) || (b == http.StatusOK && a == http.StatusUnprocessableEntity)) {
		t.Fatalf("concurrent statuses: %d, %d", a, b)
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM room_memberships WHERE room_id=$1::uuid`, demo.GroupRoomID).Scan(&count); err != nil || count != 20 {
		t.Fatalf("member count = %d, error = %v", count, err)
	}
}
