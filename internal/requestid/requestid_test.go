package requestid_test

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"cloudchat/internal/requestid"
)

func TestNewAndContext(t *testing.T) {
	id, err := requestid.New()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 || id != strings.ToLower(id) {
		t.Fatalf("request ID = %q, want 32 lowercase hexadecimal characters", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("decode request ID %q: %v", id, err)
	}
	ctx := context.Background()
	if got := requestid.FromContext(ctx); got != "" {
		t.Fatalf("empty context ID = %q", got)
	}
	if got := requestid.FromContext(requestid.WithContext(ctx, id)); got != id {
		t.Fatalf("context ID = %q, want %q", got, id)
	}
}
