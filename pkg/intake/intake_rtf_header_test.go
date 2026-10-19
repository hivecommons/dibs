package intake

import (
	"strings"
	"testing"
)

// TestStripRTFSkipsHeaderDestinations guards against font/colour table and
// generator header text leaking into extracted text.
func TestStripRTFSkipsHeaderDestinations(t *testing.T) {
	t.Parallel()
	const wordpad = `{\rtf1\ansi\deff0{\fonttbl{\f0\fnil\fcharset0 Calibri;}}{\colortbl ;\red0\green0\blue255;}{\*\generator Riched20 10.0.19041}\viewkind4\uc1\pard\f0\fs22 Hello world\par Second paragraph\par}`
	t.Run("wordpad header", func(t *testing.T) {
		t.Parallel()
		got := StripRTF(wordpad)
		for _, bad := range []string{"Calibri", "Riched20", ";"} {
			if strings.Contains(got, bad) {
				t.Fatalf("StripRTF output %q contains %q", got, bad)
			}
		}
		h := strings.Index(got, "Hello world")
		s := strings.Index(got, "Second paragraph")
		if h < 0 || s < 0 || h > s {
			t.Fatalf("StripRTF output %q missing expected text", got)
		}
		if !strings.Contains(got[h:s], "\n") {
			t.Fatalf("StripRTF output %q has no newline between paragraphs", got)
		}
	})
	t.Run("nested skipped group with escaped brace", func(t *testing.T) {
		t.Parallel()
		if got, want := StripRTF(`{\*\foo a\}b}kept`), "kept"; got != want {
			t.Fatalf("StripRTF = %q, want %q", got, want)
		}
	})
}
