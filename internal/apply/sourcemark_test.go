package apply_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

func dropInSum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(apply.SourceMarkDropInContent)))
}

// index is the first command containing part, -1 for none.
func (h *fakeHost) index(part string) int {
	for i, c := range h.commands {
		if strings.Contains(c, part) {
			return i
		}
	}
	return -1
}

// A namespace that copied src_valid_mark 1 from its host is set to 0, and
// the drop-in that keeps it 0 at every daemon start is installed first, both
// before any stack moves, so that no container is started into a namespace
// whose published ports wait a second.
func TestApplySetsTheRootlessNamespacesSourceMarkBeforeAnyStack(t *testing.T) {
	host := newHost()
	host.sourceMark = "dropin absent\nmark 1\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceMark != (apply.SourceMarkStep{DropIn: true, Reset: true}) {
		t.Fatalf("the plan is %+v", p.SourceMark)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	install, reset, up := host.index("daemon-reload"), host.index("echo 0 > /proc/sys/net/ipv4/conf/all/src_valid_mark"), host.index("up -d")
	if install < 0 || reset < 0 || up < 0 || install > reset || reset > up {
		t.Fatalf("the drop-in, the reset and the first stack ran out of order (%d, %d, %d): %q", install, reset, up, host.commands)
	}
	found := false
	for _, in := range host.inputs {
		if in == apply.SourceMarkDropInContent {
			found = true
		}
	}
	if !found {
		t.Error("the drop-in's content was not sent")
	}
	if c := host.commands[reset]; !strings.Contains(c, "nsenter -U --preserve-credentials") || strings.Contains(c, "sudo") {
		t.Errorf("the reset does not run as the user, in RootlessKit's namespace: %s", c)
	}
}

// A host with the drop-in in place and the value at 0 is left alone.
func TestApplyLeavesARightNamespaceAlone(t *testing.T) {
	host := newHost()
	host.sourceMark = "dropin " + dropInSum() + "\nmark 0\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceMark.Needed() {
		t.Fatalf("the plan is %+v", p.SourceMark)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.ran("echo 0 > /proc/sys") || host.ran("daemon-reload") {
		t.Errorf("apply changed a namespace that needed nothing: %q", host.commands)
	}
}

// A drop-in somebody changed is replaced, and a value at 0 is not written.
func TestApplyReplacesADifferentDropIn(t *testing.T) {
	host := newHost()
	host.sourceMark = "dropin " + strings.Repeat("0", 64) + "\nmark 0\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceMark != (apply.SourceMarkStep{DropIn: true}) {
		t.Fatalf("the plan is %+v", p.SourceMark)
	}
}

// The probe is not a gate. A namespace it cannot read, or a probe that
// fails, asks for nothing and stops nothing: doctor reports it.
func TestAnUnreadableNamespaceAsksForNothing(t *testing.T) {
	for _, out := range []string{"", "dropin " + dropInSum() + "\nmark unknown\n"} {
		host := newHost()
		host.sourceMark = out
		p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
		if err != nil {
			t.Fatal(err)
		}
		if p.SourceMark.Needed() {
			t.Errorf("%q: the plan is %+v", out, p.SourceMark)
		}
	}
	host := newHost()
	host.fail = "dropin absent"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceMark.Needed() {
		t.Errorf("a failed probe: the plan is %+v", p.SourceMark)
	}
}

// A namespace that cannot be set stops the apply before any stack moves.
func TestAFailedResetStopsTheApplyBeforeAnyStack(t *testing.T) {
	host := newHost()
	host.sourceMark = "dropin " + dropInSum() + "\nmark 1\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	host.fail = "echo 0 > /proc/sys/net/ipv4/conf/all/src_valid_mark"
	err = apply.Execute(p, host)
	if err == nil || !strings.Contains(err.Error(), "src_valid_mark") {
		t.Fatalf("got %v", err)
	}
	if host.ran("up -d") {
		t.Error("a stack moved after the namespace could not be set")
	}
}

// A scoped plan, which leaves every stack to its caller, never reads or
// changes the namespace.
func TestAScopedPlanLeavesTheNamespaceAlone(t *testing.T) {
	host := newHost()
	host.sourceMark = "dropin absent\nmark 1\n"
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Scope("srv/paisans/f2a9/psns-f2a9.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceMark.Needed() || host.ran("src_valid_mark") {
		t.Errorf("a scoped plan read or planned the namespace: %+v", p.SourceMark)
	}
}

// The drop-in never fails the daemon's start, and reaches RootlessKit's
// namespace through the user manager's runtime directory with systemd's
// own escapes.
func TestTheDropInCannotFailTheDaemon(t *testing.T) {
	c := apply.SourceMarkDropInContent
	for _, want := range []string{"[Service]\n", "ExecStartPost=-/bin/sh -c '", "r=%t/dockerd-rootless;", `"$$(cat "$$r/child_pid")"`, `--net="$$r/netns"`} {
		if !strings.Contains(c, want) {
			t.Errorf("the drop-in lacks %q:\n%s", want, c)
		}
	}
	for _, line := range strings.Split(c, "\n") {
		if strings.HasPrefix(line, "ExecStartPost=") && strings.Contains(strings.ReplaceAll(line, "$$", ""), "$") {
			t.Errorf("a lone $ in %q is a systemd variable", line)
		}
	}
}
