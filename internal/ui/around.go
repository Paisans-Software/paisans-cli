package ui

import (
	"errors"
	"strings"
)

// Around is err told in a caller's own words, when err holds a Problem: a
// Problem whose hint is the caller's, whose explanation is err's hint and
// explanation followed by next, the caller's advice, and whose cause is err,
// so --verbose still shows err's own cause and errors.Is and errors.As still
// find what it wraps. Folding err's text into a message of the caller's
// instead would print that cause, Eg: the health gate's log tail, by default.
//
// With an empty hint the result keeps err's hint and adds next to its
// explanation. When err is the Problem itself, the result takes its cause in
// place of err, so --verbose does not repeat the hint.
//
// For an error that holds no Problem it is nil, and the caller words the
// error as it would anyway.
func Around(hint string, err error, next string) *Problem {
	var p *Problem
	if !errors.As(err, &p) {
		return nil
	}
	inner, explain := Describe(err)
	var lines []string
	cause := err
	if hint == "" {
		hint = inner
		if q, ok := err.(*Problem); ok {
			cause = q.Cause
		}
	} else {
		lines = append(lines, strings.TrimSuffix(inner, ".")+".")
	}
	for _, s := range []string{explain, next} {
		if s != "" {
			lines = append(lines, s)
		}
	}
	return &Problem{Hint: hint, Explain: strings.Join(lines, "\n"), Cause: cause}
}
