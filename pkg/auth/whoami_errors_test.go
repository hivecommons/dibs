package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// whoAmIServer answers every /whoami with the given status and raw body.
func whoAmIServer(t *testing.T, status int, body string) *HTTPHubClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WhoAmIPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &HTTPHubClient{BaseURL: srv.URL}
}

// TestHTTPHubClientMalformedBody: a 200 whose body is not the identity JSON
// (a proxy error page, a truncated response) is a hard error — never an
// authenticated identity and never ErrUnauthenticated, which would send the
// user back to log in for a hub-side fault.
func TestHTTPHubClientMalformedBody(t *testing.T) {
	c := whoAmIServer(t, http.StatusOK, "<html>bad gateway</html>")
	for name, call := range map[string]func() (*Identity, error){
		"cookie": func() (*Identity, error) { return c.WhoAmI(context.Background(), "sess") },
		"bearer": func() (*Identity, error) { return c.WhoAmIBearer(context.Background(), "tok") },
	} {
		id, err := call()
		if err == nil || id != nil {
			t.Fatalf("%s: malformed body: id=%+v err=%v, want decode error", name, id, err)
		}
		if !strings.Contains(err.Error(), "decoding hub response") {
			t.Fatalf("%s: err = %v, want decoding error", name, err)
		}
		if errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s: hub fault must not be reported as ErrUnauthenticated", name)
		}
	}
}

// TestHTTPHubClientEmptyUsername: a well-formed 200 with no username is
// rejected — a blank identity would pass the middleware and own ideas as "".
func TestHTTPHubClientEmptyUsername(t *testing.T) {
	c := whoAmIServer(t, http.StatusOK, `{"displayName":"Nobody","email":"n@example.com"}`)
	id, err := c.WhoAmI(context.Background(), "sess")
	if err == nil || id != nil {
		t.Fatalf("empty username: id=%+v err=%v, want error", id, err)
	}
	if !strings.Contains(err.Error(), "empty username") {
		t.Fatalf("err = %v, want empty-username error", err)
	}
}

// TestHTTPHubClientUnexpectedStatus: only 401/403 mean "not signed in";
// any other non-200 (hub 5xx, 404 on a mis-set HUB_URL) is an error that
// names the status and is NOT ErrUnauthenticated.
func TestHTTPHubClientUnexpectedStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway} {
		c := whoAmIServer(t, status, `{"error":"nope"}`)
		_, err := c.WhoAmIBearer(context.Background(), "tok")
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		if errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("status %d must not map to ErrUnauthenticated: %v", status, err)
		}
		if !strings.Contains(err.Error(), "unexpected status "+strconv.Itoa(status)) {
			t.Fatalf("status %d: err = %v, want it named", status, err)
		}
	}
}

// TestHTTPHubClientForbiddenIsUnauthenticated: 403 joins 401 as "sign in
// again" rather than an unexpected-status error.
func TestHTTPHubClientForbiddenIsUnauthenticated(t *testing.T) {
	c := whoAmIServer(t, http.StatusForbidden, `{"error":"forbidden"}`)
	if _, err := c.WhoAmI(context.Background(), "sess"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("403: err = %v, want ErrUnauthenticated", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestHTTPHubClientUsesInjectedClient: a caller-supplied http.Client is the
// one that talks to the hub (no real dial to the BaseURL).
func TestHTTPHubClientUsesInjectedClient(t *testing.T) {
	var calls atomic.Int64
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path != WhoAmIPath {
			t.Errorf("path = %s, want %s", r.URL.Path, WhoAmIPath)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
	c := &HTTPHubClient{BaseURL: "http://hub.invalid", Client: &http.Client{Transport: rt}}
	// An empty body decodes to an error; what matters is that the injected
	// transport served the request exactly once.
	if _, err := c.WhoAmI(context.Background(), "sess"); err == nil || !strings.Contains(err.Error(), "decoding hub response") {
		t.Fatalf("err = %v, want decoding error via injected transport", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("injected client called %d times, want 1", calls.Load())
	}
}
