package settle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubestellar/ideate/pkg/store"
)

func testIdea() *store.Idea {
	return &store.Idea{
		ID: "abc123", Author: "josh", AuthorDisplay: "Josh Q",
		Title: "Great idea", Body: "Full body text.", TLDR: "The tldr.",
		Visibility: store.VisibilityPublic, Status: store.StatusAccepted,
	}
}

// TestSettleWithFake: the credited issue carries attribution, TLDR, full
// text, and the ideated label; the label is ensured first.
func TestSettleWithFake(t *testing.T) {
	fake := &Fake{}
	s := &Settler{GitHub: fake}
	url, err := s.Settle(context.Background(), testIdea(), "org/repo")
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if url == "" {
		t.Fatal("expected an issue URL")
	}
	if len(fake.Labels) != 1 || fake.Labels[0] != "org/repo/"+Label {
		t.Fatalf("label not ensured: %+v", fake.Labels)
	}
	if len(fake.Issues) != 1 {
		t.Fatalf("issues: %+v", fake.Issues)
	}
	is := fake.Issues[0]
	if is.RepoID != "org/repo" || is.Title != "💡 Great idea" {
		t.Fatalf("issue: %+v", is)
	}
	for _, want := range []string{"@josh", "Josh Q", "The tldr.", "Full body text.", "abc123"} {
		if !strings.Contains(is.Body, want) {
			t.Errorf("issue body missing %q:\n%s", want, is.Body)
		}
	}
	if len(is.Labels) != 1 || is.Labels[0] != Label {
		t.Fatalf("labels: %+v", is.Labels)
	}
}

// TestSettleUnconfigured: nil settler / nil client → ErrNoGitHub, never a
// panic.
func TestSettleUnconfigured(t *testing.T) {
	var nilSettler *Settler
	if _, err := nilSettler.Settle(context.Background(), testIdea(), "org/repo"); err != ErrNoGitHub {
		t.Fatalf("nil settler: %v", err)
	}
	if _, err := (&Settler{}).Settle(context.Background(), testIdea(), "org/repo"); err != ErrNoGitHub {
		t.Fatalf("nil client: %v", err)
	}
}

// TestHTTPClient: label 422 (already exists) is success; issue creation
// returns the html_url; the token rides along.
func TestHTTPClient(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/repos/org/repo/labels":
			w.WriteHeader(http.StatusUnprocessableEntity) // label exists
		case "/repos/org/repo/issues":
			var in struct {
				Title  string   `json:"title"`
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Title == "" || len(in.Labels) != 1 || in.Labels[0] != Label {
				t.Errorf("bad issue payload: %+v", in)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"html_url": "https://github.com/org/repo/issues/7"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &HTTPClient{Token: "tok", BaseURL: srv.URL}
	s := &Settler{GitHub: c}
	url, err := s.Settle(context.Background(), testIdea(), "org/repo")
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if url != "https://github.com/org/repo/issues/7" {
		t.Fatalf("url: %q", url)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth header: %q", gotAuth)
	}
}
