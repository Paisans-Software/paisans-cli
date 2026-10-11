package ui_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// healthProblem is the shape of the health gate's error: a hint, one cause
// line and what was done, with the raw log tail as its cause.
func healthProblem() *ui.Problem {
	return &ui.Problem{
		Hint:    "home-a: stack talk is not healthy: app (exited, exit code 1)",
		Explain: "app: database unreachable\nThe app was started.",
		Cause:   errors.New("--- app, last 30 log lines ---\napp-1  | raw log line"),
	}
}

// A command that words a Problem inside its own context keeps the Problem's
// hint and explanation in the default output, adds its own advice after
// them, and leaves the cause to --verbose.
func TestAroundKeepsTheProblemShortAndItsCauseForVerbose(t *testing.T) {
	inner := fmt.Errorf("gate: %w", healthProblem())
	p := ui.Around("site add home-b stopped at stage 6 (haproxy)", inner, "Run site add again.")
	if p == nil {
		t.Fatal("no Problem around a Problem")
	}
	if !errors.Is(p, inner) {
		t.Error("the wrapped error is not in the chain")
	}
	var plain bytes.Buffer
	ui.PrintError(&plain, p, false)
	out := plain.String()
	for _, want := range []string{"FAIL site add home-b stopped at stage 6 (haproxy)", "stack talk is not healthy", "app: database unreachable", "The app was started.", "Run site add again."} {
		if !strings.Contains(out, want) {
			t.Errorf("default output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "raw log line") {
		t.Errorf("default output shows the logs:\n%s", out)
	}
	var verbose bytes.Buffer
	ui.PrintError(&verbose, p, true)
	if !strings.Contains(verbose.String(), "raw log line") {
		t.Errorf("verbose output lacks the logs:\n%s", verbose.String())
	}
}

// With no hint of its own, the advice is added to the Problem's own, and
// --verbose does not repeat its hint.
func TestAroundWithNoHintExtendsTheProblem(t *testing.T) {
	p := ui.Around("", healthProblem(), "Run paisans apply to finish.")
	var plain bytes.Buffer
	ui.PrintError(&plain, p, false)
	out := plain.String()
	if !strings.HasPrefix(out, "FAIL home-a: stack talk is not healthy") || !strings.Contains(out, "Run paisans apply to finish.") {
		t.Errorf("got:\n%s", out)
	}
	var verbose bytes.Buffer
	ui.PrintError(&verbose, p, true)
	if n := strings.Count(verbose.String(), "stack talk is not healthy"); n != 1 {
		t.Errorf("the hint shows %d times with -v:\n%s", n, verbose.String())
	}
	if !strings.Contains(verbose.String(), "raw log line") {
		t.Errorf("verbose output lacks the logs:\n%s", verbose.String())
	}
}

// An error with no Problem in it is left for the caller to word as before.
func TestAroundIgnoresAPlainError(t *testing.T) {
	if p := ui.Around("x", errors.New("plain"), "y"); p != nil {
		t.Errorf("got %v", p)
	}
}
