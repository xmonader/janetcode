package tool

import "encoding/json"

// ReadFileData is a decoded text window. Lines contain source text without
// display gutters or anchors. Offset is one-based; Truncated means there
// are more source lines after the window, independent of display clipping.
// Notebook and document extraction results do not currently provide Data.
type ReadFileData struct {
	Path       string   `json:"path"`
	Offset     int      `json:"offset"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
	Truncated  bool     `json:"truncated"`
}

// GlobData contains paths relative to Path, in the same order as the
// display. Truncated means the result count exceeded the tool's limit.
type GlobData struct {
	Path      string   `json:"path"`
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// GrepMatch identifies a matching source line. Text has no path/line
// decoration, but remains bounded by the tool's per-line display limit.
type GrepMatch struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// GrepData contains matches relative to the requested directory (or the
// requested path for a single file). Truncated marks the match-count cap;
// PartiallySearchedLines counts source lines beyond the search byte limit.
type GrepData struct {
	Path                   string      `json:"path"`
	Matches                []GrepMatch `json:"matches"`
	Truncated              bool        `json:"truncated"`
	PartiallySearchedLines int         `json:"partially_searched_lines"`
}

// setResultData only encodes when a programmatic caller requested it.
func setResultData(dst *json.RawMessage, value any) error {
	if dst == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	*dst = b
	return nil
}
