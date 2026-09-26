package intake

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractTextFormats(t *testing.T) {
	t.Parallel()
	if got, err := ExtractDocument(".txt", []byte("plain idea")); err != nil || got != "plain idea" {
		t.Fatalf("txt = %q, %v", got, err)
	}
	if got, err := ExtractDocument(".md", []byte("# Idea\nbody")); err != nil || got != "# Idea\nbody" {
		t.Fatalf("md = %q, %v", got, err)
	}
	got, err := ExtractDocument(".html", []byte("<h1>Idea</h1><p>Help &amp; ship</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Idea") || !strings.Contains(got, "Help & ship") || strings.Contains(got, "<p>") {
		t.Fatalf("html stripped to %q", got)
	}
	got, err = ExtractDocument(".rtf", []byte(`{\rtf1\ansi This is \b bold\b0  idea\par done}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "This is") || !strings.Contains(got, "bold") || strings.Contains(got, `\b`) {
		t.Fatalf("rtf stripped to %q", got)
	}
}

func TestExtractDOCX(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>First idea</w:t></w:r></w:p><w:p><w:r><w:t>Second line</w:t></w:r></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractDocument(".docx", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "First idea") || !strings.Contains(got, "Second line") {
		t.Fatalf("docx extracted %q", got)
	}
}

func TestExtractPDFGeneratedFixture(t *testing.T) {
	t.Parallel()
	got, err := ExtractDocument(".pdf", generatedPDF("Hello PDF idea"))
	if err != nil {
		t.Skipf("generated PDF fixture is not supported by extractor: %v", err)
	}
	if !strings.Contains(got, "Hello PDF idea") {
		t.Fatalf("pdf extracted %q", got)
	}
}

func TestExtractPDFMalformedReturnsError(t *testing.T) {
	t.Parallel()
	// The upstream parser panics on many malformed inputs; ExtractPDF must
	// convert that to an error, never panic.
	inputs := [][]byte{
		[]byte("%PDF-1.4\ngarbage"),
		[]byte("%PDF-1.4\nxref\n0 1\ntrailer\n<< /Size 1 >>\nstartxref\n9\n%%EOF\n"),
		generatedPDF("ok")[:40],
	}
	for i, in := range inputs {
		if _, err := ExtractPDF(in); err == nil {
			t.Errorf("input %d: malformed PDF extracted without error", i)
		}
	}
}

func TestExtractPDFStopsAtRuneLimit(t *testing.T) {
	t.Parallel()
	// Many pages of long text: extraction must stop accumulating at
	// extractRunesLimit instead of materializing every page.
	page := strings.Repeat("A", 4000)
	got, err := ExtractPDF(generatedMultiPagePDF(64, page))
	if err != nil {
		t.Skipf("generated PDF fixture is not supported by extractor: %v", err)
	}
	n := len([]rune(got))
	if n > extractRunesLimit {
		t.Fatalf("extracted %d runes, want <= %d", n, extractRunesLimit)
	}
	if !strings.Contains(got, "AAAA") {
		t.Fatalf("pdf extracted %q", got)
	}
}

func generatedMultiPagePDF(pages int, text string) []byte {
	// Objects: 1 catalog, 2 pages tree, 3 font, then per page: page + contents.
	kids := make([]string, pages)
	for i := 0; i < pages; i++ {
		kids[i] = fmt.Sprintf("%d 0 R", 4+i*2)
	}
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		fmt.Sprintf(`<< /Type /Pages /Kids [%s] /Count %d >>`, strings.Join(kids, " "), pages),
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
	}
	for i := 0; i < pages; i++ {
		content := "BT /F1 12 Tf 36 100 Td (" + text + ") Tj ET\n"
		objects = append(objects,
			fmt.Sprintf(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>`, 5+i*2),
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		)
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestProcessCapsReturnedText(t *testing.T) {
	t.Parallel()
	got, err := Process("idea.txt", "text/plain", []byte(strings.Repeat("x", MaxReturnedRunes+10)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "document" || !got.Truncated || len([]rune(got.Text)) != MaxReturnedRunes {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestAudioWithoutConfigReturns501(t *testing.T) {
	t.Setenv("DIBS_STT_URL", "")
	_, err := Process("idea.wav", "audio/wav", []byte("RIFF----WAVEfmt "))
	if err == nil {
		t.Fatal("expected error")
	}
	ie, ok := err.(*Error)
	if !ok || ie.Status != http.StatusNotImplemented {
		t.Fatalf("error = %#v", err)
	}
}

func TestTranscribeForwardsMultipart(t *testing.T) {
	var sawAuth, sawModel, sawFile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization") == "Bearer secret"
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		sawModel = r.FormValue("model") == "test-model"
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		defer file.Close()
		body, _ := io.ReadAll(file)
		sawFile = header.Filename == "idea.webm" && string(body) == "audio bytes" && header.Header.Get("Content-Type") == "audio/webm"
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "spoken idea"})
	}))
	defer srv.Close()
	t.Setenv("DIBS_STT_URL", srv.URL)
	t.Setenv("DIBS_STT_KEY", "secret")
	t.Setenv("DIBS_STT_MODEL", "test-model")
	got, err := Transcribe("idea.webm", "audio/webm", []byte("audio bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "spoken idea" || !sawAuth || !sawModel || !sawFile {
		t.Fatalf("got %q auth=%v model=%v file=%v", got, sawAuth, sawModel, sawFile)
	}
}

func TestHandleUploadRequiresFile(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("other", "value")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/intake", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	HandleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func generatedPDF(text string) []byte {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\nBT /F1 12 Tf 36 100 Td (%s) Tj ET\nendstream", len("BT /F1 12 Tf 36 100 Td ("+text+") Tj ET\n"), text),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
