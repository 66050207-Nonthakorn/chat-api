package migrations

import "testing"

func TestSchemaCompatibilityDuringImageRollout(t *testing.T) {
	for _, tc := range []struct {
		version         int
		images, allowed bool
	}{
		{0, false, false}, {1, false, true}, {1, true, false},
		{2, false, true}, {2, true, true}, {3, false, false}, {3, true, false},
	} {
		if got := CheckVersion(tc.version, tc.images) == nil; got != tc.allowed {
			t.Errorf("version=%d images=%v allowed=%v want=%v", tc.version, tc.images, got, tc.allowed)
		}
	}
}
