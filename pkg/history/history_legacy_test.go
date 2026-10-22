package history

import "testing"

func TestClankerMarkersLegacyConfiguration(t *testing.T) {
	t.Setenv(EnvClankerMarkers, "")
	t.Setenv("IDEATE_CLANKER_MARKERS", "legacy-agent")
	if got := clankerMarkers(); len(got) != 1 || got[0] != "legacy-agent" {
		t.Fatalf("legacy markers = %v", got)
	}
	t.Setenv(EnvClankerMarkers, "current-agent")
	if got := clankerMarkers(); len(got) != 1 || got[0] != "current-agent" {
		t.Fatalf("current markers = %v", got)
	}
}
