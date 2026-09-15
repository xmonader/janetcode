package lsp

import (
	"path/filepath"
	"strconv"
	"strings"
)

func toURI(abs string) string {
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

func fromURI(uri string) string {
	p := strings.TrimPrefix(uri, "file://")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	// Unescape %20 and friends without pulling in net/url edge cases.
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) {
			if v, err := strconv.ParseUint(p[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return filepath.FromSlash(b.String())
}

func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}
