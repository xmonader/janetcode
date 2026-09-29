package agent

import (
	"strings"
	"testing"
)

func TestSecurityReportRanksAndDedupes(t *testing.T) {
	a := "Findings:\n[LOW] a.go:3 — log noise — n/a — drop it\n- **HIGH**: srv.go:40 — path traversal — ../ in name — Clean and check prefix\nHigh-level summary: fine otherwise"
	b := "1. CRITICAL | db.go:9 — SQL built with Sprintf — id param — use placeholders\n[HIGH] srv.go:40 — path traversal — ../ in name — Clean and check prefix\nNO FINDINGS"
	got := SecurityReport([]string{a, b}, nil)
	want := "security review: 3 findings (1 critical, 1 high, 1 low)\nCRITICAL db.go:9"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("report =\n%s", got)
	}
	if strings.Index(got, "HIGH") > strings.Index(got, "LOW ") || strings.Count(got, "srv.go:40") != 1 {
		t.Fatalf("not ranked or not deduped:\n%s", got)
	}
	if strings.Contains(got, "level summary") {
		t.Fatalf("a sentence starting with High was read as a finding:\n%s", got)
	}
}

func TestSecurityReportKeepsUnformattedReport(t *testing.T) {
	got := SecurityReport([]string{"NO FINDINGS", "The handler trusts X-Forwarded-For for its allowlist."}, []string{"security: access (timeout)"})
	if !strings.Contains(got, "no ranked findings") || !strings.Contains(got, "X-Forwarded-For") || !strings.Contains(got, "did not finish: security: access") {
		t.Fatalf("report =\n%s", got)
	}
	if got := SecurityReport([]string{"NO FINDINGS", "NO FINDINGS"}, nil); got != "security review: no findings" {
		t.Fatalf("clean = %q", got)
	}
}

func TestSecurityReviewTasksAreReadOnlyWithFormat(t *testing.T) {
	for _, in := range SecurityReviewTasks("", "DIFF") {
		if in.Agent != "explore" || !strings.Contains(in.Prompt, "[SEVERITY] path:line") || !strings.HasSuffix(in.Prompt, "DIFF") {
			t.Fatalf("task = %+v", in)
		}
	}
}
