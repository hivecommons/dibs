package intake

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAudioFileMapping(t *testing.T) {
	for _, tt := range []struct{ in, ext, ctype string }{
		{"audio/webm", "webm", "audio/webm"},
		{"audio/webm;codecs=opus", "webm", "audio/webm"},
		{"AUDIO/WEBM; codecs=opus", "webm", "audio/webm"},
		{"video/webm", "webm", "audio/webm"},
		{"audio/ogg;codecs=opus", "ogg", "audio/ogg"},
		{"audio/mp4", "m4a", "audio/mp4"},
		{"audio/mp4;codecs=mp4a.40.2", "m4a", "audio/mp4"},
		{"video/mp4", "mp4", "video/mp4"},
		{"audio/wav", "wav", "audio/wav"},
	} {
		ext, ct, ok := AudioFile(tt.in)
		if !ok || ext != tt.ext || ct != tt.ctype {
			t.Errorf("AudioFile(%q) = %q, %q, %v; want %q, %q", tt.in, ext, ct, ok, tt.ext, tt.ctype)
		}
	}
	for _, in := range []string{"", "text/plain", "application/pdf"} {
		if _, _, ok := AudioFile(in); ok {
			t.Errorf("AudioFile(%q) ok, want unsupported", in)
		}
	}
}

func TestSniffAcceptsRealRecorderContainers(t *testing.T) {
	mp4 := append([]byte{0, 0, 0, 0x18}, []byte("ftypmp42\x00\x00\x00\x00mp42isom")...)
	ogg := []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00")
	webm := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81}
	for _, tt := range []struct {
		name, file, declared string
		data                 []byte
	}{
		{"m4a undeclared", "idea.m4a", "", mp4},
		{"mp4 undeclared", "idea.mp4", "", mp4},
		{"mp4 declared", "idea.m4a", "audio/mp4", mp4},
		{"ogg undeclared", "idea.ogg", "", ogg},
		{"ogg declared", "idea.ogg", "audio/ogg", ogg},
		{"webm undeclared", "idea.webm", "", webm},
	} {
		sniff := http.DetectContentType(tt.data)
		ext := tt.file[strings.LastIndex(tt.file, "."):]
		if !sniffMatchesAudio(ext, sniff, tt.declared) {
			t.Errorf("%s: sniff %q declared %q rejected", tt.name, sniff, tt.declared)
		}
	}
	// A document body must not pass as audio just because of the extension.
	if sniffMatchesAudio(".mp4", http.DetectContentType([]byte("%PDF-1.4 text")), "") {
		t.Error("PDF bytes accepted as .mp4 audio")
	}
}

type sttCapture struct {
	filename, ctype string
	data            []byte
}

func fakeSTT(t *testing.T, cap *sttCapture) {
	t.Helper()
	sttServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				cap.filename = p.FileName()
				cap.ctype = p.Header.Get("Content-Type")
				cap.data, _ = io.ReadAll(p)
			}
		}
		w.Write([]byte(`{"text":"transcribed words"}`))
	})
}

func chunkMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/intake/audio", HandleChunkStart)
	mux.HandleFunc("PUT /api/intake/audio/{id}/chunks/{n}", HandleChunkPut)
	mux.HandleFunc("POST /api/intake/audio/{id}/finish", HandleChunkFinish)
	mux.HandleFunc("DELETE /api/intake/audio/{id}", HandleChunkAbort)
	return mux
}

func chunkSetup(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	chunkTempDir = dir
	chunkMu.Lock()
	chunkSessions = map[string]*chunkSession{}
	chunkMu.Unlock()
	t.Cleanup(func() { chunkTempDir = "" })
	return chunkMux(), dir
}

func do(mux *http.ServeMux, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func startSession(t *testing.T, mux *http.ServeMux, mimeType string) string {
	t.Helper()
	rec := do(mux, "POST", "/api/intake/audio", []byte(`{"mime_type":"`+mimeType+`"}`))
	if rec.Code != 200 {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	var out struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.ID) != 2*chunkIDBytes {
		t.Fatalf("id = %q", out.ID)
	}
	return out.ID
}

func put(mux *http.ServeMux, id string, n int, b string) *httptest.ResponseRecorder {
	return do(mux, "PUT", "/api/intake/audio/"+id+"/chunks/"+strconv.Itoa(n), []byte(b))
}

func TestChunkedFinishTranscribesWithRealFilenameAndDeletesAudio(t *testing.T) {
	mux, dir := chunkSetup(t)
	var cap sttCapture
	fakeSTT(t, &cap)
	id := startSession(t, mux, "audio/mp4;codecs=mp4a.40.2")
	for i, c := range []string{"\x00\x00\x00\x18ftypmp42", "BBBB", "CCCC"} {
		if rec := put(mux, id, i, c); rec.Code != 200 {
			t.Fatalf("chunk %d = %d %s", i, rec.Code, rec.Body)
		}
	}
	rec := do(mux, "POST", "/api/intake/audio/"+id+"/finish", nil)
	if rec.Code != 200 {
		t.Fatalf("finish = %d %s", rec.Code, rec.Body)
	}
	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Text != "transcribed words" || resp.Kind != "audio" {
		t.Fatalf("resp = %+v", resp)
	}
	if cap.filename != "idea.m4a" || cap.ctype != "audio/mp4" {
		t.Fatalf("STT got filename %q type %q, want idea.m4a audio/mp4", cap.filename, cap.ctype)
	}
	if string(cap.data) != "\x00\x00\x00\x18ftypmp42BBBBCCCC" {
		t.Fatalf("STT audio = %q, want chunks concatenated in order", cap.data)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("audio left on disk after finish: %v", left)
	}
	if rec := do(mux, "POST", "/api/intake/audio/"+id+"/finish", nil); rec.Code != 404 {
		t.Fatalf("second finish = %d, want 404 (single use)", rec.Code)
	}
}

func TestChunkOrderGapAndIdempotentResend(t *testing.T) {
	mux, _ := chunkSetup(t)
	var cap sttCapture
	fakeSTT(t, &cap)
	id := startSession(t, mux, "audio/webm")
	if rec := put(mux, id, 1, "late"); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"next":0`) {
		t.Fatalf("gap = %d %s, want 409 naming next 0", rec.Code, rec.Body)
	}
	put(mux, id, 0, "AAAA")
	// Lost response: the client resends chunk 0 verbatim.
	if rec := put(mux, id, 0, "AAAA"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"bytes":4`) {
		t.Fatalf("resend = %d %s, want 200 no-op", rec.Code, rec.Body)
	}
	if rec := put(mux, id, 0, "ZZZZ"); rec.Code != 409 {
		t.Fatalf("different resend = %d, want 409", rec.Code)
	}
	put(mux, id, 1, "BBBB")
	if rec := put(mux, id, 1, "BBBB"); rec.Code != 200 {
		t.Fatalf("resend last = %d", rec.Code)
	}
	do(mux, "POST", "/api/intake/audio/"+id+"/finish", nil)
	if string(cap.data) != "AAAABBBB" {
		t.Fatalf("audio = %q, want AAAABBBB with no duplicate", cap.data)
	}
	if cap.filename != "idea.webm" {
		t.Fatalf("filename = %q", cap.filename)
	}
}

func TestChunkSizeCaps(t *testing.T) {
	mux, dir := chunkSetup(t)
	fakeSTT(t, &sttCapture{})
	t.Setenv("DIBS_INTAKE_MAX_MB", "1")
	id := startSession(t, mux, "audio/webm")
	if rec := do(mux, "PUT", "/api/intake/audio/"+id+"/chunks/0", bytes.Repeat([]byte("x"), MaxChunkBytes+1)); rec.Code != 413 {
		t.Fatalf("oversize chunk = %d, want 413", rec.Code)
	}
	if rec := put(mux, id, 0, ""); rec.Code != 400 {
		t.Fatalf("empty chunk = %d, want 400", rec.Code)
	}
	half := strings.Repeat("x", 600<<10)
	if rec := put(mux, id, 0, half); rec.Code != 200 {
		t.Fatalf("first half = %d", rec.Code)
	}
	if rec := put(mux, id, 1, half); rec.Code != 413 {
		t.Fatalf("over total cap = %d, want 413", rec.Code)
	}
	// The rejected chunk was not stored, so the session still finishes.
	if rec := do(mux, "DELETE", "/api/intake/audio/"+id, nil); rec.Code != 204 {
		t.Fatalf("abort = %d", rec.Code)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("audio left after abort: %v", left)
	}
	if rec := put(mux, id, 0, "x"); rec.Code != 404 {
		t.Fatalf("put after abort = %d, want 404", rec.Code)
	}
	if rec := put(mux, id, -1, "x"); rec.Code != 404 {
		t.Fatalf("unknown session = %d", rec.Code)
	}
}

func TestChunkStartValidationAndLimits(t *testing.T) {
	mux, _ := chunkSetup(t)
	fakeSTT(t, &sttCapture{})
	if rec := do(mux, "POST", "/api/intake/audio", []byte(`{"mime_type":"text/plain"}`)); rec.Code != 415 {
		t.Fatalf("bad type = %d", rec.Code)
	}
	if rec := do(mux, "POST", "/api/intake/audio", []byte(`nope`)); rec.Code != 400 {
		t.Fatalf("bad json = %d", rec.Code)
	}
	id := startSession(t, mux, "audio/webm")
	if rec := put(mux, id, -3, "x"); rec.Code != 400 {
		t.Fatalf("negative chunk = %d, want 400", rec.Code)
	}
	if rec := do(mux, "POST", "/api/intake/audio/"+id+"/finish", nil); rec.Code != 400 {
		t.Fatalf("finish with no audio = %d, want 400", rec.Code)
	}
	for i := 0; i < maxChunkSessions; i++ {
		startSession(t, mux, "audio/webm")
	}
	if rec := do(mux, "POST", "/api/intake/audio", []byte(`{"mime_type":"audio/webm"}`)); rec.Code != 503 {
		t.Fatalf("session cap = %d, want 503", rec.Code)
	}
}

func TestChunkStartRequiresSTTAndSweepsIdle(t *testing.T) {
	mux, dir := chunkSetup(t)
	t.Setenv("DIBS_STT_URL", "")
	if rec := do(mux, "POST", "/api/intake/audio", []byte(`{"mime_type":"audio/webm"}`)); rec.Code != 501 {
		t.Fatalf("no STT = %d, want 501", rec.Code)
	}
	fakeSTT(t, &sttCapture{})
	id := startSession(t, mux, "audio/webm")
	put(mux, id, 0, "AAAA")
	now := time.Now()
	chunkNow = func() time.Time { return now.Add(chunkSessionIdle + time.Minute) }
	t.Cleanup(func() { chunkNow = time.Now })
	startSession(t, mux, "audio/webm") // triggers the sweep
	if rec := put(mux, id, 1, "BBBB"); rec.Code != 404 {
		t.Fatalf("idle session = %d, want 404", rec.Code)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("want only the new session's file on disk, got %d", len(left))
	}
}

func TestChunkFinishSTTFailureStillDiscardsAudio(t *testing.T) {
	mux, dir := chunkSetup(t)
	sttServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); w.Write([]byte(`{}`)) })
	id := startSession(t, mux, "audio/ogg")
	put(mux, id, 0, "OggS....")
	if rec := do(mux, "POST", "/api/intake/audio/"+id+"/finish", nil); rec.Code != 502 {
		t.Fatalf("finish = %d, want 502", rec.Code)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("audio kept after STT failure: %v", left)
	}
}
