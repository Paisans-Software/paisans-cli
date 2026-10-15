package siteadd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
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

// sudo refusing the helper is the host's owner's problem, said as such, and
// it cannot clear itself while the gate waits, so the gate stops at once
// instead of polling until meshWait runs out.
func TestTheMeshGateStopsAtOnceWhenSudoRefusesTheHelper(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	refused := "sudo: a password is required\n"
	w.handshakesFail = &refused
	err := siteadd.Execute(build(t, w))
	if !errors.Is(err, apply.ErrHelperRefused) || !strings.Contains(err.Error(), "could not be run on home-a") || !strings.Contains(err.Error(), "host admit") {
		t.Fatalf("want the helper's sudo refusal as the owner's problem, got %v", err)
	}
	reads := 0
	for _, h := range w.hosts {
		for _, c := range h.commands {
			if strings.HasSuffix(c, "show latest-handshakes") {
				reads++
			}
		}
	}
	if reads != 1 {
		t.Errorf("the handshakes were read %d times; a sudo refusal is not retried", reads)
	}
}

// A failed handshake read that is not sudo's refusal keeps its error in the
// gate's message, not only the last lines of what the host printed.
func TestAFailedHandshakeReadKeepsItsError(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	failed := "wg: something went wrong\n"
	w.handshakesFail = &failed
	err := siteadd.Execute(build(t, w))
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("want the read's error kept, got %v", err)
	}
}
