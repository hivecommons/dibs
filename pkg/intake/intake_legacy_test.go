package intake

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadLimitLegacyConfiguration(t *testing.T) {
	t.Setenv("DIBS_INTAKE_MAX_MB", "")
	t.Setenv("IDEATE_INTAKE_MAX_MB", "7")
	if got := MaxUploadBytes(); got != 7*1024*1024 {
		t.Fatalf("legacy limit = %d", got)
	}
	t.Setenv("DIBS_INTAKE_MAX_MB", "3")
	if got := MaxUploadBytes(); got != 3*1024*1024 {
		t.Fatalf("current limit = %d", got)
	}
}

func TestTranscriptionLegacyConfiguration(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprintf("current=%t", current), func(t *testing.T) {
			want := "legacy"
			if current {
				want = "current"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/"+want+"/audio/transcriptions" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+want+"-key" {
					t.Error("wrong STT key")
				}
				if err := r.ParseMultipartForm(1024); err != nil {
					t.Error(err)
					http.Error(w, "invalid multipart", 400)
					return
				}
				defer r.MultipartForm.RemoveAll()
				if got := r.FormValue("model"); got != want+"-model" {
					t.Errorf("model = %q", got)
				}
				fmt.Fprint(w, `{"text":"hello"}`)
			}))
			defer srv.Close()
			for _, suffix := range []string{"URL", "KEY", "MODEL"} {
				t.Setenv("DIBS_STT_"+suffix, "")
			}
			t.Setenv("IDEATE_STT_URL", srv.URL+"/legacy")
			t.Setenv("IDEATE_STT_KEY", "legacy-key")
			t.Setenv("IDEATE_STT_MODEL", "legacy-model")
			if current {
				t.Setenv("DIBS_STT_URL", srv.URL+"/current")
				t.Setenv("DIBS_STT_KEY", "current-key")
				t.Setenv("DIBS_STT_MODEL", "current-model")
			}
			if !AudioEnabled() {
				t.Fatal("audio should be enabled")
			}
			text, err := TranscribeWithContext(context.Background(), "sample.wav", "audio/wav", []byte("audio"))
			if err != nil || text != "hello" {
				t.Fatalf("transcribe = %q, %v", text, err)
			}
		})
	}
}
