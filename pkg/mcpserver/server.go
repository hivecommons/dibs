// Package mcpserver exposes Dibs tools over the Model Context Protocol.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kubestellar/dibs/pkg/auth"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName          = "dibs"
	serverVersion       = "v0.1.0"
	toolTimeout         = 10 * time.Second
	maxMCPRequestBytes  = store.MaxBodyBytes + 64*1024
	defaultIdeaScheme   = "https"
	forwardedProto      = "X-Forwarded-Proto"
	forwardedHost       = "X-Forwarded-Host"
	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
)

// Config wires Dibs dependencies into the MCP endpoint.
type Config struct {
	Hub      auth.HubClient
	Store    *store.Store
	Registry *registry.Registry
	BasePath string
}

// NewHandler returns a stateless Streamable HTTP MCP handler. Browser GETs
// (Accept: text/html, no MCP Accept types) get a plain instructions page
// instead of the protocol's 405, so pasting the endpoint URL into a browser
// explains how to connect an agent rather than dead-ending.
func NewHandler(cfg Config) http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: serverName, Version: serverVersion}, nil)
	toolset := &tools{cfg: cfg}
	registerTools(server, toolset)
	mcp := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless:                    true,
		MaxRequestBodyBytes:          maxMCPRequestBytes,
		PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && wantsHTML(r) {
			serveInstructions(w, r)
			return
		}
		mcp.ServeHTTP(w, r)
	})
}

// wantsHTML reports whether the request is a browser page load: it accepts
// text/html and is not an MCP client (those send text/event-stream and/or
// application/json, plus protocol headers).
func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "text/html") {
		return false
	}
	if strings.Contains(accept, "text/event-stream") {
		return false
	}
	return r.Header.Get("Mcp-Session-Id") == "" && r.Header.Get("MCP-Protocol-Version") == ""
}

func serveInstructions(w http.ResponseWriter, r *http.Request) {
	endpoint := defaultIdeaScheme + "://" + r.Host + r.URL.Path
	if p := r.Header.Get(forwardedProto); p != "" {
		endpoint = p + "://" + r.Host + r.URL.Path
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprintf(w, instructionsHTML, endpoint, endpoint, endpoint)
}

const instructionsHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dibs MCP endpoint</title>
<style>
body{margin:0;min-height:100vh;background:#0d1117;color:#e6edf3;font-family:ui-sans-serif,system-ui,-apple-system,sans-serif;line-height:1.55}
main{max-width:680px;margin:0 auto;padding:48px 24px}
h1{font-size:1.4rem;margin:0 0 4px}
p{color:#a8b3c2;margin:0 0 16px}
h2{font-size:1rem;margin:28px 0 8px}
pre{background:#161b22;border:1px solid #30363d;border-radius:6px;padding:12px 14px;overflow-x:auto;font-size:.85rem}
code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
table{border-collapse:collapse;width:100%%;font-size:.9rem}
td,th{border:1px solid #30363d;padding:6px 10px;text-align:left}
th{color:#a8b3c2;font-weight:600}
a{color:#f4c75f}
</style></head><body><main>
<h1>Dibs MCP endpoint</h1>
<p>This URL is a Model Context Protocol server, not a web page. Point an MCP
client at it to submit ideas to Dibs from inside your agent.</p>
<h2>Claude Code</h2>
<pre><code>claude mcp add --transport http dibs %s \
  --header "Authorization: Bearer $(gh auth token)"</code></pre>
<h2>Generic MCP client config</h2>
<pre><code>{
  "mcpServers": {
    "dibs": {
      "type": "http",
      "url": "%s",
      "headers": { "Authorization": "Bearer &lt;token&gt;" }
    }
  }
}</code></pre>
<h2>Token</h2>
<p>Use a GitHub token for the account you sign in to the hub with
(<code>gh auth token</code> prints one). Read-only tools work without a token.</p>
<h2>Tools</h2>
<table>
<tr><th>Tool</th><th>Auth</th><th>Purpose</th></tr>
<tr><td><code>submit_idea</code></td><td>required</td><td>List an idea under your name</td></tr>
<tr><td><code>list_my_ideas</code></td><td>required</td><td>Your ideas and their status</td></tr>
<tr><td><code>get_idea</code></td><td>public: none</td><td>Idea detail and settlement state</td></tr>
<tr><td><code>list_repos</code></td><td>none</td><td>Registered repo venues</td></tr>
</table>
<p style="margin-top:24px"><a href="/">dibs.kubestellar.io</a> &middot; endpoint: <code>%s</code></p>
</main></body></html>
`

type tools struct{ cfg Config }

type submitIdeaInput struct {
	Title       string   `json:"title" jsonschema:"idea title"`
	Description string   `json:"description" jsonschema:"plain language or markdown idea description"`
	Tags        []string `json:"tags,omitempty" jsonschema:"optional short tags for the idea"`
}

type submitIdeaOutput struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Status string   `json:"status"`
	Tags   []string `json:"tags,omitempty"`
}

type listMyIdeasInput struct{}

type ideaSummary struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Phase      string    `json:"phase"`
	Visibility string    `json:"visibility"`
	Tags       []string  `json:"tags,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
	URL        string    `json:"url"`
}

type listMyIdeasOutput struct {
	Ideas []ideaSummary `json:"ideas"`
}

type getIdeaInput struct {
	ID string `json:"id" jsonschema:"Dibs idea id"`
}

type getIdeaOutput struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Author      string        `json:"author"`
	Visibility  string        `json:"visibility"`
	Status      string        `json:"status"`
	Phase       string        `json:"phase"`
	Tags        []string      `json:"tags,omitempty"`
	Matches     []store.Match `json:"matches"`
	Offers      []store.Offer `json:"offers,omitempty"`
	TargetRepo  string        `json:"targetRepo,omitempty"`
	IssueURL    string        `json:"issueURL,omitempty"`
	URL         string        `json:"url"`
}

type listReposInput struct{}

type listReposOutput struct {
	Repos []registry.RepoProfile `json:"repos"`
}

func registerTools(server *mcpsdk.Server, t *tools) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "submit_idea",
		Title:       "Submit idea",
		Description: "Create a Dibs idea owned by the authenticated caller. Requires Authorization: Bearer <token>.",
	}, t.submitIdea)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_my_ideas",
		Title:       "List my ideas",
		Description: "List the authenticated caller's Dibs ideas with lifecycle phase/status.",
	}, t.listMyIdeas)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "get_idea",
		Title:       "Get idea",
		Description: "Fetch an idea, including match and settlement state. Public ideas are readable without authentication; private ideas require ownership.",
	}, t.getIdea)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_repos",
		Title:       "List repos",
		Description: "List registered repository venues known to Dibs.",
	}, t.listRepos)
}

func (t *tools) submitIdea(ctx context.Context, req *mcpsdk.CallToolRequest, in submitIdeaInput) (*mcpsdk.CallToolResult, submitIdeaOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, submitIdeaOutput{}, err
	}
	idea := &store.Idea{
		Author:        id.Username,
		AuthorDisplay: firstNonEmpty(id.DisplayName, id.Username),
		AuthorAvatar:  id.AvatarURL,
		Title:         in.Title,
		Body:          in.Description,
		Tags:          normalizeTags(in.Tags),
		Visibility:    store.VisibilityPrivate,
		Status:        store.StatusDraft,
	}
	if err := t.cfg.Store.Create(idea); err != nil {
		return nil, submitIdeaOutput{}, err
	}
	return nil, submitIdeaOutput{ID: idea.ID, URL: ideaURL(req, t.cfg.BasePath, idea.ID), Status: idea.Status, Tags: idea.Tags}, nil
}

func (t *tools) listMyIdeas(ctx context.Context, req *mcpsdk.CallToolRequest, _ listMyIdeasInput) (*mcpsdk.CallToolResult, listMyIdeasOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, listMyIdeasOutput{}, err
	}
	ideas, err := t.cfg.Store.ListByAuthor(id.Username)
	if err != nil {
		return nil, listMyIdeasOutput{}, err
	}
	out := listMyIdeasOutput{Ideas: make([]ideaSummary, 0, len(ideas))}
	for _, idea := range ideas {
		out.Ideas = append(out.Ideas, ideaSummary{
			ID: idea.ID, Title: idea.Title, Status: idea.Status, Phase: idea.Status,
			Visibility: idea.Visibility, Tags: idea.Tags, UpdatedAt: idea.UpdatedAt,
			URL: ideaURL(req, t.cfg.BasePath, idea.ID),
		})
	}
	return nil, out, nil
}

func (t *tools) getIdea(ctx context.Context, req *mcpsdk.CallToolRequest, in getIdeaInput) (*mcpsdk.CallToolResult, getIdeaOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	idea, err := t.cfg.Store.Get(in.ID)
	if err != nil {
		return nil, getIdeaOutput{}, err
	}
	id, authErr := t.identity(ctx, req)
	if idea.Visibility != store.VisibilityPublic {
		if authErr != nil || id == nil || idea.Author != id.Username {
			return nil, getIdeaOutput{}, store.ErrNotFound
		}
	}
	return nil, getIdeaOutput{
		ID: idea.ID, Title: idea.Title, Description: idea.Body, Author: idea.Author,
		Visibility: idea.Visibility, Status: idea.Status, Phase: idea.Status, Tags: idea.Tags,
		Matches: idea.Matches, Offers: idea.Offers, TargetRepo: idea.TargetRepo, IssueURL: idea.IssueURL,
		URL: ideaURL(req, t.cfg.BasePath, idea.ID),
	}, nil
}

func (t *tools) listRepos(ctx context.Context, _ *mcpsdk.CallToolRequest, _ listReposInput) (*mcpsdk.CallToolResult, listReposOutput, error) {
	_, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	return nil, listReposOutput{Repos: t.cfg.Registry.List(false)}, nil
}

func (t *tools) identity(ctx context.Context, req *mcpsdk.CallToolRequest) (*auth.Identity, error) {
	token := bearerToken(req)
	if token == "" {
		return nil, auth.ErrUnauthenticated
	}
	if verifier, ok := t.cfg.Hub.(auth.BearerHubClient); ok {
		id, err := verifier.WhoAmIBearer(ctx, token)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return nil, err
		}
	}
	return t.cfg.Hub.WhoAmI(ctx, token)
}

func bearerToken(req *mcpsdk.CallToolRequest) string {
	if req == nil || req.Extra == nil {
		return ""
	}
	authz := req.Extra.Header.Get(authorizationHeader)
	if !strings.HasPrefix(authz, bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authz, bearerPrefix))
}

func ideaURL(req *mcpsdk.CallToolRequest, basePath, id string) string {
	scheme, host := defaultIdeaScheme, "dibs.kubestellar.io"
	if req != nil && req.Extra != nil {
		if v := strings.TrimSpace(req.Extra.Header.Get(forwardedProto)); v != "" {
			scheme = strings.Split(v, ",")[0]
		}
		if v := strings.TrimSpace(req.Extra.Header.Get(forwardedHost)); v != "" {
			host = strings.Split(v, ",")[0]
		} else if v := strings.TrimSpace(req.Extra.Header.Get("Host")); v != "" {
			host = v
		}
	}
	base := strings.TrimRight(basePath, "/")
	return fmt.Sprintf("%s://%s%s/api/ideas/%s", scheme, host, base, id)
}

func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(tag, "#"))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
