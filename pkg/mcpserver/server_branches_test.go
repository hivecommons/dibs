package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWantsHTMLRejectsMCPClients(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"plain browser", map[string]string{"Accept": "text/html,*/*;q=0.8"}, true},
		{"no text/html", map[string]string{"Accept": "application/json"}, false},
		{"html plus event-stream is an MCP client", map[string]string{"Accept": "text/html, text/event-stream"}, false},
		{"html with session id is an MCP client", map[string]string{"Accept": "text/html", "Mcp-Session-Id": "s1"}, false},
		{"html with protocol version is an MCP client", map[string]string{"Accept": "text/html", "MCP-Protocol-Version": "2025-06-18"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := wantsHTML(req); got != tc.want {
				t.Fatalf("wantsHTML = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBearerTokenEdgeCases(t *testing.T) {
	if got := bearerToken(nil); got != "" {
		t.Fatalf("nil request token = %q, want empty", got)
	}
	if got := bearerToken(&mcpsdk.CallToolRequest{}); got != "" {
		t.Fatalf("nil Extra token = %q, want empty", got)
	}
	basic := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: http.Header{authorizationHeader: []string{"Basic abc"}}}}
	if got := bearerToken(basic); got != "" {
		t.Fatalf("non-Bearer scheme token = %q, want empty", got)
	}
	padded := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: http.Header{authorizationHeader: []string{bearerPrefix + "  tok  "}}}}
	if got := bearerToken(padded); got != "tok" {
		t.Fatalf("padded bearer token = %q, want tok", got)
	}
}

func TestIdeaURLHonorsForwardedHeadersAndBasePath(t *testing.T) {
	if got := ideaURL(nil, "", "abc"); got != "https://dibs.hivecommons.dev/api/ideas/abc" {
		t.Fatalf("nil request URL = %q", got)
	}
	if got := ideaURL(&mcpsdk.CallToolRequest{}, "/dibs/", "abc"); got != "https://dibs.hivecommons.dev/dibs/api/ideas/abc" {
		t.Fatalf("nil Extra URL with base path = %q", got)
	}

	hdr := http.Header{"Host": []string{"origin.example.test"}}
	hdr.Set(forwardedProto, " http, https ")
	hdr.Set(forwardedHost, " edge.example.test, inner.example.test ")
	req := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: hdr}}
	if got := ideaURL(req, "", "abc"); got != "http://edge.example.test/api/ideas/abc" {
		t.Fatalf("forwarded URL = %q, want first proto/host of the comma lists", got)
	}

	hostOnly := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: http.Header{"Host": []string{"origin.example.test"}}}}
	if got := ideaURL(hostOnly, "", "abc"); got != "https://origin.example.test/api/ideas/abc" {
		t.Fatalf("Host fallback URL = %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Fatalf("all-empty = %q, want empty", got)
	}
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Fatalf("firstNonEmpty = %q, want b", got)
	}
}

func TestSubmitIdeaReturnsStoreValidationError(t *testing.T) {
	tls := newTestTools(t)
	_, _, err := tls.submitIdea(context.Background(), callReq("github-token"), submitIdeaInput{Title: "   ", Description: "blank title"})
	var verr *store.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("submitIdea blank title err = %v, want *store.ValidationError", err)
	}
}

func TestListMyIdeasRequiresAuthentication(t *testing.T) {
	tls := newTestTools(t)
	if _, _, err := tls.listMyIdeas(context.Background(), callReq(""), listMyIdeasInput{}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("listMyIdeas unauthenticated err = %v, want ErrUnauthenticated", err)
	}
}

func TestListMyIdeasPropagatesStoreReadError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	tls := &tools{cfg: Config{
		Hub:  &auth.FakeHub{BearerTokens: map[string]auth.Identity{"github-token": {Username: "alice"}}},
		Deps: deps.Deps{Store: st},
	}}
	idea := &store.Idea{Author: "alice", Title: "Soon corrupt", Body: "b", Visibility: store.VisibilityPrivate}
	if err := st.Create(idea); err != nil {
		t.Fatalf("Store.Create: %v", err)
	}
	// Corrupt the on-disk record so the index still lists it but the read fails.
	if err := os.WriteFile(filepath.Join(dir, "ideas", idea.ID+".json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupting idea file: %v", err)
	}
	_, _, err = tls.listMyIdeas(context.Background(), callReq("github-token"), listMyIdeasInput{})
	if err == nil {
		t.Fatal("listMyIdeas over a corrupt record returned nil error")
	}
	if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("listMyIdeas err = %v, want the store read error, not an auth/not-found error", err)
	}
}

func TestGetIdeaUnknownIDReturnsNotFound(t *testing.T) {
	tls := newTestTools(t)
	if _, _, err := tls.getIdea(context.Background(), callReq("github-token"), getIdeaInput{ID: "does-not-exist"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("getIdea unknown id err = %v, want ErrNotFound", err)
	}
}

// failingBearerHub returns a non-auth error from WhoAmIBearer; the identity
// resolver must surface it instead of silently retrying the cookie path.
type failingBearerHub struct {
	auth.FakeHub
	err error
}

func (h *failingBearerHub) WhoAmIBearer(context.Context, string) (*auth.Identity, error) {
	return nil, h.err
}

func TestIdentityPropagatesNonAuthBearerErrors(t *testing.T) {
	hubErr := errors.New("hub unreachable")
	tls := &tools{cfg: Config{Hub: &failingBearerHub{
		FakeHub: auth.FakeHub{Sessions: map[string]auth.Identity{"github-token": {Username: "carol"}}},
		err:     hubErr,
	}}}
	_, err := tls.identity(context.Background(), callReq("github-token"))
	if !errors.Is(err, hubErr) {
		t.Fatalf("identity err = %v, want the hub transport error", err)
	}
}
