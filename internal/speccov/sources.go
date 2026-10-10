package speccov

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Limits on what is read from the workspace: the task's files and the
// test files are read whole, so a huge repository costs a bounded walk.
const (
	maxSpecFileBytes = 256 << 10
	maxTestFileBytes = 512 << 10
	maxTestBytes     = 4 << 20
	maxWalkFiles     = 5000
)

// docNameRe finds a document named in a prompt: "Read BRIEF.md and ...".
var docNameRe = regexp.MustCompile(`[\w./-]+\.(?:md|markdown|txt|rst)\b`)

// TaskText is the text the matrix reads: the prompt itself, followed by
// each document it names that exists under root. It returns the files it
// read, relative to root.
func TaskText(root, prompt string) (string, []string) {
	var b strings.Builder
	b.WriteString(prompt)
	var files []string
	seen := map[string]bool{}
	for _, name := range docNameRe.FindAllString(prompt, -1) {
		p := filepath.Clean(name)
		if filepath.IsAbs(p) || strings.HasPrefix(p, "..") || seen[p] {
			continue
		}
		seen[p] = true
		data, ok := readCapped(filepath.Join(root, p), maxSpecFileBytes)
		if !ok {
			continue
		}
		b.WriteString("\n\n")
		b.Write(data)
		files = append(files, p)
	}
	return b.String(), files
}

// skipDirs are directories that hold no public test of the project.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true,
	".mypy_cache": true, ".tox": true, ".janetcode": true,
}

// IsTestFile reports whether a path, relative to the workspace, is a test
// file by the naming conventions of the languages effbench and most
// projects use.
func IsTestFile(rel string) bool {
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch ext {
	case ".py":
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") || stem == "tests"
	case ".go":
		return strings.HasSuffix(stem, "_test")
	case ".ts", ".js", ".mjs", ".cjs", ".tsx", ".jsx":
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") || inDir(rel, "tests", "__tests__", "test")
	case ".rs":
		return inDir(rel, "tests") || strings.HasSuffix(stem, "_test") || stem == "tests"
	case ".rb":
		return strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, "_spec")
	case ".java", ".kt":
		return strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests")
	}
	return false
}

func inDir(rel string, names ...string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		for _, n := range names {
			if part == n {
				return true
			}
		}
	}
	return false
}

// PublicTests reads the test files under root, as relative path →
// contents, within the walk and size limits.
func PublicTests(root string) map[string]string {
	out := map[string]string{}
	total, walked := 0, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		walked++
		if walked > maxWalkFiles || total > maxTestBytes {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || !IsTestFile(rel) {
			return nil
		}
		data, ok := readCapped(p, maxTestFileBytes)
		if !ok {
			return nil
		}
		total += len(data)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	return out
}

// readCapped reads a regular file no larger than limit.
func readCapped(p string, limit int64) ([]byte, bool) {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, false
	}
	data, err := os.ReadFile(p)
	return data, err == nil
}
