package strictjson_test

import (
	"testing"

	"cloudchat/internal/strictjson"
)

func TestDecodeRequiresOneValueWithKnownFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		valid bool
	}{
		{name: "object with whitespace", input: "  {\"name\":\"Ada\"} \n", valid: true},
		{name: "unknown field", input: `{"name":"Ada","extra":true}`},
		{name: "second value", input: `{"name":"Ada"} {}`},
		{name: "trailing invalid text", input: `{"name":"Ada"} garbage`},
		{name: "malformed", input: `{"name":`},
		{name: "empty", input: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result struct {
				Name string `json:"name"`
			}
			err := strictjson.Decode([]byte(tc.input), &result)
			if (err == nil) != tc.valid {
				t.Fatalf("Decode(%q) error = %v, want valid = %v", tc.input, err, tc.valid)
			}
			if tc.valid && result.Name != "Ada" {
				t.Fatalf("decoded name = %q, want Ada", result.Name)
			}
		})
	}
}
