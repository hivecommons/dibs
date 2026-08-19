package settle

// This file is the DEFAULT settlement flow — Dibs as pure matchmaker.
// Instead of opening the issue server-side, Dibs hands the ideator a
// prefilled GitHub new-issue URL; they file it with their OWN GitHub
// account, so GitHub natively attributes the issue to them. Dibs then
// records the issue URL the ideator pastes back
// (accepted → issue_launched → settled).

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Footer is the attribution line every launched issue body ends with.
const Footer = "🐝 Matched via [Dibs](https://dibs.kubestellar.io) (dibs.kubestellar.io)"

// footerBlock is the footer with its separator, as appended to bodies.
const footerBlock = "\n\n---\n" + Footer

// MaxIssueURLLen is the budget for a prefilled new-issue URL. Browsers and
// GitHub tolerate roughly 8k characters; stay safely under it.
const MaxIssueURLLen = 7500

// truncationNote is appended (before the footer) when the body had to be
// cut to fit the URL budget.
const truncationNote = "\n\n_(Draft truncated to fit the URL — paste the full text from Dibs over this section.)_"

// LaunchBody returns body terminated by the Dibs footer (idempotent).
func LaunchBody(body string) string {
	body = strings.TrimRight(body, "\n ")
	if strings.HasSuffix(body, Footer) {
		return body
	}
	return body + footerBlock
}

// NewIssueURL builds the prefilled GitHub new-issue URL for repoID
// ("org/name") with the ideated label. When the encoded URL would exceed
// MaxIssueURLLen the body is truncated rune-safely (a note and the footer
// are preserved) and truncated=true — callers should then offer the full
// body via copy-to-clipboard.
func NewIssueURL(repoID, title, body string) (issueURL string, truncated bool) {
	build := func(b string) string {
		q := url.Values{}
		q.Set("title", title)
		q.Set("body", b)
		q.Set("labels", Label)
		return "https://github.com/" + repoID + "/issues/new?" + q.Encode()
	}
	full := build(body)
	if len(full) <= MaxIssueURLLen {
		return full, false
	}
	tail := truncationNote + footerBlock
	runes := []rune(body)
	// Binary-search the longest body prefix whose encoded URL fits.
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(build(string(runes[:mid])+"…"+tail)) <= MaxIssueURLLen {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return build(string(runes[:lo]) + "…" + tail), true
}

// ValidateIssueURL checks that raw is a real GitHub issue URL on repoID —
// https://github.com/{org}/{repo}/issues/{N} — the shape the ideator pastes
// back to confirm they filed the issue.
func ValidateIssueURL(raw, repoID string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("settle: not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("settle: issue URL must use https")
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return fmt.Errorf("settle: issue URL must be on github.com")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "issues" {
		return fmt.Errorf("settle: expected an URL like https://github.com/%s/issues/<number>", repoID)
	}
	if !strings.EqualFold(parts[0]+"/"+parts[1], repoID) {
		return fmt.Errorf("settle: issue URL is not on the accepting repo %s", repoID)
	}
	if n, err := strconv.Atoi(parts[3]); err != nil || n <= 0 {
		return fmt.Errorf("settle: %q is not an issue number", parts[3])
	}
	return nil
}
