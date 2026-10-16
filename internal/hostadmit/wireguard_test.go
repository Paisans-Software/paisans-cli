package hostadmit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

var wireguardCases = addCases(
	goldenCase{name: "wgdir-absent", site: "vm", host: home},
	goldenCase{name: "wgdir-correct", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwx------", 0, 0) }},
	goldenCase{name: "wgdir-group-writable-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwxrwxr-x", 0, 1001) }},
	goldenCase{name: "wgdir-other-owner-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwx------", 1001, 0) }},
	goldenCase{name: "wgdir-link-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "lrwxrwxrwx", 0, 0) }},
	goldenCase{name: "wgdir-file-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "-rw-------", 0, 0) }},
	goldenCase{name: "wgdir-acl-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/wireguard", "drwx------+", 0, 0) }},
	goldenCase{name: "helper-dialled", site: "home-a", flags: hostadmit.Flags{Watchdog: true, PublicPorts: true}, host: home},
	goldenCase{name: "helper-no-listen-port", site: "store", host: home},
	goldenCase{name: "helper-changed", site: "vm", host: func() *fakeHost {
		return helperInstalled(home(), "vm").withFile(helperPath, "#!/bin/sh\nexec sh\n", "755", 0)
	}},
	goldenCase{name: "sudoers-changed", site: "vm", host: func() *fakeHost {
		return helperInstalled(home(), "vm").withFile("/etc/sudoers.d/paisans-f2a9", "paisans-f2a9 ALL=(ALL) NOPASSWD: ALL\n", "440", 0)
	}},
	goldenCase{name: "srv-root-owned-refused", site: "vm", host: func() *fakeHost {
		return home().withDir("/srv/paisans", "drwxr-xr-x", 0, 0).withDir("/srv/paisans/f2a9", "drwxr-xr-x", 0, 0)
	}},
)

const helperPath = "/usr/local/libexec/paisans/wg-psns-f2a9"

// helperInstalled is h with the helper and sudoers line for site as admit
// writes them, so a case can change one.
func helperInstalled(h *fakeHost, site string) *fakeHost {
	req := requestFor(site)
	d := deployment.Deployment{ID: req.Deployment}
	script, _ := hostprep.WGHelperScript(d, req.ListenPort, req.MeshSubnet)
	return h.withDir(apply.HelperDir, "drwxr-xr-x", 0, 0).withFile(helperPath, script, "755", 0).
		withFile("/etc/sudoers.d/paisans-f2a9", "paisans-f2a9 ALL=(root) NOPASSWD: "+helperPath+"\n", "440", 0).
		withDir("/etc/wireguard", "drwx------", 0, 0)
}

func TestWireGuardPlans(t *testing.T) { runGolden(t, wireguardCases) }

// The sudoers line reaches sudo only once visudo has checked it, under a
// name sudo's includedir skips until then.
func TestTheSudoersLineIsCheckedBeforeItIsInPlace(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.File == nil || !strings.HasPrefix(s.File.Path, "/etc/sudoers.d/") {
			continue
		}
		if s.File.Path != "/etc/sudoers.d/paisans-f2a9.new" || s.File.Mode != 0o440 {
			t.Errorf("the sudoers line is written to %s mode %o", s.File.Path, s.File.Mode)
		}
		if !strings.HasPrefix(s.Command, "visudo -cqf /etc/sudoers.d/paisans-f2a9.new && ") || !strings.Contains(s.Command, "mv -f /etc/sudoers.d/paisans-f2a9.new /etc/sudoers.d/paisans-f2a9") {
			t.Errorf("the line is not checked before it is moved: %s", s.Command)
		}
		if s.File.Content != "paisans-f2a9 ALL=(root) NOPASSWD: "+helperPath+"\n" {
			t.Errorf("the line is %q", s.File.Content)
		}
		return
	}
	t.Error("no sudoers step")
}

// A host where everything is in place plans nothing for the helper, its
// line or /etc/wireguard, so a re-run changes none of them.
func TestAnInstalledHelperIsLeftAlone(t *testing.T) {
	h := helperInstalled(home(), "vm").withDir("/srv/paisans", "drwxr-xr-x", 0, 0).withDir("/srv/paisans/f2a9", "drwxr-x---", 1001, 1001).
		withFile(delegatePath, delegateConf, "644", 0).withFile(meshPath, meshConf, "644", 0)
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		t.Errorf("planned %q on a host that has it", s.Title)
	}
}

// What a host prints reaches a refusal only through ui.Visible, so an
// interface name or a mode carrying an escape sequence is shown, never
// drawn on the owner's terminal.
func TestHostTextInARefusalIsVisible(t *testing.T) {
	for name, h := range map[string]*fakeHost{
		"route": home().answer("ip -j route", `[{"dst":"10.44.0.0/16","dev":"ts\u001b[2J0"}]`),
		"mode":  home().withDir("/etc/wireguard", "drwx\x1b[2J---+", 0, 0),
	} {
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
		if err == nil {
			t.Errorf("%s: not refused", name)
			continue
		}
		if strings.Contains(err.Error(), "\x1b") {
			t.Errorf("%s: the refusal carries the host's escape: %q", name, err)
		}
	}
}

// A helper with the right content is still restored when the deployment's
// user owns it or anyone may write it, since either lets that user change
// what it runs as root.
func TestAHelperAnotherMayWriteIsRestored(t *testing.T) {
	req := requestFor("vm")
	script, err := hostprep.WGHelperScript(deployment.Deployment{ID: req.Deployment}, req.ListenPort, req.MeshSubnet)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mode string
		uid  int
	}{{"755", 1001}, {"777", 0}} {
		plan, err := hostadmit.Build(helperInstalled(home(), "vm").withFile(helperPath, script, c.mode, c.uid), input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		restored := false
		for _, s := range plan.Steps {
			restored = restored || s.Title == "restore: install the WireGuard helper"
		}
		if !restored {
			t.Errorf("mode %s owner %d: the helper is not restored", c.mode, c.uid)
		}
	}
}

// A mesh subnet inside Tailscale's 100.64.0.0/10 is refused on a host
// running Tailscale, whose route lives in table 52, not main: the probe
// reads every table. The fake answers only the command that does, so a
// probe of main alone would see no route and plan on.
func TestAMeshInsideTailscalesRangeIsRefused(t *testing.T) {
	const tailscale = `[{"dst":"default","gateway":"192.168.1.1","dev":"eth0"},
{"dst":"100.64.0.0/10","dev":"tailscale0","table":"52"},
{"dst":"100.100.100.100","dev":"tailscale0","table":"52"},
{"dst":"192.168.1.0/24","dev":"eth0"}]`
	h := home().answer("ip -j route show table all", tailscale)
	in := input(t, "vm", hostadmit.Flags{})
	in.Request.MeshSubnet = "100.88.0.0/24"
	in.Digest = admission.Digest(admission.Encode(in.Request))
	got := "(planned)\n"
	if _, err := hostadmit.Build(h, in); err != nil {
		got = "refused: " + err.Error() + "\n"
	}
	path := filepath.Join("testdata", "plans", "mesh-subnet-tailscale-refused.txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/hostadmit/ -update, then read the file)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from what admit plans now:\n%s", path, diff(string(want), got))
	}
}
