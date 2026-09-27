package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoTickerSymbolDerivation(t *testing.T) {
	cases := map[string]string{
		"kubestellar/hive":             "HIVE",
		"projectbluefin/bluefin":       "BLUF",
		"destiny/destiny-videos":       "DSTV",
		"IBM/mcp-context-forge":        "MCPF",
		"example/web":                  "WEBX",
		"open-horizon/open-horizon.io": "OHIP",
	}
	for repoID, want := range cases {
		if got := RepoTickerSymbol(repoID); got != want {
			t.Errorf("RepoTickerSymbol(%q) = %q, want %q", repoID, got, want)
		}
	}
}

func TestRepoTickerSymbolRealFleet(t *testing.T) {
	cases := map[string]string{
		"IBM/mcp-context-forge":                               "MCPF",
		"TradingAsBuddies/falcon-core":                        "FLCC",
		"TradingAsBuddies/falcon-gateway":                     "FLCG",
		"TradingAsBuddies/falcon-messenger":                   "FLCM",
		"TradingAsBuddies/falcon-operator":                    "FLCO",
		"TradingAsBuddies/falcon-signal-web":                  "FSWF",
		"TradingAsBuddies/falcon-stats":                       "FLCS",
		"ai-native-systems-research/ai-native-storage-certus": "ANSC",
		"aslom/hive-agent":                                    "HAHV",
		"castrojo/endusers":                                   "ENDS",
		"hashicorp/dev-portal":                                "DPDV",
		"inference-sim/sim2real":                              "SRSM",
		"jeejz/incubator-kie-drools":                          "IKDN",
		"jeejz/incubator-kie-kogito-apps":                     "IKKA",
		"jeejz/incubator-kie-tools":                           "IKTN",
		"jumppad-labs/spektacular":                            "SPEK",
		"jumppad-labs/spektacular-gc":                         "SPKG",
		"kubestellar/console":                                 "CONS",
		"kubestellar/console-kb":                              "CNSK",
		"kubestellar/console-marketplace":                     "CNSM",
		"kubestellar/docs":                                    "DOCS",
		"kubestellar/hive":                                    "HIVE",
		"kubestellar/homebrew-tap":                            "HMBT",
		"kubestellar/kubestellar-mcp":                         "KBSM",
		"open-horizon-services/Getting-Started":               "GTTS",
		"open-horizon-services/service-contextforge":          "SRVC",
		"open-horizon-services/web-helloworld-python":         "WHPW",
		"open-horizon/.github":                                "GITH",
		"open-horizon/open-horizon.github.io":                 "OHGI",
		"projectbluefin/actions":                              "ACTN",
		"projectbluefin/bluefin":                              "BLUF",
		"projectbluefin/bluefin-lts":                          "BLFL",
		"projectbluefin/common":                               "COMM",
		"projectbluefin/dakota":                               "DAKT",
		"projectbluefin/dakota-iso":                           "DKTI",
		"projectbluefin/finpilot":                             "FINP",
		"projectbluefin/fsdk-containers":                      "FSDC",
		"projectbluefin/server":                               "SERV",
		"projectbluefin/testsuite":                            "TEST",
	}
	for repoID, want := range cases {
		if got := RepoTickerSymbol(repoID); got != want {
			t.Errorf("RepoTickerSymbol(%q) = %q, want %q", repoID, got, want)
		}
	}
}

func TestUniqueRepoTickerSymbolCollisionSequence(t *testing.T) {
	taken := map[string]bool{"HIVE": true}
	got := UniqueRepoTickerSymbol("org/hive", func(s string) bool { return taken[s] })
	if got != "HIVH" {
		t.Fatalf("first collision = %q, want HIVH", got)
	}
	taken[got] = true
	got = UniqueRepoTickerSymbol("org/hive", func(s string) bool { return taken[s] })
	if got != "HIVI" {
		t.Fatalf("second collision = %q, want HIVI", got)
	}
}

func TestUniqueRepoTickerSymbolTwoLetterTail(t *testing.T) {
	// Every base[:3]+letter candidate taken: fall back to base[:2] + two-letter tail.
	taken := func(s string) bool { return strings.HasPrefix(s, "HIV") }
	if got := UniqueRepoTickerSymbol("org/hive", taken); got != "HIAA" {
		t.Fatalf("two-letter tail fallback = %q, want HIAA", got)
	}
}

func TestUniqueRepoTickerSymbolFullScan(t *testing.T) {
	// Every HI?? candidate also taken: fall back to the full 4-letter scan.
	taken := func(s string) bool { return strings.HasPrefix(s, "HI") }
	if got := UniqueRepoTickerSymbol("org/hive", taken); got != "AAAA" {
		t.Fatalf("full-scan fallback = %q, want AAAA", got)
	}
}

func TestUniqueRepoTickerSymbolExhaustionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when every 4-letter symbol is taken")
		}
	}()
	UniqueRepoTickerSymbol("org/hive", func(string) bool { return true })
}

func TestRepoSymbolsPersistAndSurviveResync(t *testing.T) {
	r, dir := newTestRegistry(t)
	hub := &FakeHub{Repos: []RepoProfile{
		{RepoID: "kubestellar/hive", HiveID: "h1", Owner: "alice"},
		{RepoID: "org/hive", HiveID: "h2", Owner: "bob"},
	}}
	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	first := map[string]string{}
	for _, rp := range r.List(false) {
		first[rp.RepoID] = rp.Symbol
	}
	if first["kubestellar/hive"] != "HIVE" || first["org/hive"] != "HIVH" {
		t.Fatalf("unexpected initial symbols: %v", first)
	}

	hub.Repos = []RepoProfile{
		{RepoID: "kubestellar/hive", HiveID: "h1", Owner: "alice"},
		{RepoID: "org/hive", HiveID: "h2", Owner: "bob"},
		{RepoID: "new/hive", HiveID: "h3", Owner: "carol"},
	}
	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("re-Sync: %v", err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for repoID, want := range first {
		rp, err := reopened.Get(repoID)
		if err != nil {
			t.Fatalf("Get %s: %v", repoID, err)
		}
		if rp.Symbol != want {
			t.Fatalf("%s symbol changed after resync/reopen: %q → %q", repoID, want, rp.Symbol)
		}
	}
	rp, err := reopened.Get("new/hive")
	if err != nil {
		t.Fatalf("Get new/hive: %v", err)
	}
	if rp.Symbol == "" || rp.Symbol == first["kubestellar/hive"] || rp.Symbol == first["org/hive"] {
		t.Fatalf("new repo got invalid/colliding symbol: %+v", rp)
	}
}

func TestRepoSymbolLazyMigrationOnOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.json")
	raw, err := json.Marshal([]RepoProfile{{RepoID: "projectbluefin/bluefin", HiveID: "h1", Owner: "alice"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rp, err := r.Get("projectbluefin/bluefin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rp.Symbol != "BLUF" {
		t.Fatalf("lazy symbol = %q, want BLUF", rp.Symbol)
	}
}

func TestIncomingRepoSymbolsAreIgnored(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge([]RepoProfile{
		{RepoID: "kubestellar/hive", HiveID: "h1", Owner: "alice", Symbol: "BAD"},
		{RepoID: "org/hive", HiveID: "h2", Owner: "bob", Symbol: "BAD"},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	a, err := r.Get("kubestellar/hive")
	if err != nil {
		t.Fatalf("Get kubestellar/hive: %v", err)
	}
	b, err := r.Get("org/hive")
	if err != nil {
		t.Fatalf("Get org/hive: %v", err)
	}
	if a.Symbol != "HIVE" || b.Symbol != "HIVH" {
		t.Fatalf("incoming symbols were not ignored/uniquified: %q %q", a.Symbol, b.Symbol)
	}
}
