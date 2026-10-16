package main

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const (
	monitorID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"
	neighbour = "0c1d2e3f-4a5b-4c6d-8e7f-8091a2b3c4d5"
	strayID   = "dead0000-1111-4222-8333-444455556666"
)

// listedHost is a host that answers its registry, `true` and the
// deployments probe, and fails anything else, which no command here may run.
type listedHost struct {
	t        *testing.T
	registry string
	probe    string
	// records are the admission records, by path, the records listing
	// answers with.
	records  map[string]string
	commands []string
	// readErr fails the registry's read, and probeErr the probe, which
	// then answers probe as its output.
	readErr, probeErr error
	registryRead      bool
}

func (h *listedHost) Describe() string { return "admin@192.0.2.30" }
func (h *listedHost) ReadFile(path string) (string, bool, error) {
	if path == registry.Path {
		h.registryRead = true
		return h.registry, h.registry != "", h.readErr
	}
	return "", false, nil
}
func (h *listedHost) WriteFile(string, string, uint32) error {
	h.t.Error("a file was written")
	return nil
}
func (h *listedHost) RunInput(command, _ string) (string, error) { return h.Run(command) }
func (h *listedHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	switch {
	case command == "true":
		return "", nil
	case command == deploymentsProbe:
		return h.probe, h.probeErr
	case command == hostDeploymentsAdmitted:
		var b strings.Builder
		paths := make([]string, 0, len(h.records))
		for p := range h.records {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			b.WriteString("record " + p + "\n" + h.records[p] + "\n")
		}
		b.WriteString("end\n")
		return b.String(), nil
	}
	h.t.Errorf("unexpected command %q", command)
	return "", nil
}

func twoDeployments(t *testing.T) string {
	data, err := registry.Encode(registry.Registry{Version: registry.Version, Deployments: map[string]registry.Entry{
		monitorID: {Token: "f2a9", Root: "/srv/paisans/f2a9", Domain: "example.org", Site: "watch", Roles: "monitor", ClaimedAt: "2026-10-01T00:00:00Z", Interface: "psns-f2a9", Subnet: "10.44.0.0/24", Address: "10.44.0.6"},
		neighbour: {Token: "0c1d", Root: "/srv/paisans/0c1d", Domain: "example.net", Site: "edge", Roles: "apps,gateway", ClaimedAt: "2026-10-01T00:00:00Z", Interface: "psns-0c1d", Subnet: "10.45.0.0/24", Address: "10.45.0.2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// withHost stands h in for every host a command without a configuration
// reaches: as its owner, as host deployments does, or as the deployment's
// user, as site remove --id does.
func withHost(t *testing.T, h apply.Transport) {
	t.Helper()
	saved := reachOwner
	reachOwner = func(config.Destination, bool) apply.Transport { return h }
	t.Cleanup(func() { reachOwner = saved })
	savedDest := reachDestination
	reachDestination = func(config.Destination) apply.Transport { return h }
	t.Cleanup(func() { reachDestination = savedDest })
}

func plainOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	reporterOverride = ui.NewPlain(&b, false)
	t.Cleanup(func() { reporterOverride = nil })
	return &b
}

// admittedRecords is the records listing's records: the monitor
// deployment's, admitted as vm, a gateway, with public ports.
func admittedRecords(t *testing.T) map[string]string {
	t.Helper()
	rec := admission.Record{
		Version: admission.RecordVersion, Deployment: monitorID, User: "paisans-f2a9", Site: "vm", Roles: []string{"gateway"},
		Grants: admission.Grants{PublicPorts: true}, Ports: admission.Ports{SSH: 22, WireGuard: 51820, Public: []int{80, 443}},
		AdmittedAt: "2026-10-15T00:00:00Z", MeshSubnet: "10.44.0.0/24",
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := admission.Path("f2a9")
	return map[string]string{p: string(b)}
}

// runDeployments runs host deployments against h and returns what it printed.
func runDeployments(t *testing.T, h *listedHost, args ...string) string {
	t.Helper()
	withHost(t, h)
	out := plainOutput(t)
	if err := runHostDeployments(args); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The owner's listing names every admission record beside the registry:
// the user, the site, the grants and when it was admitted.
func TestHostDeploymentsListsAdmissionRecords(t *testing.T) {
	h := &listedHost{t: t, registry: twoDeployments(t), records: admittedRecords(t), probe: "end\n"}
	out := runDeployments(t, h, "--ssh", "owner@vm.example.org")
	for _, want := range []string{monitorID + ": admitted: paisans-f2a9 as vm (gateway), watchdog no, public ports yes, at 2026-10-15T00:00:00Z", "token 0c1d"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

// Without sudo, only the records are read: they are 644 in a 755 directory.
func TestHostDeploymentsWithoutSudoListsOnlyRecords(t *testing.T) {
	h := &listedHost{t: t, registry: twoDeployments(t), records: admittedRecords(t), probe: "end\n"}
	out := runDeployments(t, h, "--ssh", "owner@vm.example.org", "--sudo=false")
	if !strings.Contains(out, "admitted: paisans-f2a9") || strings.Contains(out, "token 0c1d") {
		t.Errorf("not the records alone:\n%s", out)
	}
	for _, c := range h.commands {
		if strings.Contains(c, "registry") || strings.Contains(c, deployment.Base) {
			t.Errorf("read %q without sudo", c)
		}
	}
	if h.registryRead {
		t.Error("read the registry without sudo")
	}
}

// A record that does not parse is a warning naming the file and why, and
// the rest is still listed.
func TestHostDeploymentsWarnsOfARecordThatDoesNotParse(t *testing.T) {
	records := admittedRecords(t)
	records["/etc/paisans/admitted.0c1d.json"] = "{}"
	out := runDeployments(t, &listedHost{t: t, registry: twoDeployments(t), records: records, probe: "end\n"}, "--ssh", "owner@vm.example.org")
	if !strings.Contains(out, "WARN /etc/paisans/admitted.0c1d.json:") || !strings.Contains(out, "admitted: paisans-f2a9") {
		t.Errorf("printed:\n%s", out)
	}
}

// host deployments lists each registry entry in full, then each deployment
// directory with no entry, and changes nothing.
func TestHostDeploymentsListsEntriesAndLeftovers(t *testing.T) {
	h := &listedHost{t: t, registry: twoDeployments(t), probe: "root f2a9\nroot 0c1d\nroot dead\nend\n"}
	got := runDeployments(t, h, "--ssh", "admin@192.0.2.30")
	// The host is read in a step, so a spinner shows while ssh answers.
	if !strings.Contains(got, "ok   read admin@192.0.2.30:22") {
		t.Errorf("the read is not a step:\n%s", got)
	}
	for _, want := range []string{
		monitorID, "token f2a9", "example.org", "site watch", "roles monitor", "root /srv/paisans/f2a9",
		neighbour, "token 0c1d", "example.net", "site edge", "roles apps,gateway", "root /srv/paisans/0c1d",
		"  ok   " + monitorID + ": token f2a9",
		"  ok   " + neighbour + ": token 0c1d",
		"  WARN /srv/paisans/dead: no registry entry",
		"--id <id or token>",
		"no admission record in /etc/paisans",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/srv/paisans/f2a9: no registry entry") {
		t.Errorf("a registered deployment is listed as a leftover:\n%s", got)
	}
}

func TestHostDeploymentsOnAnEmptyHost(t *testing.T) {
	out := runDeployments(t, &listedHost{t: t, probe: "end\n"}, "--ssh", "admin@192.0.2.30")
	if !strings.Contains(out, "No deployment") || !strings.Contains(out, "no admission record") {
		t.Errorf("printed:\n%s", out)
	}
	out = runDeployments(t, &listedHost{t: t, probe: "end\n"}, "--ssh", "admin@192.0.2.30", "--sudo=false")
	if !strings.Contains(out, "0 admission record(s)") {
		t.Errorf("--sudo=false printed:\n%s", out)
	}
}

func TestHostDeploymentsRefusals(t *testing.T) {
	noSiteHosts(t)
	for args, want := range map[string]string{
		"":                         "--ssh",
		"--ssh myalias":            "not user@host",
		"--ssh admin@192.0.2.30 x": "extra",
	} {
		err := runHostDeployments(strings.Fields(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", args, err, want)
		}
	}
}

// --id needs no configuration, and every refusal that needs no host reaches
// none.
func TestSiteRemoveByIDRefusesBeforeReachingAHost(t *testing.T) {
	noSiteHosts(t)
	for args, want := range map[string]string{
		"--ssh admin@192.0.2.30 --id f2a9": "add --force",
		"--force --id f2a9":                "--ssh",
		"--force --ssh admin@192.0.2.30 --id f2a9 --config paisans-stack.yaml": "--config",
		"--force --ssh admin@192.0.2.30 --id f2a9 --secrets s.enc.yaml":        "--secrets",
		"watch --force --ssh admin@192.0.2.30 --id f2a9":                       "names the site",
		"--force --ssh admin@192.0.2.30 --id f2a":                              "Eg: --id f2a9",
		"--force --ssh admin@192.0.2.30 --id F2A9":                             "Eg: --id f2a9",
		"--force --ssh myalias --id f2a9":                                      "not user@host",
		"--force --ssh admin@192.0.2.30 --id f2a9 --host-gone":                 "Drop one of them",
		"--force --ssh admin@192.0.2.30 --id f2a9 --execute":                   "terminal",
		"--force --ssh admin@192.0.2.30 --id f2a9 --execute --delete-data":     "terminal",
	} {
		err := runSiteRemove(strings.Fields(args), strings.NewReader("watch\n"), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
		// Each is a short hint and an explanation, as main prints it.
		var p *ui.Problem
		if !errors.As(err, &p) || len(p.Hint) > 80 || p.Explain == "" || strings.HasPrefix(p.Hint, "site remove") {
			t.Errorf("%s: not the Problem main prints: %#v", args, p)
		}
	}
}

// --id naming another deployment than the one the login's admission record
// names is refused naming that one, and nothing past the record is read.
func TestSiteRemoveByIDRefusesAnotherDeployment(t *testing.T) {
	h := &recordingIDHost{idHost: idHost{runningHost{failingHost{match: "\x00"}}, twoDeployments(t)}}
	withHost(t, h)
	plainOutput(t)
	err := runSiteRemove([]string{"--force", "--ssh", "ubuntu@192.0.2.30", "--id", "0c1d"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "this login's admission record on ubuntu@192.0.2.30:22 is for deployment "+monitorID+" (watch)") {
		t.Errorf("err = %v", err)
	}
	for _, c := range h.commands {
		if c != "true" && !strings.Contains(c, "$(id -un)") {
			t.Errorf("ran %q", c)
		}
	}
}

// recordingIDHost is an idHost that records the commands it runs.
type recordingIDHost struct {
	idHost
	commands []string
}

func (h *recordingIDHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	return h.idHost.Run(command)
}

// A host whose registry or probe cannot be read is an error naming what
// failed, and nothing is listed: an empty or partial listing would read as a
// host holding nothing.
func TestHostDeploymentsFailsRatherThanListingPartly(t *testing.T) {
	good := "root f2a9\nend\n"
	for name, tc := range map[string]struct {
		h    *listedHost
		want string
	}{
		"registry unreadable":    {&listedHost{readErr: errors.New("exit status 1: Permission denied"), probe: good}, registry.Path},
		"registry malformed":     {&listedHost{registry: "not json", probe: good}, registry.Path},
		"registry dir hidden":    {&listedHost{probe: "cannot read /var/lib/paisans\n", probeErr: errors.New("exit status 3")}, "cannot read /var/lib/paisans"},
		"deployments dir hidden": {&listedHost{probe: "cannot read /srv/paisans\n", probeErr: errors.New("exit status 3")}, "cannot read /srv/paisans"},
		"cut short":              {&listedHost{probe: "root f2a9\n"}, "cut short"},
		"unreadable line":        {&listedHost{probe: "root f2a9\nsomething else\nend\n"}, "something else"},
	} {
		tc.h.t = t
		if tc.h.registry == "" && tc.h.readErr == nil {
			tc.h.registry = twoDeployments(t)
		}
		withHost(t, tc.h)
		out := plainOutput(t)
		err := runHostDeployments([]string{"--ssh", "admin@192.0.2.30"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		// The read is a step, so its failed line shows; nothing is listed.
		if strings.Contains(out.String(), "token ") || strings.Contains(out.String(), "no registry entry") {
			t.Errorf("%s: printed a listing:\n%s", name, out.String())
		}
	}
}

// A host named only by --ssh is reached the way the operator's own ssh
// reaches it, with their agent, keys and config: there are no declared keys
// to offer, since there is no configuration. A deployment command reaches it
// as the deployment's user, without sudo; host deployments reaches it as its
// owner, through sudo.
func TestReachDestinationUsesTheOperatorsSSH(t *testing.T) {
	for in, want := range map[string]string{
		"ubuntu@192.0.2.10":         "ubuntu@192.0.2.10",
		"ubuntu@192.0.2.10:2222":    "ssh://ubuntu@192.0.2.10:2222",
		"ubuntu@[2001:db8::1]:2222": "ssh://ubuntu@[2001:db8::1]:2222",
	} {
		dest, err := config.ParseDestination(in)
		if err != nil {
			t.Fatal(err)
		}
		tr, ok := reachDestination(dest).(apply.SSHTransport)
		if !ok || tr.Destination != want || tr.Sudo || tr.Auth != nil {
			t.Errorf("%s: %+v, want destination %s without sudo", in, tr, want)
		}
		owner := ownerTransport(dest, true)
		if owner.Destination != want || !owner.Sudo || owner.Auth == nil {
			t.Errorf("%s: owner %+v, want destination %s with sudo", in, owner, want)
		}
		if tr.Login == nil {
			t.Errorf("%s: a deployment command's transport has no Login", in)
		}
		if owner.Login != nil {
			t.Errorf("%s: the owner's transport points docker at a user's daemon", in)
		}
		if plain := ownerTransport(dest, false); plain.Sudo || plain.Auth != nil {
			t.Errorf("%s: owner with --sudo=false %+v", in, plain)
		}
	}
}
