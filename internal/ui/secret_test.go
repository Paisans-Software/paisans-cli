package ui_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// secret stands in for a sudo password, with a space so that wrapping
// would split it. It is not anyone's.
const secret = "not a-real-password"

// Every way text reaches the screen strips a registered secret: each
// reporter method, drawn on a terminal and plain, with --verbose, a line
// wrapped at the space inside the secret, a spinner line cut at the
// terminal's edge, and the error a command ends on.
func TestASecretNeverReachesTheScreen(t *testing.T) {
	ui.Secret(secret)
	long := strings.Repeat("word ", 14) + secret + " " + strings.Repeat("more ", 10)
	for _, terminal := range []bool{true, false} {
		var b strings.Builder
		r := ui.NewForTestWidth(&b, terminal, 40, func() time.Time { return time.Time{} })
		if !terminal {
			r = ui.NewPlain(&b, true)
		}
		r.Section("site (" + secret + ")")
		s := r.Step("a step whose title runs to " + secret)
		s.Progress("reading " + secret)
		s.Detail("detail %s", secret)
		s.Fail(errors.New("failed: " + secret))
		r.Warn("warn "+secret, long)
		r.Note("note "+secret, long)
		r.Explain(long)
		r.Refuse("refuse "+secret, long)
		r.Detail("%s", long)
		r.Trace("trace", secret+"\n"+long)
		r.Result("result %s", secret)
		lines := r.(interface {
			Group(...string) []ui.Line
		}).Group("line " + secret)
		lines[0].Explain(long)
		lines[0].Fail(errors.New(secret))
		ui.PrintError(&b, &ui.Problem{Hint: "hint " + secret, Explain: long, Cause: errors.New("cause\n" + secret)}, true)
		if strings.Contains(b.String(), "a-re") {
			t.Errorf("terminal %v: the secret, or part of it, is shown:\n%s", terminal, b.String())
		}
		if !strings.Contains(b.String(), "[redacted]") {
			t.Errorf("terminal %v: nothing was redacted:\n%s", terminal, b.String())
		}
	}

	rec := &ui.Recorder{}
	rec.Step("step " + secret).Fail(errors.New(secret))
	rec.Warn(secret, secret)
	if strings.Contains(rec.Lines(), secret) {
		t.Errorf("the recorder keeps the secret:\n%s", rec.Lines())
	}
}

// A redacted error is still what it wraps to errors.Is and errors.As, and a
// Problem stays a Problem.
func TestARedactedErrorStillUnwraps(t *testing.T) {
	ui.Secret(secret)
	sentinel := errors.New("sentinel")
	err := ui.RedactError(errors.Join(sentinel, errors.New(secret)))
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), secret) {
		t.Errorf("redacted %v", err)
	}
	p := &ui.Problem{Hint: secret, Explain: secret, Cause: errors.Join(sentinel, errors.New(secret))}
	var got *ui.Problem
	err = ui.RedactError(p)
	if !errors.As(err, &got) || got.Hint != "[redacted]" || got.Explain != "[redacted]" || !errors.Is(err, sentinel) || strings.Contains(err.Error(), secret) {
		t.Errorf("redacted problem %#v", err)
	}
	if ui.RedactError(nil) != nil {
		t.Error("nil is not nil")
	}
}
