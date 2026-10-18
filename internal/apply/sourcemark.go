package apply

import (
	"fmt"
	"strings"
)

// The deployment user's rootless Docker daemon runs its bridges in a network
// namespace of RootlessKit's, and RootlessKit's port driver hands each
// connection to a published port into it with a transparent dial: a socket
// bound to the real client address, so the app sees the gateway's mesh
// address and trusts its X-Forwarded-For. Its replies are routed home by a
// mark, 0x64, and a routing table of RootlessKit's own whose one route is
// local.
//
// That breaks when the namespace's net.ipv4.conf.all.src_valid_mark is 1. The
// kernel then checks a reply's source with the reply's mark, finds it in that
// local table, takes the container's address for one of the namespace's own,
// and drops the reply. RootlessKit waits a second and dials again without the
// client address, so every new connection to every app waits a second, and
// the app sees the bridge's gateway instead of the mesh, outside the range it
// trusts a forwarded address from. A namespace copies the host's value when it
// is created, and Tailscale sets the host's to 1.
//
// The namespace is the deployment user's, so apply sets the value to 0 there
// as that user, with no root and nothing on the host changed: Tailscale keeps
// its 1. The drop-in does the same at every start of the daemon, since a
// restart makes the namespace afresh with the host's value.

// SourceMarkDropIn is the drop-in, relative to the deployment user's home.
const SourceMarkDropIn = ".config/systemd/user/docker.service.d/paisans-src-valid-mark.conf"

// sourceMarkCommand writes 0 to every value in the namespace: all's, which
// the kernel ORs with each device's own, each device's, and default's, which
// a device made later starts with.
// It holds no variable or quote, so the drop-in carries it as it is.
const sourceMarkCommand = "echo 0 | tee /proc/sys/net/ipv4/conf/*/src_valid_mark > /dev/null"

// SourceMarkRead prints the highest value in the namespace, 1 when any one
// device, all or default holds 1. doctor reads it too.
const SourceMarkRead = "cat /proc/sys/net/ipv4/conf/*/src_valid_mark | sort -n | tail -n 1"

// SourceMarkDropInContent is the drop-in apply installs. %t is the user
// manager's runtime directory, where RootlessKit keeps its state, and $$ is a
// dollar sign to systemd. The leading - keeps a failure here from failing the
// daemon's start: the daemon then runs with the delay, and doctor names it.
const SourceMarkDropInContent = `# Written by paisans deployment apply. Sets net.ipv4.conf.all.src_valid_mark
# to 0 in RootlessKit's network namespace at every start of the daemon: at 1,
# which the namespace copies from a host running Tailscale, every new
# connection to a published port waits a second and the app sees the bridge's
# address in place of the client's.
[Service]
ExecStartPost=-/bin/sh -c 'r=%t/dockerd-rootless; nsenter -U --preserve-credentials -t "$$(cat "$$r/child_pid")" -m nsenter --net="$$r/netns" sh -c "` + sourceMarkCommand + `"'
`

// InRootlessNamespace is command run in the deployment user's RootlessKit
// network namespace, as the user. The namespace file is mounted only in
// RootlessKit's own mount namespace, so the command enters that first.
func InRootlessNamespace(command string) string {
	return `r=/run/user/$(id -u)/dockerd-rootless && nsenter -U --preserve-credentials -t "$(cat "$r/child_pid")" -m nsenter --net="$r/netns" sh -c ` + shellQuote(command)
}

// DropInLoaded prints loaded when the user's manager has read the drop-in
// for docker.service, absent otherwise. A file written whose reload failed
// is on disk and not loaded.
const DropInLoaded = `systemctl --user show docker.service -p DropInPaths --value 2>/dev/null | grep -q paisans-src-valid-mark.conf && echo loaded || echo absent`

// SourceMarkProbe prints the drop-in's checksum, or absent, whether the
// user's manager has loaded it, and the namespace's highest src_valid_mark,
// or unknown when the namespace cannot be read.
var SourceMarkProbe = `f="$HOME/` + SourceMarkDropIn + `"; ` +
	`if [ -f "$f" ]; then echo "dropin $(sha256sum < "$f" | cut -c1-64)"; else echo "dropin absent"; fi; ` +
	`echo "dropin-loaded $(` + DropInLoaded + `)"; ` +
	`if v=$(` + InRootlessNamespace(SourceMarkRead) + ` 2>/dev/null); then echo "mark $v"; else echo "mark unknown"; fi`

// SourceMarkStep is what the deployment user's rootless namespace needs.
type SourceMarkStep struct {
	// DropIn installs SourceMarkDropInContent and reloads the user's
	// manager: the host has none, one that differs, or one the manager has
	// not loaded.
	DropIn bool
	// Reset sets every value in the running namespace to 0: one reads 1.
	Reset bool
}

// Needed is whether the step does anything.
func (s SourceMarkStep) Needed() bool { return s.DropIn || s.Reset }

// SourceMarkAction is one thing the step does, as a plan and the progress
// output name it, and how Execute runs it: doing names it in an error.
type SourceMarkAction struct {
	Title, Describe string
	doing           string
	run             func(Transport) (string, error)
}

// Steps is what the step does, in the order Execute does it.
func (s SourceMarkStep) Steps() []SourceMarkAction {
	var out []SourceMarkAction
	if s.DropIn {
		out = append(out, SourceMarkAction{
			Title:    "install the rootless namespace's drop-in",
			Describe: "~/" + SourceMarkDropIn + " sets src_valid_mark to 0 in RootlessKit's namespace at every start of the daemon",
			doing:    "installing ~/" + SourceMarkDropIn,
			run: func(t Transport) (string, error) {
				return t.RunInput(installSourceMarkDropIn(), SourceMarkDropInContent)
			},
		})
	}
	if s.Reset {
		out = append(out, SourceMarkAction{
			Title:    "set src_valid_mark to 0 in the rootless namespace",
			Describe: "at 1 every new connection to a published port waits a second, and the app sees the bridge's address in place of the client's",
			doing:    "setting src_valid_mark in RootlessKit's namespace",
			run:      func(t Transport) (string, error) { return t.Run(InRootlessNamespace(sourceMarkCommand)) },
		})
	}
	return out
}

// sourceMarkStep reads what the host has. A read that fails or prints
// nothing it knows asks for nothing: the probe is not a gate, and doctor
// reports a namespace it cannot read.
func sourceMarkStep(t Transport) SourceMarkStep {
	out, err := t.Run(SourceMarkProbe)
	if err != nil {
		return SourceMarkStep{}
	}
	var s SourceMarkStep
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "dropin":
			s.DropIn = s.DropIn || value == "absent" || len(value) == 64 && value != sum(SourceMarkDropInContent)
		case "dropin-loaded":
			s.DropIn = s.DropIn || value == "absent"
		case "mark":
			s.Reset = value == "1"
		}
	}
	return s
}

// installSourceMarkDropIn is the command that writes the drop-in from stdin,
// through a temporary file, and has the user's manager read it.
func installSourceMarkDropIn() string {
	dir := `"$HOME/` + strings.TrimSuffix(SourceMarkDropIn, "/paisans-src-valid-mark.conf") + `"`
	return "mkdir -p " + dir + ` && cat > "$HOME/` + SourceMarkDropIn + `.paisans-tmp" && mv -f "$HOME/` + SourceMarkDropIn + `.paisans-tmp" "$HOME/` + SourceMarkDropIn + `" && systemctl --user daemon-reload`
}

// fixSourceMark runs the step, drop-in first, so that a daemon restarted
// between the two still starts with the value set.
func fixSourceMark(plan *Plan, t Transport) error {
	for _, step := range plan.SourceMark.Steps() {
		done := plan.step(step.Title)
		done.Detail("%s", step.Describe)
		out, err := step.run(t)
		if err != nil {
			err = fmt.Errorf("%s: %s, so no stack was changed: %w:\n%s", plan.Site, step.doing, err, out)
		}
		finish(done, err)
		if err != nil {
			return err
		}
	}
	return nil
}
