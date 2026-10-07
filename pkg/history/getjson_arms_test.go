package history

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/registry"
)

// recordingTransport captures the outgoing request and answers it with a
// canned response, so default-base-URL handling can be asserted offline.
type recordingTransport struct {
	req  *http.Request
	resp *http.Response
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.req = req
	rt.resp.Request = req
	return rt.resp, nil
}

func TestGetJSONDefaultsToGitHubAPIWhenBaseURLEmpty(t *testing.T) {
	rt := &recordingTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("[]")),
	}}
	b := &Backfiller{Client: &http.Client{Transport: rt}}

	var out []struct{}
	if err := b.getJSON(context.Background(), "/repos/org/repo/pulls", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if rt.req == nil {
		t.Fatal("request never reached the transport")
	}
	if got := rt.req.URL.String(); got != defaultBaseURL+"/repos/org/repo/pulls" {
		t.Fatalf("url = %q, want default GitHub API base", got)
	}
	if rt.req.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization must be absent without a token, got %q", rt.req.Header.Get("Authorization"))
	}
}

func TestGetJSONRejectsMalformedBaseURL(t *testing.T) {
	b := &Backfiller{BaseURL: "http://bad host", Client: &http.Client{}}
	err := b.getJSON(context.Background(), "/repos/org/repo/pulls", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("err = %v, want request-construction error for malformed base URL", err)
	}
}

func TestGetJSONNilClientUsesDefaultAndSurfacesDialError(t *testing.T) {
	// A listener that is closed immediately yields a port nothing accepts on,
	// so the default client's Do fails with a connection error.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	b := &Backfiller{BaseURL: addr}
	err := b.getJSON(context.Background(), "/repos/org/repo/pulls", &struct{}{})
	if err == nil {
		t.Fatal("expected transport error from closed server, got nil")
	}
	if errors.Is(err, errSkipRepo) || errors.Is(err, errStatsPending) {
		t.Fatalf("transport error must not be mapped to a sentinel, got %v", err)
	}
}

func TestGetJSONStatusArms(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantIs  error
		wantMsg string
	}{
		{name: "202 stats pending", status: http.StatusAccepted, wantIs: errStatsPending},
		{name: "500 non-OK", status: http.StatusInternalServerError, wantMsg: "github returned status 500"},
		{name: "404 non-OK", status: http.StatusNotFound, wantMsg: "github returned status 404"},
		{name: "403 without rate-limit headers is not a skip", status: http.StatusForbidden, wantMsg: "github returned status 403"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			b := &Backfiller{BaseURL: srv.URL, Client: srv.Client()}
			err := b.getJSON(context.Background(), "/repos/org/repo/pulls", &struct{}{})
			if err == nil {
				t.Fatalf("status %d: expected error", tc.status)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want %v", err, tc.wantIs)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Fatalf("err = %q, want %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestFetchMergedPullRequestsPropagatesGetJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	b := &Backfiller{BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	got, err := b.FetchMergedPullRequests(context.Background(), "org/repo")
	if err == nil || !strings.Contains(err.Error(), "github returned status 502") {
		t.Fatalf("err = %v, want propagated status error", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil slice on error", got)
	}
}

func TestBackfillNilReceiverAndNilStoreAreNoOps(t *testing.T) {
	var nilB *Backfiller
	if err := nilB.Backfill(context.Background(), "org/repo"); err != nil {
		t.Fatalf("nil receiver: err = %v, want nil", err)
	}
	b := &Backfiller{BaseURL: "http://127.0.0.1:1"}
	if err := b.Backfill(context.Background(), "org/repo"); err != nil {
		t.Fatalf("nil store: err = %v, want nil (must not touch the network)", err)
	}
}

func TestBackfillReturnsContextErrorWhileSemaphoreIsFull(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	sem := b.semaphore()
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(sem); i++ {
			<-sem
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = b.Backfill(ctx, "org/repo")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if requests != 0 {
		t.Fatalf("Backfill made %d GitHub requests while blocked on the semaphore; want 0", requests)
	}
	if _, ok := st.Get("org/repo"); ok {
		t.Fatal("store must stay untouched when Backfill is cancelled before acquiring a slot")
	}
}

func TestRefreshAsyncLogsBackfillFailure(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	logged := make(chan string, 1)
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow,
		Logf: func(format string, args ...any) {
			select {
			case logged <- strings.TrimSpace(format):
			default:
			}
		}}
	b.RefreshAsync([]registry.RepoProfile{{RepoID: "org/repo"}})

	select {
	case msg := <-logged:
		if !strings.Contains(msg, "history backfill") {
			t.Fatalf("log = %q, want backfill failure message", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RefreshAsync never logged the backfill failure")
	}
	if _, ok := st.Get("org/repo"); ok {
		t.Fatal("failed backfill must not persist history")
	}
}
