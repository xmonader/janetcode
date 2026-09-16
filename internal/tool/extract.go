package tool

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os/exec"
	"strings"
)

func extractReadable(path string, data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF")):
		return extractPDF(path, data)
	case isZip(data) && strings.HasSuffix(strings.ToLower(path), ".pptx"):
		text, err := extractPPTX(data)
		if err != nil {
			return "", false
		}
		return text, true
	default:
		if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			return fmt.Sprintf("(image %s %dx%d, %d bytes; not inlined as text — attach with @%s if the model should see it)", format, cfg.Width, cfg.Height, len(data), path), true
		}
	}
	return "", false
}

func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K'
}

func extractPDF(path string, data []byte) (string, bool) {
	cmd := exec.Command("pdftotext", "-layout", "-nopgbrk", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		// Fall back to a crude stream extract so we still return something.
		text := crudePDFText(data)
		if strings.TrimSpace(text) == "" {
			return fmt.Sprintf("(pdf %d bytes; install pdftotext to extract text from %s)", len(data), path), true
		}
		return textfmtTruncate(text), true
	}
	return textfmtTruncate(string(out)), true
}

func crudePDFText(data []byte) string {
	var b strings.Builder
	for i := 0; i < len(data); i++ {
		if data[i] == '(' {
			j := i + 1
			for j < len(data) && data[j] != ')' {
				j++
			}
			if j < len(data) && j-i > 2 && j-i < 200 {
				chunk := data[i+1 : j]
				if isMostlyPrintable(chunk) {
					b.Write(chunk)
					b.WriteByte('\n')
				}
			}
			i = j
		}
	}
	return b.String()
}

func isMostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	ok := 0
	for _, c := range b {
		if c >= 32 && c < 127 || c == '\n' || c == '\t' {
			ok++
		}
	}
	return ok*4 >= len(b)*3
}

func extractPPTX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var slides []string
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "ppt/slides/slide") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		slides = append(slides, stripXMLText(body))
	}
	if len(slides) == 0 {
		return "", fmt.Errorf("no slides")
	}
	return textfmtTruncate(strings.Join(slides, "\n\n")), nil
}

func stripXMLText(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if ch, ok := tok.(xml.CharData); ok {
			s := strings.TrimSpace(string(ch))
			if s != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func textfmtTruncate(s string) string {
	const max = 32_000
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncated)"
}
