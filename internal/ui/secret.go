package ui

import (
	"io"
	"slices"
	"strings"
	"sync"
)

// Secrets.
//
// A secret this process holds for a host, such as the sudo password an
// operator typed, must never reach the operator's screen or a log, whatever
// path text takes to get there: a host's output quoted in a warning, an
// error's cause under --verbose, a retry line. The text that reaches the
// screen comes from too many places for each to be trusted to leave it out,
// so the one place it all passes through, this package, takes it out: every
// line a reporter writes, every line PrintError writes, and every event a
// Recorder keeps. Secret registers a value, and Redact is the one function
// that strips every value registered.
//
// Redact runs on whole strings before they are wrapped or cut to a
// terminal's width, so a secret split across two lines, or cut short at the
// edge, is still found whole.

// redacted is what stands in for a secret.
const redacted = "[redacted]"

var secrets struct {
	sync.RWMutex
	// list is longest first, so a secret that holds another is replaced
	// whole rather than around the shorter one.
	list []string
}

// Secret registers s, so that Redact strips it from everything shown from
// now on. An empty s is ignored.
func Secret(s string) {
	if s == "" {
		return
	}
	secrets.Lock()
	defer secrets.Unlock()
	if slices.Contains(secrets.list, s) {
		return
	}
	secrets.list = append(secrets.list, s)
	slices.SortStableFunc(secrets.list, func(a, b string) int { return len(b) - len(a) })
}

// Redact is text with every registered secret replaced by [redacted].
func Redact(text string) string {
	secrets.RLock()
	defer secrets.RUnlock()
	for _, s := range secrets.list {
		if strings.Contains(text, s) {
			text = strings.ReplaceAll(text, s, redacted)
		}
	}
	return text
}

// RedactError is err with Redact applied to its text. errors.Is and
// errors.As still see through it to what it wraps. A Problem stays a
// Problem, its hint and explanation redacted and its cause wrapped, so a
// caller that asks whether err is one still finds it. Nil stays nil.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	if p, ok := err.(*Problem); ok {
		return &Problem{Hint: Redact(p.Hint), Explain: Redact(p.Explain), Cause: RedactError(p.Cause)}
	}
	return redactedError{err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return Redact(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// RedactWriter writes to w what Redact makes of each write, for output
// that does not go through a reporter. A write is redacted on its own, so
// a caller writes a line or more at a time, never a secret in pieces.
func RedactWriter(w io.Writer) io.Writer { return redactWriter{w} }

type redactWriter struct{ w io.Writer }

func (r redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
