package main

import (
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/version"
)

// buildGate is what this build refuses before any command runs: every
// command when the commit set with -ldflags -X is no commit a record may
// hold, and, when the build names no commit at all, any command that
// writes a record. A command writes one with --execute, and render writes
// its manifests whenever it runs; everything else (version, help,
// validate, doctor, a dry run) only reads.
func buildGate(args []string) error {
	if err := version.CheckLinked(); err != nil {
		return err
	}
	if writesRecords(args) {
		return version.Writable()
	}
	return nil
}

// writesRecords reports whether the command line args runs a command that
// writes a record.
func writesRecords(args []string) bool {
	if len(args) > 0 && args[0] == "render" {
		return true
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "execute" {
			continue
		}
		if !hasValue {
			return true
		}
		if on, err := strconv.ParseBool(value); err != nil || on {
			return true
		}
	}
	return false
}
