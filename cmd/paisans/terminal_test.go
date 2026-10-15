package main

import (
	"os"
	"strings"
	"testing"
)

// /dev/null is a character device, and an older check took it for a
// terminal: the real host finding. It is how an unattended run says there is
// nothing to read.
func TestStdinIsTerminalRefusesDevNull(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	if stdinIsTerminal(devNull) {
		t.Error("/dev/null was taken for a terminal")
	}
}

func TestStdinIsTerminalIgnoresAReader(t *testing.T) {
	if stdinIsTerminal(strings.NewReader("x")) {
		t.Error("a strings.Reader was taken for a terminal")
	}
}
