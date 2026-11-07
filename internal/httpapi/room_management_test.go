package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"cloudchat/internal/ratelimit"
)

func TestNormalizeMembersAndWriteRateLimits(t *testing.T) {
	a := "11111111-1111-4111-8111-111111111111"
	b := "22222222-2222-4222-8222-222222222222"
	got, ok := normalizeMembers([]string{b, a, b})
	if !ok || !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("normalize members = %v, %v", got, ok)
	}
	if _, ok := normalizeMembers([]string{"invalid"}); ok {
		t.Fatal("accepted invalid user ID")
	}
	for _, path := range []string{"/api/v1/rooms", "/api/v1/rooms/" + a + "/members"} {
		op, limited := requestRateLimitOperation(httptest.NewRequest(http.MethodPost, path, nil))
		if !limited || op != ratelimit.RoomWrite {
			t.Fatalf("%s: operation %q limited %v", path, op, limited)
		}
	}
}
