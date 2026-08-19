package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHubServer stands up an httptest hub implementing the whoami contract.
func fakeHubServer(t *testing.T, sessions map[string]Identity) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WhoAmIPath {
			http.NotFound(w, r)
			return
		}
		c, err := r.Cookie(SessionCookieName)
		if err != nil {
			http.Error(w, `{"error":"no session"}`, http.StatusUnauthorized)
			return
		}
		id, ok := sessions[c.Value]
		if !ok {
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(id)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPHubClientWhoAmI(t *testing.T) {
	hub := fakeHubServer(t, map[string]Identity{
		"good-session": {Username: "alice", DisplayName: "Alice A", Email: "a@example.com", AvatarURL: "https://img/a.png"},
	})
	c := &HTTPHubClient{BaseURL: hub.URL}

	id, err := c.WhoAmI(context.Background(), "good-session")
	if err != nil {
		t.Fatalf("valid session: %v", err)
	}
	if id.Username != "alice" || id.DisplayName != "Alice A" || id.Email != "a@example.com" || id.AvatarURL != "https://img/a.png" {
		t.Fatalf("identity mismatch: %+v", id)
	}

	if _, err := c.WhoAmI(context.Background(), "expired-session"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session: want ErrUnauthenticated, got %v", err)
	}
	if _, err := c.WhoAmI(context.Background(), ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("empty session: want ErrUnauthenticated, got %v", err)
	}
}

func newTestMiddleware(hub HubClient) *Middleware {
	return &Middleware{
		Hub:    hub,
		HubURL: "https://hive.kubestellar.io",
		IsAPI:  func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/ideas/api/") },
	}
}

func echoIdentity() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := FromContext(r.Context())
		if id == nil {
			http.Error(w, "no identity on context", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(id.Username))
	})
}

func TestMiddlewareValidSession(t *testing.T) {
	hub := fakeHubServer(t, map[string]Identity{"s1": {Username: "bob"}})
	mw := newTestMiddleware(&HTTPHubClient{BaseURL: hub.URL})

	req := httptest.NewRequest("GET", "/ideas/api/ideas", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "s1"})
	rec := httptest.NewRecorder()
	mw.Wrap(echoIdentity()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "bob" {
		t.Fatalf("identity not attached: %q", rec.Body.String())
	}
}

func TestMiddlewareNoCookieAPI(t *testing.T) {
	hub := fakeHubServer(t, map[string]Identity{})
	mw := newTestMiddleware(&HTTPHubClient{BaseURL: hub.URL})

	req := httptest.NewRequest("GET", "/ideas/api/ideas", nil)
	rec := httptest.NewRecorder()
	mw.Wrap(echoIdentity()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 body is not JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("401 body missing error field: %v", body)
	}
}

func TestMiddlewareExpiredSessionPage(t *testing.T) {
	hub := fakeHubServer(t, map[string]Identity{}) // every session is expired
	mw := newTestMiddleware(&HTTPHubClient{BaseURL: hub.URL})

	req := httptest.NewRequest("GET", "/ideas/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "stale"})
	rec := httptest.NewRecorder()
	mw.Wrap(echoIdentity()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hive.kubestellar.io") || !strings.Contains(body, "Sign in") {
		t.Fatalf("interstitial missing sign-in link: %s", body)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page rejection should be HTML, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestMiddlewareHubDown(t *testing.T) {
	// Point at a closed server: middleware must fail closed with 502, not 500
	// or (worse) pass through.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	mw := newTestMiddleware(&HTTPHubClient{BaseURL: srv.URL})

	req := httptest.NewRequest("GET", "/ideas/api/ideas", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "s1"})
	rec := httptest.NewRecorder()
	mw.Wrap(echoIdentity()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
