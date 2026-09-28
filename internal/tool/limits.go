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

// maxReadBytes caps the file read_file and read_many_files will load. A
// read decodes, copies and splits the whole file to show at most 2000
// lines of it, so its peak memory is several times the file's size, and
// a multi-gigabyte log would take the process down to answer one call.
// Past this the file is refused on its stat, and the model is pointed at
// the tools that stream.
const maxReadBytes = 128 << 20

// checkReadSize refuses a file whose stat'ed size is over maxReadBytes.
func checkReadSize(display string, size int64) error {
	if size <= maxReadBytes {
		return nil
	}
	return fmt.Errorf("%s is too large to read whole (%d bytes, over the %d MiB ceiling); search it with grep, or take a window of it with bash (sed -n '100,200p', head, tail)", display, size, maxReadBytes>>20)
}

// checkWriteSize refuses n bytes over MaxWriteBytes, naming what the
// caller was measuring; hint says what to do instead, per tool.
func checkWriteSize(what string, n int, hint string) error {
	if n <= MaxWriteBytes {
		return nil
	}
	return fmt.Errorf("%s is %d bytes, over the %d-byte (8 MiB) ceiling for one file-write call — %s", what, n, MaxWriteBytes, hint)
}
