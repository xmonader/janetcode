package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// readClipboardImage returns PNG bytes from the system clipboard using
// whichever helper the platform has. It fails clearly when none is
// installed or the clipboard holds no image.
func readClipboardImage() ([]byte, error) {
	type candidate struct {
		name string
		args []string
	}
	var cands []candidate
	switch runtime.GOOS {
	case "darwin":
		cands = []candidate{{"pngpaste", []string{"-"}}}
	case "windows":
		cands = []candidate{{"powershell", []string{"-NoProfile", "-Command",
			"$i=Get-Clipboard -Format Image; if($i -eq $null){exit 1}; $m=New-Object IO.MemoryStream; $i.Save($m,[Drawing.Imaging.ImageFormat]::Png); [Console]::OpenStandardOutput().Write($m.ToArray(),0,$m.Length)"}}}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cands = append(cands, candidate{"wl-paste", []string{"-t", "image/png"}})
		}
		cands = append(cands, candidate{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}})
	}
	var tried []string
	for _, c := range cands {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		tried = append(tried, c.name)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, c.name, c.args...).Output()
		cancel()
		if err == nil && isPNG(out) {
			return out, nil
		}
	}
	if len(tried) == 0 {
		return nil, errors.New("no clipboard helper found (install wl-paste or xclip on Linux, pngpaste on macOS)")
	}
	return nil, fmt.Errorf("no image in the clipboard (checked with %v)", tried)
}

func isPNG(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }

// writeClipboardText copies s to the system clipboard. On failure the
// caller can still show the text.
func writeClipboardText(s string) error {
	type cand struct {
		name string
		args []string
	}
	var cands []cand
	switch runtime.GOOS {
	case "darwin":
		cands = []cand{{"pbcopy", nil}}
	case "windows":
		cands = []cand{{"clip", nil}}
	default:
		cands = []cand{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
		}
	}
	var tried []string
	for _, c := range cands {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		tried = append(tried, c.name)
		// Bounded like the read above: this runs on the UI goroutine, and
		// a helper that does not return (a Wayland clipboard manager that
		// holds the selection in the foreground) would freeze the window
		// rather than fail to copy.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, c.name, c.args...)
		cmd.Stdin = strings.NewReader(s)
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
	}
	if len(tried) == 0 {
		return errors.New("no clipboard helper found (install wl-copy or xclip on Linux, pbcopy on macOS)")
	}
	return fmt.Errorf("clipboard write failed (tried %v)", tried)
}

// saveClipboardImage writes the image where an @mention can find it and
// returns the absolute path.
func saveClipboardImage(data []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), "janetcode-clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, time.Now().Format("20060102-150405.000")+".png")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func (m *model) copyCommand() {
	text := ""
	if m.opts.Agent != nil {
		text = m.opts.Agent.LastAssistantText()
	}
	if text == "" {
		m.transcript.errorf("no assistant reply to copy")
		return
	}
	fn := m.copyText
	if fn == nil {
		fn = writeClipboardText
	}
	if err := fn(text); err != nil {
		m.transcript.info("copy (clipboard unavailable): " + err.Error() + "\n" + text)
		return
	}
	m.note("copied last reply (" + strconv.Itoa(len(text)) + " bytes)")
}
