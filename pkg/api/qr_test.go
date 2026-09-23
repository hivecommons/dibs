package api

import (
	"strings"
	"testing"
)

// TestDefaultContributeURLUsesCanonicalHost pins the QR code target. The
// QR code served by GET /api/repos/{org}/{repo}/qr.png gets printed onto
// physical things (stickers, posters, conference signage) where it cannot
// be corrected afterwards, so it must encode the canonical hub host rather
// than lean on the retired kubestellar.io redirect.
func TestDefaultContributeURLUsesCanonicalHost(t *testing.T) {
	if strings.Contains(defaultContributeURL, "kubestellar.io") {
		t.Errorf("QR target advertises the retired host: %q", defaultContributeURL)
	}
	if !strings.HasPrefix(defaultContributeURL, "https://hive.hivecommons.dev") {
		t.Errorf("QR target is not the canonical hub: %q", defaultContributeURL)
	}
}
