package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

// Cleaning a host deletes the deployment's configuration store: the
// repository, the peers file, the sync log, a pushed bundle a push left
// behind and the sync script's directory, and the units host prepare wrote
// for it in its user's own systemd directory, disabled and stopped through
// the user's manager, with nothing of it left.
func TestCleaningAHostDeletesItsConfigurationStore(t *testing.T) {
	w := setup(t)
	vm := w.hosts["vm"]
	repo, peers, syncLog, scripts := hostprep.ConfigRepo(dep), hostprep.ConfigPeers(dep), hostprep.ConfigSyncLog(dep), hostprep.ConfigScriptDir(dep)
	incoming := hostprep.ConfigIncoming(dep)
	other := "/srv/paisans/0c1d/.paisans/config/0c1d.git/HEAD"
	units := hostprep.UserUnitDir("/home/ubuntu")
	daemon, timer := units+"/paisans-f2a9-config-daemon.service", units+"/paisans-f2a9-config-sync.timer"
	otherUnit := units + "/paisans-0c1d-config-daemon.service"
	for path, content := range map[string]string{
		repo + "/HEAD": "ref: refs/heads/main\n", repo + "/objects/ab/cdef": "x",
		peers: "10.44.0.2\n", syncLog: "{}\n", syncLog + ".tmp": "{", scripts + "/config-sync.sh": "#!/bin/sh\n",
		incoming: "a bundle",
		daemon:   "[Unit]\n",
		timer:    "[Timer]\n",
		other:    "another deployment's\n", otherUnit: "[Unit]\n",
	} {
		vm.files[path] = content
	}
	dest, _ := config.ParseDestination("ubuntu@192.0.2.10")
	p, err := siteremove.BuildForced(w.cfg.WithoutSite("vm"), w.secrets, "vm", dest, vm, siteremove.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{repo, peers, syncLog, syncLog + ".tmp", incoming, scripts} {
		if !hasStepIn(stageNamed(p, "clean the host"), "vm", "delete", path) {
			t.Errorf("no deletion of %s:\n%s", path, printed(p))
		}
	}
	for _, unit := range []string{daemon, timer} {
		if !hasStepIn(stageNamed(p, "clean the host"), "vm", "remove", unit) {
			t.Errorf("no removal of %s:\n%s", unit, printed(p))
		}
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	for path := range vm.files {
		if strings.HasPrefix(path, repo) || strings.HasPrefix(path, scripts) || path == peers || path == syncLog || path == syncLog+".tmp" || path == incoming || strings.HasPrefix(path, units+"/paisans-f2a9-") {
			t.Errorf("%s is still on the host", path)
		}
	}
	for _, path := range []string{other, otherUnit} {
		if _, ok := vm.files[path]; !ok {
			t.Errorf("another deployment's %s was deleted", path)
		}
	}
	if vm.ran(hostprep.UserSystemctl+" disable --now") != 1 || vm.ran(hostprep.UserSystemctl+" daemon-reload") != 1 {
		t.Errorf("the units were not stopped through the user's manager:\n%s", strings.Join(vm.commands, "\n"))
	}
}

// The host stage reaches only what the deployment's user owns: no ufw, no
// registry, no system unit, no WireGuard unit, and no path outside its root
// or its user's home.
func TestTheHostStageIsTheUsersOwn(t *testing.T) {
	w := setup(t)
	p := w.mustBuild("home-b", siteremove.Options{DeleteData: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.hosts["home-b"].commands {
		for _, root := range []string{"ufw ", "registry.json", "/etc/systemd/system", "wg-quick@", "sudo "} {
			if strings.Contains(c, root) {
				t.Errorf("site remove ran %q: %s", root, c)
			}
		}
	}
	if !strings.Contains(p.Evict, "sudo paisans host evict --deployment "+dep.ID+" --user ") {
		t.Errorf("evict line %q", p.Evict)
	}
}

// A manifest written outside the deployment's root is never deleted: the
// user cannot, and it is not provably this deployment's to the owner.
func TestSiteRemoveNeverReachesOutsideTheRoot(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	b.addToManifest(t, "etc/hosts", "127.0.0.1 localhost\n")
	p := w.mustBuild("home-b", siteremove.Options{})
	if hasStepIn(stageNamed(p, "clean the host"), "home-b", "delete", "/etc/hosts") {
		t.Errorf("planned deleting /etc/hosts:\n%s", printed(p))
	}
	if !strings.Contains(strings.Join(p.Kept, "\n"), "/etc/hosts: outside "+dep.Root()) {
		t.Errorf("kept %q", p.Kept)
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.files["/etc/hosts"]; !ok {
		t.Error("/etc/hosts was deleted")
	}
}

// A manifest path that is not clean, one with a .. element above all, is
// never deleted: the manifest is the deployment user's own file, and such a
// path can name a file anywhere while it starts with the root. It is kept
// and reported, and no command names it.
func TestSiteRemoveNeverFollowsADotDotPath(t *testing.T) {
	for _, bad := range []string{
		dep.Rel() + "/../../../etc/ssh/sshd_config",
		dep.Rel() + "/talk/../../0c1d/talk/.env",
		dep.Rel() + "/./talk/x",
		dep.Rel() + "//talk/x",
	} {
		t.Run(bad, func(t *testing.T) {
			w := setup(t)
			b := w.hosts["home-b"]
			b.addToManifest(t, bad, "PermitRootLogin no\n")
			p := w.mustBuild("home-b", siteremove.Options{DeleteData: true})
			if strings.Contains(printed(p), bad) {
				t.Errorf("the plan names %s:\n%s", bad, printed(p))
			}
			if !strings.Contains(strings.Join(p.Kept, "\n"), "/"+bad+": not a clean path under "+dep.Root()) {
				t.Errorf("kept %q", p.Kept)
			}
			if err := siteremove.Execute(p); err != nil {
				t.Fatal(err)
			}
			for _, c := range b.commands {
				if strings.Contains(c, bad) {
					t.Errorf("ran %s", c)
				}
			}
		})
	}
}

// The host stage runs only as the deployment's user: a destination that
// logs in as root is refused before anything on the host is read or
// changed, since as root a link the user planted under the root could steer
// a deletion anywhere. The refusal names what to run instead.
func TestSiteRemoveRefusesARootLogin(t *testing.T) {
	builds := map[string]func(*world) error{
		"full": func(w *world) error { _, err := w.build("home-b", siteremove.Options{}); return err },
		"forced": func(w *world) error {
			dest, _ := config.ParseDestination("root@192.0.2.12")
			_, err := siteremove.BuildForced(w.cfg, w.secrets, "home-b", dest, w.hosts["home-b"], siteremove.Options{DeleteData: true})
			return err
		},
		"by id": func(w *world) error {
			dest, _ := config.ParseDestination("root@192.0.2.12")
			_, err := siteremove.BuildForcedByID(dest, w.hosts["home-b"], ourID, siteremove.Options{DeleteData: true})
			return err
		},
	}
	for name, build := range builds {
		t.Run(name, func(t *testing.T) {
			w := setup(t)
			b := w.hosts["home-b"]
			b.loginRoot = true
			err := build(w)
			if err == nil {
				t.Fatal("a root login was not refused")
			}
			for _, want := range []string{"logs in as root", "Nothing was changed", "--ssh", "the deployment's user", "sudo paisans host evict --deployment " + ourID} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("no %q in: %v", want, err)
				}
			}
			for _, c := range b.commands {
				if c != "true" && !strings.Contains(c, "getent passwd") && !strings.Contains(c, "ip -br addr") && !strings.Contains(c, "$(id -un)") {
					t.Errorf("ran %s before refusing", c)
				}
			}
			recordPath, _ := admission.Path(dep.Token())
			for _, r := range b.reads {
				if r != registry.Path && r != recordPath {
					t.Errorf("read %s before refusing", r)
				}
			}
		})
	}
}

// The host stage runs only as the deployment's user, the one its admission
// record names: a login as anyone else, or a host with no record to say who
// that is, is refused on every entry path before anything is read or
// changed, naming the --ssh to run instead and the evict line. The
// deployment's own user proceeds.
func TestSiteRemoveRefusesALoginThatIsNotTheDeploymentsUser(t *testing.T) {
	builds := map[string]func(*world) (*siteremove.Plan, error){
		"full": func(w *world) (*siteremove.Plan, error) { return w.build("home-b", siteremove.Options{}) },
		"forced": func(w *world) (*siteremove.Plan, error) {
			dest, _ := config.ParseDestination("admin@192.0.2.12")
			return siteremove.BuildForced(w.cfg, w.secrets, "home-b", dest, w.hosts["home-b"], siteremove.Options{DeleteData: true})
		},
		"by id": func(w *world) (*siteremove.Plan, error) {
			dest, _ := config.ParseDestination("admin@192.0.2.12")
			return siteremove.BuildForcedByID(dest, w.hosts["home-b"], ourID, siteremove.Options{DeleteData: true})
		},
	}
	recordPath, _ := admission.Path(dep.Token())
	quiet := func(t *testing.T, b *host) {
		t.Helper()
		for _, c := range b.commands {
			if c != "true" && !strings.Contains(c, "getent passwd") && !strings.Contains(c, "ip -br addr") && !strings.Contains(c, "$(id -un)") {
				t.Errorf("ran %s before refusing", c)
			}
		}
		for _, r := range b.reads {
			if r != registry.Path && r != recordPath {
				t.Errorf("read %s before refusing", r)
			}
		}
	}
	for name, build := range builds {
		t.Run(name+", another user", func(t *testing.T) {
			w := setup(t)
			b := w.hosts["home-b"]
			b.loginName = "admin"
			_, err := build(w)
			if err == nil {
				t.Fatal("a login as admin was not refused")
			}
			for _, want := range []string{"logs in as admin", "not the deployment's user ubuntu", "Nothing was changed", "--ssh ubuntu@", "sudo paisans host evict --deployment " + ourID + " --user ubuntu --execute"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("no %q in: %v", want, err)
				}
			}
			quiet(t, b)
		})
		t.Run(name+", no record", func(t *testing.T) {
			w := setup(t)
			b := w.hosts["home-b"]
			delete(b.files, recordPath)
			_, err := build(w)
			if err == nil {
				t.Fatal("a host with no admission record was not refused")
			}
			for _, want := range []string{"no admission record", "Nothing was changed", "sudo paisans host evict --deployment " + ourID} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("no %q in: %v", want, err)
				}
			}
			quiet(t, b)
		})
		t.Run(name+", the deployment's user", func(t *testing.T) {
			w := setup(t)
			p, err := build(w)
			if err != nil {
				t.Fatal(err)
			}
			if p.Evict != siteremove.EvictLine(dep, "ubuntu") {
				t.Errorf("Evict = %q", p.Evict)
			}
			if err := siteremove.Execute(p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// --delete-data empties the root through rootlesskit, as root of the
// user's namespace, so files a container's user owns go too, and never
// removes the root itself, which is root's to remove.
func TestDeleteDataEmptiesTheRootInTheUsersNamespace(t *testing.T) {
	w := setup(t)
	p := w.mustBuild("home-b", siteremove.Options{DeleteData: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	b := w.hosts["home-b"]
	if b.ran("rootlesskit find '"+dep.Root()+"' -mindepth 1 -delete") != 1 {
		t.Errorf("the root was not emptied through rootlesskit:\n%s", strings.Join(b.commands, "\n"))
	}
	if b.ran("rm -rf -- '"+dep.Root()+"'") != 0 {
		t.Error("tried to remove the root directory itself")
	}
}

// The registry is root's, and host evict's to change: site remove neither
// reads nor writes it on the host it cleans, whether it keeps the data, a
// gateway's Caddy, or nothing.
func TestSiteRemoveLeavesTheRegistryToEvict(t *testing.T) {
	for name, o := range map[string]siteremove.Options{"data kept": {}, "data deleted": {DeleteData: true}} {
		t.Run(name, func(t *testing.T) {
			w := setup(t)
			b := w.hosts["home-b"]
			before := b.files[registry.Path]
			p := w.mustBuild("home-b", o)
			if strings.Contains(printed(p), registry.Path) {
				t.Errorf("the plan names the registry:\n%s", printed(p))
			}
			if err := siteremove.Execute(p); err != nil {
				t.Fatal(err)
			}
			if b.files[registry.Path] != before {
				t.Errorf("the registry changed:\n%s", b.files[registry.Path])
			}
			for _, c := range b.commands {
				if strings.Contains(c, registry.Path) {
					t.Errorf("ran %s", c)
				}
			}
			for _, r := range b.reads {
				if r == registry.Path {
					t.Error("read the registry")
				}
			}
		})
	}
}

// --delete-data leaves nothing of the deployment's under its state
// directory, the lock file an earlier command left included: the user side
// empties the root, and host evict removes the root itself.
func TestDeleteDataLeavesNothingUnderTheStateDir(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	b.files[dep.StatePath("lock")] = ""
	if err := siteremove.Execute(w.mustBuild("home-b", siteremove.Options{DeleteData: true})); err != nil {
		t.Fatal(err)
	}
	for path := range b.files {
		if strings.HasPrefix(path, dep.StateDir()+"/") {
			t.Errorf("%s is left under the state directory", path)
		}
	}
}

// The key record is deleted with a plain rm, as the deployment and grant
// records are, never under the deployment's lock, which would create a lock
// file in the state directory again.
func TestTheKeyRecordIsDeletedWithoutTheLock(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	dest, _ := config.ParseDestination("ubuntu@192.0.2.12")
	if err := siteremove.Execute(forced(t, w, w.cfg, "home-b", dest, siteremove.Options{})); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.files[record]; ok {
		t.Error("the key record is still there")
	}
	if b.ran("rm -f -- '"+record+"'") != 1 {
		t.Errorf("the key record was not deleted with a plain rm:\n%s", strings.Join(b.commands, "\n"))
	}
	for _, c := range b.commands {
		if strings.Contains(c, "exec 9>>") || strings.Contains(c, "flock ") {
			t.Errorf("ran the lock prelude: %s", c)
		}
	}
	if _, ok := b.files[dep.StatePath("lock")]; ok {
		t.Error("the lock file was created")
	}
}

// Every plan that reaches or skips a host ends with the line the host's
// owner runs to finish, naming the site's login user; --host-gone's report
// carries it too.
func TestThePlanCarriesTheEvictLine(t *testing.T) {
	w := setup(t)
	want := siteremove.EvictLine(dep, w.cfg.Sites["home-b"].SSHUser())
	if want != "sudo paisans host evict --deployment "+ourID+" --user "+w.cfg.Sites["home-b"].SSHUser()+" --execute" {
		t.Errorf("EvictLine = %q", want)
	}
	if p := w.mustBuild("home-b", siteremove.Options{}); p.Evict != want {
		t.Errorf("Evict = %q, want %q", p.Evict, want)
	}
	gone := w.mustBuild("home-b", siteremove.Options{HostGone: true})
	if gone.Evict != want {
		t.Errorf("--host-gone: Evict = %q, want %q", gone.Evict, want)
	}
	if !strings.Contains(strings.Join(gone.Kept, "\n"), want) {
		t.Errorf("--host-gone: kept %q", gone.Kept)
	}
	// --force names the user the admission record names, which the login
	// must be.
	dest, _ := config.ParseDestination("ubuntu@192.0.2.12")
	if p := forced(t, w, w.cfg, "home-b", dest, siteremove.Options{}); p.Evict != siteremove.EvictLine(dep, "ubuntu") {
		t.Errorf("--force: Evict = %q", p.Evict)
	}
}
