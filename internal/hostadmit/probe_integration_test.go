//go:build hostadmit_integration

// These tests run admit's probe, as root, in a real ubuntu:24.04 container,
// or the image PAISANS_PROBE_IMAGE names (ubuntu:22.04 is the other
// release admit supports), and read what it prints with parseFacts: a blank
// image, and the hostile layouts the probe must not be fooled by. They need
// Docker, and pull the image when it is absent.
package hostadmit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

var probeImage = func() string {
	if image := os.Getenv("PAISANS_PROBE_IMAGE"); image != "" {
		return image
	}
	return "ubuntu:24.04"
}()

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

// The user's mesh drop-in is read at the path its UID names, summed as
// any file admit writes, and a link there is seen as a link.
func TestProbeReadsTheMeshDropIn(t *testing.T) {
	const content = "[Unit]\nAfter=wg-quick@psns-f2a9.service\n"
	setup := "useradd -u 1001 paisans-f2a9 && mkdir -p /etc/systemd/system/user@1001.service.d && " +
		"printf '" + strings.ReplaceAll(content, "\n", "\\n") + "' > /etc/systemd/system/user@1001.service.d/paisans-mesh.conf && " +
		"chmod 644 /etc/systemd/system/user@1001.service.d/paisans-mesh.conf"
	f := probeIn(t, setup, "paisans-f2a9", "")
	got := f.Files[admitrules.MeshDropInPath("1001")]
	if !got.Present || got.Link || got.Sum != sha(content) || got.Mode != "644" || got.UID != 0 {
		t.Errorf("mesh drop-in: %+v", got)
	}
	f = probeIn(t, "useradd -u 1001 paisans-f2a9 && mkdir -p /etc/systemd/system/user@1001.service.d && ln -s /etc/shadow /etc/systemd/system/user@1001.service.d/paisans-mesh.conf", "paisans-f2a9", "")
	if got := f.Files[admitrules.MeshDropInPath("1001")]; !got.Link {
		t.Errorf("a linked mesh drop-in: %+v", got)
	}
}

// The home useradd would give a user not made yet is read, so an earlier
// user's directory or a link there is seen; an existing user's home is
// read with its owner.
func TestProbeReadsTheHome(t *testing.T) {
	f := probeIn(t, "mkdir -p /home/paisans-f2a9 && chown 1234 /home/paisans-f2a9", "paisans-f2a9", "")
	if u := f.User; u.Exists || u.HomeKind != "dir" || u.HomeUID != 1234 || u.HomePath != "/home/paisans-f2a9" {
		t.Errorf("an earlier home: %+v", u)
	}
	f = probeIn(t, "ln -s /etc /home/paisans-f2a9", "paisans-f2a9", "")
	if u := f.User; u.HomeKind != "link" {
		t.Errorf("a linked home: %+v", u)
	}
	f = probeIn(t, "true", "paisans-f2a9", "")
	if u := f.User; u.HomeKind != "absent" || u.HomePath != "/home/paisans-f2a9" {
		t.Errorf("no home: %+v", u)
	}
	f = probeIn(t, "useradd -u 1001 --create-home paisans-f2a9", "paisans-f2a9", "")
	if u := f.User; u.HomeKind != "dir" || u.HomeUID != 1001 || !u.SudoRead || len(u.SudoRules) != 0 {
		t.Errorf("a user's own home, with no sudo installed: %+v", u)
	}
}

// Every rule sudo grants the user is read, by name and by group, with
// admit's own helper line as sudo -l prints it. It installs sudo from
// Ubuntu's archive, so it needs the network.
func TestProbeReadsTheUsersSudoRules(t *testing.T) {
	setup := "apt-get update -qq >/dev/null 2>&1 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq sudo >/dev/null 2>&1 || exit 1\n" +
		"useradd -u 1001 --create-home paisans-f2a9 && groupadd ops && usermod -aG ops paisans-f2a9\n"
	f := probeIn(t, setup, "paisans-f2a9", "")
	if u := f.User; !u.SudoRead || len(u.SudoRules) != 0 {
		t.Errorf("no rule: %+v", u)
	}
	f = probeIn(t, setup+"echo 'paisans-f2a9 ALL=(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9' > /etc/sudoers.d/paisans-f2a9 && chmod 440 /etc/sudoers.d/paisans-f2a9\n"+
		"echo '%ops ALL=(ALL:ALL) ALL' > /etc/sudoers.d/ops && chmod 440 /etc/sudoers.d/ops", "paisans-f2a9", "")
	want := []string{"(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9", "(ALL : ALL) ALL"}
	if u := f.User; !u.SudoRead || !slices.Equal(slices.Sorted(slices.Values(u.SudoRules)), slices.Sorted(slices.Values(want))) {
		t.Errorf("rules: %+v", u)
	}
	// Defaults set for the user join its matching entries; ones set for
	// the helper alone are listed apart.
	helperLine := "echo 'paisans-f2a9 ALL=(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9' > /etc/sudoers.d/paisans-f2a9 && chmod 440 /etc/sudoers.d/paisans-f2a9\n"
	f = probeIn(t, setup+helperLine+"echo 'Defaults:paisans-f2a9 setenv' > /etc/sudoers.d/zz && echo 'Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 env_keep += \"LD_PRELOAD\"' >> /etc/sudoers.d/zz && chmod 440 /etc/sudoers.d/zz", "paisans-f2a9", "")
	if u := f.User; !u.SudoRead || !slices.Contains(u.SudoDefaults, "setenv") || !slices.Contains(u.SudoDefaults, "env_reset") ||
		!slices.Equal(u.SudoSpecific, []string{`Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 env_keep+=LD_PRELOAD`}) {
		t.Errorf("defaults: %+v", u)
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

// The probe reads every IPv6 route with no gateway, and onLink6 keeps the
// global prefixes on every interface but loopback and the mesh's: a LAN
// /64 from an address, a /64 the owner routes on-link by hand, another
// WireGuard interface's prefix, but not the mesh's, a route through a
// gateway, the default route, link-local or unique local prefixes. It
// installs iproute2, so it needs the network.
func TestProbeReadsTheOnLinkPrefixes(t *testing.T) {
	setup := "apt-get update -qq >/dev/null 2>&1 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iproute2 >/dev/null 2>&1 || exit 1\n" +
		"ip link add lan0 type dummy && ip link set lan0 up && ip addr add 2001:db8:1::20/64 dev lan0 nodad && ip addr add fd00:1::20/64 dev lan0 nodad && " +
		"ip -6 route add 2001:db8:2::/64 dev lan0 && ip -6 route add 2001:db8:99::/48 via 2001:db8:1::1 dev lan0 && ip -6 route add default via 2001:db8:1::1 dev lan0 && " +
		"ip link add wg9 type wireguard && ip link set wg9 up && ip addr add 2001:db8:3::1/64 dev wg9 nodad && " +
		"ip link add psns-f2a9 type wireguard && ip link set psns-f2a9 up && ip addr add 2001:db8:4::1/64 dev psns-f2a9 nodad"
	f := probeIn(t, setup, "paisans-f2a9", "", "--cap-add", "NET_ADMIN", "--sysctl", "net.ipv6.conf.all.disable_ipv6=0")
	got := fmt.Sprint(onLink6(f, "psns-f2a9", "10.44.0.0/24"))
	if want := "[2001:db8:1::/64 2001:db8:2::/64 2001:db8:3::/64]"; got != want {
		t.Errorf("on-link prefixes %s, want %s, from %v", got, want, f.OnLink6)
	}
}

// The probe names the module the host's iptables uses, and reads it as
// loaded when the kernel lists it as built in, as the setup tool does.
func TestProbeReadsTheIptablesModule(t *testing.T) {
	builtin := `mkdir -p "/lib/modules/$(uname -r)" && echo kernel/net/netfilter/nf_tables.ko > "/lib/modules/$(uname -r)/modules.builtin"`
	legacy := `printf '#!/bin/sh\necho "iptables v1.8.10 (legacy)"\n' > /usr/sbin/iptables && chmod 755 /usr/sbin/iptables`
	if f := probeIn(t, builtin, "paisans-f2a9", ""); f.IptablesModule != "nf_tables" || !f.IptablesModuleLoaded {
		t.Errorf("built in: %q loaded %v", f.IptablesModule, f.IptablesModuleLoaded)
	}
	if f := probeIn(t, legacy, "paisans-f2a9", ""); f.IptablesModule != "ip_tables" {
		t.Errorf("legacy: %q", f.IptablesModule)
	}
}

// The user's probe reads the data-root admit writes in daemon.json, and,
// with no daemon answering, looks in that root rather than the one it was
// handed.
func TestTheUserProbeReadsTheDataRootOfItsDaemonJSON(t *testing.T) {
	for _, c := range []struct {
		dataRoot string
		want     []string
	}{
		{"/srv/data/paisans-f2a9", []string{"json-root /srv/data/paisans-f2a9", "containers unknown", "old-root held"}},
		{"", []string{"daemon-json ", "old-root empty"}},
	} {
		data, err := hostprep.DaemonJSON("10.44.0.0/24", c.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		script := fmt.Sprintf(userStateProbe, "'/root/.local/share/docker'")
		for name, content := range map[string]string{"daemon.json": data, "user.sh": script} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		setup := "mkdir -p /root/.config/docker /srv/data/paisans-f2a9/image && cp /p/daemon.json /root/.config/docker/"
		out, err := exec.Command("docker", "run", "--rm", "-v", dir+":/p:ro", probeImage, "sh", "-c", setup+" && HOME=/root sh /p/user.sh").CombinedOutput()
		if err != nil {
			t.Fatalf("docker run: %v\n%s", err, out)
		}
		for _, w := range c.want {
			if !strings.Contains(string(out), w) {
				t.Errorf("data-root %q: no %q in\n%s", c.dataRoot, w, out)
			}
		}
		if c.dataRoot == "" && strings.Contains(string(out), "json-root") {
			t.Errorf("a json-root with none set:\n%s", out)
		}
	}
}

// Whether the apparmor package ships rootlesskit's profile is read from
// its file list: Ubuntu 24.04's does, 22.04's does not, and the AppArmor
// refusal's advice follows it. It installs apparmor from Ubuntu's archive,
// so it needs the network.
func TestProbeReadsWhetherApparmorShipsTheRootlesskitProfile(t *testing.T) {
	want, known := map[string]bool{"ubuntu:24.04": true, "ubuntu:22.04": false}[probeImage]
	if !known {
		t.Skipf("no expectation for %s", probeImage)
	}
	if f := probeIn(t, "true", "paisans-f2a9", ""); f.AppArmor.Package || f.AppArmor.Shipped {
		t.Errorf("no apparmor installed, and it reads as package %v, shipped %v", f.AppArmor.Package, f.AppArmor.Shipped)
	}
	setup := "apt-get update -qq >/dev/null 2>&1 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq apparmor >/dev/null 2>&1 || exit 1"
	if f := probeIn(t, setup, "paisans-f2a9", ""); !f.AppArmor.Package || f.AppArmor.Shipped != want {
		t.Errorf("%s: package %v, shipped %v, want shipped %v", probeImage, f.AppArmor.Package, f.AppArmor.Shipped, want)
	}
}

// The system Docker's network store is read at the data-root
// /etc/docker/daemon.json sets, and at Docker's default with none; a
// data-root set in a form the probe cannot read is unreadable, and taken
// to have run.
func TestProbeReadsTheNetworkStoreAtTheDataRoot(t *testing.T) {
	store := func(root string) string {
		return "mkdir -p " + root + "/network/files && touch " + root + "/network/files/local-kv.db"
	}
	json := func(content string) string {
		return "mkdir -p /etc/docker && printf '%s\\n' '" + content + "' > /etc/docker/daemon.json"
	}
	for _, c := range []struct {
		name, setup, root string
		store, unread     bool
	}{
		{"default", store("/var/lib/docker"), "", true, false},
		{"none", "true", "", false, false},
		{"set", json(`{`) + " && printf '%s\\n' '  \"data-root\": \"/srv/docker\"' '}' >> /etc/docker/daemon.json && " + store("/srv/docker"), "/srv/docker", true, false},
		{"set, default empty", json(`{`) + " && printf '%s\\n' '  \"data-root\": \"/srv/docker\"' '}' >> /etc/docker/daemon.json && " + store("/var/lib/docker"), "/srv/docker", false, false},
		{"one line", json(`{"data-root": "/srv/docker"}`) + " && " + store("/srv/docker"), "/srv/docker", true, false},
		{"one line, among others", json(`{"debug": true, "data-root":"/srv/docker", "log-level": "warn"}`) + " && " + store("/srv/docker"), "/srv/docker", true, false},
		{"tabs", `mkdir -p /etc/docker && printf '{\n\t"data-root"\t:\t"/srv/docker"\n}\n' > /etc/docker/daemon.json && ` + store("/srv/docker"), "/srv/docker", true, false},
		{"space before the colon", json(`{ "data-root" : "/srv/docker" }`) + " && " + store("/srv/docker"), "/srv/docker", true, false},
		{"split across lines", `mkdir -p /etc/docker && printf '%s\n' '{' '  "data-root":' '    "/srv/docker"' '}' > /etc/docker/daemon.json`, "", false, true},
	} {
		f := probeIn(t, c.setup, "paisans-f2a9", "")
		if d := f.Docker; d.NetworkStore != c.store || d.DataRoot != c.root || d.DataRootUnread != c.unread || d.Ran() != (c.store || c.unread) {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

// containerd's root directory is read under the root config.toml sets
// above its first table, as root = "/absolute/path" on one line, or at
// containerd's default; a root set in another form is unreadable, and
// counts as may have run.
func TestProbeReadsContainerdsRoot(t *testing.T) {
	toml := func(lines ...string) string {
		out := "mkdir -p /etc/containerd && printf '%s\\n'"
		for _, l := range lines {
			out += " '" + l + "'"
		}
		return out + " > /etc/containerd/config.toml"
	}
	for _, c := range []struct {
		name, setup string
		ran         bool
	}{
		{"none", "true", false},
		{"default", "mkdir -p /var/lib/containerd", true},
		{"as shipped, never ran", toml(`disabled_plugins = ["cri"]`, `#root = "/var/lib/containerd"`), false},
		{"as shipped, ran", toml(`disabled_plugins = ["cri"]`, `#root = "/var/lib/containerd"`) + " && mkdir -p /var/lib/containerd", true},
		{"set, ran", toml(`version = 2`, `root = "/srv/containerd"`) + " && mkdir -p /srv/containerd", true},
		{"set, default only", toml(`root="/srv/containerd" # moved`) + " && mkdir -p /var/lib/containerd", false},
		{"in a table", toml(`[plugins.x]`, `  root = "/srv/x"`) + " && mkdir -p /srv/x", false},
		{"single quotes", toml(`root = '"'"'/srv/containerd'"'"'`), true},
	} {
		if f := probeIn(t, c.setup, "paisans-f2a9", ""); f.Docker.ContainerdRan != c.ran {
			t.Errorf("%s: ContainerdRan = %v", c.name, f.Docker.ContainerdRan)
		}
	}
}
