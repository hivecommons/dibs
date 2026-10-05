package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func testServe(got **http.Server, err error) func(*http.Server) error {
	return func(srv *http.Server) error {
		if got != nil {
			*got = srv
		}
		return err
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	called := false
	err := run([]string{"--version"}, &out, &errOut, func(*http.Server) error { called = true; return nil })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if called {
		t.Fatal("--version must not start the server")
	}
	if want := "dibs " + gitShort + " (" + gitHash + ")\n"; out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestRunBadFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--nope"}, &out, &errOut, testServe(nil, nil)); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func setupEnv(t *testing.T, dataDir string) {
	t.Helper()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("IDEATE_ADDR", "")
	t.Setenv("DIBS_ADDR", "")
	t.Setenv("DIBS_BASE_PATH", "")
	t.Setenv("IDEATE_BASE_PATH", "")
	t.Setenv("HUB_URL", "http://127.0.0.1:1")
	t.Setenv("REPOS_SEED_FILE", "")
}

func TestRunWiring(t *testing.T) {
	setupEnv(t, t.TempDir())
	t.Setenv("DIBS_ADDR", ":0")
	var srv *http.Server
	var out, errOut bytes.Buffer
	if err := run(nil, &out, &errOut, testServe(&srv, nil)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if srv == nil || srv.Handler == nil {
		t.Fatal("server was not built")
	}
	if srv.Addr != ":0" {
		t.Fatalf("Addr = %q, want :0", srv.Addr)
	}
}

func TestRunDefaultAddr(t *testing.T) {
	setupEnv(t, t.TempDir())
	var srv *http.Server
	if err := run(nil, io.Discard, io.Discard, testServe(&srv, nil)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if srv.Addr != defaultAddr {
		t.Fatalf("Addr = %q, want %q", srv.Addr, defaultAddr)
	}
}

func TestRunServeError(t *testing.T) {
	setupEnv(t, t.TempDir())
	want := errors.New("boom")
	err := run(nil, io.Discard, io.Discard, testServe(nil, want))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrapping %v", err, want)
	}
}

func TestRunStoreOpenFailure(t *testing.T) {
	// A regular file where the data directory should be makes store open fail.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setupEnv(t, f)
	called := false
	err := run(nil, io.Discard, io.Discard, func(*http.Server) error { called = true; return nil })
	if err == nil {
		t.Fatal("expected error when DATA_DIR is not a directory")
	}
	if called {
		t.Fatal("server must not start after a store failure")
	}
}

func TestRunBadSeedFile(t *testing.T) {
	setupEnv(t, t.TempDir())
	t.Setenv("REPOS_SEED_FILE", filepath.Join(t.TempDir(), "missing.json"))
	if err := run(nil, io.Discard, io.Discard, testServe(nil, nil)); err == nil {
		t.Fatal("expected error for missing seed file")
	}
}

func TestServeUntilDrainsOnCancel(t *testing.T) {
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveUntil(ctx, srv, time.Second) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveUntil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntil did not return after cancel")
	}
}

func TestServeUntilListenError(t *testing.T) {
	srv := &http.Server{Addr: "bad-address-no-port"}
	if err := serveUntil(context.Background(), srv, time.Second); err == nil {
		t.Fatal("expected listen error")
	}
}
