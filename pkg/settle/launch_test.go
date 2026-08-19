package settle

import (
	"net/url"
	"strings"
	"testing"
)

// TestNewIssueURL: the prefilled URL targets the right repo, round-trips
// title/body/label through URL encoding, and carries the Dibs footer.
func TestNewIssueURL(t *testing.T) {
	title := "💡 Great idea & more"
	body := LaunchBody("Line one.\n\nSecond ¶ with specials: ?&=#+%")
	got, truncated := NewIssueURL("kubestellar/dibs", title, body)
	if truncated {
		t.Fatal("small body must not be truncated")
	}
	if !strings.HasPrefix(got, "https://github.com/kubestellar/dibs/issues/new?") {
		t.Fatalf("url prefix: %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing built url: %v", err)
	}
	q := u.Query()
	if q.Get("title") != title {
		t.Fatalf("title round-trip: %q", q.Get("title"))
	}
	if q.Get("body") != body {
		t.Fatalf("body round-trip: %q", q.Get("body"))
	}
	if q.Get("labels") != Label {
		t.Fatalf("labels: %q", q.Get("labels"))
	}
	if !strings.Contains(q.Get("body"), Footer) {
		t.Fatalf("body missing the Dibs footer:\n%s", q.Get("body"))
	}
}

// TestLaunchBody: appends the footer exactly once.
func TestLaunchBody(t *testing.T) {
	b := LaunchBody("Body.")
	if !strings.HasSuffix(b, Footer) {
		t.Fatalf("missing footer: %q", b)
	}
	if again := LaunchBody(b); again != b {
		t.Fatalf("footer must be idempotent:\n%q\nvs\n%q", b, again)
	}
	if strings.Count(LaunchBody(LaunchBody("x")), Footer) != 1 {
		t.Fatal("footer duplicated")
	}
}

// TestNewIssueURLTruncation: an over-budget body is cut to fit, keeps the
// truncation note + footer, and reports truncated=true so the UI offers
// copy-to-clipboard of the full text.
func TestNewIssueURLTruncation(t *testing.T) {
	huge := LaunchBody(strings.Repeat("An idea so grand it overflows URLs. ", 400)) // ~15k chars
	got, truncated := NewIssueURL("org/repo", "Big one", huge)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(got) > MaxIssueURLLen {
		t.Fatalf("still over budget: %d > %d", len(got), MaxIssueURLLen)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing built url: %v", err)
	}
	body := u.Query().Get("body")
	if !strings.Contains(body, "truncated") {
		t.Fatalf("truncated body missing the paste-the-rest note:\n%.200s", body)
	}
	if !strings.HasSuffix(body, Footer) {
		t.Fatal("truncated body must still end with the Dibs footer")
	}
	if !strings.HasPrefix(body, "An idea so grand") {
		t.Fatalf("truncated body lost its beginning: %.80s", body)
	}
}

// TestNewIssueURLTruncationMultibyte: rune-safe cutting — no torn UTF-8.
func TestNewIssueURLTruncationMultibyte(t *testing.T) {
	huge := strings.Repeat("蜂蜜と🐝のアイデア ", 800)
	got, truncated := NewIssueURL("org/repo", "多バイト", huge)
	if !truncated {
		t.Fatal("expected truncation")
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing built url: %v", err)
	}
	if body := u.Query().Get("body"); strings.ContainsRune(body, '\uFFFD') {
		t.Fatal("torn multibyte rune in truncated body")
	}
}

// TestValidateIssueURL pins the confirmation validation: only a real
// https://github.com/{org}/{repo}/issues/N URL on the accepting repo passes.
func TestValidateIssueURL(t *testing.T) {
	repo := "kubestellar/dibs"
	valid := []string{
		"https://github.com/kubestellar/dibs/issues/42",
		"  https://github.com/kubestellar/dibs/issues/1 ", // whitespace trimmed
		"https://github.com/KubeStellar/Dibs/issues/7",    // GitHub is case-insensitive
		"https://www.github.com/kubestellar/dibs/issues/3",
	}
	for _, u := range valid {
		if err := ValidateIssueURL(u, repo); err != nil {
			t.Errorf("ValidateIssueURL(%q) = %v, want nil", u, err)
		}
	}
	invalid := []string{
		"",
		"not a url at all ://",
		"http://github.com/kubestellar/dibs/issues/42",        // not https
		"https://gitlab.com/kubestellar/dibs/issues/42",       // wrong host
		"https://github.com/other/repo/issues/42",             // wrong repo
		"https://github.com/kubestellar/dibs/pull/42",         // a PR, not an issue
		"https://github.com/kubestellar/dibs/issues/new",      // the form, not a filed issue
		"https://github.com/kubestellar/dibs/issues/0",        // not a positive number
		"https://github.com/kubestellar/dibs/issues/42/extra", // trailing junk
		"https://github.com/kubestellar/dibs/issues",          // no number
		"https://evil.com/https://github.com/kubestellar/dibs/issues/42",
	}
	for _, u := range invalid {
		if err := ValidateIssueURL(u, repo); err == nil {
			t.Errorf("ValidateIssueURL(%q) = nil, want error", u)
		}
	}
}
