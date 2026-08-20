// Package intake extracts idea text from uploaded documents or transcribed audio.
package intake

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const (
	DefaultMaxUploadMB = 25
	MaxReturnedRunes   = 20000
	extractRunesLimit  = MaxReturnedRunes + 1
	sttTimeout         = 60 * time.Second
)

var (
	DocumentExts = map[string]bool{".txt": true, ".md": true, ".html": true, ".htm": true, ".pdf": true, ".docx": true, ".rtf": true}
	AudioExts    = map[string]bool{".mp3": true, ".m4a": true, ".wav": true, ".ogg": true, ".webm": true, ".flac": true}
)

type Response struct {
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	Truncated bool   `json:"truncated"`
}

type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func MaxUploadBytes() int64 {
	mb := DefaultMaxUploadMB
	if raw := strings.TrimSpace(os.Getenv("DIBS_INTAKE_MAX_MB")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			mb = n
		}
	}
	return int64(mb) * 1024 * 1024
}

func AudioEnabled() bool { return strings.TrimSpace(os.Getenv("DIBS_STT_URL")) != "" }

func HandleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"audio": AudioEnabled()})
}

func HandleUpload(w http.ResponseWriter, r *http.Request) {
	limit := MaxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit+1024*1024)
	if err := r.ParseMultipartForm(limit); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "intake upload is too large")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, `multipart field "file" is required`)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading uploaded file")
		return
	}
	if int64(len(data)) > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "intake upload is too large")
		return
	}
	resp, err := ProcessWithContext(r.Context(), header.Filename, header.Header.Get("Content-Type"), data)
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			writeError(w, ie.Status, ie.Msg)
		} else {
			writeError(w, http.StatusInternalServerError, "intake failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func Process(filename, declaredType string, data []byte) (Response, error) {
	return ProcessWithContext(context.Background(), filename, declaredType, data)
}

func ProcessWithContext(ctx context.Context, filename, declaredType string, data []byte) (Response, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	sniff := http.DetectContentType(firstBytes(data, 512))
	if DocumentExts[ext] {
		if !sniffMatchesDocument(ext, sniff, declaredType) {
			return Response{}, &Error{Status: http.StatusUnsupportedMediaType, Msg: "unsupported or mismatched document type"}
		}
		text, err := ExtractDocument(ext, data)
		if err != nil {
			return Response{}, &Error{Status: http.StatusBadRequest, Msg: "could not extract text from document"}
		}
		text, truncated := capRunes(cleanText(text), MaxReturnedRunes)
		return Response{Text: text, Kind: "document", Truncated: truncated}, nil
	}
	if AudioExts[ext] {
		if !sniffMatchesAudio(ext, sniff, declaredType) {
			return Response{}, &Error{Status: http.StatusUnsupportedMediaType, Msg: "unsupported or mismatched audio type"}
		}
		text, err := TranscribeWithContext(ctx, filename, declaredType, data)
		if err != nil {
			return Response{}, err
		}
		text, truncated := capRunes(cleanText(text), MaxReturnedRunes)
		return Response{Text: text, Kind: "audio", Truncated: truncated}, nil
	}
	return Response{}, &Error{Status: http.StatusUnsupportedMediaType, Msg: "unsupported intake file type"}
}

func ExtractDocument(ext string, data []byte) (string, error) {
	switch ext {
	case ".txt", ".md":
		return string(data), nil
	case ".html", ".htm":
		return StripTags(string(data)), nil
	case ".pdf":
		return ExtractPDF(data)
	case ".docx":
		return ExtractDOCX(data)
	case ".rtf":
		return StripRTF(string(data)), nil
	default:
		return "", fmt.Errorf("unsupported extension %s", ext)
	}
}

func ExtractPDF(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	plain, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ExtractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		dec := xml.NewDecoder(rc)
		var b strings.Builder
		runes := 0
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			switch t := tok.(type) {
			case xml.CharData:
				runes += writeLimitedRunes(&b, string(t), extractRunesLimit-runes)
			case xml.EndElement:
				if t.Name.Local == "p" || t.Name.Local == "br" || t.Name.Local == "tab" {
					runes += writeLimitedRunes(&b, "\n", extractRunesLimit-runes)
				}
			}
			if runes >= extractRunesLimit {
				return b.String(), nil
			}
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("word/document.xml not found")
}

func StripTags(s string) string {
	var b strings.Builder
	inTag := false
	spacePending := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
			spacePending = true
		case '>':
			inTag = false
		default:
			if inTag {
				continue
			}
			if spacePending {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				spacePending = false
			}
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

func StripRTF(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '{', '}':
			i++
		case '\\':
			i++
			if i >= len(s) {
				break
			}
			if s[i] == '\\' || s[i] == '{' || s[i] == '}' {
				b.WriteByte(s[i])
				i++
				continue
			}
			if s[i] == '\'' && i+2 < len(s) {
				if v, ok := parseHexByte(s[i+1], s[i+2]); ok {
					b.WriteByte(v)
				}
				i += 3
				continue
			}
			for i < len(s) && ((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
				i++
			}
			if i < len(s) && (s[i] == '-' || (s[i] >= '0' && s[i] <= '9')) {
				i++
				for i < len(s) && s[i] >= '0' && s[i] <= '9' {
					i++
				}
			}
			if i < len(s) && s[i] == ' ' {
				i++
			}
			b.WriteByte(' ')
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

func Transcribe(filename, contentType string, data []byte) (string, error) {
	return TranscribeWithContext(context.Background(), filename, contentType, data)
}

func TranscribeWithContext(ctx context.Context, filename, contentType string, data []byte) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DIBS_STT_URL")), "/")
	if baseURL == "" {
		return "", &Error{Status: http.StatusNotImplemented, Msg: "audio transcription is not configured"}
	}
	key := strings.TrimSpace(os.Getenv("DIBS_STT_KEY"))
	model := strings.TrimSpace(os.Getenv("DIBS_STT_MODEL"))
	if model == "" {
		model = "whisper-1"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", model)
	// Quiet mics get fully discarded by server-side voice-activity detection;
	// let whisper decide what is speech.
	_ = mw.WriteField("vad_filter", "false")
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+escapeQuotes(filename)+`"`)
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: sttTimeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Text  string `json:"text"`
		Error any    `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &Error{Status: http.StatusBadGateway, Msg: "audio transcription failed"}
	}
	return out.Text, nil
}

func sniffMatchesDocument(ext, sniff, declared string) bool {
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	sniff = strings.ToLower(sniff)
	switch ext {
	case ".pdf":
		return sniff == "application/pdf" || declared == "application/pdf"
	case ".docx":
		return sniff == "application/zip" || sniff == "application/octet-stream" || strings.Contains(declared, "wordprocessingml.document") || declared == "application/zip"
	case ".html", ".htm":
		return strings.HasPrefix(sniff, "text/") || declared == "text/html"
	case ".txt", ".md", ".rtf":
		return strings.HasPrefix(sniff, "text/") || sniff == "application/octet-stream" || strings.HasPrefix(declared, "text/") || declared == "application/rtf"
	default:
		return false
	}
}

func sniffMatchesAudio(ext, sniff, declared string) bool {
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	sniff = strings.ToLower(sniff)
	if strings.HasPrefix(declared, "audio/") || declared == "video/webm" || declared == "application/ogg" {
		return true
	}
	switch ext {
	case ".wav":
		return strings.Contains(sniff, "wave") || strings.Contains(sniff, "wav") || sniff == "application/octet-stream"
	case ".mp3":
		return sniff == "audio/mpeg" || sniff == "application/octet-stream"
	case ".ogg", ".flac", ".m4a", ".webm":
		return strings.HasPrefix(sniff, "audio/") || strings.HasPrefix(sniff, "video/") || sniff == "application/octet-stream" || sniff == "application/ogg"
	default:
		return false
	}
}

func capRunes(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= max {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String(), true
}

func writeLimitedRunes(b *strings.Builder, s string, remaining int) int {
	written := 0
	for _, r := range s {
		if remaining <= 0 {
			return written
		}
		b.WriteRune(r)
		remaining--
		written++
	}
	return written
}

func cleanText(s string) string {
	var b strings.Builder
	lastSpace := false
	newlines := 0
	for _, r := range strings.TrimSpace(s) {
		if r == '\r' {
			continue
		}
		if r == '\n' {
			if newlines < 2 {
				b.WriteByte('\n')
			}
			newlines++
			lastSpace = false
			continue
		}
		newlines = 0
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func firstBytes(data []byte, n int) []byte {
	if len(data) < n {
		return data
	}
	return data[:n]
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(s)
}

func parseHexByte(a, b byte) (byte, bool) {
	hi, ok := hexVal(a)
	if !ok {
		return 0, false
	}
	lo, ok := hexVal(b)
	if !ok {
		return 0, false
	}
	return hi<<4 | lo, true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
