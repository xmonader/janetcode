package shell

import "testing"

// A here-document's body is stdin, not commands: it is taken out, and
// the command it feeds kept. Anything read with less than certainty
// leaves the line as it was, so its lines are still judged as commands.
func TestStripHeredocs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git commit -F - <<'EOF'\nfix: x\n\nbody\nEOF", "git commit -F -  \n"},
		{"git commit -m \"$(cat <<'EOF'\nfix: don't break (x)\nEOF\n)\"", "git commit -m \"$(cat  \n)\""},
		{"cat <<-END\n\tplain\n\tEND\nls", "cat  \nls"},
		{"cat <<\"EOF\"\n$(rm -rf x)\nEOF\nls", "cat  \nls"},
		{"cat <<EOF\nplain $HOME\nEOF", "cat  \n"},
		{"cat <<<'x <<EOF'", "cat <<<'x <<EOF'"},
		{"echo '<<EOF'\nrm -rf x\nEOF", "echo '<<EOF'\nrm -rf x\nEOF"},
		{"echo \"<<EOF\"\nrm -rf x\nEOF", "echo \"<<EOF\"\nrm -rf x\nEOF"},
	}
	for _, c := range cases {
		if got := StripHeredocs(c.in); got != c.want {
			t.Errorf("StripHeredocs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Each of these hides a command behind a << the shell does not read
	// as a here-document, or one it would run, or one never closed: the
	// line must come back unchanged.
	for _, in := range []string{
		"echo $((1<<2))\nrm -rf x\n2",
		"((x<<2))\nrm -rf x\n2",
		"true # <<EOF\nrm -rf x\nEOF",
		"cat <<EOF\n$(rm -rf x)\nEOF",
		"cat <<EOF\n`rm -rf x`\nEOF",
		"cat <<'EOF'\nrm -rf x",
		"echo ${x<<EOF}\nrm -rf x\nEOF",
		"echo \"$(case a in a) echo \"<<EOF\";; esac)\"\nrm -rf x\nEOF",
		"cat <<$X\nrm -rf x\n$X",
	} {
		if got := StripHeredocs(in); got != in {
			t.Errorf("StripHeredocs(%q) = %q, want it unchanged", in, got)
		}
	}
}
