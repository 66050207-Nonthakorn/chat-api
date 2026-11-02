package uuid

import "testing"

func TestNewGeneratesCanonicalV4(t *testing.T) {
	value, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !IsCanonical(value) || value[14] != '4' || (value[19] != '8' && value[19] != '9' && value[19] != 'a' && value[19] != 'b') {
		t.Fatalf("New() = %q, want canonical UUID v4", value)
	}
}
