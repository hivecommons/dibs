package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestTools(t *testing.T) *tools {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "kubestellar/dibs", HiveID: "h1", Owner: "alice", AcceptingIdeas: true, Topics: []string{"ideas"}}}); err != nil {
		t.Fatalf("registry.Merge: %v", err)
	}
	return &tools{cfg: Config{
		Hub: &auth.FakeHub{
			Sessions:     map[string]auth.Identity{"cookie-token": {Username: "carol", DisplayName: "Carol"}},
			BearerTokens: map[string]auth.Identity{"github-token": {Username: "alice", DisplayName: "Alice"}},
		},
		Store: st, Registry: reg, BasePath: "",
	}}
}

func callReq(token string) *mcpsdk.CallToolRequest {
	header := http.Header{"Host": []string{"dibs.example.test"}}
	if token != "" {
		header.Set(authorizationHeader, bearerPrefix+token)
	}
	return &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{Header: header}}
}

func TestSubmitIdeaUsesBearerIdentityAndStoreValidation(t *testing.T) {
	tls := newTestTools(t)
	outReq := callReq("github-token")
	_, out, err := tls.submitIdea(context.Background(), outReq, submitIdeaInput{Title: "Agent idea", Description: "Let agents send ideas", Tags: []string{"mcp", "#agents", "mcp"}})
	if err != nil {
		t.Fatalf("submitIdea: %v", err)
	}
	if out.ID == "" || out.Status != store.StatusDraft || out.URL != "https://dibs.example.test/api/ideas/"+out.ID {
		t.Fatalf("unexpected submit output: %+v", out)
	}
	if len(out.Tags) != 2 || out.Tags[0] != "mcp" || out.Tags[1] != "agents" {
		t.Fatalf("tags not normalized: %+v", out.Tags)
	}
	idea, err := tls.cfg.Store.Get(out.ID)
	if err != nil {
		t.Fatalf("Store.Get: %v", err)
	}
	if idea.Author != "alice" || idea.AuthorDisplay != "Alice" || idea.Visibility != store.VisibilityPrivate || idea.Body != "Let agents send ideas" {
		t.Fatalf("idea not created from MCP input: %+v", idea)
	}
}

func TestSubmitIdeaFallsBackToCookieValueBearer(t *testing.T) {
	tls := newTestTools(t)
	_, out, err := tls.submitIdea(context.Background(), callReq("cookie-token"), submitIdeaInput{Title: "Cookie", Description: "Copied cookie value"})
	if err != nil {
		t.Fatalf("submitIdea cookie fallback: %v", err)
	}
	idea, err := tls.cfg.Store.Get(out.ID)
	if err != nil {
		t.Fatalf("Store.Get: %v", err)
	}
	if idea.Author != "carol" {
		t.Fatalf("author = %q, want carol", idea.Author)
	}
}

func TestAuthenticatedAndReadOnlyTools(t *testing.T) {
	tls := newTestTools(t)
	created := &store.Idea{Author: "alice", AuthorDisplay: "Alice", Title: "Public", Body: "Public detail", Visibility: store.VisibilityPublic, Status: store.StatusOffered}
	if err := tls.cfg.Store.Create(created); err != nil {
		t.Fatalf("Store.Create public: %v", err)
	}
	private := &store.Idea{Author: "alice", Title: "Private", Body: "Secret", Visibility: store.VisibilityPrivate, Status: store.StatusDraft}
	if err := tls.cfg.Store.Create(private); err != nil {
		t.Fatalf("Store.Create private: %v", err)
	}

	_, repos, err := tls.listRepos(context.Background(), callReq(""), listReposInput{})
	if err != nil {
		t.Fatalf("listRepos unauthenticated: %v", err)
	}
	if len(repos.Repos) != 1 || repos.Repos[0].RepoID != "kubestellar/dibs" {
		t.Fatalf("unexpected repos: %+v", repos)
	}

	_, publicOut, err := tls.getIdea(context.Background(), callReq(""), getIdeaInput{ID: created.ID})
	if err != nil {
		t.Fatalf("get public idea unauthenticated: %v", err)
	}
	if publicOut.ID != created.ID || publicOut.Phase != store.StatusOffered {
		t.Fatalf("unexpected public idea: %+v", publicOut)
	}

	if _, _, err := tls.getIdea(context.Background(), callReq(""), getIdeaInput{ID: private.ID}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get private unauthenticated err = %v, want ErrNotFound", err)
	}

	_, mine, err := tls.listMyIdeas(context.Background(), callReq("github-token"), listMyIdeasInput{})
	if err != nil {
		t.Fatalf("listMyIdeas: %v", err)
	}
	if len(mine.Ideas) != 2 {
		t.Fatalf("listMyIdeas returned %d ideas, want 2: %+v", len(mine.Ideas), mine.Ideas)
	}
}

func TestSubmitIdeaRequiresAuthentication(t *testing.T) {
	tls := newTestTools(t)
	if _, _, err := tls.submitIdea(context.Background(), callReq(""), submitIdeaInput{Title: "Nope", Description: "No token"}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("submitIdea unauthenticated err = %v, want ErrUnauthenticated", err)
	}
}

func TestBrowserGETServesInstructions(t *testing.T) {
	h := NewHandler(Config{})
	req := httptest.NewRequest(http.MethodGet, "https://dibs.kubestellar.io/mcp", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("browser GET /mcp = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "claude mcp add") || !strings.Contains(body, "https://dibs.kubestellar.io/mcp") {
		t.Fatalf("instructions page missing connect snippet:\n%s", body)
	}
}

func TestMCPClientGETNotHijacked(t *testing.T) {
	h := NewHandler(Config{})
	req := httptest.NewRequest(http.MethodGet, "https://dibs.kubestellar.io/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "claude mcp add") {
		t.Fatal("SSE GET was served the HTML instructions page")
	}
}
