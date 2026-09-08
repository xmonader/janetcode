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
