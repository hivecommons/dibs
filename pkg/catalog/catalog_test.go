package catalog

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

const landscapeFixture = `
landscape:
  - category:
    name: Service Proxy
    subcategories:
      - subcategory:
        name: Service Mesh
        items:
          - item:
            name: Istio
            description: Service mesh sidecar proxy for Kubernetes traffic
            repo_url: https://github.com/istio/istio
            project: graduated
          - item:
            name: External Thing
            repo_url: https://example.com/not/github
            project: sandbox
          - item:
            name: Non CNCF
            repo_url: https://github.com/acme/noncncf
      - subcategory:
        name: Empty Items
        items:
  - category:
    name: Database
    subcategories:
      - subcategory:
        name: SQL
        items:
          - item:
            name: Vitess
            description: Database clustering for MySQL
            repo_url: https://github.com/vitessio/vitess.git
            project: incubating
`

func TestParseLandscapeFixture(t *testing.T) {
	projects, err := ParseLandscape([]byte(landscapeFixture))
	if err != nil {
		t.Fatalf("ParseLandscape: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(projects), projects)
	}
	byRepo := map[string]Project{}
	for _, p := range projects {
		byRepo[p.RepoID] = p
	}
	istio := byRepo["istio/istio"]
	if istio.Name != "Istio" || istio.Maturity != "graduated" || istio.Category != "Service Proxy" || istio.Subcategory != "Service Mesh" {
		t.Fatalf("istio project = %+v", istio)
	}
	if _, ok := byRepo["vitessio/vitess"]; !ok {
		t.Fatalf("missing vitess project: %+v", projects)
	}
}

func TestBM25RanksServiceMeshOverDatabase(t *testing.T) {
	projects := []Project{
		{Name: "Istio", Description: "Service mesh sidecar proxy for Kubernetes traffic management", Topics: []string{"service-mesh", "proxy"}, RepoID: "istio/istio"},
		{Name: "Postgres", Description: "Relational database storage engine and SQL query planner", Topics: []string{"database", "sql"}, RepoID: "postgres/postgres"},
	}
	got := NewBM25(projects).TopK("service mesh sidecar proxy", 2)
	if len(got) < 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Project.RepoID != "istio/istio" {
		t.Fatalf("top = %+v, want istio first", got[0])
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("istio score %v <= database score %v", got[0].Score, got[1].Score)
	}
}

func TestCatalogJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	projects := []Project{{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Topics: []string{"service-mesh"}}}
	data, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/"+CacheFile, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s, err := New(dir, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list := s.List()
	if len(list) != 1 || list[0].RepoID != "istio/istio" {
		t.Fatalf("List = %+v", list)
	}
	if got := s.TopK("service mesh", 1); len(got) != 1 || got[0].Project.RepoID != "istio/istio" {
		t.Fatalf("TopK after load = %+v", got)
	}
}

func TestSmokeLandscapeFetchParse(t *testing.T) {
	projects, err := FetchLandscape(context.Background(), nil)
	if err != nil {
		t.Skipf("network unavailable or landscape fetch failed: %v", err)
	}
	if len(projects) < 200 {
		t.Fatalf("parsed %d CNCF projects, want at least 200", len(projects))
	}
}
