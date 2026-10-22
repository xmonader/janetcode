package shell

import "testing"

// Commands that destroy work through something their path arguments do not
// show must not be graded ordinary, and must not be a safe read.
func TestClassifyDestructive(t *testing.T) {
	root := t.TempDir()
	destructive := []string{
		`python -c "open('f.txt','w').write('x')"`,
		`python3 -c "import shutil; shutil.rmtree('build')"`,
		`python3 -c 'import os; os.remove("a")'`,
		`python3 -c "from pathlib import Path; Path('a').write_text('x')"`,
		`node -e "require('fs').writeFileSync('a','b')"`,
		`node -e "fs.rmSync('d',{recursive:true})"`,
		`perl -e 'unlink "a"'`,
		`perl -e 'unlink glob "*.o"'`,
		`ruby -e 'File.delete("a")'`,
		`xargs rm -f`,
		`find . -name '*.o' | xargs rm -rf`,
		`xargs -0 -n1 mv`,
		`git push --force origin main`,
		`git push -f`,
		`git push origin +main`,
		`git push origin :old-branch`,
		`git reset --hard HEAD~3`,
		`git clean -fdx`,
		`git checkout -- .`,
		`git checkout -f main`,
		`git restore .`,
		`git branch -D topic`,
		`git stash drop`,
		`git -C sub reset --hard`,
		`sudo git clean -fd`,
		`sh -c "git reset --hard"`,
	}
	for _, c := range destructive {
		if r, _ := Classify(c, root); r < RiskDestructive {
			t.Errorf("Classify(%q) = %v, want at least destructive", c, r)
		}
		if SafeRead(c) {
			t.Errorf("SafeRead(%q) = true", c)
		}
	}
	ordinary := []string{
		`python3 -c "print(1 > 0)"`,
		`python -c "import os; print(os.getcwd(), os.path.join('a','b'))"`,
		`python3 script.py`,
		`node -e "console.log(process.argv)"`,
		`xargs grep foo`,
		`find . -name '*.go' | xargs wc -l`,
		`git status`,
		`git push origin main`,
		`git checkout main`,
		`git reset --soft HEAD~1`,
		`git clean -n`,
		`git restore --staged f.go`,
		`git branch -a`,
		`git stash`,
		`git stash pop`,
		`git commit -m "fix: reset --hard docs"`,
	}
	for _, c := range ordinary {
		if r, why := Classify(c, root); r != RiskOrdinary {
			t.Errorf("Classify(%q) = %v (%s), want ordinary", c, r, why)
		}
	}
}
