package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/mcpserver"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// dibsServer runs the real Dibs MCP handler at /mcp, authenticating the
// bearer token "hub-token" as alice.
func dibsServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewHandler(mcpserver.Config{
		Hub:  &auth.FakeHub{BearerTokens: map[string]auth.Identity{"hub-token": {Username: "alice", DisplayName: "Alice"}}},
		Deps: deps.Deps{Store: st, Registry: reg},
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

func TestDibsSinkSubmitsPrivateDraft(t *testing.T) {
	srv, st := dibsServer(t)
	sink := &DibsSink{Endpoint: srv.URL + "/mcp", Token: "hub-token"}
	if err := sink.Submit(context.Background(), Idea{Title: "Voice idea", Description: "From a voice note", Tags: []string{"plaud", "voice-note"}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := sink.Submit(context.Background(), Idea{Title: "No tags", Description: "Body"}); err != nil {
		t.Fatalf("Submit without tags: %v", err)
	}
	ideas, err := st.ListByAuthor("alice")
	if err != nil || len(ideas) != 2 {
		t.Fatalf("ideas = %d, %v", len(ideas), err)
	}
	for _, idea := range ideas {
		if idea.Status != store.StatusDraft || idea.Visibility != store.VisibilityPrivate {
			t.Fatalf("idea %q status=%s visibility=%s; want private draft", idea.Title, idea.Status, idea.Visibility)
		}
		if idea.Title == "Voice idea" && strings.Join(idea.Tags, ",") != "plaud,voice-note" {
			t.Fatalf("tags = %v", idea.Tags)
		}
	}
}

func TestDibsSinkRejectsBadToken(t *testing.T) {
	srv, _ := dibsServer(t)
	err := (&DibsSink{Endpoint: srv.URL + "/mcp", Token: "bad-token-xyz"}).Submit(context.Background(), Idea{Title: "T", Description: "D"})
	if err == nil || errors.Is(err, ErrRateLimited) {
		t.Fatalf("bad token: err = %v", err)
	}
	if strings.Contains(err.Error(), "bad-token-xyz") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestDibsSinkReports429(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	err := (&DibsSink{Endpoint: srv.URL, Token: "hub-token"}).Submit(context.Background(), Idea{Title: "T", Description: "D"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if auth != "Bearer hub-token" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestDibsSinkOtherHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := (&DibsSink{Endpoint: srv.URL, Token: "t", Transport: http.DefaultTransport}).Submit(context.Background(), Idea{Title: "T", Description: "D"})
	if err == nil || errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a non-rate-limit error", err)
	}
}

// rateLimitAfterInit lets the handshake through, then answers 429.
type rateLimitAfterInit struct {
	next http.Handler
}

func (h *rateLimitAfterInit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if bytes.Contains(body, []byte(`"tools/call"`)) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	h.next.ServeHTTP(w, r)
}

func TestDibsSinkReports429OnToolCall(t *testing.T) {
	srv, _ := dibsServer(t)
	proxy := httptest.NewServer(&rateLimitAfterInit{next: srv.Config.Handler})
	defer proxy.Close()
	err := (&DibsSink{Endpoint: proxy.URL + "/mcp", Token: "hub-token"}).Submit(context.Background(), Idea{Title: "T", Description: "D"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}
