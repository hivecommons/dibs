package settle

import "testing"

// TestSettlerHasGitHub pins the configured/unconfigured distinction pkg/api's
// settlement handlers branch on: nil Client means the fake/legacy no-GitHub
// mode, anything else is a wired client.
func TestSettlerHasGitHub(t *testing.T) {
	if (&Settler{}).HasGitHub() {
		t.Error("Settler with nil GitHub client reported HasGitHub() == true")
	}
	if !(&Settler{GitHub: &Fake{}}).HasGitHub() {
		t.Error("Settler with Fake client reported HasGitHub() == false")
	}
}
