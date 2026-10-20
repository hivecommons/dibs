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
	// Groups that do not open with a control word are not destinations, so
	// their text must survive; a stray brace at the end of input must not
	// read past the string.
	t.Run("plain groups and trailing brace keep text", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ in, want string }{
			{`{Hello} world`, "Hello world"},
			{`{{nested} text}`, "nested text"},
			{`kept{`, "kept"},
			{`kept{\`, "kept"},
		} {
			if got := StripRTF(tc.in); got != tc.want {
				t.Fatalf("StripRTF(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})
	// A skipped destination that is never closed swallows the rest of the
	// input instead of panicking or leaking header text.
	t.Run("unterminated skipped group", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ in, want string }{
			{`shown{\fonttbl{\f0 Calibri;}`, "shown"},
			{`shown{\*\generator Riched20 \}`, "shown"},
		} {
			if got := StripRTF(tc.in); got != tc.want {
				t.Fatalf("StripRTF(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})
}
