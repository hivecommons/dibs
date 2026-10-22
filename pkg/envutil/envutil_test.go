package envutil

import "testing"

func TestGet(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		key    string
		want   string
		wantOr string
	}{
		{"DIBS wins", map[string]string{"DIBS_X": "new", "IDEATE_X": "old"}, "DIBS_X", "new", "new"},
		{"IDEATE fallback", map[string]string{"IDEATE_X": "old"}, "DIBS_X", "old", "old"},
		{"neither", nil, "DIBS_X", "", "def"},
		{"non-DIBS key has no fallback", map[string]string{"IDEATE_X": "old", "OTHER_X": ""}, "OTHER_X", "", "def"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"DIBS_X", "IDEATE_X", "OTHER_X"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := Get(tt.key); got != tt.want {
				t.Errorf("Get(%q) = %q, want %q", tt.key, got, tt.want)
			}
			if got := GetOr(tt.key, "def"); got != tt.wantOr {
				t.Errorf("GetOr(%q) = %q, want %q", tt.key, got, tt.wantOr)
			}
		})
	}
}
