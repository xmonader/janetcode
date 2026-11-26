package agent

import (
	"io/fs"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/project"
)

// A weak model often ends a turn on edits it never ran a check against
// (gpt-oss-20b: 10 of 32 failed benchmark runs, some after about seven
// calls); strong models always run the tests. The verdict already knows
// this case (Edited > 0 and no check, or an edit after the last check) but
// only acts when the answer claims a pass. This sends the turn back once
// when it ends on edits no check has covered and the project has something
// to run, and does nothing else: a turn that checked, changed nothing or
// has no tests is not touched. JANETCODE_OFF=unverified turns it off.

const uncheckedNote = "[harness] You changed files but have not run the tests or any check since. Run them now and fix what fails before you finish."

// uncheckedGate reports whether the turn goes back for a check.
func (a *Agent) uncheckedGate(outcome TurnOutcome, noted *bool, emit func(Event)) bool {
	if !a.UncheckedNote || a.Off["unverified"] || *noted || a.DryRunOn() || outcome.Edited == 0 {
		return false
	}
	if outcome.Ran > 0 && !outcome.EditedAfterCheck {
		return false
	}
	if !a.projectHasCheck(a.displayRoot()) {
		return false
	}
	*noted = true
	a.stats.uncheckedNoted()
	emit(Event{Kind: EvNotice, Text: "the turn ends on edits no check has run against; asking the model to run one"})
	a.appendHarnessText(uncheckedNote)
	return true
}

// projectHasCheck reports whether the workspace has a runnable check: a
// known test command for the project, or test files.
func (a *Agent) projectHasCheck(root string) bool {
	if root == "" {
		return false
	}
	if p, ok := project.Detect(root); ok && p.Verify != "" {
		return true
	}
	found, seen := false, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > ledgerMaxFiles {
			return filepath.SkipAll
		}
		if isTestFile(d.Name()) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
