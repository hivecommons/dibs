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
	"regexp"
	"strconv"
	"strings"
)

// Footer is the attribution line issues filed on HIVE-MANAGED repos end
// with — they already have agent capacity, so no pitch is needed. It is
// public-facing text on real repos: one plain line, no flourish.
const Footer = "Listed via dibs.kubestellar.io"

// ExternalFooter is the growth-loop call-to-action appended to issues filed
// on repos NOT managed by a hive: every idea filed externally advertises
// hive to that repo's maintainers.
const ExternalFooter = "Listed via dibs.kubestellar.io.\n" +
	"This idea arrived via DIBS (https://dibs.kubestellar.io). Give this repo agent capacity by requesting a hive at https://hive.kubestellar.io; clankers, powered by donated AI tokens, implement accepted ideas. Once the repo has a hive, this issue can be assigned to it and built."

// footerFor picks the footer by hive membership of the target repo.
func footerFor(hiveManaged bool) string {
	if hiveManaged {
		return Footer
	}
	return ExternalFooter
}

// footerBlockFor is footerFor with its separator, as appended to bodies.
func footerBlockFor(hiveManaged bool) string { return "\n\n---\n" + footerFor(hiveManaged) }

// MaxIssueURLLen is the budget for a prefilled new-issue URL. Browsers and
// GitHub tolerate roughly 8k characters; stay safely under it.
const MaxIssueURLLen = 7500

// truncationNote is appended (before the footer) when the body had to be
// cut to fit the URL budget.
const truncationNote = "\n\n_(Draft truncated to fit the URL. Paste the full text from Dibs over this section.)_"

// LaunchBody returns body terminated by the Dibs footer (idempotent).
// hiveManaged selects the footer: the short attribution for hive-managed
// repos, the "request a hive" growth CTA for external ones.
func LaunchBody(body string, hiveManaged bool) string {
	body = strings.TrimRight(body, "\n ")
	if strings.HasSuffix(body, Footer) || strings.HasSuffix(body, ExternalFooter) {
		return body
	}
	return body + footerBlockFor(hiveManaged)
}

// NewIssueURL builds the prefilled GitHub new-issue URL for repoID
// ("org/name") with the ideated label. When the encoded URL would exceed
// MaxIssueURLLen the body is truncated rune-safely (a note and the footer
// matching hiveManaged are preserved) and truncated=true — callers should
// then offer the full body via copy-to-clipboard.
func NewIssueURL(repoID, title, body string, hiveManaged bool) (issueURL string, truncated bool) {
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
	tail := truncationNote + footerBlockFor(hiveManaged)
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

// repoIDPattern approximates GitHub's "org/name" shape: owners are
// alphanumeric with hyphens (max 39 chars); repo names add dots and
// underscores (max 100 chars).
var repoIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// ValidateRepoID checks that repoID looks like a real GitHub "org/name" —
// the shape required when an ideator targets an EXTERNAL (non-hive) repo.
func ValidateRepoID(repoID string) error {
	if repoIDPattern.MatchString(repoID) {
		return nil
	}
	return fmt.Errorf("settle: %q is not a GitHub repo — use the org/repo format", repoID)
}
