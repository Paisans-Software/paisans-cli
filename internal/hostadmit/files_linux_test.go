//go:build linux

package hostadmit

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// readFile judges a file by its type before it opens it for reading: a FIFO
// opened for reading waits for (or completes the open of) a writer, and a
// device may act on being opened, in a process that runs as root. So a
// FIFO, a character device and a directory are each refused without the
// open for reading ever being made, and a regular file is read through it.
func TestReadFileNeverOpensASpecialFileForReading(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opened []string
	saved := openForReading
	openForReading = func(dirfd int, name string, flags int, mode uint32) (int, error) {
		opened = append(opened, name)
		return saved(dirfd, name, flags, mode)
	}
	t.Cleanup(func() { openForReading = saved })
	for _, c := range []struct{ what, path string }{
		{"a FIFO", fifo}, {"a character device", "/dev/null"}, {"a directory", dir},
	} {
		opened = nil
		if _, found, err := readFile(c.path); found || err == nil {
			t.Errorf("%s: found %v, %v", c.what, found, err)
		}
		if len(opened) != 0 {
			t.Errorf("%s was opened for reading (%v) before its type was judged", c.what, opened)
		}
	}
	opened = nil
	if got, found, err := readFile(regular); err != nil || !found || got != "content" {
		t.Errorf("a regular file: %q, %v, %v", got, found, err)
	}
	if len(opened) != 1 {
		t.Errorf("a regular file was opened for reading %d times, want once through the seam", len(opened))
	}
}
