package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// wav is a valid, silent 16 kHz mono WAV of n samples.
func wav(n int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+2*n))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(16000))
	binary.Write(&b, binary.LittleEndian, uint32(32000))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(2*n))
	b.Write(make([]byte, 2*n))
	return b.Bytes()
}

func TestRecorderCommandPicksWhatIsInstalled(t *testing.T) {
	only := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	if _, err := RecorderCommand("", "/tmp/a.wav", only()); !errors.Is(err, ErrNoRecorder) {
		t.Errorf("nothing installed: err = %v", err)
	}
	argv, err := RecorderCommand("", "/tmp/a.wav", only("rec", "ffmpeg"))
	if err != nil || argv[0] != "rec" || argv[len(argv)-1] != "/tmp/a.wav" {
		t.Errorf("rec and ffmpeg: %v %v", argv, err)
	}
	argv, _ = RecorderCommand("", "/tmp/a.wav", only("ffmpeg"))
	if argv[0] != "ffmpeg" || !strings.Contains(strings.Join(argv, " "), "-ar 16000") {
		t.Errorf("ffmpeg: %v", argv)
	}
	if runtime.GOOS == "linux" {
		argv, _ = RecorderCommand("", "/tmp/a.wav", only("arecord", "rec"))
		if argv[0] != "arecord" {
			t.Errorf("arecord should win on linux: %v", argv)
		}
	}
	argv, _ = RecorderCommand("myrec --mono", "/tmp/a.wav", only())
	if !strings.Contains(argv[len(argv)-1], "myrec --mono") || !strings.Contains(argv[len(argv)-1], FileEnv) {
		t.Errorf("custom recorder: %v", argv)
	}
}

// A configured recorder runs until stopped and its file is what is
// returned: the stop is an interrupt the recorder can finish on, not a
// kill that leaves a half-written file.
func TestRecordStopsTheRecorderWithAnInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell recorder")
	}
	src := filepath.Join(t.TempDir(), "in.wav")
	os.WriteFile(src, wav(1600), 0o644)
	// Writes half the file at once and the rest only when interrupted,
	// as a real recorder finishes its header on SIGINT.
	rec := `head -c 44 ` + src + ` > "$JANETCODE_VOICE_FILE"; trap 'tail -c +45 ` + src + ` >> "$JANETCODE_VOICE_FILE"; exit 0' INT; while :; do sleep 0.05; done`
	stop := make(chan struct{})
	go func() { time.Sleep(300 * time.Millisecond); close(stop) }()
	path, err := Record(context.Background(), rec, stop)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, wav(1600)) {
		t.Fatalf("recorded %d bytes, want the whole %d-byte file (was the recorder killed instead of interrupted?)", len(got), len(wav(1600)))
	}
}

// A cancelled recording ends the recorder's children too. A configured
// recorder runs under a shell, and killing the shell alone left its child
// recording and holding stderr open, so Record waited on it for as long
// as the child ran.
func TestRecordCancelledStopsTheRecordersChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell recorder")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	go func() {
		// Two commands, so the shell forks sleep rather than exec'ing it.
		_, err := Record(ctx, `sleep 30; true`, make(chan struct{}))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Record did not return after its context was cancelled: the recorder's child is still running")
	}
}

func TestRecordReportsARecorderThatCannotStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell recorder")
	}
	_, err := Record(context.Background(), "echo 'no such device' >&2; exit 1", make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("err = %v", err)
	}
}

func TestTranscribeSendsTheFileAndReadsTheText(t *testing.T) {
	var gotModel, gotAuth, gotLang string
	var gotLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		gotModel, gotLang = r.FormValue("model"), r.FormValue("language")
		f, _, err := r.FormFile("file")
		if err == nil {
			b, _ := io.ReadAll(f)
			gotLen = len(b)
		}
		json.NewEncoder(w).Encode(map[string]string{"text": " add a test for the parser "})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(path, wav(800), 0o644)
	text, err := Transcribe(context.Background(), Config{Endpoint: srv.URL + "/v1/", APIKey: "k", Model: "whisper", Language: "en"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if text != "add a test for the parser" || gotModel != "whisper" || gotAuth != "Bearer k" || gotLang != "en" || gotLen != len(wav(800)) {
		t.Fatalf("text=%q model=%q auth=%q lang=%q len=%d", text, gotModel, gotAuth, gotLang, gotLen)
	}
}

func TestTranscribeNamesTheServersError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"no speech-to-text backend is configured for model whisper-1"}}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(path, wav(10), 0o644)
	_, err := Transcribe(context.Background(), Config{Endpoint: srv.URL}, path)
	if err == nil || !strings.Contains(err.Error(), "no speech-to-text backend") || !strings.Contains(err.Error(), "whisper-1") {
		t.Fatalf("err = %v", err)
	}
}

// TestLiveTranscription runs the whole path against a real endpoint:
// speech is synthesised by the same endpoint's /audio/speech, "recorded"
// by a recorder that plays that file into place, and transcribed.
// JANETCODE_LIVE_VOICE=1 with OPENAI_BASE_URL and OPENAI_API_KEY runs it;
// JANETCODE_VOICE_MODEL and JANETCODE_TTS_MODEL name the models.
func TestLiveTranscription(t *testing.T) {
	if os.Getenv("JANETCODE_LIVE_VOICE") != "1" || runtime.GOOS == "windows" {
		t.Skip("set JANETCODE_LIVE_VOICE=1 to run against a live endpoint")
	}
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	tts := os.Getenv("JANETCODE_TTS_MODEL")
	if tts == "" {
		tts = "tts"
	}
	body, _ := json.Marshal(map[string]string{"model": tts, "input": "Please add a unit test for the config loader.", "voice": "alloy", "response_format": "wav"})
	req, _ := http.NewRequest("POST", strings.TrimRight(base, "/")+"/audio/speech", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("speech synthesis: HTTP %d %q", resp.StatusCode, audio[:min(len(audio), 200)])
	}
	src := filepath.Join(t.TempDir(), "speech.wav")
	os.WriteFile(src, audio, 0o644)
	stop := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(stop) }()
	text, err := Session(context.Background(), Config{
		Endpoint: base, APIKey: key, Model: os.Getenv("JANETCODE_VOICE_MODEL"),
		Recorder: `cp ` + src + ` "$JANETCODE_VOICE_FILE"; trap 'exit 0' INT; while :; do sleep 0.05; done`,
	}, stop)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcript: %q", text)
	if !strings.Contains(strings.ToLower(text), "unit test") {
		t.Fatalf("transcript %q does not carry what was said", text)
	}
}
