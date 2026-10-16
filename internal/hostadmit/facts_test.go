package hostadmit

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// The probe's lines become facts; a repeated key is a list, and an answer
// cut short, without "end", is refused rather than read as a host with
// nothing on it.
func TestParseFacts(t *testing.T) {
	out := "engine 29.9.1\npkg docker-ce installed\npkg uidmap absent\n" +
		"passwd paisans-f2a9:x:1001:1002::/home/paisans-f2a9:/bin/bash\ngroups paisans-f2a9 users\ngroup paisans-f2a9\n" +
		"subuid ubuntu:100000:65536\nsubuid paisans-f2a9:165536:65536\n" +
		"file /usr/local/libexec/paisans/wg-psns-f2a9 " + "ab12" + " 755 0 0\n" +
		"file /etc/sudoers.d/paisans-f2a9 link\n" +
		"dir /etc/wireguard drwx------ 0 0\ndir /srv/my data drwxr-xr-x 0 0\nbridge virbr0\nbridge docker0\nlan 192.168.1.20/24\nend\n"
	f, err := parseFacts(out, "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if f.Docker.Engine != "29.9.1" || !f.Packages["docker-ce"] || f.Packages["uidmap"] {
		t.Errorf("docker: %+v %v", f.Docker, f.Packages)
	}
	u := f.User
	if !u.Exists || u.UID != 1001 || u.GID != 1002 || u.Home != "/home/paisans-f2a9" || u.Group != "paisans-f2a9" || len(u.Groups) != 2 {
		t.Errorf("user: %+v", u)
	}
	if r := f.SubUID["paisans-f2a9"]; len(r) != 1 || r[0].Start != 165536 || r[0].Count != 65536 {
		t.Errorf("subuid: %+v", f.SubUID)
	}
	if h := f.Files["/usr/local/libexec/paisans/wg-psns-f2a9"]; !h.Present || h.Sum != "ab12" || h.Mode != "755" || h.UID != 0 {
		t.Errorf("helper: %+v", h)
	}
	if s := f.Files["/etc/sudoers.d/paisans-f2a9"]; !s.Link {
		t.Errorf("sudoers: %+v", s)
	}
	if w := f.Dirs["/etc/wireguard"]; !w.Present || w.Mode != "drwx------" {
		t.Errorf("wireguard: %+v", w)
	}
	if d := f.Dirs["/srv/my data"]; !d.Present || d.Mode != "drwxr-xr-x" {
		t.Errorf("a directory with a space: %+v", f.Dirs)
	}
	if len(f.Bridges) != 2 || f.LAN != "192.168.1.20/24" {
		t.Errorf("bridges %v, lan %q", f.Bridges, f.LAN)
	}
	if _, err := parseFacts("engine 29.9.1\n", "paisans-f2a9"); err == nil {
		t.Error("an answer with no end line was read")
	}
	for _, bad := range []string{"subuid paisans-f2a9\nend\n", "subuid a:1:2:3\nend\n", "dir /etc/wireguard 0 0\nend\n", "dir /etc/wireguard drwx------ x 0\nend\n", "file /etc/x ab12 755\nend\n"} {
		if _, err := parseFacts(bad, "paisans-f2a9"); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// Every path and name the probe is rendered with is quoted for the shell,
// so a --data-dir that is absolute and clean but holds a $( or a space is
// a path to it and nothing else; and the script it renders is one sh reads.
func TestProbeQuotesWhatItIsGiven(t *testing.T) {
	in := Input{Request: admission.Request{Deployment: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: "paisans-f2a9"},
		Flags: Flags{DataDir: "/srv/a b$(touch x)"}}
	script, err := probeScript(deployment.Deployment{ID: in.Request.Deployment}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "# paisans host admit: read only\n") {
		t.Error("the probe does not carry its marker")
	}
	if strings.Contains(script, " /srv/a b$(touch x)") || !strings.Contains(script, `'/srv/a b$(touch x)'`) {
		t.Errorf("the data directory is not quoted:\n%s", script)
	}
	if !strings.Contains(script, "\nu='paisans-f2a9'\n") {
		t.Errorf("the user is not quoted:\n%s", script)
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v: %s", err, out)
	}
}

// A path below a link, or below a directory another user owns or can
// write, is read as unsafe and never as what is there: the probe prints
// link-above or unsafe-above in place of its state, and the parse marks
// the fact Unsafe, as it does the data directory's.
func TestParseFactsMarksAPathBelowALinkOrAnotherUsersDirectory(t *testing.T) {
	out := "dir /srv/paisans/f2a9 link-above 0 0\ndir /mnt/ext/data unsafe-above 0 0\ndir /etc/wireguard drwx------ 0 0\n" +
		"file /etc/sudoers.d/paisans-f2a9 link-above\nfile /etc/paisans/admitted.f2a9.json unsafe-above\n" +
		"data-above unsafe-above\ndata-path /mnt/ext/data\nend\n"
	f, err := parseFacts(out, "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/srv/paisans/f2a9", "/mnt/ext/data"} {
		if d := f.Dirs[p]; !d.Unsafe || !d.Present || strings.HasPrefix(d.Mode, "d") {
			t.Errorf("%s: %+v", p, d)
		}
	}
	if d := f.Dirs["/etc/wireguard"]; d.Unsafe {
		t.Errorf("/etc/wireguard: %+v", d)
	}
	for _, p := range []string{"/etc/sudoers.d/paisans-f2a9", "/etc/paisans/admitted.f2a9.json"} {
		if s := f.Files[p]; !s.Unsafe || !s.Present || s.Sum != "" {
			t.Errorf("%s: %+v", p, s)
		}
	}
	if f.Files["/etc/sudoers.d/paisans-f2a9"].Mode != "link-above" || f.Files["/etc/paisans/admitted.f2a9.json"].Mode != "unsafe-above" {
		t.Errorf("files: %+v", f.Files)
	}
	if !f.Data.Unsafe || f.Data.Path != "/mnt/ext/data" {
		t.Errorf("data: %+v", f.Data)
	}
	f, err = parseFacts("data-above safe\ndata-path /home\nend\n", "paisans-f2a9")
	if err != nil || f.Data.Unsafe {
		t.Errorf("a safe data path: %+v %v", f.Data, err)
	}
	if _, err := parseFacts("data-above maybe\nend\n", "paisans-f2a9"); err == nil {
		t.Error("an unknown data-above was read")
	}
}
