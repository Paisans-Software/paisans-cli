package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/paisans-software/paisans-cli/internal/version"
)

// runVersion prints the binary's version and the commit it was built from.
func runVersion(args []string, w io.Writer) error {
	fs := newFlagSet("version", flag.ContinueOnError)
	commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version takes no arguments, and was given %q", fs.Arg(0))
	}
	fmt.Fprintf(w, "paisans %s\n", toolkitVersion())
	return nil
}

// toolkitVersion is this binary's version, with the commit the Go toolchain
// recorded it was built from, as "0.3.0 (519299d5cb00-dirty)".
func toolkitVersion() string { return version.Current().String() }
