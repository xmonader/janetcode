// Package voice records a spoken prompt from the microphone and turns it
// into text through an OpenAI-compatible /audio/transcriptions endpoint.
//
// Recording is left to a program the machine already has — arecord
// (ALSA), sox's rec, or ffmpeg — rather than to a cgo audio library, so
// the binary stays pure Go and the capture uses whatever device the
// system is set up with. The recorder writes a 16 kHz mono WAV file and
// is stopped with an interrupt, which each of them answers by finishing
// the file's header cleanly.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultModel is the transcription model asked for when none is set.
const DefaultModel = "whisper-1"

// MaxRecording bounds one recording, so a stop that never comes cannot
// fill the disk. OpenAI's endpoint takes files up to 25 MB; five minutes
// of 16 kHz mono 16-bit audio is under 10.
const MaxRecording = 5 * time.Minute

// Config is where a recording goes to be transcribed.
type Config struct {
	// Endpoint is the OpenAI-compatible base URL (".../v1").
	Endpoint string
	APIKey   string
	Model    string
	// Recorder, when set, is the recording command; the WAV path is
	// appended as its last argument.
	Recorder string
	// Language, when set, is passed to the endpoint as a hint (an ISO
	// 639-1 code such as "es").
	Language string
	Client   *http.Client
}

// ErrNoRecorder says no recording program was found.
var ErrNoRecorder = errors.New("no recorder found: install alsa-utils (arecord), sox (rec) or ffmpeg, or set voice.recorder")

// FileEnv names the environment variable a configured recorder command
// finds the WAV path in.
const FileEnv = "JANETCODE_VOICE_FILE"

// RecorderCommand is the argv that records to path: the configured
// command, run by the shell with the path in $JANETCODE_VOICE_FILE (and
// appended as its last argument when the command does not mention it),
// else the first of arecord, rec and ffmpeg on PATH.
func RecorderCommand(custom, path string, lookPath func(string) (string, error)) ([]string, error) {
	if custom = strings.TrimSpace(custom); custom != "" {
		if !strings.Contains(custom, FileEnv) {
			if runtime.GOOS == "windows" {
				custom += ` "$env:` + FileEnv + `"`
			} else {
				custom += ` "$` + FileEnv + `"`
			}
		}
		return shellArgv(custom), nil
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("arecord"); err == nil && runtime.GOOS == "linux" {
		return []string{"arecord", "-q", "-f", "S16_LE", "-r", "16000", "-c", "1", "-t", "wav", path}, nil
	}
	if _, err := lookPath("rec"); err == nil {
		return []string{"rec", "-q", "-c", "1", "-r", "16000", "-b", "16", path}, nil
	}
	if _, err := lookPath("ffmpeg"); err == nil {
		var in []string
		switch runtime.GOOS {
		case "darwin":
			in = []string{"-f", "avfoundation", "-i", ":0"}
		case "windows":
			in = []string{"-f", "dshow", "-i", "audio=default"}
		default:
			in = []string{"-f", "pulse", "-i", "default"}
		}
		args := append([]string{"ffmpeg", "-loglevel", "error", "-y"}, in...)
		return append(args, "-ac", "1", "-ar", "16000", "-t", fmt.Sprint(int(MaxRecording.Seconds())), path), nil
	}
	return nil, ErrNoRecorder
}

// Record runs the recorder until stop is closed or ctx ends, and returns
// the WAV file's path. The caller removes the file.
func Record(ctx context.Context, custom string, stop <-chan struct{}) (string, error) {
	f, err := os.CreateTemp("", "janetcode-voice-*.wav")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	argv, err := RecorderCommand(custom, path, nil)
	if err != nil {
		os.Remove(path)
		return "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), FileEnv+"="+path)
	prepare(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("start %s: %w", argv[0], err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	limit := time.NewTimer(MaxRecording)
	defer limit.Stop()
	var werr error
	select {
	case werr = <-exited:
		// It stopped by itself: a device that could not be opened.
		if werr != nil {
			os.Remove(path)
			return "", fmt.Errorf("%s: %v: %s", argv[0], werr, strings.TrimSpace(stderr.String()))
		}
	case <-stop:
		werr = interrupt(cmd, exited)
	case <-limit.C:
		werr = interrupt(cmd, exited)
	case <-ctx.Done():
		_ = kill(cmd.Process)
		<-exited
		os.Remove(path)
		return "", ctx.Err()
	}
	_ = werr // an interrupted recorder exits non-zero; the file is what counts
	if st, err := os.Stat(path); err != nil || st.Size() <= 44 {
		os.Remove(path)
		return "", fmt.Errorf("%s recorded nothing: %s", argv[0], strings.TrimSpace(stderr.String()))
	}
	return path, nil
}

// interrupt stops the recorder the way a person at its terminal would,
// so it finishes the file, and kills it if it will not.
func interrupt(cmd *exec.Cmd, exited <-chan error) error {
	_ = signalStop(cmd.Process)
	select {
	case err := <-exited:
		return err
	case <-time.After(3 * time.Second):
		_ = kill(cmd.Process)
		return <-exited
	}
}

// Transcribe sends the audio file at path to cfg's endpoint and returns
// the text heard.
func Transcribe(ctx context.Context, cfg Config, path string) (string, error) {
	if cfg.Endpoint == "" {
		return "", errors.New("no transcription endpoint: set voice.endpoint to an OpenAI-compatible base URL")
	}
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "json")
	if cfg.Language != "" {
		_ = mw.WriteField("language", cfg.Language)
	}
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	fw.Write(audio)
	if err := mw.Close(); err != nil {
		return "", err
	}
	url := strings.TrimRight(cfg.Endpoint, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return "", fmt.Errorf("transcription failed (HTTP %d from %s, model %s): %s", resp.StatusCode, url, model, clip(msg, 300))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// response_format is a request, not a promise: a server that
		// answers in plain text has still answered.
		return strings.TrimSpace(string(raw)), nil
	}
	return strings.TrimSpace(out.Text), nil
}

// Session records until stop and transcribes what was recorded.
func Session(ctx context.Context, cfg Config, stop <-chan struct{}) (string, error) {
	path, err := Record(ctx, cfg.Recorder, stop)
	if err != nil {
		return "", err
	}
	defer os.Remove(path)
	return Transcribe(ctx, cfg, path)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
