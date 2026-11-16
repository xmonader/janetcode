package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// MaxPageBytes caps one rendered page. A page is HTML the model wrote, so
// 2 MiB is far more than a chart or a table needs and still small enough
// that a runaway loop cannot fill the disk one turn at a time.
const MaxPageBytes = 2 << 20

// Page is the metadata of one HTML page attached to a session by the
// render_html tool. The page body is <id>.page-<n>.html beside the
// session file, which a browser can open directly; this record is
// <id>.page-<n>.json. Both match the glob Delete uses for sidecars, so
// removing the session removes its pages.
type Page struct {
	N       int       `json:"n"`
	Title   string    `json:"title"`
	Created time.Time `json:"created"`
	Bytes   int       `json:"bytes"`
}

var pageMu sync.Mutex

var pageFileRE = regexp.MustCompile(`\.page-(\d+)\.html$`)

func pageBase(sessionPath string, n int) string {
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".page-" + strconv.Itoa(n)
}

// PagePath is the HTML file of page n of the session at sessionPath.
func PagePath(sessionPath string, n int) string { return pageBase(sessionPath, n) + ".html" }

// PathFor is the session file of id under dir, refusing an id that could
// name anything outside it.
func PathFor(dir, id string) (string, error) { return pathOf(dir, id) }

// SavePage stores html as the next page of the session at sessionPath
// and returns its record. An empty page, or one over MaxPageBytes, is an
// error that says so; nothing is written.
func SavePage(sessionPath, title string, html []byte) (Page, error) {
	if strings.TrimSpace(string(html)) == "" {
		return Page{}, errors.New("html is empty")
	}
	if len(html) > MaxPageBytes {
		return Page{}, fmt.Errorf("page is %d bytes; the limit is %d (2 MiB). Trim it, or load large data from a CDN", len(html), MaxPageBytes)
	}
	if sessionPath == "" {
		return Page{}, errors.New("no session to attach the page to")
	}
	pageMu.Lock()
	defer pageMu.Unlock()
	pages, err := ListPages(sessionPath)
	if err != nil {
		return Page{}, err
	}
	n := 1
	if len(pages) > 0 {
		n = pages[len(pages)-1].N + 1
	}
	p := Page{N: n, Title: strings.TrimSpace(title), Created: time.Now().UTC(), Bytes: len(html)}
	meta, err := json.Marshal(p)
	if err != nil {
		return Page{}, err
	}
	if err := fsutil.AtomicWriteMode(PagePath(sessionPath, n), html, fsutil.PrivateFileMode); err != nil {
		return Page{}, fmt.Errorf("save page: %w", err)
	}
	if err := fsutil.AtomicWriteMode(pageBase(sessionPath, n)+".json", meta, fsutil.PrivateFileMode); err != nil {
		_ = os.Remove(PagePath(sessionPath, n))
		return Page{}, fmt.Errorf("save page: %w", err)
	}
	return p, nil
}

// LoadPage reads page n of the session at sessionPath.
func LoadPage(sessionPath string, n int) (Page, []byte, error) {
	html, err := os.ReadFile(PagePath(sessionPath, n))
	if err != nil {
		return Page{}, nil, err
	}
	p := Page{N: n, Bytes: len(html)}
	if raw, err := os.ReadFile(pageBase(sessionPath, n) + ".json"); err == nil {
		_ = json.Unmarshal(raw, &p)
		p.N = n
	}
	return p, html, nil
}

// ListPages lists the session's pages, oldest first. A session with none
// is not an error.
func ListPages(sessionPath string) ([]Page, error) {
	files, err := filepath.Glob(strings.TrimSuffix(sessionPath, ".jsonl") + ".page-*.html")
	if err != nil {
		return nil, err
	}
	var out []Page
	for _, f := range files {
		m := pageFileRE.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		p, _, err := LoadPage(sessionPath, n)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out, nil
}
