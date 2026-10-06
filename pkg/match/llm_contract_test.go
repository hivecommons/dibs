package match

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hivecommons/dibs/pkg/registry"
)

// recordingGateway captures the request the LLM client sends so the wire
// contract with the litellm gateway (auth header, model, message roles)
// can be asserted, and answers with the given status and raw body.
type recordedRequest struct {
	Path   string
	Header http.Header
}

func recordingGateway(t *testing.T, status int, body string) (*httptest.Server, *recordedRequest, *chatRequest) {
	t.Helper()
	got := &recordedRequest{}
	payload := &chatRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		got.Header = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(payload)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got, payload
}

const okReply = `{"choices":[{"message":{"content":"  42  "}}]}`

// TestChatSendsBearerAndModel: the gateway receives the configured model,
// a system+user message pair in that order, and the API key as a Bearer
// token — the contract hive's litellm gateway authenticates on.
func TestChatSendsBearerAndModel(t *testing.T) {
	srv, req, payload := recordingGateway(t, http.StatusOK, okReply)
	l := &LLM{BaseURL: srv.URL + "/v1", APIKey: "sekrit", Model: "router-alias"}
	out, err := l.Chat(context.Background(), "be terse", "score this")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "42" {
		t.Fatalf("Chat = %q, want trimmed %q", out, "42")
	}
	if req.Path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sekrit" {
		t.Fatalf("Authorization = %q, want Bearer token", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if payload.Model != "router-alias" {
		t.Fatalf("model = %q, want router-alias", payload.Model)
	}
	if len(payload.Messages) != 2 ||
		payload.Messages[0].Role != "system" || payload.Messages[0].Content != "be terse" ||
		payload.Messages[1].Role != "user" || payload.Messages[1].Content != "score this" {
		t.Fatalf("messages = %+v, want [system, user]", payload.Messages)
	}
}

// TestChatOmitsAuthorizationWithoutKey: an unset DIBS_LLM_API_KEY must not
// leak an empty "Bearer " header — gateways reject malformed auth.
func TestChatOmitsAuthorizationWithoutKey(t *testing.T) {
	srv, req, _ := recordingGateway(t, http.StatusOK, okReply)
	l := &LLM{BaseURL: srv.URL, Model: "m"}
	if _, err := l.Chat(context.Background(), "s", "u"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, present := req.Header["Authorization"]; present {
		t.Fatalf("Authorization header sent without an API key: %q", req.Header.Get("Authorization"))
	}
}

// TestChatMalformedResponse: a 200 whose body is not JSON is an error, not
// an empty reply — callers must fall back, not cache garbage.
func TestChatMalformedResponse(t *testing.T) {
	srv, _, _ := recordingGateway(t, http.StatusOK, `<html>gateway proxy page</html>`)
	l := &LLM{BaseURL: srv.URL, Model: "m"}
	_, err := l.Chat(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "decoding llm response") {
		t.Fatalf("malformed body: err = %v, want decoding error", err)
	}
}

// TestChatNoChoices: a well-formed envelope with zero choices (filtered
// completion, exhausted quota) is surfaced as an error.
func TestChatNoChoices(t *testing.T) {
	srv, _, _ := recordingGateway(t, http.StatusOK, `{"choices":[]}`)
	l := &LLM{BaseURL: srv.URL, Model: "m"}
	_, err := l.Chat(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("empty choices: err = %v, want no-choices error", err)
	}
}

// TestChatNon200IncludesBody: gateway error status and the (bounded) body
// both reach the log line so operators can see WHY litellm refused.
func TestChatNon200IncludesBody(t *testing.T) {
	srv, _, _ := recordingGateway(t, http.StatusServiceUnavailable, `{"error":"no healthy deployments"}`)
	l := &LLM{BaseURL: srv.URL, Model: "m"}
	_, err := l.Chat(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "returned 503") || !strings.Contains(err.Error(), "no healthy deployments") {
		t.Fatalf("503: err = %v, want status and body", err)
	}
}

// TestChatUsesInjectedClient: a caller-supplied http.Client is honored
// (tests and the fake hub rely on this to avoid real sockets).
func TestChatUsesInjectedClient(t *testing.T) {
	var calls atomic.Int64
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
	l := &LLM{BaseURL: "http://gateway.invalid/v1", Model: "m", Client: &http.Client{Transport: rt}}
	// An empty body is a decode error — what matters here is that the
	// injected transport, not a real dial to gateway.invalid, served it.
	if _, err := l.Chat(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "decoding llm response") {
		t.Fatalf("err = %v, want decoding error from injected transport", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("injected client called %d times, want 1", calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// refineOutcome runs Refine against a gateway replying with reply and
// returns the single refine telemetry outcome recorded.
func refineOutcome(t *testing.T, reply string) (*RefinedDraft, string) {
	t.Helper()
	srv := fakeLLMServer(t, reply, nil)
	defer srv.Close()
	e := &Engine{LLM: &LLM{BaseURL: srv.URL, Model: "test"}}
	llmStats.snapshotAndReset()
	d := e.Refine(context.Background(), "rough", "rough body", nil)
	outcome := ""
	for _, s := range llmStats.snapshotAndReset() {
		if s.Op == opRefine {
			if outcome != "" {
				t.Fatalf("multiple refine outcomes recorded: %s and %s", outcome, s.Outcome)
			}
			outcome = s.Outcome
		}
	}
	return d, outcome
}

// TestRefineEmptyReplyRecorded: a blank reply skips refinement AND is
// counted as llm_empty, so the fallback rate in metrics tells empty from
// unparsable.
func TestRefineEmptyReplyRecorded(t *testing.T) {
	d, outcome := refineOutcome(t, "   \n  ")
	if d != nil {
		t.Fatalf("empty reply must yield nil draft, got %+v", d)
	}
	if outcome != outcomeLLMEmpty {
		t.Fatalf("outcome = %q, want %q", outcome, outcomeLLMEmpty)
	}
}

// TestRefineUnparsableReplyRecorded: a one-line reply (no body) is skipped
// and counted as unparsable.
func TestRefineUnparsableReplyRecorded(t *testing.T) {
	d, outcome := refineOutcome(t, "Just a title, no blank line, no body")
	if d != nil {
		t.Fatalf("unparsable reply must yield nil draft, got %+v", d)
	}
	if outcome != outcomeUnparsable {
		t.Fatalf("outcome = %q, want %q", outcome, outcomeUnparsable)
	}
}

// TestParseRefinedEmptyTitleAfterStrip: a reply whose first line is only a
// "Title:" label (nothing after the prefix is stripped) is rejected even
// though it has a body.
func TestParseRefinedEmptyTitleAfterStrip(t *testing.T) {
	for _, in := range []string{"Title:\n\nA real body.", "# \n\nA real body.", "#Title:  \n\nbody"} {
		if d := parseRefined(in); d != nil {
			t.Fatalf("parseRefined(%q) = %+v, want nil for empty title", in, d)
		}
	}
}

// TestMatchesForIdeaCappedAtMaxMatches: with more candidate repos than
// MaxMatches the result is truncated to the top-scoring MaxMatches, sorted
// by score descending — the LLM cost control the constant documents.
func TestMatchesForIdeaCappedAtMaxMatches(t *testing.T) {
	st, reg := newFixtures(t)
	n := MaxMatches + 5
	repos := make([]registry.RepoProfile, 0, n)
	for i := 0; i < n; i++ {
		// Half of each profile's terms miss the idea, so the fallback
		// scorer's matched/total ratio sits well below the winner's.
		repos = append(repos, registry.RepoProfile{
			RepoID:      fmt.Sprintf("org/repo-%02d", i),
			Owner:       "bob",
			Description: "multicluster scheduling placement telemetry dashboards grafana",
			Topics:      []string{"kubernetes", "observability"},
		})
	}
	// One repo's profile is drawn entirely from the idea's own words so the
	// cap has a definite winner to keep.
	repos[n-1].Description = "kubernetes multicluster scheduling placement policies clusters"
	repos[n-1].Topics = []string{"kubernetes", "multicluster", "scheduling"}
	if err := reg.Merge(repos); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "Kubernetes multicluster scheduling", "Schedule workloads across clusters with placement policies.")
	ms, err := e.MatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("MatchesForIdea: %v", err)
	}
	if len(ms) != MaxMatches {
		t.Fatalf("len(matches) = %d, want MaxMatches=%d (registry has %d repos)", len(ms), MaxMatches, n)
	}
	for i := 1; i < len(ms); i++ {
		if ms[i-1].Score < ms[i].Score {
			t.Fatalf("matches not sorted desc at %d: %v then %v", i, ms[i-1].Score, ms[i].Score)
		}
	}
	if ms[0].RepoID != repos[n-1].RepoID {
		t.Fatalf("top match = %s, want the high-overlap repo %s", ms[0].RepoID, repos[n-1].RepoID)
	}
	kept, err := st.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(kept.Matches) != MaxMatches {
		t.Fatalf("persisted %d matches, want the capped %d", len(kept.Matches), MaxMatches)
	}
}
