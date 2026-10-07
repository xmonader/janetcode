package webui

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/i18n"
)

// maxVoiceBytes bounds one recording the page may send: five minutes of
// Opus is well under a megabyte, and OpenAI's endpoint stops at 25 MB.
const maxVoiceBytes = 25 << 20

// voiceAPI takes a recording the page made with MediaRecorder and
// returns its transcript, through the same transcription the terminal's
// /voice uses. The page puts the text in the input; nothing is sent.
func (s *server) voiceAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.opts.Transcribe == nil {
		http.Error(w, "voice input is not set up in this session", http.StatusNotImplemented)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxVoiceBytes)
	if err := r.ParseMultipartForm(maxVoiceBytes); err != nil {
		http.Error(w, "bad recording: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, fh, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "no audio in the request", http.StatusBadRequest)
		return
	}
	defer f.Close()
	// The endpoint tells the format from the file name, so the page's
	// container (webm, ogg, mp4) is kept on the temporary file.
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	switch ext {
	case ".webm", ".ogg", ".mp4", ".m4a", ".wav", ".mp3":
	default:
		ext = ".webm"
	}
	tmp, err := os.CreateTemp("", "janetcode-voice-*"+ext)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, f)
	tmp.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	text, err := s.opts.Transcribe(r.Context(), tmp.Name())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"text": strings.TrimSpace(text)})
}

// uiAPI is what the page's chrome needs once at load: the catalogue in the
// interface's language (the page translates its elements by their English
// text) and whether the voice button has anything behind it.
func (s *server) uiAPI(w http.ResponseWriter, r *http.Request) {
	code := i18n.Active()
	out := map[string]any{"lang": code, "voice": s.opts.Transcribe != nil}
	if code != "en" {
		out["strings"] = i18n.Catalog(code)
	}
	writeJSON(w, out)
}
