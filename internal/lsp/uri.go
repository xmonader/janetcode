package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// toURI turns an absolute path into the file URI a language server
// expects. The path is percent-escaped: servers compare URIs as strings,
// and gopls, pyright and typescript-language-server all publish their
// diagnostics under the escaped form. An unescaped "file:///my project/a.go"
// was never matched by the diagnostics the server sent back for
// "file:///my%20project/a.go", so every edit in a directory with a space
// in its name waited out the whole timeout and reported nothing.
func toURI(abs string) string {
	abs = filepath.ToSlash(abs)
	// A Windows drive letter is written in upper case, so the same file
	// is one URI whichever case the path or the server spelled it in.
	if len(abs) >= 2 && abs[1] == ':' && isASCIILetter(abs[0]) {
		abs = strings.ToUpper(abs[:1]) + abs[1:]
	}
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return (&url.URL{Scheme: "file", Path: abs}).String()
}

// canonicalURI rewrites a URI the server sent into the form toURI makes,
// so a server that escapes differently from us — "c%3A" for a drive
// letter, a lower-case drive, an escaped character we leave alone — still
// files its diagnostics under the key the waiting caller is looking at.
func canonicalURI(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	return toURI(fromURI(uri))
}

func isASCIILetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }

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
	s := b.String()
	// "file:///C:/src" names C:\src; the slash before the drive letter
	// belongs to the URI, not to the path.
	if runtime.GOOS == "windows" && len(s) >= 3 && s[0] == '/' && s[2] == ':' && isASCIILetter(s[1]) {
		s = s[1:]
	}
	return filepath.FromSlash(s)
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
