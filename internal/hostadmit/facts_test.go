package hostadmit

import (
	"net/netip"
	"os/exec"
	"slices"
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
		"file /usr/local/libexec/paisans/wg-psns-f2a9 " + sha("helper") + " 755 0 0\n" +
		"file /etc/sudoers.d/paisans-f2a9 link\n" +
		"dir /etc/wireguard drwx------ 0 0\ndir /srv/my data drwxr-xr-x 0 0\nbridge virbr0\nbridge docker0\nufw installed\nufw-active yes\nlan 192.168.1.20/24\n" +
		"lan6 2001:db8:1::/64 eth0\nlan6 2001:db8:1::5 eth0\nlan6 fe80::/64 eth0\nend\n"
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
	if h := f.Files["/usr/local/libexec/paisans/wg-psns-f2a9"]; !h.Present || h.Sum != sha("helper") || h.Mode != "755" || h.UID != 0 {
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
	if !f.UFWInstalled || !f.UFWActive {
		t.Errorf("ufw: installed %v, active %v", f.UFWInstalled, f.UFWActive)
	}
	if len(f.Bridges) != 2 || f.LAN != "192.168.1.20/24" {
		t.Errorf("bridges %v, lan %q", f.Bridges, f.LAN)
	}
	want := []Route6{{netip.MustParsePrefix("2001:db8:1::/64"), "eth0"}, {netip.MustParsePrefix("2001:db8:1::5/128"), "eth0"}, {netip.MustParsePrefix("fe80::/64"), "eth0"}}
	if !slices.Equal(f.OnLink6, want) {
		t.Errorf("on-link routes %v, want %v", f.OnLink6, want)
	}
	if _, err := parseFacts("engine 29.9.1\n", "paisans-f2a9"); err == nil {
		t.Error("an answer with no end line was read")
	}
	for _, bad := range []string{"subuid paisans-f2a9\nend\n", "subuid a:1:2:3\nend\n", "dir /etc/wireguard 0 0\nend\n", "dir /etc/wireguard drwx------ x 0\nend\n", "file /etc/x ab12 755\nend\n",
		"lan6 2001:db8:1::/64\nend\n", "lan6 2001:db8:1::1/64 eth0\nend\n", "lan6 10.0.0.0/8 eth0\nend\n", "lan6 fe80::/64%eth0 eth0\nend\n", "lan6 2001:db8::/64; eth0\nend\n", "lan6 2001:db8::/64 eth0 x\nend\n"} {
		if _, err := parseFacts(bad, "paisans-f2a9"); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// A number or a checksum the probe printed is read as one or refused: a
// garbled owner never reads as 0, which is root, a sysctl the probe could
// not read never reaches sysctl.before as an empty value, and free space
// never reads as none.
func TestParseFactsRefusesWhatIsNotANumber(t *testing.T) {
	file := "file /etc/x " + sha("x")
	for _, bad := range []string{
		file + " 644 x 0\n", file + " 644 0 -\n", file + " 644 -1 0\n", file + " 6x4 0 0\n", file + " 99 0 0\n",
		"file /etc/x ab12 644 0 0\n", "file /etc/x " + strings.ToUpper(sha("x")) + " 644 0 0\n",
		"port-start \n", "port-start 80x\n", "ip-forward \n", "data-avail \n", "data-avail 12G\n",
		"mem-total 4G\n", "mem-total -1\n", "root-avail 1.5e9\n",
	} {
		if _, err := parseFacts(bad+"end\n", "paisans-f2a9"); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
	f, err := parseFacts(file+" 2755 0 1000\nport-start 1024\nip-forward 0\ndata-avail 42\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if x := f.Files["/etc/x"]; x.Mode != "2755" || x.GID != 1000 || f.PortStart != "1024" || f.IPForward != "0" || f.Data.Avail != 42 {
		t.Errorf("read %+v %q %q %d", x, f.PortStart, f.IPForward, f.Data.Avail)
	}
}

// A file admit writes that is there but neither a regular file nor a link,
// a FIFO, a socket or a device, is read as one, never as absent, and Build
// refuses it as it refuses a path below a link.
func TestParseFactsReadsAFileThatIsNotRegular(t *testing.T) {
	f, err := parseFacts("file /etc/sudoers.d/paisans-f2a9 other\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	s := f.Files["/etc/sudoers.d/paisans-f2a9"]
	if !s.Present || !s.Unsafe || s.Mode != "other" {
		t.Errorf("a FIFO: %+v", s)
	}
	err = refuseUnsafe(f, "admit")
	if err == nil || !strings.Contains(err.Error(), "/etc/sudoers.d/paisans-f2a9 (not a regular file)") {
		t.Errorf("a FIFO is not refused: %v", err)
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
	if !strings.Contains(script, "# paisans site admit: read only\n") {
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
	if d := f.Dirs["/srv/paisans/f2a9"]; d.UID != -1 || d.GID != -1 {
		t.Errorf("an unsafe directory reports an owner: %+v", d)
	}
	if s := f.Files["/etc/sudoers.d/paisans-f2a9"]; s.UID != -1 || s.GID != -1 {
		t.Errorf("an unsafe file reports an owner: %+v", s)
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

// ls -l marks a file with an SELinux context by a trailing dot, which
// grants nothing, so a directory's mode is read without it. An ACL's +
// is kept, since an ACL grants what the mode does not show.
func TestParseFactsReadsAnSELinuxModeAsItsMode(t *testing.T) {
	f, err := parseFacts("dir /etc/wireguard drwx------. 0 0\ndir /etc/paisans drwxr-xr-x+ 0 0\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if m := f.Dirs["/etc/wireguard"].Mode; m != "drwx------" {
		t.Errorf("an SELinux mode reads %q", m)
	}
	if m := f.Dirs["/etc/paisans"].Mode; m != "drwxr-xr-x+" {
		t.Errorf("an ACL's mode reads %q", m)
	}
}

// Evict's facts: the mesh unit's state, as systemctl names it, and how many
// entries each directory evict may remove holds. A line that cannot be read
// is refused, so evict never removes a directory it miscounted.
func TestParseEvictFacts(t *testing.T) {
	f, err := parseFacts("mesh-unit enabled active\nentries 3 /etc/paisans\nentries 1 /srv/my data\nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if !f.MeshUnit.Enabled || !f.MeshUnit.Active {
		t.Errorf("mesh unit: %+v", f.MeshUnit)
	}
	if n, ok := f.Entries["/etc/paisans"]; !ok || n != 3 || f.Entries["/srv/my data"] != 1 {
		t.Errorf("entries: %v", f.Entries)
	}
	for _, c := range []struct {
		line            string
		enabled, active bool
	}{{"disabled inactive", false, false}, {"enabled-runtime activating", true, true}, {"not-found failed", false, false}, {"static inactive", false, false}} {
		f, err := parseFacts("mesh-unit "+c.line+"\nend\n", "paisans-f2a9")
		if err != nil {
			t.Fatal(err)
		}
		if f.MeshUnit.Enabled != c.enabled || f.MeshUnit.Active != c.active {
			t.Errorf("%q: %+v", c.line, f.MeshUnit)
		}
	}
	for _, bad := range []string{"mesh-unit enabled\nend\n", "entries x /etc/paisans\nend\n", "entries 3\nend\n"} {
		if _, err := parseFacts(bad, "paisans-f2a9"); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// Swap in each form is read whether or not it is on, and RAM or free space
// the probe printed nothing for is 0, unread, rather than a refusal.
func TestParseFactsReadsSwap(t *testing.T) {
	f, err := parseFacts("mem-total 4113297408\nswap-active no\nswap-partition yes\nswap-fstab no\nswap-zram yes\nswapfile absent\nroot-fs xfs\nroot-avail \nend\n", "paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if s := f.Swap; s.MemTotal != 4113297408 || s.Active || !s.Partition || s.FSTab || !s.ZRAM || s.File || s.RootFS != "xfs" || s.RootAvail != 0 {
		t.Errorf("swap: %+v", s)
	}
}

// The swapfile is twice the RAM to the nearest GiB, at least one, up to
// 16 GiB: a 4 GB VPS reports a little under 4 GiB and gets 8.
func TestSwapSize(t *testing.T) {
	for _, c := range []struct{ mem, want int64 }{
		{4113297408, 8 << 30}, {1 << 30, 2 << 30}, {300 << 20, 1 << 30}, {6 << 30, 12 << 30}, {8 << 30, 16 << 30}, {64 << 30, 16 << 30},
	} {
		if got := swapSize(c.mem); got != c.want {
			t.Errorf("swapSize(%d) = %d GiB, want %d GiB", c.mem, got>>30, c.want>>30)
		}
	}
}
