// Package progname is the name the command was invoked by, for every line
// that tells an operator which command to run. A release installs the command
// as paisans, but a test or development build is often installed beside it
// under another name (Eg: paisans-test), and advice that names paisans would
// send the operator to the other build.
//
// Only text for the person who invoked the command, naming a command they
// run here, names it so. A command to be run on a host names Default, the
// release's name installed there, whoever reads it; so does advice addressed
// to someone else, such as the host's owner, and text written onto a host or
// into a record: it outlives the run, and another operator's paisans reads it.
package progname

import (
	"path/filepath"
	"strings"
)

// Default is the name a release installs the command under: the name in
// advice for anyone but the person who invoked the command, and the name
// until Set is called, as in tests.
const Default = "paisans"

var name = Default

// Set records the name the command was invoked by, the last element of argv0
// (os.Args[0]). An argv0 with no name in it leaves Default. main calls it
// first, before any text is built; a package-level variable built from Name
// is built before main runs, so text that names a command is built when it is
// printed.
func Set(argv0 string) {
	name = Default
	if base := filepath.Base(argv0); argv0 != "" && base != "." && base != string(filepath.Separator) {
		name = base
	}
}

// Name is the name the command was invoked by, for prose. A command line to
// copy takes Cmd.
func Name() string { return name }

// Cmd is Name as it goes in a command line to copy: Name itself, or Name in
// single quotes when it holds anything but letters, digits, dots,
// underscores and hyphens, so a pasted line runs the command it names.
func Cmd() string {
	safe := name != "" && strings.IndexFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) < 0
	if safe {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
}
