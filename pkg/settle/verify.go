package settle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hivecommons/dibs/pkg/store"
)

// FiledIssue is what settlement needs to know about the issue an ideator
// says they filed: who opened it, when, and whether it is really an issue.
type FiledIssue struct {
	// Author is the GitHub login that opened the issue.
	Author    string
	CreatedAt time.Time
	// PullRequest is true when the number resolves to a pull request; the
	// issues endpoint returns those too.
	PullRequest bool
	// Body is the issue text, checked for the IdeaMarker the launch footer
	// carries when no GitHub login is available to compare.
	Body string
}

// ErrIssueNotFound means GitHub has no such issue on that repo (404/410).
var ErrIssueNotFound = errors.New("settle: issue not found on GitHub")

// IssueLookup fetches an issue's attribution facts from GitHub.
type IssueLookup interface {
	GetIssue(ctx context.Context, repoID string, number int) (*FiledIssue, error)
}

// IssueNumber extracts the trailing issue number from an already-validated
// github.com/{org}/{repo}/issues/{N} URL (see ValidateIssueURL).
func IssueNumber(raw string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("settle: not a valid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 {
		return 0, fmt.Errorf("settle: not an issue URL")
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("settle: %q is not an issue number", parts[3])
	}
	return n, nil
}

// GetIssue implements IssueLookup against api.github.com. It works without a
// token (public repos, GitHub's anonymous rate limit); the token is sent
// when configured.
func (c *HTTPClient) GetIssue(ctx context.Context, repoID string, number int) (*FiledIssue, error) {
	var got struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt   time.Time       `json:"created_at"`
		PullRequest *map[string]any `json:"pull_request,omitempty"`
		Body        string          `json:"body"`
	}
	status, err := c.do(ctx, http.MethodGet, "/repos/"+repoID+"/issues/"+strconv.Itoa(number), nil, &got)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &FiledIssue{Author: got.User.Login, CreatedAt: got.CreatedAt, PullRequest: got.PullRequest != nil, Body: got.Body}, nil
	case http.StatusNotFound, http.StatusGone:
		return nil, ErrIssueNotFound
	default:
		return nil, fmt.Errorf("settle: fetching issue %s#%d: status %d", repoID, number, status)
	}
}

// githubLogin returns the GitHub login behind an idea author key, or false
// when the author signed in through another provider ("provider:sub").
func githubLogin(author string) (string, bool) {
	if rest, ok := strings.CutPrefix(author, "github:"); ok {
		return rest, rest != ""
	}
	if strings.Contains(author, ":") {
		return "", false
	}
	return author, author != ""
}

// VerifyFiledIssue checks that issue is one the ideator could have filed for
// idea through the launch flow: a real issue (not a pull request), opened no
// earlier than the idea itself, and tied to the ideator — opened by the
// same login when they signed in with GitHub, or otherwise carrying the
// IdeaMarker the launch footer embeds for this idea. A pasted URL that
// fails these checks is somebody else's issue, and settling against it
// would credit the ideator on the public wall for work that is not theirs.
func VerifyFiledIssue(issue *FiledIssue, idea *store.Idea) error {
	if issue == nil {
		return errors.New("settle: no issue to verify")
	}
	if issue.PullRequest {
		return errors.New("settle: the URL points at a pull request, not an issue")
	}
	if !idea.CreatedAt.IsZero() && issue.CreatedAt.Before(idea.CreatedAt) {
		return errors.New("settle: the issue predates this idea — file a new issue from the launch step")
	}
	login, ok := githubLogin(idea.Author)
	switch {
	case ok && !strings.EqualFold(login, issue.Author):
		return fmt.Errorf("settle: the issue was not opened by @%s", login)
	case !ok && !HasIdeaMarker(issue.Body, idea.ID):
		// No GitHub login to compare: without the marker any issue on the
		// repo filed after the idea would be claimable.
		return errors.New("settle: the issue does not carry this idea's Dibs marker — file it from the launch step, or add the marker line from the launch body to the issue")
	}
	return nil
}

// FakeIssueLookup is an in-memory IssueLookup for tests: Issues is keyed by
// "org/repo#N"; unknown keys report ErrIssueNotFound. Err, when set, is
// returned for every call (a GitHub outage).
type FakeIssueLookup struct {
	Issues map[string]FiledIssue
	Err    error
}

// GetIssue implements IssueLookup.
func (f *FakeIssueLookup) GetIssue(_ context.Context, repoID string, number int) (*FiledIssue, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	is, ok := f.Issues[repoID+"#"+strconv.Itoa(number)]
	if !ok {
		return nil, ErrIssueNotFound
	}
	cp := is
	return &cp, nil
}
