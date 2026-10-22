package tool

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

// A terminal's safety check follows the cd of earlier sends: `cd /` and
// then `rm -rf home` deletes /home, and is judged as that.
func TestTermGuardFollowsCd(t *testing.T) {
	root := "/work/proj"
	terms := &Terms{all: []*Term{{ID: 1, cwd: root, cwdKnown: true}}}
	tl := TermTool{Terms: terms, Root: root}
	send := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"action": "send", "id": 1, "text": text})
		return b
	}
	if risk, _ := shell.Classify("rm -rf /home", root); risk == shell.RiskOrdinary {
		t.Fatal("premise: rm -rf /home should not be ordinary")
	}
	if ask, _ := tl.Guard(send("rm -rf home")); ask {
		t.Fatal("rm -rf home inside the workspace should not ask")
	}
	terms.follow(1, "cd /")
	ask, why := tl.Guard(send("rm -rf home"))
	absAsk, absWhy := tl.Guard(send("rm -rf /home"))
	if !ask || !absAsk {
		t.Errorf("after cd /, rm -rf home = (%v, %q), rm -rf /home = (%v, %q)", ask, why, absAsk, absWhy)
	}
	if tl.Warn(send("rm -rf home")) == "" {
		t.Error("no warning for rm -rf home after cd /")
	}
}

func TestTermCwdTracking(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		cmds  []string
		cwd   string
		known bool
	}{
		{[]string{"cd /"}, "/", true},
		{[]string{"cd /usr", "cd lib"}, "/usr/lib", true},
		{[]string{"cd /usr && cd .."}, "/", true},
		{[]string{"cd /usr", "cd"}, home, true},
		{[]string{"cd /usr", "cd ~"}, home, true},
		{[]string{"cd /usr", "cd -"}, "", false},
		{[]string{"cd $X"}, "", false},
		{[]string{"cd $(mktemp -d)"}, "", false},
		{[]string{"pushd /usr"}, "/usr", true},
		{[]string{"pushd /usr", "popd"}, "", false},
		{[]string{"ls"}, "/work", true},
	} {
		terms := &Terms{all: []*Term{{ID: 1, cwd: "/work", cwdKnown: true}}}
		for _, cmd := range c.cmds {
			terms.follow(1, cmd)
		}
		cwd, known := terms.cwdOf(1, "")
		if known != c.known || (known && !strings.EqualFold(cwd, c.cwd)) {
			t.Errorf("%q: cwd = (%q, %v), want (%q, %v)", c.cmds, cwd, known, c.cwd, c.known)
		}
	}
}

// Once a cd cannot be followed, a relative destructive path is judged as
// one from an unknown directory.
func TestTermUnknownCwdIsConservative(t *testing.T) {
	root := "/work/proj"
	terms := &Terms{all: []*Term{{ID: 1, cwd: root, cwdKnown: true}}}
	tl := TermTool{Terms: terms, Root: root}
	rm := json.RawMessage(`{"action":"send","id":1,"text":"rm -rf build"}`)
	if ask, _ := tl.Guard(rm); ask {
		t.Fatal("rm -rf build inside the workspace should not ask")
	}
	terms.follow(1, "cd $(pwd)/..")
	if ask, _ := tl.Guard(rm); !ask {
		t.Error("rm -rf build from an unknown directory should ask")
	}
}
