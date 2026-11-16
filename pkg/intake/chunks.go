package intake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Chunked voice upload. The browser recorder slices a long recording every
// few seconds and PUTs each slice in order, so a flaky mobile link only
// re-sends one slice instead of the whole recording, and no single request
// comes near the upload read deadline or the proxy body limit. The server
// appends the slices to a private temp file, and finish runs the same
// transcription path as a one-shot upload. As before, the audio is never kept:
// the temp file is removed on finish, abort, failure or idle expiry.
const (
	// MaxChunkBytes caps one slice. Ten seconds of audio is ~40 KB at the
	// recorder's 32 kbps and ~160 KB at a browser default of 128 kbps.
	MaxChunkBytes = 2 << 20
	// maxChunkSessions bounds concurrent recordings so open sessions cannot
	// fill the temp volume (each is capped by MaxUploadBytes).
	maxChunkSessions = 16
	// chunkSessionIdle is how long a session may sit with no chunk before it
	// is discarded.
	chunkSessionIdle = 30 * time.Minute
	chunkIDBytes     = 16
	// finishBodyLimit bounds the small JSON body of a start/finish request.
	finishBodyLimit = 4 << 10
)

// AudioFile maps a MediaRecorder mimeType (parameters such as ";codecs=opus"
// ignored) to the file extension and canonical Content-Type used for the STT
// call. Some STT APIs infer the container from the file name, so the
// extension must describe the bytes. ok is false for an unknown type.
func AudioFile(mimeType string) (ext, contentType string, ok bool) {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch base {
	case "audio/webm", "video/webm":
		return "webm", "audio/webm", true
	case "audio/ogg", "application/ogg", "video/ogg":
		return "ogg", "audio/ogg", true
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "audio/aac":
		return "m4a", "audio/mp4", true
	case "video/mp4":
		return "mp4", "video/mp4", true
	case "audio/mpeg", "audio/mp3":
		return "mp3", "audio/mpeg", true
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav", "audio/wav", true
	}
	return "", "", false
}

type chunkSession struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	ext      string
	ctype    string
	next     int
	size     int64
	sums     [][sha256.Size]byte
	lastSeen time.Time
	done     bool
}

var (
	chunkMu       sync.Mutex
	chunkSessions = map[string]*chunkSession{}
	chunkNow      = time.Now
	chunkTempDir  = ""
)

func (s *chunkSession) discard() {
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
	if s.path != "" {
		os.Remove(s.path)
	}
	s.done = true
}

// sweepChunkSessions drops idle sessions. Callers hold chunkMu.
func sweepChunkSessions() {
	now := chunkNow()
	for id, s := range chunkSessions {
		if !s.mu.TryLock() {
			continue
		}
		if now.Sub(s.lastSeen) > chunkSessionIdle {
			s.discard()
			delete(chunkSessions, id)
		}
		s.mu.Unlock()
	}
}

func lookupChunkSession(id string) *chunkSession {
	chunkMu.Lock()
	defer chunkMu.Unlock()
	return chunkSessions[id]
}

// HandleChunkStart opens a recording session. Body: {"mime_type": "..."}.
func HandleChunkStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, finishBodyLimit)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ext, ctype, ok := AudioFile(req.MimeType)
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported audio type")
		return
	}
	if !AudioEnabled() {
		writeError(w, http.StatusNotImplemented, "audio transcription is not configured")
		return
	}
	chunkMu.Lock()
	sweepChunkSessions()
	if len(chunkSessions) >= maxChunkSessions {
		chunkMu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "too many recordings in progress, retry later")
		return
	}
	idb := make([]byte, chunkIDBytes)
	if _, err := rand.Read(idb); err != nil {
		chunkMu.Unlock()
		writeError(w, http.StatusInternalServerError, "intake failed")
		return
	}
	f, err := os.CreateTemp(chunkTempDir, "dibs-audio-*."+ext)
	if err != nil {
		chunkMu.Unlock()
		writeError(w, http.StatusInternalServerError, "intake failed")
		return
	}
	id := hex.EncodeToString(idb)
	chunkSessions[id] = &chunkSession{path: f.Name(), file: f, ext: ext, ctype: ctype, lastSeen: chunkNow()}
	chunkMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "max_bytes": MaxUploadBytes(), "max_chunk_bytes": MaxChunkBytes})
}

// HandleChunkPut appends chunk {n} (0-based) to the session. A chunk must be
// the next in order; resending an already stored chunk with identical bytes is
// accepted as a no-op (a retry after a lost response), a different body for a
// stored index is a 409, and a gap is a 409 that names the index to send.
func HandleChunkPut(w http.ResponseWriter, r *http.Request) {
	s := lookupChunkSession(r.PathValue("id"))
	if s == nil {
		writeError(w, http.StatusNotFound, "recording session not found or expired")
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, "invalid chunk number")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxChunkBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "audio chunk is too large")
		} else {
			writeError(w, http.StatusBadRequest, "reading audio chunk")
		}
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty audio chunk")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		writeError(w, http.StatusNotFound, "recording session not found or expired")
		return
	}
	s.lastSeen = chunkNow()
	sum := sha256.Sum256(data)
	switch {
	case n < s.next:
		if s.sums[n] != sum {
			writeError(w, http.StatusConflict, "chunk "+strconv.Itoa(n)+" was already stored with different content")
			return
		}
	case n > s.next:
		writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("missing chunk %d", s.next), "next": s.next})
		return
	default:
		if s.size+int64(len(data)) > MaxUploadBytes() {
			writeError(w, http.StatusRequestEntityTooLarge, "recording is too long for the upload limit")
			return
		}
		if _, err := s.file.Write(data); err != nil {
			writeError(w, http.StatusInternalServerError, "intake failed")
			return
		}
		s.size += int64(len(data))
		s.sums = append(s.sums, sum)
		s.next++
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": s.next, "bytes": s.size})
}

// HandleChunkFinish transcribes the assembled recording and discards it.
func HandleChunkFinish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s := lookupChunkSession(id)
	if s == nil {
		writeError(w, http.StatusNotFound, "recording session not found or expired")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		writeError(w, http.StatusNotFound, "recording session not found or expired")
		return
	}
	// The session is single-use whatever happens next: audio is not kept.
	defer func() {
		s.discard()
		chunkMu.Lock()
		delete(chunkSessions, id)
		chunkMu.Unlock()
	}()
	if s.next == 0 {
		writeError(w, http.StatusBadRequest, "no audio was received")
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil || int64(len(data)) != s.size {
		writeError(w, http.StatusInternalServerError, "intake failed")
		return
	}
	resp, err := ProcessWithContext(r.Context(), "idea."+s.ext, s.ctype, data)
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

// HandleChunkAbort discards a session. It is idempotent.
func HandleChunkAbort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s := lookupChunkSession(id); s != nil {
		s.mu.Lock()
		s.discard()
		s.mu.Unlock()
		chunkMu.Lock()
		delete(chunkSessions, id)
		chunkMu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}
