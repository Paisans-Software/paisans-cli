package siteadd_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/siteadd"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// A host clock that does not print one positive integer is a clock that
// could not be read, said as such, and not a mesh whose handshakes are
// all missing.
func TestAnUnreadableClockIsNotAMissingHandshake(t *testing.T) {
	defer siteadd.SetFast()()
	for _, clock := range []string{"", "Thu Oct 15 12:00:00 UTC 2026\n", "0\n", "-5\n", "1000 2000\n", "\x1b[2Jboom\n"} {
		w := newWorld(t)
		w.clock = &clock
		err := siteadd.Execute(build(t, w))
		if err == nil || !strings.Contains(err.Error(), "could not read home-a's clock") {
			t.Fatalf("clock %q: want the clock named as unreadable, got %v", clock, err)
		}
		if strings.Contains(err.Error(), "never completed a handshake") {
			t.Errorf("clock %q: an unreadable clock was taken for missing handshakes: %v", clock, err)
		}
		if strings.Contains(err.Error(), "\x1b") {
			t.Errorf("clock %q: the host's output reached the error raw: %q", clock, err)
		}
		if want := ui.Visible(strings.TrimSpace(clock)); !strings.Contains(err.Error(), `"`+want+`"`) {
			t.Errorf("clock %q: the error does not quote what date printed, %q: %v", clock, want, err)
		}
	}
}

// What a host printed reaches a mesh gate's error visible and redacted: a
// terminal escape as its Go escape, and a registered secret not at all.
func TestTheMeshGateShowsHostOutputVisiblyAndRedacted(t *testing.T) {
	defer siteadd.SetFast()()
	secret := "mesh-gate-secret-9f3c"
	ui.Secret(secret)
	w := newWorld(t)
	w.noPing = true
	w.pingOut = "\x1b[2J" + secret + " 100% packet loss\n"
	err := siteadd.Execute(build(t, w))
	if err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("want a failed ping in the error, got %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), `\x1b[2J`) {
		t.Errorf("the ping's escape is not shown as its Go escape: %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("a registered secret reached the error: %q", err)
	}

	w = newWorld(t)
	failed := "\x1b[2J" + secret + "\n"
	w.handshakesFail = &failed
	err = siteadd.Execute(build(t, w))
	if err == nil {
		t.Fatal("want the failed handshake read to stop the gate")
	}
	if strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), secret) {
		t.Errorf("the handshake read's output reached the error raw: %q", err)
	}
}
