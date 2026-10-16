package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// userState answers the probe admit runs as the user.
const userStateMarker = "paisans host admit: as the user"

// idleUser is a user with no rootless daemon installed yet, as on a host
// where admit has just made it.
const idleUser = "daemon inactive\nunit absent\ndaemon-json absent\ncontainers 0\nimages 0\nvolumes 0\nend\n"

// modulesLoadPath and modulesLoadConf are br_netfilter's boot entry as
// admit writes it.
const (
	modulesLoadPath = "/etc/modules-load.d/paisans.conf"
	modulesLoadConf = "# Written by paisans host admit. The rootless Docker daemons of admitted\n" +
		"# deployments need br_netfilter to start with their userland proxy off,\n" +
		"# and the module their iptables uses for the setup tool to install them.\nbr_netfilter\nnf_tables\n"
)

// witnessDaemonJSON is vm's daemon.json: the userland proxy off and
// Docker's default pools, none of which overlaps the mesh's 10.44.0.0/24,
// each cut in /24s.
const witnessDaemonJSON = `{
  "userland-proxy": false,
  "default-address-pools": [
    {
      "base": "172.17.0.0/16",
      "size": 24
    },
    {
      "base": "172.18.0.0/16",
      "size": 24
    },
    {
      "base": "172.19.0.0/16",
      "size": 24
    },
    {
      "base": "172.20.0.0/14",
      "size": 24
    },
    {
      "base": "172.24.0.0/14",
      "size": 24
    },
    {
      "base": "172.28.0.0/14",
      "size": 24
    },
    {
      "base": "192.168.0.0/16",
      "size": 24
    }
  ]
}
`

func running(h *fakeHost, containers int) *fakeHost {
	return h.answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum("{}")+"\ncontainers "+itoa(containers)+"\nimages 0\nvolumes 0\nend\n")
}

// daemonInstalled is h once admit has loaded br_netfilter and installed
// vm's rootless daemon with its daemon.json, running nothing.
func daemonInstalled(h *fakeHost) *fakeHost {
	return h.with("br_netfilter", "loaded").withFile(modulesLoadPath, modulesLoadConf, "644", 0).
		answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum(witnessDaemonJSON)+"\nroot /home/paisans-f2a9/.local/share/docker\ncontainers 0\nimages 0\nvolumes 0\nend\n")
}

// communityHome is a home host whose existing user is home-b's,
// community, with its own home.
func communityHome() *fakeHost {
	return home().with("passwd", "community:x:1002:1002::/home/community:/bin/bash").with("home", "dir 1002 /home/community").
		with("groups", "community").with("group", "community").
		add("subuid", "community:231072:65536").add("subgid", "community:231072:65536").with("data-path", "/home/community")
}

var daemonCases = addCases(
	goldenCase{name: "bridge-loaded", site: "vm", host: func() *fakeHost { return home().with("br_netfilter", "loaded").add("bridge", "docker0") }},
	goldenCase{name: "bridge-docker-only", site: "vm", host: func() *fakeHost { return home().add("bridge", "docker0").add("bridge", "br-3f2a1c9d8e7b") }},
	goldenCase{name: "bridge-libvirt-stopped", site: "vm", host: func() *fakeHost { return home().add("bridge", "virbr0").with("forward-policy", "DROP") }},
	goldenCase{name: "bridge-libvirt-with-flag", site: "vm", flags: hostadmit.Flags{BridgeNetfilter: true}, host: func() *fakeHost {
		return home().add("bridge", "virbr0").with("forward-policy", "DROP")
	}},
	goldenCase{name: "bridge-lookalike-stopped", site: "vm", host: func() *fakeHost { return home().add("bridge", "br-lan") }},
	goldenCase{name: "bridge-nf-tables-absent", site: "vm", host: func() *fakeHost { return home().with("iptables-module", "nf_tables absent") }},
	goldenCase{name: "bridge-ip-tables-absent", site: "vm", host: func() *fakeHost { return home().with("iptables-module", "ip_tables absent") }},
	goldenCase{name: "datadir-none", site: "vm", host: home},
	goldenCase{name: "datadir-given", site: "home-b", flags: hostadmit.Flags{Watchdog: true, DataDir: "/srv/data"}, host: func() *fakeHost {
		return communityHome().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", "absent", 0, 0).
			with("data-path", "/srv/data").with("data-fs", "xfs").with("data-avail", "500000000000")
	}},
	goldenCase{name: "datadir-given-mode-fixed", site: "vm", flags: hostadmit.Flags{DataDir: "/srv/data"}, host: func() *fakeHost {
		return home().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", "drwxr-xr-x", 1001, 1001).
			with("data-path", "/srv/data").with("data-fs", "btrfs")
	}},
	goldenCase{name: "datadir-given-in-use", site: "vm", flags: hostadmit.Flags{DataDir: "/srv/data"}, host: func() *fakeHost {
		h := daemonInstalled(home().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", "drwx--x---", 1001, 1001).
			with("data-path", "/srv/data").with("data-fs", "xfs"))
		return h.answer(userStateMarker, "daemon active\nunit present\ndaemon-json x\nroot /srv/data/paisans-f2a9\ncontainers 2\nimages 3\nvolumes 1\nend\n")
	}},
	goldenCase{name: "datadir-nfs-refused", site: "vm", flags: hostadmit.Flags{DataDir: "/mnt/nas"}, host: func() *fakeHost {
		return home().withDir("/mnt/nas", "drwxr-xr-x", 0, 0).with("data-path", "/mnt/nas").with("data-fs", "nfs")
	}},
	goldenCase{name: "datadir-home-on-nfs-refused", site: "vm", host: func() *fakeHost { return home().with("data-fs", "nfs4") }},
	goldenCase{name: "datadir-short-of-space", site: "home-b", flags: hostadmit.Flags{Watchdog: true}, host: func() *fakeHost { return communityHome().with("data-avail", "8000000000") }},
	goldenCase{name: "datadir-storage-short-of-space", site: "store", host: func() *fakeHost { return home().with("data-avail", "8000000000") }},
	goldenCase{name: "datadir-witness-short-of-space", site: "vm", host: func() *fakeHost { return home().with("data-avail", "4000000000") }},
	goldenCase{name: "datadir-parent-writable-refused", site: "vm", flags: hostadmit.Flags{DataDir: "/srv/data"}, host: func() *fakeHost {
		return home().withDir("/srv/data", "drwxrwxrwt", 0, 0).with("data-path", "/srv/data")
	}},
	goldenCase{name: "datadir-child-another-users-refused", site: "vm", flags: hostadmit.Flags{DataDir: "/srv/data"}, host: func() *fakeHost {
		return home().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", "drwx------", 1234, 1234).with("data-path", "/srv/data")
	}},
	goldenCase{name: "daemon-running-with-containers", site: "vm", host: func() *fakeHost { return running(home(), 3) }},
	goldenCase{name: "daemon-running-idle", site: "vm", host: func() *fakeHost { return running(home(), 0) }},
	goldenCase{name: "daemon-installed-unchanged", site: "home-b", flags: dataSite, host: func() *fakeHost { return admittedHost(requestFor("home-b"), dataSite) }},
)

func TestDaemonPlans(t *testing.T) { runGolden(t, daemonCases) }

// The parent --data-dir names is the owner's and must stay so: one the
// deployment's user, or anyone but root, could write would let them swap
// the directory admit hands over for a link.
func TestTheDataDirectoryParentMustBeRoots(t *testing.T) {
	for _, dir := range []struct {
		mode string
		uid  int
	}{
		{"drwxrwxr-x", 0}, {"drwxr-xrwx", 0}, {"drwxr-xr-x", 1001}, {"lrwxrwxrwx", 0}, {"-rw-r--r--", 0}, {"drwxr-xr-x+", 0}, {"absent", 0},
		{"link-above", -1}, {"unsafe-above", -1},
	} {
		h := home().withDir("/srv/data", dir.mode, dir.uid, dir.uid).with("data-path", "/srv/data")
		if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"})); err == nil {
			t.Errorf("--data-dir under a parent %s owned by %d was planned", dir.mode, dir.uid)
		}
	}
}

// The directory admit hands the user is the user's, a directory and not a
// link, or admit refuses: below a link or another's directory it is not
// what its path names.
func TestTheDataDirectoryItselfMustBeTheUsers(t *testing.T) {
	for _, dir := range []struct {
		mode string
		uid  int
	}{
		{"lrwxrwxrwx", 1001}, {"-rw-------", 1001}, {"drwx------", 0}, {"drwx------", 1234}, {"drwx------+", 1001}, {"link-above", -1}, {"unsafe-above", -1},
	} {
		h := home().withDir("/srv/data", "drwxr-xr-x", 0, 0).withDir("/srv/data/paisans-f2a9", dir.mode, dir.uid, dir.uid).with("data-path", "/srv/data")
		if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"})); err == nil {
			t.Errorf("a data directory %s owned by %d was accepted", dir.mode, dir.uid)
		}
	}
}

// --data-dir /home would make the user's home its data root.
func TestTheDataDirectoryIsNotTheHome(t *testing.T) {
	h := home().withDir("/home", "drwxr-xr-x", 0, 0).withDir("/home/paisans-f2a9", "drwxr-x---", 1001, 1001).with("data-path", "/home")
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/home"})); err == nil || !strings.Contains(err.Error(), "its home") {
		t.Errorf("Build = %v", err)
	}
}

// A filesystem Docker's overlay storage cannot keep its data on is
// refused, with --data-dir or without; one that can is planned.
func TestTheDataFilesystemMustSuitOverlay(t *testing.T) {
	for fs, ok := range map[string]bool{
		"ext2/ext3": true, "xfs": true, "btrfs": true, "zfs": true,
		"nfs": false, "nfs4": false, "cifs": false, "smb2": false, "fuseblk": false, "fuse.sshfs": false,
		"overlayfs": false, "msdos": false, "exfat": false, "ntfs": false, "": false,
	} {
		_, err := hostadmit.Build(home().with("data-fs", fs), input(t, "vm", hostadmit.Flags{}))
		if (err == nil) != ok {
			t.Errorf("filesystem %q: Build = %v", fs, err)
		}
	}
}

// A daemon running containers is never restarted for a changed
// daemon.json; one running none is.
func TestARunningDaemonIsNotRestarted(t *testing.T) {
	for n, wantRestart := range map[int]bool{0: true, 3: false} {
		plan, err := hostadmit.Build(running(home(), n), input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		restarted := false
		for _, s := range plan.Steps {
			restarted = restarted || strings.Contains(s.Command, "restart docker")
		}
		if restarted != wantRestart {
			t.Errorf("%d containers: restart planned %v", n, restarted)
		}
	}
}

// A daemon that is active but does not answer may be running containers
// admit cannot count, so it is not restarted either.
func TestADaemonThatCannotBeCountedIsNotRestarted(t *testing.T) {
	h := home().answer(userStateMarker, "daemon active\nunit present\ndaemon-json "+sum("{}")+"\ncontainers unknown\nimages unknown\nvolumes unknown\nold-root empty\nend\n")
	plan, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "restart docker") {
			t.Errorf("an uncounted daemon is restarted: %q", s.Command)
		}
	}
	if !strings.Contains(plan.Text(), "warning: daemon: paisans-f2a9's daemon picks up its new daemon.json at its next restart") {
		t.Errorf("no warning:\n%s", plan.Text())
	}
}

// Moving Docker's data root under a deployment that already has data
// would hide every image, container and volume: admit refuses, naming
// both paths.
func TestChangingTheDataDirWithDataIsRefused(t *testing.T) {
	h := home().withDir("/srv/data", "drwxr-xr-x", 0, 0).with("data-path", "/srv/data").
		answer(userStateMarker, "daemon active\nunit present\ndaemon-json x\ncontainers 0\nimages 4\nvolumes 2\nend\n")
	h.file("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", "")).withFile("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", ""), "644", 0)
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"}))
	if err == nil || !strings.Contains(err.Error(), "/srv/data/paisans-f2a9") || !strings.Contains(err.Error(), "/home/paisans-f2a9") {
		t.Fatalf("Build = %v", err)
	}
}

// Dropping --data-dir moves the data root back under the home, and is
// refused the same way while the data directory holds anything.
func TestDroppingTheDataDirWithDataIsRefused(t *testing.T) {
	h := home().answer(userStateMarker, "daemon active\nunit present\ndaemon-json x\ncontainers 1\nimages 0\nvolumes 0\nend\n")
	h.file("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", "/srv/data/paisans-f2a9"))
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "/srv/data/paisans-f2a9") || !strings.Contains(err.Error(), "/home/paisans-f2a9/.local/share/docker") {
		t.Fatalf("Build = %v", err)
	}
}

// The daemon's own data root is what it holds its data in, whatever the
// record says: a user's daemon already keeping data elsewhere is not
// moved from under it either.
func TestTheDaemonsOwnDataRootIsWhatMoves(t *testing.T) {
	h := home().answer(userStateMarker, "daemon active\nunit present\ndaemon-json x\nroot /var/tmp/elsewhere\ncontainers 0\nimages 2\nvolumes 0\nend\n")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "/var/tmp/elsewhere") {
		t.Fatalf("Build = %v", err)
	}
}

// A stopped daemon counts nothing, so admit looks in its data root
// instead: one holding anything is not moved from.
func TestAStoppedDaemonsDataIsNotMovedFrom(t *testing.T) {
	for held, wantErr := range map[string]bool{"held": true, "empty": false} {
		h := home().withDir("/srv/data", "drwxr-xr-x", 0, 0).with("data-path", "/srv/data").
			answer(userStateMarker, "daemon inactive\nunit present\ndaemon-json x\ncontainers unknown\nimages unknown\nvolumes unknown\nold-root "+held+"\nend\n")
		_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data"}))
		if (err != nil) != wantErr {
			t.Errorf("old root %s: Build = %v", held, err)
		}
	}
}

// What admit writes in the user's home, and the setup tool, run as the
// user, with daemon.json on stdin and never on a command line.
func TestTheDaemonIsSetUpAsTheUser(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range plan.Steps {
		for _, what := range []string{"daemon.json", "dockerd-rootless-setuptool.sh"} {
			if !strings.Contains(s.Command, what) {
				continue
			}
			seen[what] = true
			if !strings.HasPrefix(s.Command, asUserPrefix+"paisans-f2a9 -- env -i ") {
				t.Errorf("%s runs as root: %q", s.Title, s.Command)
			}
		}
		if strings.Contains(s.Command, "daemon.json") && (s.Input != witnessDaemonJSON || strings.Contains(s.Command, "userland-proxy")) {
			t.Errorf("daemon.json is not on stdin: %q, %q", s.Command, s.Input)
		}
	}
	if !seen["daemon.json"] || !seen["dockerd-rootless-setuptool.sh"] {
		t.Errorf("planned %v", seen)
	}
}

// The user's state is read as the user, so a link in its home is followed
// with the user's rights alone.
func TestTheUsersStateIsReadAsTheUser(t *testing.T) {
	h := home()
	if _, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{})); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.ran {
		if strings.Contains(c, userStateMarker) && !strings.HasPrefix(c, asUserPrefix+"paisans-f2a9 -- env -i ") {
			t.Errorf("the user's state is read as root: %q", c)
		}
	}
}

// The record carries --data-dir's directory and --bridge-netfilter.
func TestTheRecordCarriesTheDataDirAndTheBridgeFlag(t *testing.T) {
	h := home().add("bridge", "virbr0").withDir("/srv/data", "drwxr-xr-x", 0, 0).with("data-path", "/srv/data")
	rec, err := hostadmit.RecordOf(h, input(t, "vm", hostadmit.Flags{DataDir: "/srv/data", BridgeNetfilter: true}))
	if err != nil {
		t.Fatal(err)
	}
	if rec.DataDir != "/srv/data/paisans-f2a9" || !rec.BridgeNetfilter {
		t.Errorf("data_dir %q, bridge_netfilter %v", rec.DataDir, rec.BridgeNetfilter)
	}
}

// The setup tool refuses to install the daemon unless the module the
// host's iptables uses is loaded or built in, so admit loads it first, and
// at boot beside br_netfilter; a host that has it loads nothing.
func TestTheIptablesModuleIsLoadedBeforeTheSetupTool(t *testing.T) {
	for module, want := range map[string]string{"nf_tables absent": "modprobe nf_tables", "ip_tables absent": "modprobe ip_tables", "nf_tables loaded": ""} {
		plan, err := hostadmit.Build(home().with("iptables-module", module), input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatal(err)
		}
		loaded, setup, boot := -1, -1, ""
		for i, s := range plan.Steps {
			if strings.HasPrefix(s.Command, "modprobe ") && s.Command != "modprobe br_netfilter" {
				loaded = i
				if s.Command != want {
					t.Errorf("%s: %q", module, s.Command)
				}
			}
			if strings.Contains(s.Command, "dockerd-rootless-setuptool.sh") {
				setup = i
			}
			if s.File != nil && s.File.Path == modulesLoadPath {
				boot = s.File.Content
			}
		}
		switch {
		case want == "" && loaded >= 0:
			t.Errorf("%s: a module is loaded", module)
		case want != "" && (loaded < 0 || setup < 0 || loaded > setup):
			t.Errorf("%s: loaded at step %d, setup tool at %d", module, loaded, setup)
		}
		name := strings.Fields(module)[0]
		if !strings.Contains(boot, "\nbr_netfilter\n"+name+"\n") {
			t.Errorf("%s: the boot entry is %q", module, boot)
		}
	}
}

// With its daemon stopped and no record, the user's own daemon.json says
// where its data is: a re-run without --data-dir does not move the data
// root home from under it.
func TestAStoppedDaemonsDataRootIsReadFromItsDaemonJSON(t *testing.T) {
	h := home().answer(userStateMarker, "daemon inactive\nunit present\ndaemon-json x\njson-root /srv/data/paisans-f2a9\ncontainers unknown\nimages unknown\nvolumes unknown\nold-root held\nend\n")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "/srv/data/paisans-f2a9") || !strings.Contains(err.Error(), "/home/paisans-f2a9/.local/share/docker") {
		t.Fatalf("Build = %v", err)
	}
}

// With its daemon stopped and no data-root in its daemon.json, the guard
// looks in the record's data_dir: the probe is handed it, and a held root
// refuses dropping --data-dir.
func TestAStoppedDaemonsGuardLooksInTheRecordsDataDir(t *testing.T) {
	h := home().answer(userStateMarker, "daemon inactive\nunit present\ndaemon-json x\ncontainers unknown\nimages unknown\nvolumes unknown\nold-root held\nend\n")
	h.file("/etc/paisans/admitted.f2a9.json", previousRecord(t, "vm", "/srv/data/paisans-f2a9"))
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "/srv/data/paisans-f2a9") || !strings.Contains(err.Error(), "/home/paisans-f2a9/.local/share/docker") {
		t.Fatalf("Build = %v", err)
	}
	handed := false
	for _, c := range h.ran {
		handed = handed || strings.Contains(c, userStateMarker) && strings.Contains(c, "old='\\''/srv/data/paisans-f2a9'\\''")
	}
	if !handed {
		t.Errorf("the probe was not handed the record's data_dir: %q", h.ran)
	}
}
