package hostadmit_test

import (
	"io"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
	// With no record and no --data-dir it warns that a data directory
	// outside the home cannot be found, and plans nothing.
	if len(plan.Steps) != 0 || len(plan.Warnings) != 1 {
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
	if len(plan.Steps) != 0 || len(plan.Warnings) != 2 {
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
		kill := stepIndex(plan, "pkill -KILL -U 1001")
		if kill < 0 || stepIndex(plan, "pgrep -U 1001") != kill {
			t.Errorf("%s: no step kills and checks uid 1001's processes:\n%s", c.name, plan.Text())
			continue
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
