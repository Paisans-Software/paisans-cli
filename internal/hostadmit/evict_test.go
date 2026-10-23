package hostadmit_test

import (
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/version"
)

const id = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

// evicted is a host where admit of site ran with flags, with the mesh up.
func evicted(site string, flags hostadmit.Flags) func() *fakeHost {
	return func() *fakeHost {
		return admittedHost(requestFor(site), flags).with("mesh-unit", "enabled active").
			withFile("/etc/wireguard/psns-f2a9.conf", "[Interface]\n", "600", 0)
	}
}

// withEntries is h with the probe's count of what each directory evict may
// remove holds: every file, directory and record h's facts place in it.
func withEntries(h *fakeHost) *fakeHost {
	for _, dir := range []string{"/etc/paisans", "/srv/paisans", "/usr/local/libexec/paisans"} {
		seen := map[string]bool{}
		for _, kv := range h.facts {
			f := strings.Fields(kv[1])
			switch {
			case len(f) == 0 || kv[0] != "file" && kv[0] != "dir" && kv[0] != "record":
				continue
			case kv[0] != "record" && len(f) > 1 && f[1] == "absent":
				continue
			case path.Dir(f[0]) == dir:
				seen[f[0]] = true
			}
		}
		if h.has("dir", dir) {
			h.add("entries", itoa(len(seen))+" "+dir)
		}
	}
	return h
}

// withoutRecordLine is h with no record line for this deployment's record.
func withoutRecordLine(h *fakeHost) *fakeHost {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv != [2]string{"record", "/etc/paisans/admitted.f2a9.json"} {
			kept = append(kept, kv)
		}
	}
	h.facts = kept
	return h
}

// has reports whether h has a key line for path that is not absent.
func (h *fakeHost) has(key, path string) bool {
	for _, kv := range h.facts {
		if kv[0] == key && strings.HasPrefix(kv[1], path+" ") && !strings.HasPrefix(kv[1], path+" absent") {
			return true
		}
	}
	return false
}

var evictCases = []struct {
	name string
	user string
	host func() *fakeHost
}{
	{"evict-only-deployment", "paisans-f2a9", func() *fakeHost {
		return withEntries(evicted("watch", hostadmit.Flags{PublicPorts: true})())
	}},
	{"evict-one-of-two", "paisans-f2a9", func() *fakeHost {
		return withEntries(withOther(evicted("watch", hostadmit.Flags{PublicPorts: true})(), true, true))
	}},
	{"evict-beside-unreadable", "paisans-f2a9", func() *fakeHost {
		return withEntries(withUnreadable(evicted("watch", hostadmit.Flags{PublicPorts: true})()))
	}},
	{"evict-with-data-dir", "paisans-f2a9", func() *fakeHost {
		h := admittedWithDataDir("vm", "/srv/data")
		return withEntries(h.with("mesh-unit", "enabled active"))
	}},
	{"evict-data-site", "paisans-f2a9", func() *fakeHost {
		return withEntries(evicted("home-a", gateway)())
	}},
	{"evict-without-record", "paisans-f2a9", func() *fakeHost {
		h := evicted("vm", hostadmit.Flags{})()
		delete(h.files, "/etc/paisans/admitted.f2a9.json")
		h.dropKeyed("file", "/etc/paisans/admitted.f2a9.json")
		return withEntries(withoutRecordLine(h))
	}},
	{"evict-installed-by-admit", "paisans-f2a9", func() *fakeHost {
		return withEntries(installedByAdmit(evicted("vm", hostadmit.Flags{})()))
	}},
	{"evict-hold-left", "paisans-f2a9", func() *fakeHost {
		return withEntries(withOther(evicted("vm", hostadmit.Flags{})(), false, false).with("docker-hold", "policy"))
	}},
	{"evict-nothing-left", "paisans-f2a9", func() *fakeHost { return newBlank() }},
}

func TestEvictPlans(t *testing.T) {
	for _, c := range evictCases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := hostadmit.BuildEvict(c.host(), hostadmit.EvictInput{Deployment: id, User: c.user})
			got := ""
			if err != nil {
				got = "refused: " + err.Error() + "\n"
			} else {
				got = plan.Text()
			}
			checkGoldenAt(t, filepath.Join("testdata", "evict", c.name+".txt"), got)
		})
	}
}

// A host with nothing of the deployment left plans nothing, so a re-run of
// an evict that finished is a no-op.
func TestEvictOfAnEvictedHostPlansNothing(t *testing.T) {
	plan, err := hostadmit.BuildEvict(newBlank(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	// With no user and no subordinate range left, there is no range left
	// to free, so the warning about a data directory evict cannot find is
	// not given again.
	if len(plan.Steps) != 0 || len(plan.Warnings) != 0 {
		t.Errorf("evict of an evicted host plans:\n%s", plan.Text())
	}
	// What a finished evict leaves: the SSH allow it keeps, and a registry
	// with no entry. The kept allow is named again, and nothing is planned.
	left := newBlank().answer("ufw show added", "ufw present\nstatus active\nrule allow 22/tcp comment 'paisans-f2a9: ssh, the bootstrap route'\n").
		file("/var/lib/paisans/registry.json", "{\"version\":1,\"deployments\":{\n}}\n")
	plan, err = hostadmit.BuildEvict(left, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 || len(plan.Warnings) != 1 {
		t.Errorf("evict of an evicted host plans:\n%s", plan.Text())
	}
}

// Evict deletes a user only when it is the deployment's: the record's, or,
// with no record, paisans-<token>. The owner's login, root, another
// deployment's user and a privileged account are refused, with nothing
// planned.
func TestEvictRefusesAnotherUser(t *testing.T) {
	withRecord := evicted("vm", hostadmit.Flags{})
	noRecord := func() *fakeHost {
		h := withRecord()
		delete(h.files, "/etc/paisans/admitted.f2a9.json")
		return h
	}
	for _, c := range []struct {
		name, user string
		host       func() *fakeHost
	}{
		{"root", "root", withRecord},
		{"another deployment's", "paisans-0c0d", withRecord},
		{"not the record's", "community", withRecord},
		{"the owner's login, no record", "ubuntu", noRecord},
		{"privileged", "paisans-f2a9", func() *fakeHost { return withRecord().with("groups", "paisans-f2a9 sudo") }},
		{"uid 0", "paisans-f2a9", func() *fakeHost {
			return withRecord().with("passwd", "paisans-f2a9:x:0:0::/home/paisans-f2a9:/bin/bash")
		}},
	} {
		h := c.host()
		_, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: c.user})
		if err == nil {
			t.Errorf("%s: evict of %s was planned", c.name, c.user)
		}
	}
}

// With another deployment admitted, evict leaves the host-wide files and
// every sysctl another deployment needs; it takes only its own.
func TestEvictOfOneOfTwoKeepsTheHostWide(t *testing.T) {
	h := withOther(evicted("watch", hostadmit.Flags{PublicPorts: true})(), true, true)
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Text()
	for _, kept := range []string{"delegate.conf", "modules-load.d/paisans.conf", "60-paisans.conf", "sysctl -w", "rmdir /etc/paisans"} {
		for _, s := range plan.Steps {
			if strings.Contains(s.Command, kept) {
				t.Errorf("evict of one of two touches %s: %s", kept, s.Command)
			}
		}
	}
	if !strings.Contains(text, "kept for deployment 0c0dc4e1") {
		t.Errorf("the plan does not say what it keeps, and for whom:\n%s", text)
	}
}

// Beside a record evict cannot read, it keeps what that record may need:
// the host-wide files, the sysctls and /etc/paisans, and says why.
func TestEvictBesideAnUnreadableRecordKeepsTheHostWide(t *testing.T) {
	h := withEntries(withUnreadable(evicted("watch", hostadmit.Flags{PublicPorts: true})()))
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"delegate.conf", "modules-load.d/paisans.conf", "60-paisans.conf", "sysctl -w", "rmdir /etc/paisans"} {
		for _, s := range plan.Steps {
			if strings.Contains(s.Command, kept) {
				t.Errorf("evict beside an unreadable record touches %s: %s", kept, s.Command)
			}
		}
	}
	if !strings.Contains(plan.Text(), "/etc/paisans/admitted.0c0d.json cannot be read") {
		t.Errorf("the plan does not name the record it cannot read:\n%s", plan.Text())
	}
}

// The user's manager stops before anything that fences it goes: the mesh,
// the egress table and the user itself all come after, and the registry
// entry is the last thing taken away, so a failed evict is found and run
// again.
func TestEvictOrder(t *testing.T) {
	plan, err := hostadmit.BuildEvict(withEntries(evicted("watch", hostadmit.Flags{PublicPorts: true})()), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	at := func(sub string) int {
		for i, s := range plan.Steps {
			if strings.Contains(s.Command, sub) {
				return i
			}
		}
		t.Fatalf("no step runs %q:\n%s", sub, plan.Text())
		return -1
	}
	stop := at("systemctl stop user@1001.service")
	for _, later := range []string{"wg-quick@psns-f2a9", "nft delete table inet paisans_f2a9", "userdel"} {
		if at(later) < stop {
			t.Errorf("%q runs before the user's manager stops", later)
		}
	}
	if last := plan.Steps[len(plan.Steps)-1]; !strings.Contains(last.Command, "/var/lib/paisans/registry.json") {
		t.Errorf("the last step is %q, not the registry entry", last.Title)
	}
}

// RunSteps runs every step in order through the transport and stops at
// none of them on a host that answers.
func TestRunStepsRunsEveryStep(t *testing.T) {
	h := withEntries(evicted("vm", hostadmit.Flags{})())
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	h.ran = nil
	if err := hostadmit.RunSteps(h, plan, ui.NewPlain(io.Discard, false)); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, s := range plan.Steps {
		if s.Command != "" {
			want = append(want, s.Command)
		}
	}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(h.ran, "\n"), strings.Join(want, "\n"))
	}
}

// stepIndex is the index of plan's first step whose command holds sub, -1
// when none does.
func stepIndex(plan *hostadmit.Plan, sub string) int {
	for i, s := range plan.Steps {
		if strings.Contains(s.Command, sub) {
			return i
		}
	}
	return -1
}

// Whenever the user exists, every process of its uid is killed, and none
// is left, before the mesh drop-in or the egress table goes and before
// userdel: a process the manager's stop missed, one runuser or cron
// started, would otherwise run unfenced. It is planned with the manager
// stopped already, and on a re-run whose egress table is gone.
func TestEvictKillsEveryProcessOfTheUser(t *testing.T) {
	for _, c := range []struct {
		name string
		host func() *fakeHost
	}{
		{"admitted", evicted("vm", hostadmit.Flags{})},
		{"manager stopped, linger off", func() *fakeHost {
			return evicted("vm", hostadmit.Flags{})().with("linger", "no").with("manager", "inactive")
		}},
		{"egress gone, userdel failed", func() *fakeHost {
			h := evicted("vm", hostadmit.Flags{})().with("linger", "no").with("manager", "inactive").
				with("egress-enabled", "no").with("egress-table", "absent").with("mesh-unit", "disabled inactive")
			for _, f := range []string{"/etc/systemd/system/paisans-f2a9-egress.service", "/etc/paisans/egress.f2a9.nft",
				"/etc/systemd/system/user@1001.service.d/paisans-mesh.conf", "/etc/wireguard/psns-f2a9.conf"} {
				h.dropKeyed("file", f)
			}
			return h
		}},
	} {
		plan, err := hostadmit.BuildEvict(c.host(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		kill := stepIndex(plan, "ps -eo pid=,ruid=,euid=,suid=")
		if kill < 0 || !strings.Contains(plan.Steps[kill].Command, "$2 == 1001 || $3 == 1001 || $4 == 1001") ||
			!strings.Contains(plan.Steps[kill].Command, "$2 >= 165536 && $2 < 231072") || !strings.Contains(plan.Steps[kill].Command, "survived SIGKILL") {
			t.Errorf("%s: no step kills and checks every process of uid 1001 and its subordinate range, by real, effective and saved uid:\n%s", c.name, plan.Text())
			continue
		}
		if del := stepIndex(plan, "userdel"); del >= 0 && !strings.Contains(plan.Steps[del].Command, "ps -eo pid=,ruid=,euid=,suid=") {
			t.Errorf("%s: userdel does not kill and check again first: %s", c.name, plan.Steps[del].Command)
		}
		for _, later := range []string{"paisans-mesh.conf", "nft delete table", "userdel"} {
			if i := stepIndex(plan, later); i >= 0 && i < kill {
				t.Errorf("%s: %q runs before the user's processes are killed", c.name, later)
			}
		}
		if stepIndex(plan, "userdel") < 0 {
			t.Errorf("%s: no userdel", c.name)
		}
	}
}

// noRecordWithDataDir is an admitted vm with --data-dir /srv/data whose
// record is gone, its Docker data directory owned by owner with mode.
func noRecordWithDataDir(mode string, owner int) *fakeHost {
	h := admittedWithDataDir("vm", "/srv/data").withDir("/srv/data/paisans-f2a9", mode, owner, owner)
	delete(h.files, "/etc/paisans/admitted.f2a9.json")
	h.dropKeyed("file", "/etc/paisans/admitted.f2a9.json")
	return withoutRecordLine(h)
}

// With no record, --data-dir names where admit put the Docker data: it is
// removed when it is a directory of the user's or of its subordinate
// range, and refused otherwise. Without --data-dir, the plan warns that
// data outside the home cannot be found.
func TestEvictDataDirWithoutRecord(t *testing.T) {
	for _, c := range []struct {
		name, mode string
		owner      int
		refused    bool
	}{
		{"the user's", "drwx------", 1001, false},
		{"its subordinate range's", "drwx--x---", 165536, false},
		{"root's", "drwx------", 0, true},
		{"another range's", "drwx------", 300000, true},
		{"a link", "lrwxrwxrwx", 1001, true},
	} {
		plan, err := hostadmit.BuildEvict(noRecordWithDataDir(c.mode, c.owner), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9", DataDir: "/srv/data"})
		switch {
		case c.refused && err == nil:
			t.Errorf("%s: planned:\n%s", c.name, plan.Text())
		case !c.refused && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case !c.refused && stepIndex(plan, "rm -rf --one-file-system -- '/srv/data/paisans-f2a9'") < 0:
			t.Errorf("%s: the data directory is not removed:\n%s", c.name, plan.Text())
		}
	}
	plan, err := hostadmit.BuildEvict(noRecordWithDataDir("drwx------", 1001), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Text(), "warning: data: ") || !strings.Contains(plan.Text(), "--data-dir") || !strings.Contains(plan.Text(), "subordinate range") {
		t.Errorf("no warning that the data directory cannot be found:\n%s", plan.Text())
	}
}

// With a record, its data_dir is the one evict removes, and a --data-dir
// that names another is refused rather than ignored.
func TestEvictDataDirAgainstTheRecord(t *testing.T) {
	h := func() *fakeHost { return admittedWithDataDir("vm", "/srv/data").with("mesh-unit", "enabled active") }
	if _, err := hostadmit.BuildEvict(h(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9", DataDir: "/srv/data"}); err != nil {
		t.Errorf("the record's own --data-dir: %v", err)
	}
	for _, other := range []string{"/srv/other", "/srv/data/"} {
		if _, err := hostadmit.BuildEvict(h(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9", DataDir: other}); err == nil {
			t.Errorf("--data-dir %s, against the record's /srv/data, was planned", other)
		}
	}
	if _, err := hostadmit.BuildEvict(evicted("vm", hostadmit.Flags{})(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9", DataDir: "/srv/data"}); err == nil {
		t.Error("--data-dir against a record with none was planned")
	}
}

// A deployment claimed here with no record yet, an admit in flight or one
// that failed before its record, may need the host-wide files and sysctls
// as much as an admitted one: evict keeps them, as beside a record it
// cannot read, and says why.
func TestEvictBesideAClaimWithNoRecordKeepsTheHostWide(t *testing.T) {
	h := evicted("watch", hostadmit.Flags{PublicPorts: true})()
	_, other := registry.ForRequest(requestFor("home-a"), admitted)
	other.Token, other.Root, other.Interface, other.ListenPort, other.Subnet, other.Address, other.Roles = "0c0d", "/srv/paisans/0c0d", "psns-0c0d", 51899, "10.50.0.0/24", "10.50.0.1", ""
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{
		id:                                     func() registry.Entry { _, e := registry.ForRequest(requestFor("watch"), admitted); return e }(),
		"0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01": other,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := hostadmit.BuildEvict(withEntries(h.file(registry.Path, string(data))), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"delegate.conf", "modules-load.d/paisans.conf", "60-paisans.conf", "sysctl -w"} {
		if i := stepIndex(plan, kept); i >= 0 {
			t.Errorf("evict beside a claim with no record touches %s: %s", kept, plan.Steps[i].Command)
		}
	}
	if !strings.Contains(plan.Text(), "deployment 0c0d is claimed here and has no admission record") {
		t.Errorf("the plan does not say why it keeps them:\n%s", plan.Text())
	}
}

// userdel --remove deletes whatever home passwd names, so evict checks it
// as admit did: a link or another's directory is refused; a home already
// gone deletes the user alone. A system account is no deployment's user.
func TestEvictChecksTheHomeItDeletes(t *testing.T) {
	for _, c := range []struct {
		name, home, want string
	}{
		{"a link", "link 0 /home/paisans-f2a9", "is a link"},
		{"another's", "dir 1234 /home/paisans-f2a9", "is not paisans-f2a9's"},
	} {
		_, err := hostadmit.BuildEvict(evicted("vm", hostadmit.Flags{})().with("home", c.home), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	plan, err := hostadmit.BuildEvict(evicted("vm", hostadmit.Flags{})().with("home", "absent 0 /home/paisans-f2a9"), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if i := stepIndex(plan, "userdel"); i < 0 || strings.Contains(plan.Steps[i].Command, "--remove") {
		t.Errorf("a user whose home is gone is deleted by %q", plan.Steps[i].Command)
	}
	_, err = hostadmit.BuildEvict(evicted("vm", hostadmit.Flags{})().with("passwd", "paisans-f2a9:x:33:33::/home/paisans-f2a9:/bin/bash").with("home", "dir 33 /home/paisans-f2a9"), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err == nil || !strings.Contains(err.Error(), "system account") {
		t.Errorf("a system account: %v", err)
	}
}

// A host-wide file that is not as admit wrote it is the owner's, kept, and
// the plan says so.
func TestEvictKeepsAHostWideFileItDidNotWrite(t *testing.T) {
	h := evicted("vm", hostadmit.Flags{})().withFile("/etc/systemd/system/user@.service.d/delegate.conf", "[Service]\nDelegate=yes\n", "644", 0)
	plan, err := hostadmit.BuildEvict(withEntries(h), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if stepIndex(plan, "delegate.conf") >= 0 || !strings.Contains(plan.Text(), "delegate.conf kept: it is not as admit wrote it") {
		t.Errorf("the owner's delegate.conf:\n%s", plan.Text())
	}
}

// A user the owner deleted by hand leaves its mesh drop-in, whose path
// holds the UID evict can no longer read; the probe finds it by the
// deployment it names, and evict removes it.
func TestEvictFindsTheDropInOfADeletedUser(t *testing.T) {
	h := newBlank().add("orphan-dropin", "/etc/systemd/system/user@1001.service.d/paisans-mesh.conf")
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if stepIndex(plan, "rm -f /etc/systemd/system/user@1001.service.d/paisans-mesh.conf") < 0 {
		t.Errorf("the drop-in is left:\n%s", plan.Text())
	}
	if _, err := hostadmit.BuildEvict(newBlank().add("orphan-dropin", "/etc/x; reboot"), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"}); err == nil {
		t.Error("an orphan drop-in that is no drop-in's path was read")
	}
}

// An admission record evict cannot read is to be moved aside; the refusal
// says --data-dir then finds the Docker data admit put outside the home.
func TestEvictsUnreadableRecordRefusalNamesTheDataDir(t *testing.T) {
	h := evicted("vm", hostadmit.Flags{})().file("/etc/paisans/admitted.f2a9.json", "{")
	_, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err == nil || !strings.Contains(err.Error(), "Move it aside") || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("BuildEvict = %v", err)
	}
}

// An admit stopped between writing the egress table beside its file and
// moving it over leaves egress.<token>.nft.new; evict removes it, and
// counts it among its own in /etc/paisans.
func TestEvictRemovesALeftoverEgressTable(t *testing.T) {
	h := newBlank().withDir("/etc/paisans", "drwxr-xr-x", 0, 0).withFile(egressFile+".new", "table inet paisans_f2a9 {}\n", "644", 0).add("entries", "1 /etc/paisans")
	plan, err := hostadmit.BuildEvict(h, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if i := stepIndex(plan, egressFile+".new"); i < 0 {
		t.Errorf("the leftover is kept:\n%s", plan.Text())
	}
	if stepIndex(plan, "rmdir /etc/paisans") < 0 {
		t.Errorf("/etc/paisans is kept:\n%s", plan.Text())
	}
}

// Evict removes the mesh's conf drop-in, and its directory once empty.
func TestEvictRemovesTheMeshConfDropIn(t *testing.T) {
	plan, err := hostadmit.BuildEvict(evicted("vm", hostadmit.Flags{})(), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if i := stepIndex(plan, "rm -f "+meshCondPath+" && rmdir --ignore-fail-on-non-empty /etc/systemd/system/wg-quick@psns-f2a9.service.d && systemctl daemon-reload"); i < 0 {
		t.Errorf("evict leaves the mesh's conf drop-in:\n%s", plan.Text())
	}
}

// What admit installed stays when the last deployment goes, and evict's
// plan names it, from /etc/paisans/installed, which stays with it, and
// /etc/paisans with that, as admit wrote them; the admission record and
// the registry entry go. A list that names nothing left goes, with
// /etc/paisans. Beside another admitted deployment, nothing is said and
// the file stays.
func TestEvictNamesWhatAdmitInstalled(t *testing.T) {
	content := "# Written by paisans site admit: what it installed on this host, which stays\n# when paisans site evict removes the last admitted deployment, and each\n# directory it created, which evict removes once empty.\n" +
		"package docker-ce\npackage docker-ce-cli\npackage containerd.io\npackage docker-ce-rootless-extras\npackage uidmap\npackage wireguard-tools\n" +
		"file /etc/apt/keyrings/docker.asc\nfile /etc/apt/sources.list.d/docker.sources\n"
	host := func(shared bool) *fakeHost {
		h := evicted("vm", hostadmit.Flags{})()
		h.file("/etc/paisans/installed", content).withFile("/etc/paisans/installed", content, "644", 0)
		for _, p := range []string{"docker-ce-cli", "containerd.io", "wireguard-tools"} {
			h.withPkg(p, "installed")
		}
		if shared {
			withOther(h, false, false)
		}
		return withEntries(h)
	}
	plan, err := hostadmit.BuildEvict(host(false), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "packages: what admit installed stays: docker-ce, docker-ce-cli, containerd.io, docker-ce-rootless-extras, uidmap, wireguard-tools; /etc/apt/keyrings/docker.asc, /etc/apt/sources.list.d/docker.sources; the docker group"
	if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool { return w.Hint == want }) {
		t.Errorf("the plan does not say %q:\n%s", want, plan.Text())
	}
	if stepIndex(plan, "/etc/paisans/installed") >= 0 || stepIndex(plan, "rmdir /etc/paisans") >= 0 {
		t.Errorf("the installed file or /etc/paisans goes:\n%s", plan.Text())
	}
	if !slices.Contains(plan.Present, "evict: /etc/paisans kept, for /etc/paisans/installed, which stays with what admit installed") {
		t.Errorf("the plan does not say /etc/paisans is kept:\n%s", plan.Text())
	}
	if stepIndex(plan, "rm -f /etc/paisans/admitted.f2a9.json") < 0 || titled(plan, "remove the registry entry") < 0 {
		t.Errorf("the admission record or the registry entry is left:\n%s", plan.Text())
	}
	gone := host(false)
	for _, p := range []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-ce-rootless-extras", "uidmap", "wireguard-tools"} {
		gone.withPkg(p, "absent")
	}
	gone.file("/etc/paisans/installed", "package docker-ce\n").withFile("/etc/paisans/installed", "package docker-ce\n", "644", 0)
	plan, err = hostadmit.BuildEvict(gone, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if stepIndex(plan, "rm -f /etc/paisans/installed") < 0 || stepIndex(plan, "rmdir /etc/paisans") < 0 {
		t.Errorf("a list that names nothing left, or /etc/paisans, is kept:\n%s", plan.Text())
	}
	plan, err = hostadmit.BuildEvict(host(true), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Text(), "what admit installed") || stepIndex(plan, "/etc/paisans/installed") >= 0 {
		t.Errorf("beside another deployment:\n%s", plan.Text())
	}
}

// Admit after evict on the same host finds the system Docker the first
// admit installed and disabled as it was left: disabled, stopped, its
// socket gone. The installed list evict keeps says it is admit's, so the
// mesh's check reads no networks for it, and admit leaves it disabled
// without a step that touches it.
func TestAdmitAfterEvictKeepsTheSystemDockerDisabled(t *testing.T) {
	before := withEntries(installedByAdmit(evicted("vm", hostadmit.Flags{})()))
	plan, err := hostadmit.BuildEvict(before, hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	after := current().with("system-unit", "docker.service disabled inactive").add("system-unit", "docker.socket disabled inactive").
		answer("unix:///var/run/docker.sock network ls", dockerDown)
	if stepIndex(plan, "rm -f /etc/paisans/installed") < 0 {
		content := before.files["/etc/paisans/installed"]
		after.withDir("/etc/paisans", "drwxr-xr-x", 0, 0).file("/etc/paisans/installed", content).withFile("/etc/paisans/installed", content, "644", 0)
	}
	admit, err := hostadmit.Build(after, input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatalf("admit after evict: %v", err)
	}
	if !slices.Contains(admit.Present, "docker: the system daemon admit installed is disabled") || !slices.Contains(admit.Present, "mesh: nothing on this host overlaps 10.44.0.0/24") {
		t.Errorf("admit after evict:\n%s", admit.Text())
	}
	for _, s := range admit.Steps {
		if strings.Contains(s.Command, "systemctl disable") || strings.Contains(s.Command, "systemctl enable") || strings.Contains(s.Command, "systemctl start docker") {
			t.Errorf("step %q touches the system Docker: %s", s.Title, s.Command)
		}
	}
	if !recordWritten(t, admit).SystemDockerDisabled {
		t.Error("the record does not say admit disabled the system Docker")
	}
}

// Evict removes the boot entries of br_netfilter and the watchdog's
// module and unloads neither: the plan says each stays loaded until the
// next boot, by name.
func TestEvictSaysTheModulesStayLoaded(t *testing.T) {
	const unit = "/etc/systemd/system/paisans-f2a9-watchdog.service"
	content := "[Unit]\nDescription=Load the softdog watchdog for Patroni\n\n[Service]\nType=oneshot\nExecStart=/usr/sbin/modprobe softdog\nRemainAfterExit=yes\n"
	h := evicted("home-a", gateway)()
	h.file(unit, content).withFile(unit, content, "644", 0)
	plan, err := hostadmit.BuildEvict(withEntries(h), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"br_netfilter: stays loaded until the next boot", "watchdog: softdog stays loaded until the next boot"} {
		if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool { return w.Hint == want }) {
			t.Errorf("the plan does not say %q:\n%s", want, plan.Text())
		}
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "modprobe -r") || strings.Contains(s.Command, "rmmod") {
			t.Errorf("step %q unloads a module: %s", s.Title, s.Command)
		}
	}
}

// The drop-in directory delegate.conf lives in goes with it, once empty,
// when admit created it, which /etc/paisans/installed records; one the
// host had is left.
func TestEvictRemovesTheDelegationDirectoryAdmitMade(t *testing.T) {
	for _, made := range []bool{true, false} {
		h := evicted("vm", hostadmit.Flags{})()
		content := "package uidmap\n"
		if made {
			content += "dir /etc/systemd/system/user@.service.d\n"
		}
		h.file("/etc/paisans/installed", content).withFile("/etc/paisans/installed", content, "644", 0)
		plan, err := hostadmit.BuildEvict(withEntries(h), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
		if err != nil {
			t.Fatal(err)
		}
		rmdir := stepIndex(plan, "rm -f /etc/systemd/system/user@.service.d/delegate.conf && rmdir --ignore-fail-on-non-empty /etc/systemd/system/user@.service.d && systemctl daemon-reload")
		if made != (rmdir >= 0) {
			t.Errorf("admit made the directory: %v; the plan:\n%s", made, plan.Text())
		}
	}
}

// A hold an admit stopped in left is never legitimate outside a running
// install, so evict puts the owner's policy-rc.d back, whether or not
// another deployment stays.
func TestEvictPutsBackALeftHold(t *testing.T) {
	for _, shared := range []bool{false, true} {
		h := evicted("vm", hostadmit.Flags{})().with("docker-hold", "policy")
		if shared {
			withOther(h, false, false)
		}
		plan, err := hostadmit.BuildEvict(withEntries(h), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
		if err != nil {
			t.Fatal(err)
		}
		if stepIndex(plan, "release_docker_hold") < 0 {
			t.Errorf("shared %v: evict leaves the hold:\n%s", shared, plan.Text())
		}
	}
}

// The record says admit disabled the system daemon once, not that it is
// disabled now: an owner may have enabled it since, so evict does not say
// it stays disabled.
func TestEvictDoesNotSayTheSystemDockerStaysDisabled(t *testing.T) {
	plan, err := hostadmit.BuildEvict(withEntries(installedByAdmit(evicted("vm", hostadmit.Flags{})())), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Detail, "stays disabled") {
			t.Errorf("evict says %q", w.Detail)
		}
	}
	if !strings.Contains(plan.Text(), "it is left as it is now") {
		t.Errorf("evict does not say the daemon is left as it is:\n%s", plan.Text())
	}
}

// A malformed installed list matters only to the last deployment's evict:
// another deployment's evict, beside it, goes on.
func TestABadInstalledListStopsOnlyTheLastEvict(t *testing.T) {
	h := withOther(evicted("vm", hostadmit.Flags{})(), false, false)
	h.file("/etc/paisans/installed", "rm -rf /\n").withFile("/etc/paisans/installed", "rm -rf /\n", "644", 0)
	if _, err := hostadmit.BuildEvict(withEntries(h), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"}); err != nil {
		t.Errorf("BuildEvict beside another deployment = %v", err)
	}
}

// evictBeside is evict of watch on a host whose registry also holds 0c0d,
// with the releases that claimed each.
func evictBeside(t *testing.T, ours, theirs string) error {
	t.Helper()
	h := evicted("watch", hostadmit.Flags{PublicPorts: true})()
	_, mine := registry.ForRequest(requestFor("watch"), admitted)
	mine.Build.Version = ours
	_, other := registry.ForRequest(requestFor("home-a"), admitted)
	other.Token, other.Root, other.Interface, other.ListenPort, other.Subnet, other.Address, other.Roles = "0c0d", "/srv/paisans/0c0d", "psns-0c0d", 51899, "10.50.0.0/24", "10.50.0.1", ""
	other.Build.Version = theirs
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{
		id:                                     mine,
		"0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01": other,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = hostadmit.BuildEvict(withEntries(h.file(registry.Path, string(data))), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	return err
}

// A host any deployment of which a newer paisans claimed, this one or
// another, is refused before anything is planned, naming the highest
// release to upgrade to.
func TestEvictRefusesAHostANewerToolkitClaimed(t *testing.T) {
	for _, c := range []struct{ ours, theirs, highest string }{
		{"99.0.0", version.Version, "99.0.0"},
		{version.Version, "99.1.0", "99.1.0"},
		{"99.0.0", "99.10.0", "99.10.0"},
		{"99.10.0", "99.2.0", "99.10.0"},
	} {
		err := evictBeside(t, c.ours, c.theirs)
		if err == nil {
			t.Errorf("%s beside %s: evict was planned", c.ours, c.theirs)
			continue
		}
		if !strings.Contains(err.Error(), "Upgrade paisans to "+c.highest+" or later") || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s beside %s: %v", c.ours, c.theirs, err)
		}
	}
	if err := evictBeside(t, version.Version, "next"); err == nil || !strings.Contains(err.Error(), `"next"`) {
		t.Errorf("a release evict cannot order was not refused: %v", err)
	}
}

// This release, or an older one, claiming every deployment is evicted as
// before.
func TestEvictProceedsWhereNoNewerToolkitClaimed(t *testing.T) {
	for _, theirs := range []string{version.Version, "0.0.1"} {
		if err := evictBeside(t, "0.0.1", theirs); err != nil {
			t.Errorf("beside %s: %v", theirs, err)
		}
	}
}

// Another deployment's registry entry that does not say which build
// claimed the host does not stop evict: the plan names it for its owner,
// and evicts this deployment.
func TestEvictGoesOnPastAnotherEntryWithNoBuild(t *testing.T) {
	h := evicted("watch", hostadmit.Flags{PublicPorts: true})()
	_, mine := registry.ForRequest(requestFor("watch"), admitted)
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{id: mine}})
	if err != nil {
		t.Fatal(err)
	}
	other := `"0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01":{"token":"0c0d","root":"/srv/paisans/0c0d","domain":"","site":"edge","claimed_at":"2026-10-01T09:00:00Z","interface":"psns-0c0d","listen_port":51899,"subnet":"10.50.0.0/24","address":"10.50.0.1","roles":""},`
	withOther := strings.Replace(string(data), "{\"version\":1,\"deployments\":{\n", "{\"version\":1,\"deployments\":{\n"+other+"\n", 1)
	plan, err := hostadmit.BuildEvict(withEntries(h.file(registry.Path, withOther)), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w hostprep.Warning) bool {
		return strings.Contains(w.Hint, "0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01's entry does not say which build") && strings.Contains(w.Detail, "`paisans site evict`")
	}) {
		t.Errorf("the other entry is not named:\n%s", plan.Text())
	}
	if len(plan.Steps) == 0 {
		t.Error("nothing was planned")
	}
}

// On a data site, stopping the user's manager stops Patroni, and Docker
// kills one that outlasts its stop timeout with the watchdog armed. The
// manager's step then disarms the watchdog as root, once nothing holds it,
// so the host does not reboot.
func TestEvictDisarmsTheWatchdogAfterTheManagerStops(t *testing.T) {
	plan, err := hostadmit.BuildEvict(withEntries(evicted("home-a", gateway)()), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	var manager *hostadmit.Step
	for i, s := range plan.Steps {
		if s.Title == "stop paisans-f2a9's manager" {
			manager = &plan.Steps[i]
		}
	}
	if manager == nil {
		t.Fatalf("no manager step:\n%s", plan.Text())
	}
	stop, disarm := strings.Index(manager.Command, "systemctl stop user@1001.service"), strings.Index(manager.Command, "printf V > /dev/watchdog")
	if stop < 0 || disarm < stop {
		t.Errorf("the manager step does not disarm the watchdog after the manager stops: %s", manager.Command)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Hint, "manager stops its Patroni") {
			t.Errorf("still warns about a reboot: %s", w.Hint)
		}
	}
}

// A site without this deployment's watchdog leaves the device alone.
func TestEvictOfASiteWithoutTheWatchdogDoesNotTouchIt(t *testing.T) {
	plan, err := hostadmit.BuildEvict(withEntries(evicted("watch", hostadmit.Flags{PublicPorts: true})()), hostadmit.EvictInput{Deployment: id, User: "paisans-f2a9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Title, "manager") && strings.Contains(s.Command, "/dev/watchdog") {
			t.Errorf("the manager step touches the watchdog on a site without one: %s", s.Command)
		}
	}
}
