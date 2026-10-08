// verifyFiledIssue's URL-number arm: a URL that passes nothing else but
// still lacks a numeric trailing segment is rejected with 400 before any
// GitHub lookup.
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

func TestVerifyFiledIssueRejectsNonNumericIssueURL(t *testing.T) {
	a, _ := newAPIFixture(t)
	a.Issues = &settle.FakeIssueLookup{Issues: map[string]settle.FiledIssue{}}
	idea := &store.Idea{ID: "idea-1", Author: "bob", TargetRepo: "kubestellar/dibs"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/ideas/idea-1/confirm-issue", nil)
	ok := a.verifyFiledIssue(rec, req, idea, "https://github.com/kubestellar/dibs/issues/not-a-number")
	if ok {
		t.Fatal("verifyFiledIssue accepted a URL without an issue number")
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not an issue number") {
		t.Fatalf("status=%d body=%s, want 400 'not an issue number'", rec.Code, rec.Body.String())
	}
}
