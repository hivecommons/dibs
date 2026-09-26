package intake

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStripRTFHexEscapes covers parseHexByte/hexVal via the \'xx RTF escape:
// valid lower/upper/digit hex decodes to the byte; invalid hex is dropped.
func TestStripRTFHexEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"lower hex", `\'68\'65llo`, "hello"},
		{"upper hex", `\'48I`, "HI"},
		{"digit hex", `\'31\'32`, "12"},
		{"mixed case hex", `\'4a\'4B`, "JK"},
		{"invalid first nibble", `\'zz` + "ok", "ok"},
		{"invalid second nibble", `\'4z` + "ok", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripRTF(tc.in); got != tc.want {
				t.Fatalf("StripRTF(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseHexByte pins the helper directly: full nibble ranges and rejects.
func TestParseHexByte(t *testing.T) {
	t.Parallel()
	if v, ok := parseHexByte('f', 'F'); !ok || v != 0xFF {
		t.Fatalf("parseHexByte(f,F) = %#x, %v", v, ok)
	}
	if v, ok := parseHexByte('0', '9'); !ok || v != 0x09 {
		t.Fatalf("parseHexByte(0,9) = %#x, %v", v, ok)
	}
	if _, ok := parseHexByte('g', '0'); ok {
		t.Fatal("parseHexByte(g,0) should fail")
	}
	if _, ok := parseHexByte('0', 'g'); ok {
		t.Fatal("parseHexByte(0,g) should fail")
	}
}

func TestErrorImplementsError(t *testing.T) {
	t.Parallel()
	e := &Error{Status: http.StatusTeapot, Msg: "short and stout"}
	if got := e.Error(); got != "short and stout" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestSniffMatchesDocument covers the upload type-confusion gate for every
// document extension arm, both accept and reject sides.
func TestSniffMatchesDocument(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, ext, sniff, declared string
		want                       bool
	}{
		{"pdf by sniff", ".pdf", "application/pdf", "", true},
		{"pdf by declared", ".pdf", "application/octet-stream", "application/pdf", true},
		{"pdf mismatch", ".pdf", "text/plain; charset=utf-8", "text/plain", false},
		{"docx zip sniff", ".docx", "application/zip", "", true},
		{"docx octet sniff", ".docx", "application/octet-stream", "", true},
		{"docx declared wordprocessingml", ".docx", "text/plain", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", true},
		{"docx declared zip", ".docx", "text/plain", "application/zip", true},
		{"docx mismatch", ".docx", "image/png", "image/png", false},
		{"html text sniff", ".html", "text/html; charset=utf-8", "", true},
		{"htm declared", ".htm", "application/octet-stream", "text/html", true},
		{"html mismatch", ".html", "image/png", "image/png", false},
		{"txt text sniff", ".txt", "text/plain; charset=utf-8", "", true},
		{"md octet sniff", ".md", "application/octet-stream", "", true},
		{"rtf declared rtf", ".rtf", "application/octet-stream", "application/rtf", true},
		{"txt declared text", ".txt", "application/octet-stream; x", "text/markdown", true},
		{"txt mismatch", ".txt", "image/png", "image/png", false},
		{"unknown ext", ".exe", "application/pdf", "application/pdf", false},
		{"declared params stripped", ".pdf", "application/octet-stream", "APPLICATION/PDF; charset=binary", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffMatchesDocument(tc.ext, tc.sniff, tc.declared); got != tc.want {
				t.Fatalf("sniffMatchesDocument(%q,%q,%q) = %v, want %v", tc.ext, tc.sniff, tc.declared, got, tc.want)
			}
		})
	}
}

// TestSniffMatchesAudio covers every audio extension arm and the declared
// type fast paths.
func TestSniffMatchesAudio(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, ext, sniff, declared string
		want                       bool
	}{
		{"declared audio prefix wins", ".xyz", "application/pdf", "audio/mpeg", true},
		{"declared video webm", ".xyz", "application/pdf", "video/webm", true},
		{"declared application ogg", ".xyz", "application/pdf", "application/ogg", true},
		{"declared params stripped", ".xyz", "application/pdf", "AUDIO/WAV; codec=1", true},
		{"wav wave sniff", ".wav", "audio/wave", "", true},
		{"wav octet sniff", ".wav", "application/octet-stream", "", true},
		{"wav mismatch", ".wav", "image/png", "image/png", false},
		{"mp3 mpeg sniff", ".mp3", "audio/mpeg", "", true},
		{"mp3 octet sniff", ".mp3", "application/octet-stream", "", true},
		{"mp3 mismatch", ".mp3", "text/plain", "text/plain", false},
		{"ogg audio sniff", ".ogg", "audio/ogg", "", true},
		{"flac video sniff", ".flac", "video/mp4", "", true},
		{"m4a octet sniff", ".m4a", "application/octet-stream", "", true},
		{"webm application ogg sniff", ".webm", "application/ogg", "", true},
		{"ogg mismatch", ".ogg", "image/png", "image/png", false},
		{"unknown ext", ".txt", "audio/mpeg", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffMatchesAudio(tc.ext, tc.sniff, tc.declared); got != tc.want {
				t.Fatalf("sniffMatchesAudio(%q,%q,%q) = %v, want %v", tc.ext, tc.sniff, tc.declared, got, tc.want)
			}
		})
	}
}

// TestCleanText covers CR stripping, blank-line capping, and whitespace
// collapse — the branches the format-path tests never reach.
func TestCleanText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"cr stripped", "a\r\nb", "a\nb"},
		{"max two newlines", "a\n\n\n\n\nb", "a\n\nb"},
		{"spaces collapse", "a  \t  b", "a b"},
		{"tabs become space", "a\tb", "a b"},
		{"trimmed", "  \n a \n  ", "a"},
		{"newline resets space state", "a \n b", "a \n b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanText(tc.in); got != tc.want {
				t.Fatalf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMaxUploadBytesEnvOverride covers the DIBS_INTAKE_MAX_MB branches.
func TestMaxUploadBytesEnvOverride(t *testing.T) {
	t.Setenv("DIBS_INTAKE_MAX_MB", "5")
	if got := MaxUploadBytes(); got != 5*1024*1024 {
		t.Fatalf("override = %d, want 5MB", got)
	}
	t.Setenv("DIBS_INTAKE_MAX_MB", "not-a-number")
	if got := MaxUploadBytes(); got != int64(DefaultMaxUploadMB)*1024*1024 {
		t.Fatalf("invalid override = %d, want default", got)
	}
	t.Setenv("DIBS_INTAKE_MAX_MB", "-3")
	if got := MaxUploadBytes(); got != int64(DefaultMaxUploadMB)*1024*1024 {
		t.Fatalf("negative override = %d, want default", got)
	}
}

func uploadRequest(t *testing.T, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/intake", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// TestHandleUploadSuccess covers the happy path end to end: a .txt upload
// comes back as extracted, cleaned document text.
func TestHandleUploadSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleUpload(rec, uploadRequest(t, "idea.txt", []byte("ship  it\r\ntoday")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"document"`) || !strings.Contains(body, "ship it\\ntoday") {
		t.Fatalf("body = %s", body)
	}
}

// TestHandleUploadUnsupportedType covers the *Error propagation branch:
// ProcessWithContext rejects the extension and its status must surface.
func TestHandleUploadUnsupportedType(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleUpload(rec, uploadRequest(t, "idea.exe", []byte("MZ")))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleUploadTooLarge covers the oversize-payload branch via a tiny
// DIBS_INTAKE_MAX_MB so no large buffer is allocated.
func TestHandleUploadTooLarge(t *testing.T) {
	t.Setenv("DIBS_INTAKE_MAX_MB", "1")
	rec := httptest.NewRecorder()
	HandleUpload(rec, uploadRequest(t, "idea.txt", bytes.Repeat([]byte("a"), 1024*1024+10)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleUploadNotMultipart covers the ParseMultipartForm error branch.
func TestHandleUploadNotMultipart(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/intake", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	HandleUpload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}
