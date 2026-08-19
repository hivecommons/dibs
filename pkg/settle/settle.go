// Package settle turns an accepted idea into a credited GitHub issue on the
// accepting repo. Dibs is just the matchmaker, so the DEFAULT flow (see
// launch.go) never touches the GitHub API: it builds a prefilled
// github.com/{org}/{repo}/issues/new URL the ideator opens and files with
// their own GitHub account — native attribution — then validates the issue
// URL they paste back to complete settlement.
//
// The token-based path below (Client/HTTPClient/Settler) is a demoted
// OPTIONAL LEGACY mode: only when DIBS_GITHUB_TOKEN is set does Dibs open
// the issue server-side on accept, with an attribution line and the
// `ideated` label. GitHub access is abstracted behind the Client interface
// with a fake for tests.
package settle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kubestellar/dibs/pkg/store"
)

// EnvGitHubToken configures the GitHub PAT for the LEGACY server-side
// settlement mode. Unset (the default) means the matchmaker URL flow:
// the ideator files the prefilled issue themselves.
const EnvGitHubToken = "DIBS_GITHUB_TOKEN"

// Label is applied to every settled issue.
const Label = "ideated"

// LabelColor is the amber from the Dibs/hive palette.
const LabelColor = "f4c75f"

// LabelDescription documents the label on GitHub.
const LabelDescription = "Idea contributed through Dibs — creators get credit, agents do the work"

// ErrNoGitHub means settlement is not configured (no token).
var ErrNoGitHub = errors.New("settle: no GitHub client configured (set " + EnvGitHubToken + ")")

// Client is the GitHub surface settlement needs.
type Client interface {
	// EnsureLabel makes sure the label exists on repoID ("org/name");
	// idempotent.
	EnsureLabel(ctx context.Context, repoID, name, color, description string) error
	// CreateIssue opens an issue and returns its HTML URL.
	CreateIssue(ctx context.Context, repoID, title, body string, labels []string) (string, error)
}

const githubRequestTimeout = 15 * time.Second

// maxGitHubResponse bounds GitHub responses we parse.
const maxGitHubResponse = 1 << 20

// HTTPClient is the production Client (PAT auth against api.github.com).
type HTTPClient struct {
	Token   string
	BaseURL string // defaults to https://api.github.com
	Client  *http.Client
}

// FromEnv returns an HTTPClient from DIBS_GITHUB_TOKEN (legacy
// IDEATE_GITHUB_TOKEN still honored), or nil if unset.
func FromEnv() *HTTPClient {
	tok := strings.TrimSpace(os.Getenv(EnvGitHubToken))
	if tok == "" {
		tok = strings.TrimSpace(os.Getenv("IDEATE_GITHUB_TOKEN"))
	}
	if tok == "" {
		return nil
	}
	return &HTTPClient{Token: tok}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, payload, out any) (int, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("settle: marshaling: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return 0, fmt.Errorf("settle: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: githubRequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("settle: github unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponse))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("settle: reading github response: %w", err)
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("settle: decoding github response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// EnsureLabel implements Client. A 422 from the create call means the label
// already exists — success.
func (c *HTTPClient) EnsureLabel(ctx context.Context, repoID, name, color, description string) error {
	status, err := c.do(ctx, http.MethodPost, "/repos/"+repoID+"/labels", map[string]string{
		"name": name, "color": color, "description": description,
	}, nil)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusUnprocessableEntity {
		return nil
	}
	return fmt.Errorf("settle: creating label on %s: status %d", repoID, status)
}

// CreateIssue implements Client.
func (c *HTTPClient) CreateIssue(ctx context.Context, repoID, title, body string, labels []string) (string, error) {
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	status, err := c.do(ctx, http.MethodPost, "/repos/"+repoID+"/issues", map[string]any{
		"title": title, "body": body, "labels": labels,
	}, &created)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("settle: creating issue on %s: status %d", repoID, status)
	}
	return created.HTMLURL, nil
}

// Fake is an in-memory Client for tests.
type Fake struct {
	Labels  []string // "repoID/name"
	Issues  []FakeIssue
	FailErr error // when set, every call fails with this error
}

// FakeIssue records one CreateIssue call.
type FakeIssue struct {
	RepoID string
	Title  string
	Body   string
	Labels []string
	URL    string
}

// EnsureLabel implements Client.
func (f *Fake) EnsureLabel(_ context.Context, repoID, name, _, _ string) error {
	if f.FailErr != nil {
		return f.FailErr
	}
	f.Labels = append(f.Labels, repoID+"/"+name)
	return nil
}

// CreateIssue implements Client.
func (f *Fake) CreateIssue(_ context.Context, repoID, title, body string, labels []string) (string, error) {
	if f.FailErr != nil {
		return "", f.FailErr
	}
	url := fmt.Sprintf("https://github.com/%s/issues/%d", repoID, len(f.Issues)+1)
	f.Issues = append(f.Issues, FakeIssue{RepoID: repoID, Title: title, Body: body, Labels: labels, URL: url})
	return url, nil
}

// Settler opens credited issues. GitHub == nil means not configured.
type Settler struct {
	GitHub Client
}

// IssueTitle builds the settled issue's title.
func IssueTitle(idea *store.Idea) string {
	return "💡 " + idea.Title
}

// IssueBody builds the settled issue's body: attribution, TLDR, full text.
func IssueBody(idea *store.Idea) string {
	handle := idea.Author
	display := idea.AuthorDisplay
	if display == "" {
		display = handle
	}
	var b strings.Builder
	fmt.Fprintf(&b, "> 💡 **Idea by @%s** (%s) via [Dibs](https://dibs.kubestellar.io) — creators get credit, agents do the work, projects take the bow.\n\n", handle, display)
	if idea.TLDR != "" {
		fmt.Fprintf(&b, "**TLDR:** %s\n\n---\n\n", idea.TLDR)
	}
	b.WriteString(idea.Body)
	fmt.Fprintf(&b, "\n\n---\n_Opened automatically by Dibs (idea `%s`)._\n", idea.ID)
	return b.String()
}

// Settle opens the credited issue on repoID and returns its URL. It does NOT
// mutate the idea — the caller records the state transition.
func (s *Settler) Settle(ctx context.Context, idea *store.Idea, repoID string) (string, error) {
	if s == nil || s.GitHub == nil {
		return "", ErrNoGitHub
	}
	if err := s.GitHub.EnsureLabel(ctx, repoID, Label, LabelColor, LabelDescription); err != nil {
		return "", err
	}
	return s.GitHub.CreateIssue(ctx, repoID, IssueTitle(idea), IssueBody(idea), []string{Label})
}
