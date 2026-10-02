package intake

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wavHeader is enough of a RIFF/WAVE header for http.DetectContentType to
// classify the payload as audio/wave, so .wav uploads pass the sniff gate
// without a declared Content-Type.
const wavHeader = "RIFF----WAVEfmt "

func sttServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("DIBS_STT_URL", srv.URL+"/")
	t.Setenv("DIBS_STT_KEY", "")
	t.Setenv("DIBS_STT_MODEL", "")
	return srv
}

// TestTranscribeDefaultsModelAndOmitsAuth covers the DIBS_STT_MODEL fallback
// to whisper-1, the trailing-slash trim on DIBS_STT_URL, and that no
// Authorization header is sent when DIBS_STT_KEY is unset.
func TestTranscribeDefaultsModelAndOmitsAuth(t *testing.T) {
	var gotModel, gotAuth, gotPath string
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		w.Write([]byte(`{"text":"hello"}`))
	})
	got, err := TranscribeWithContext(context.Background(), "idea.wav", "audio/wav", []byte(wavHeader))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
	if gotModel != "whisper-1" {
		t.Fatalf("model = %q, want whisper-1 default", gotModel)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want none without DIBS_STT_KEY", gotAuth)
	}
	if gotPath != "/audio/transcriptions" {
		t.Fatalf("path = %q, want /audio/transcriptions (trailing slash trimmed)", gotPath)
	}
}

// TestTranscribeUpstreamErrorStatusReturns502 covers the non-2xx branch: an
// STT failure must surface as a typed 502 rather than the upstream status.
func TestTranscribeUpstreamErrorStatusReturns502(t *testing.T) {
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"model overloaded"}}`))
	})
	_, err := TranscribeWithContext(context.Background(), "idea.wav", "audio/wav", []byte(wavHeader))
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("err = %#v, want *Error", err)
	}
	if ie.Status != http.StatusBadGateway || ie.Msg != "audio transcription failed" {
		t.Fatalf("error = %d %q, want 502 audio transcription failed", ie.Status, ie.Msg)
	}
}

// TestTranscribeNonJSONBodyIsUntypedError covers the decode-error branch: a
// body that is not JSON yields a plain error, not an *Error.
func TestTranscribeNonJSONBodyIsUntypedError(t *testing.T) {
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>gateway timeout</html>"))
	})
	_, err := TranscribeWithContext(context.Background(), "idea.wav", "audio/wav", []byte(wavHeader))
	if err == nil {
		t.Fatal("expected error for non-JSON body")
	}
	var ie *Error
	if errors.As(err, &ie) {
		t.Fatalf("err = %#v, want untyped decode error", err)
	}
}

// TestTranscribeUnreachableServerIsUntypedError covers the http.Client.Do
// error branch by pointing DIBS_STT_URL at a server that has been closed.
func TestTranscribeUnreachableServerIsUntypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv("DIBS_STT_URL", url)
	_, err := TranscribeWithContext(context.Background(), "idea.wav", "audio/wav", []byte(wavHeader))
	if err == nil {
		t.Fatal("expected transport error")
	}
	var ie *Error
	if errors.As(err, &ie) {
		t.Fatalf("err = %#v, want untyped transport error", err)
	}
}

// TestHandleUploadUntypedTranscribeErrorIs500 covers HandleUpload's fallback
// branch: an error that is not an *Error becomes a generic 500.
func TestHandleUploadUntypedTranscribeErrorIs500(t *testing.T) {
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})
	rec := httptest.NewRecorder()
	HandleUpload(rec, uploadRequest(t, "idea.wav", []byte(wavHeader)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "intake failed") {
		t.Fatalf("body = %s, want intake failed", rec.Body.String())
	}
}

// TestHandleUploadUpstream502Propagates covers the typed-error path from the
// STT client through HandleUpload.
func TestHandleUploadUpstream502Propagates(t *testing.T) {
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	})
	rec := httptest.NewRecorder()
	HandleUpload(rec, uploadRequest(t, "idea.wav", []byte(wavHeader)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
}

// TestProcessAudioTruncatesLongTranscript covers the audio truncation branch:
// transcripts longer than MaxReturnedRunes are capped and flagged.
func TestProcessAudioTruncatesLongTranscript(t *testing.T) {
	long := strings.Repeat("é", MaxReturnedRunes+50)
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"text":"` + long + `"}`))
	})
	resp, err := ProcessWithContext(context.Background(), "idea.wav", "audio/wav", []byte(wavHeader))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "audio" || !resp.Truncated {
		t.Fatalf("resp = %#v, want kind=audio truncated", resp)
	}
	if n := len([]rune(resp.Text)); n != MaxReturnedRunes {
		t.Fatalf("len(text) = %d runes, want %d", n, MaxReturnedRunes)
	}
}

// TestProcessRejectsMismatchedTypes covers the two 415 sniff-gate branches in
// ProcessWithContext: a document extension whose bytes are not that
// document, and an audio extension whose bytes are plain text.
func TestProcessRejectsMismatchedTypes(t *testing.T) {
	t.Setenv("DIBS_STT_URL", "http://stt.invalid")
	cases := []struct {
		name, filename, declared, wantMsg string
		data                              []byte
	}{
		{"pdf-with-text", "idea.pdf", "text/plain", "unsupported or mismatched document type", []byte("just some text")},
		{"mp3-with-text", "idea.mp3", "", "unsupported or mismatched audio type", []byte("just some text")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProcessWithContext(context.Background(), tc.filename, tc.declared, tc.data)
			var ie *Error
			if !errors.As(err, &ie) {
				t.Fatalf("err = %#v, want *Error", err)
			}
			if ie.Status != http.StatusUnsupportedMediaType || ie.Msg != tc.wantMsg {
				t.Fatalf("error = %d %q, want 415 %q", ie.Status, ie.Msg, tc.wantMsg)
			}
		})
	}
}

// TestProcessDocumentExtractFailureIs400 covers the extraction-error branch:
// a .docx that passes the sniff gate but is not a zip archive.
func TestProcessDocumentExtractFailureIs400(t *testing.T) {
	_, err := ProcessWithContext(context.Background(), "idea.docx", "application/zip", []byte{0x00, 0x01, 0x02, 0x03})
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("err = %#v, want *Error", err)
	}
	if ie.Status != http.StatusBadRequest || ie.Msg != "could not extract text from document" {
		t.Fatalf("error = %d %q, want 400 could not extract text from document", ie.Status, ie.Msg)
	}
}

// TestExtractDocumentUnknownExtension covers ExtractDocument's default case.
func TestExtractDocumentUnknownExtension(t *testing.T) {
	if _, err := ExtractDocument(".xyz", []byte("x")); err == nil || !strings.Contains(err.Error(), "unsupported extension") {
		t.Fatalf("err = %v, want unsupported extension", err)
	}
}

// TestExtractDOCXMissingDocumentXML covers the branch where the archive is a
// valid zip that has no word/document.xml entry.
func TestExtractDOCXMissingDocumentXML(t *testing.T) {
	_, err := ExtractDOCX(zipWithEntries(t, map[string]string{"[Content_Types].xml": "<Types/>"}))
	if err == nil || !strings.Contains(err.Error(), "word/document.xml not found") {
		t.Fatalf("err = %v, want word/document.xml not found", err)
	}
}

func zipWithEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
