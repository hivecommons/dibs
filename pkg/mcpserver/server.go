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

// NewHandler returns a stateless Streamable HTTP MCP handler.
func NewHandler(cfg Config) http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: serverName, Version: serverVersion}, nil)
	toolset := &tools{cfg: cfg}
	registerTools(server, toolset)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless:                    true,
		MaxRequestBodyBytes:          maxMCPRequestBytes,
		PropagateRequestCancellation: true,
	})
}

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
