package main

import "testing"

func TestEnvOr(t *testing.T) {
	t.Run("prefers the DIBS_ key when set", func(t *testing.T) {
		t.Setenv("DIBS_ADDR", ":9999")
		t.Setenv("IDEATE_ADDR", ":1111")
		if got := envOr("DIBS_ADDR", defaultAddr); got != ":9999" {
			t.Fatalf("envOr = %q, want %q", got, ":9999")
		}
	})

	t.Run("falls back to the legacy IDEATE_ prefix", func(t *testing.T) {
		t.Setenv("DIBS_ADDR", "")
		t.Setenv("IDEATE_ADDR", ":1111")
		if got := envOr("DIBS_ADDR", defaultAddr); got != ":1111" {
			t.Fatalf("envOr = %q, want %q", got, ":1111")
		}
	})

	t.Run("returns the default when neither is set", func(t *testing.T) {
		t.Setenv("DIBS_ADDR", "")
		t.Setenv("IDEATE_ADDR", "")
		if got := envOr("DIBS_ADDR", defaultAddr); got != defaultAddr {
			t.Fatalf("envOr = %q, want %q", got, defaultAddr)
		}
	})

	t.Run("non-DIBS keys never consult the legacy prefix", func(t *testing.T) {
		t.Setenv("HUB_URL", "")
		t.Setenv("IDEATE_HUB_URL", "https://legacy.example")
		if got := envOr("HUB_URL", defaultHubURL); got != defaultHubURL {
			t.Fatalf("envOr = %q, want %q", got, defaultHubURL)
		}
	})
}

func TestDisplayBasePath(t *testing.T) {
	if got := displayBasePath(""); got != "/" {
		t.Fatalf("displayBasePath(\"\") = %q, want %q", got, "/")
	}
	if got := displayBasePath("/dibs"); got != "/dibs" {
		t.Fatalf("displayBasePath(\"/dibs\") = %q, want %q", got, "/dibs")
	}
}
