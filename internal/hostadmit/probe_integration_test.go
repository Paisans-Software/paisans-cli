//go:build hostadmit_integration

// These tests run admit's probe, as root, in a real ubuntu:24.04 container,
// and read what it prints with parseFacts: a blank image, and the hostile
// layouts the probe must not be fooled by. They need Docker, and pull the
// image when it is absent.
package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

const probeImage = "ubuntu:24.04"

// probeIn runs setup, then the probe rendered for the user and --data-dir,
// in a fresh container as root with docker run's extra arguments, and
// parses what the probe printed.
func probeIn(t *testing.T, setup, user, dataDir string, extra ...string) Facts {
	t.Helper()
	in := Input{Request: admission.Request{Deployment: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: user}, Flags: Flags{DataDir: dataDir}}
	script, err := probeScript(deployment.Deployment{ID: in.Request.Deployment}, in)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"run", "--rm", "--user", "0", "-v", dir + ":/p:ro"}, extra...), probeImage, "sh", "-c", setup+"\nsh /p/probe.sh")
	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	f, err := parseFacts(string(out), user)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return f
}

// A blank image: every path is safe to read, and none is unsafe.
func TestProbeOnABlankHost(t *testing.T) {
	f := probeIn(t, "true", "paisans-f2a9", "")
	for p, d := range f.Dirs {
		if d.Unsafe {
			t.Errorf("%s: %+v", p, d)
		}
	}
	for p, s := range f.Files {
		if s.Unsafe || s.Present {
			t.Errorf("%s: %+v", p, s)
		}
	}
	if f.Data.Unsafe || f.Data.Path != "/home" || f.Data.Avail <= 0 {
		t.Errorf("data: %+v", f.Data)
	}
	// The image has no ufw: not installed, so not active.
	if f.UFWInstalled || f.UFWActive {
		t.Errorf("ufw: installed %v, active %v", f.UFWInstalled, f.UFWActive)
	}
}

// /srv/paisans linked to /etc: the deployment's directory below it is
// link-above, never /etc/f2a9 read as if it were the deployment's.
func TestProbeSeesALinkedSrvPaisans(t *testing.T) {
	f := probeIn(t, "mkdir -p /srv /etc/f2a9 && ln -s /etc /srv/paisans", "paisans-f2a9", "")
	if d := f.Dirs["/srv/paisans"]; d.Unsafe || !strings.HasPrefix(d.Mode, "l") {
		t.Errorf("/srv/paisans: %+v", d)
	}
	if d := f.Dirs["/srv/paisans/f2a9"]; !d.Unsafe || d.Mode != "link-above" {
		t.Errorf("/srv/paisans/f2a9: %+v", d)
	}
}

// A --data-dir below a directory another user owns, or below a link that
// user planted into their home: the directory and the data path are
// unsafe, whatever is at the end of the path.
func TestProbeSeesAnotherUsersAncestor(t *testing.T) {
	for name, setup := range map[string]string{
		"owned":  "useradd -m mallory && mkdir -p /mnt/ext/data && chown mallory /mnt/ext",
		"linked": "useradd -m mallory && mkdir -p /home/mallory/data && ln -s /home/mallory /mnt/ext",
	} {
		t.Run(name, func(t *testing.T) {
			f := probeIn(t, setup, "paisans-f2a9", "/mnt/ext/data")
			if d := f.Dirs["/mnt/ext/data"]; !d.Unsafe {
				t.Errorf("/mnt/ext/data: %+v", d)
			}
			if d := f.Dirs["/mnt/ext/data/paisans-f2a9"]; !d.Unsafe {
				t.Errorf("/mnt/ext/data/paisans-f2a9: %+v", d)
			}
			if !f.Data.Unsafe {
				t.Errorf("data: %+v", f.Data)
			}
		})
	}
}

// A group-writable directory above a path, even root's: whoever is in its
// group could swap what is below it between the probe and the change.
func TestProbeSeesAGroupWritableAncestor(t *testing.T) {
	f := probeIn(t, "chmod 775 /srv", "paisans-f2a9", "")
	for _, p := range []string{"/srv/paisans", "/srv/paisans/f2a9"} {
		if d := f.Dirs[p]; !d.Unsafe || d.Mode != "unsafe-above" {
			t.Errorf("%s: %+v", p, d)
		}
	}
	if d := f.Dirs["/etc/wireguard"]; d.Unsafe {
		t.Errorf("/etc/wireguard: %+v", d)
	}
}

// A file admit writes as root, below a link: link-above, its content never
// summed through the link.
func TestProbeSeesAFileBelowALink(t *testing.T) {
	f := probeIn(t, "mv /etc/sudoers.d /etc/sudoers.real 2>/dev/null; mkdir -p /etc/sudoers.real && ln -s /etc/sudoers.real /etc/sudoers.d && echo x > /etc/sudoers.real/paisans-f2a9", "paisans-f2a9", "")
	if s := f.Files["/etc/sudoers.d/paisans-f2a9"]; !s.Unsafe || s.Mode != "link-above" || s.Sum != "" {
		t.Errorf("sudoers: %+v", s)
	}
}

// ufw installed but not enabled is installed and not active; enabled, it is
// both. It installs ufw, so it needs the network.
func TestProbeReadsUFW(t *testing.T) {
	install := "apt-get update -qq >/dev/null 2>&1 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ufw nftables iptables >/dev/null 2>&1 || exit 1\n"
	caps := []string{"--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW"}
	if f := probeIn(t, install, "paisans-f2a9", "", caps...); !f.UFWInstalled || f.UFWActive {
		t.Errorf("disabled: installed %v, active %v", f.UFWInstalled, f.UFWActive)
	}
	if f := probeIn(t, install+"ufw allow 22/tcp >/dev/null && ufw --force enable >/dev/null", "paisans-f2a9", "", caps...); !f.UFWInstalled || !f.UFWActive {
		t.Errorf("enabled: installed %v, active %v", f.UFWInstalled, f.UFWActive)
	}
}
