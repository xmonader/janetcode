package tool

import "fmt"

// MaxWriteBytes caps what one write_file or edit_file call may put on
// disk. A runaway model can emit a write far larger than any file in
// the workspace before the token or cost budget notices — the write
// executes first — so the size itself is the earlier tripwire. The cap
// is generous: real source files, lockfiles and fixtures are orders of
// magnitude under it. A caller that genuinely needs more writes the
// file in pieces.
const MaxWriteBytes = 8 << 20

// checkWriteSize refuses n bytes over MaxWriteBytes, naming what the
// caller was measuring; hint says what to do instead, per tool.
func checkWriteSize(what string, n int, hint string) error {
	if n <= MaxWriteBytes {
		return nil
	}
	return fmt.Errorf("%s is %d bytes, over the %d-byte (8 MiB) ceiling for one file-write call — %s", what, n, MaxWriteBytes, hint)
}
