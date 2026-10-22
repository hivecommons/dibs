package envutil

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyFallback(t *testing.T) {
	for _, key := range []string{
		"DIBS_ADDR", "DIBS_BASE_PATH", "DIBS_METRICS_ADDR", "DIBS_GITHUB_TOKEN",
		"DIBS_LLM_BASE_URL", "DIBS_LLM_API_KEY", "DIBS_LLM_MODEL",
		"DIBS_ADMINS", "DIBS_CLANKER_MARKERS", "DIBS_INTAKE_MAX_MB",
		"DIBS_STT_URL", "DIBS_STT_KEY", "DIBS_STT_MODEL",
	} {
		t.Run(key, func(t *testing.T) {
			legacy := "IDEATE_" + strings.TrimPrefix(key, "DIBS_")
			for _, tc := range []struct {
				name, current, old, want string
				unset                    bool
			}{
				{"current wins", "current", "legacy", "current", false},
				{"legacy when unset", "", "legacy", "legacy", true},
				{"legacy when empty", "", "legacy", "legacy", false},
				{"neither set", "", "", "", true},
				{"both empty", "", "", "", false},
				{"values unchanged", "  current  ", "legacy", "  current  ", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Setenv(key, tc.current)
					t.Setenv(legacy, tc.old)
					if tc.unset {
						if err := os.Unsetenv(key); err != nil {
							t.Fatal(err)
						}
						if tc.old == "" {
							if err := os.Unsetenv(legacy); err != nil {
								t.Fatal(err)
							}
						}
					}
					if got := Get(key); got != tc.want {
						t.Fatalf("Get = %q, want %q", got, tc.want)
					}
					wantOr := tc.want
					if wantOr == "" {
						wantOr = "default"
					}
					if got := GetOr(key, "default"); got != wantOr {
						t.Fatalf("GetOr = %q, want %q", got, wantOr)
					}
				})
			}
		})
	}
}

func TestNonDibsKeys(t *testing.T) {
	for _, key := range []string{"HUB_URL", "DATA_DIR", "OTHER_DIBS_ADDR"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			t.Setenv("IDEATE_"+key, "legacy")
			if got := Get(key); got != "" {
				t.Fatalf("Get = %q, want empty", got)
			}
			if got := GetOr(key, "default"); got != "default" {
				t.Fatalf("GetOr = %q", got)
			}
			t.Setenv(key, "current")
			if got := Get(key); got != "current" {
				t.Fatalf("Get = %q", got)
			}
		})
	}
}
