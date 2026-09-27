package intake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAudioEnabled(t *testing.T) {
	t.Setenv("DIBS_STT_URL", "")
	if AudioEnabled() {
		t.Fatal("AudioEnabled() = true with DIBS_STT_URL unset")
	}
	t.Setenv("DIBS_STT_URL", "   ")
	if AudioEnabled() {
		t.Fatal("AudioEnabled() = true with whitespace-only DIBS_STT_URL")
	}
	t.Setenv("DIBS_STT_URL", "http://stt.local")
	if !AudioEnabled() {
		t.Fatal("AudioEnabled() = false with DIBS_STT_URL set")
	}
}

func TestHandleConfig(t *testing.T) {
	for _, tc := range []struct {
		sttURL string
		want   bool
	}{
		{"", false},
		{"http://stt.local", true},
	} {
		t.Setenv("DIBS_STT_URL", tc.sttURL)
		rec := httptest.NewRecorder()
		HandleConfig(rec, httptest.NewRequest("GET", "/api/intake/config", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("HandleConfig status = %d, want 200", rec.Code)
		}
		var body map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if body["audio"] != tc.want {
			t.Fatalf("audio = %v with DIBS_STT_URL=%q, want %v", body["audio"], tc.sttURL, tc.want)
		}
	}
}
