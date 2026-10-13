package ui_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// A name in a message is set off in backticks, so `app` reads as the name of
// a service rather than the word.
func TestNameSetsANameOffInBackticks(t *testing.T) {
	for in, want := range map[string]string{
		"talk":                        "`talk`",
		"apps.talk.images.app":        "`apps.talk.images.app`",
		"ghcr.io/example/mbin:v1.2.3": "`ghcr.io/example/mbin:v1.2.3`",
	} {
		if got := ui.Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}
