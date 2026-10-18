package hostprep_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// fakeHost answers each probe by a substring that only that probe's script
// contains, and keeps files in a map. Nothing here runs a shell: what is under
// test is what the planner does with what a host says, which is where every
// decision in this package is made.
type fakeHost struct {
	files     map[string]string
	responses map[string]string // keyed by a substring of the command
	failures  map[string]error
	ran       []string
	reads     []string
	written   []string
}

func (h *fakeHost) Describe() string { return "fake" }

func (h *fakeHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	for key, err := range h.failures {
		if strings.Contains(command, key) {
			return "", err
		}
	}
	for key, out := range h.responses {
		if strings.Contains(command, key) {
			return out, nil
		}
	}
	return "", nil
}

func (h *fakeHost) ReadFile(path string) (string, bool, error) {
	h.reads = append(h.reads, path)
	c, ok := h.files[path]
	return c, ok, nil
}

func (h *fakeHost) WriteFile(path, content string, mode uint32) error {
	h.written = append(h.written, path)
	h.files[path] = content
	return nil
}

// Probe keys: each is a string only one probe script contains.
const (
	probePackages = "docker compose version"
	probeWatchdog = "RuntimeWatchdogUSec"
	probePasswd   = "getent passwd"
)

// The fixture's sites all log in as ubuntu with alice's key. These are
// obviously fake ed25519 keys: 32 bytes of zeros, ones, twos and threes.
const (
	keyAlice = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	keyBob   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
	keyCarol = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC carol@example.org"
	keyCloud = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMD cloud-init"

	passwdUbuntu   = "ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash\n"
	authorizedKeys = "/home/ubuntu/.ssh/authorized_keys"
	ownedKeys      = "/srv/paisans/f2a9/.paisans/authorized_keys.owned"
)

// keysPrepared is the user's key state after a prepare: alice's key in
// authorized_keys and in the sidecar.
func keysPrepared(h *fakeHost) *fakeHost {
	h.responses[probePasswd] = passwdUbuntu
	h.files[authorizedKeys] = keyAlice + "\n"
	h.files[ownedKeys] = "# header\nadded " + fingerprint(keyAlice) + " alice\n"
	return h
}

func fingerprint(line string) string {
	k, _, err := config.ParseKeyLine(line)
	if err != nil {
		panic(err)
	}
	return k.Fingerprint
}

const ubuntu2404 = `PRETTY_NAME="Ubuntu 24.04.1 LTS"

NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
UBUNTU_CODENAME=noble
`

// freshHost is an Ubuntu 24.04 install its owner has admitted and nothing
// of the deployment user's is on yet: to site admit's package probe it has no
// Docker, WireGuard tools, ufw or git; to site prepare, softdog is loaded and
// /dev/watchdog is the user's, and the user's Docker runs no container. Its
// login user's keys are already as prepared, so that tests of everything else
// see no key steps; the key tests set them up themselves.
func freshHost() *fakeHost {
	return keysPrepared(&fakeHost{
		files: map[string]string{"/etc/os-release": ubuntu2404},
		responses: map[string]string{
			probePackages:  "compose absent\npkg docker-ce absent\npkg wireguard-tools absent\npkg ufw absent\npkg git absent\nkeyring absent\narch amd64\n",
			probeWatchdog:  "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid paisans-f2a9:165536:65536\n",
			"docker ps -q": "",
		},
	})
}

// preparedHost is what an admitted host looks like after prepare ran on it
// with a hardware watchdog present, for home-a or, as the gateway, vm.
func preparedHost(gateway bool) *fakeHost {
	site := "home-a"
	if gateway {
		site = "vm"
	}
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		panic(err)
	}
	daemon, err := hostprep.DaemonJSON(cfg.Mesh.Subnet, "")
	if err != nil {
		panic(err)
	}
	return configStored(site, keysPrepared(&fakeHost{
		files: map[string]string{
			"/etc/os-release": ubuntu2404,
			daemonJSONPath:    daemon,
		},
		responses: map[string]string{
			probeWatchdog:  "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid paisans-f2a9:165536:65536\n",
			"docker ps -q": "3f2a9c1d0b7e\n9b1c4e2f8a6d\n",
		},
	}))
}

// configStored gives a host the configuration store site prepare leaves on
// it for site: the repository, the files, and the units running.
func configStored(site string, h *fakeHost) *fakeHost {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		panic(err)
	}
	profile, err := hostprep.Select(hostprep.ParseOSRelease(ubuntu2404))
	if err != nil {
		panic(err)
	}
	var peers []string
	for _, name := range cfg.SiteNames() {
		if name != site {
			peers = append(peers, cfg.Sites[name].Address)
		}
	}
	section, err := profile.ConfigStore(&fakeHost{files: map[string]string{}, responses: map[string]string{}}, cfg.Deployment(), cfg.Sites[site], peers, "/home/ubuntu")
	if err != nil {
		panic(err)
	}
	for _, s := range section.Steps {
		if s.File != nil {
			h.files[s.File.Path] = s.File.Content
		}
	}
	h.files["/srv/paisans/f2a9/.paisans/config/f2a9.git/HEAD"] = "ref: refs/heads/main\n"
	h.responses["is-enabled paisans-f2a9-config-daemon.service"] = "enabled enabled\nactive active\n"
	h.responses["is-enabled paisans-f2a9-config-sync.timer"] = "enabled enabled\nactive active\n"
	return h
}

// fixture is the render fixture with every site logging in with alice's key
// alone: the fixture declares a second operator, bob, so validate is quiet,
// and these tests add bob's key themselves where they are about one.
func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatalf("loading the fixture configuration: %v", err)
	}
	for name, s := range cfg.Sites {
		s.SSH.Keys = []config.NamedKey{{Name: "alice", Line: keyAlice}}
		cfg.Sites[name] = s
	}
	return cfg
}

func withWatchdog(t *testing.T, site string, mode config.WatchdogMode) *config.Config {
	t.Helper()
	cfg := fixture(t)
	s := cfg.Sites[site]
	s.Watchdog = mode
	cfg.Sites[site] = s
	return cfg
}

func describes(plan *hostprep.Plan) string {
	var out []string
	for _, s := range plan.Steps {
		out = append(out, s.Describe)
	}
	return strings.Join(out, "\n")
}

// printed is everything Show reports with --verbose, one event per line.
func printed(plan *hostprep.Plan) string {
	rec := &ui.Recorder{Verbose_: true}
	plan.Show(rec)
	return rec.Lines()
}

// The wording is the founder's. It names what the host is, then lists what it
// could have been, one per line, so the answer to "what do I install" is on
// the screen.
func TestAnUnsupportedHostIsNamedWithWhatIsSupported(t *testing.T) {
	host := freshHost()
	host.files["/etc/os-release"] = "ID=debian\nVERSION_ID=\"12\"\n"
	_, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	want := "debian 12 is not supported. Use one of the following:\nubuntu 22.04\nubuntu 24.04\nubuntu 26.04"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want exactly:\n%s", err, want)
	}
	if len(host.ran) != 0 {
		t.Errorf("nothing should run on a host no profile matches, ran %d command(s)", len(host.ran))
	}
}

// Another Ubuntu release is not one of these. Docker's apt source names the
// codename, and a profile matched on ID alone would accept a release nobody
// has checked admit and prepare against.
func TestAnotherUbuntuReleaseIsNotSupported(t *testing.T) {
	host := freshHost()
	host.files["/etc/os-release"] = strings.ReplaceAll(strings.ReplaceAll(ubuntu2404, "24.04", "20.04"), "noble", "focal")
	_, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err == nil || !strings.HasPrefix(err.Error(), "ubuntu 20.04 is not supported. Use one of the following:\n") {
		t.Fatalf("got %v", err)
	}
}

const ubuntu2204 = `PRETTY_NAME="Ubuntu 22.04.5 LTS"
NAME="Ubuntu"
VERSION_ID="22.04"
VERSION="22.04.5 LTS (Jammy Jellyfish)"
VERSION_CODENAME=jammy
ID=ubuntu
ID_LIKE=debian
UBUNTU_CODENAME=jammy
`

const ubuntu2604 = `PRETTY_NAME="Ubuntu 26.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="26.04"
VERSION="26.04.1 LTS (Resolute Raccoon)"
VERSION_CODENAME=resolute
ID=ubuntu
ID_LIKE=debian
UBUNTU_CODENAME=resolute
`

// Ubuntu 22.04 and 26.04 are prepared as 24.04 is: each one's profile is
// selected, and site prepare plans the same user's own steps and installs
// nothing. Docker's jammy and resolute repositories are site admit's
// (internal/hostadmit's jammy and resolute goldens).
func TestOtherUbuntuReleasesArePreparedAs2404Is(t *testing.T) {
	noble, err := hostprep.Build("home-a", fixture(t), freshHost(), admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	for version, release := range map[string]string{"22.04": ubuntu2204, "26.04": ubuntu2604} {
		host := freshHost()
		host.files["/etc/os-release"] = release
		plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Profile != "ubuntu "+version {
			t.Errorf("profile is %q", plan.Profile)
		}
		if got, want := describes(plan), describes(noble); got != want {
			t.Errorf("%s plans:\n%s\n24.04 plans:\n%s", version, got, want)
		}
	}
}

// A site prepare already prepared plans nothing, on a data site and on a
// gateway, and says what it found.
func TestAPreparedHostPlansNothing(t *testing.T) {
	for _, tc := range []struct {
		site    string
		gateway bool
	}{{"home-a", false}, {"vm", true}} {
		t.Run(tc.site, func(t *testing.T) {
			plan, err := hostprep.Build(tc.site, fixture(t), preparedHost(tc.gateway), admitted(t, fixture(t), tc.site))
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Steps) != 0 || len(plan.Warnings) != 0 {
				t.Fatalf("a prepared host planned:\n%s", printed(plan))
			}
			if len(plan.Present) == 0 {
				t.Fatal("a plan with nothing to do should still say what it found")
			}
		})
	}
}

// Running Execute writes the files and runs the commands it planned, in order.
func TestExecuteRunsEveryStepInOrder(t *testing.T) {
	host := freshHost()
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	host.ran = nil
	if err := hostprep.Execute(plan, host, ui.Discard); err != nil {
		t.Fatal(err)
	}
	if len(host.written) != 5 {
		t.Errorf("want the five files of the configuration store written, got %v", host.written)
	}
	if len(host.ran) == 0 || !strings.Contains(host.ran[0], daemonJSONPath) {
		t.Errorf("writing daemon.json is the first thing run, got %q", host.ran)
	}
}

// Each step is reported under its short title, ended as done, with the full
// sentence as its detail, so a run reads as a list of verbs and a failure
// leaves the line that failed marked.
func TestExecuteReportsEachStepByItsTitle(t *testing.T) {
	host := freshHost()
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{Verbose_: true}
	if err := hostprep.Execute(plan, host, rec); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"write daemon.json", "create the configuration repository", "start the configuration store"} {
		if !rec.Has("done", title) {
			t.Errorf("no finished step %q:\n%s", title, rec.Lines())
		}
	}
	if !rec.Has("detail", "docker: write "+daemonJSONPath) {
		t.Errorf("a step's sentence is its detail:\n%s", rec.Lines())
	}
}

// A title is a verb and its object, short enough to share a line with a
// result; the sentence behind it is for --verbose.
func TestEveryStepHasAShortTitle(t *testing.T) {
	plan, err := hostprep.Build("home-a", fixture(t), freshHost(), admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.Title == "" || len(s.Title) > 40 {
			t.Errorf("step %q has title %q", s.Describe, s.Title)
		}
	}
}

// A dry run marks each step still to run as pending, with its sentence for
// --verbose, and under --verbose also marks what was already there as done,
// the way apply's dry run marks its steps.
func TestShowMarksStepsPendingAndFindingsDone(t *testing.T) {
	host := preparedHost(false)
	delete(host.files, daemonJSONPath)
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}

	rec := &ui.Recorder{}
	plan.Show(rec)
	if !rec.Has("pending", "write daemon.json") || !rec.Has("warn", "reads its new daemon.json at its next restart") {
		t.Errorf("got:\n%s", rec.Lines())
	}
	if rec.Has("done", "") {
		t.Errorf("a finding shows without --verbose:\n%s", rec.Lines())
	}

	verbose := &ui.Recorder{Verbose_: true}
	plan.Show(verbose)
	if !verbose.Has("done", "watchdog: watchdog0 is iTCO_wdt, the deployment's user's") || !verbose.Has("detail", "docker: write "+daemonJSONPath) {
		t.Errorf("got:\n%s", verbose.Lines())
	}

	// Plain output carries the writer's own words and no label column.
	var b strings.Builder
	plan.Show(ui.NewPlain(&b, true))
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"  todo write daemon.json\n",
		"      docker: write " + daemonJSONPath + ": userland proxy off, address pools clear of the mesh\n",
		"  ok   watchdog: watchdog0 is iTCO_wdt, the deployment's user's\n",
		"  WARN docker: the deployment's Docker reads its new daemon.json at its next restart\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, label := range []string{"change ", "present ", "adopt ", "add ", "remove "} {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), label) {
				t.Errorf("a padded label: %q", line)
			}
		}
	}
}

// A step a fresh host needs is pending, and a warning is still a warning.
func TestAFreshHostMarksItsStepsPending(t *testing.T) {
	plan, err := hostprep.Build("home-a", fixture(t), freshHost(), admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	plan.Show(rec)
	if !rec.Has("pending", "write daemon.json") || !rec.Has("warn", "watchdog: softdog") {
		t.Errorf("got:\n%s", rec.Lines())
	}
}

// Execute stops at the first failure and names the step.
func TestExecuteStopsAtTheFirstFailure(t *testing.T) {
	host := freshHost()
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	host.failures = map[string]error{"base64 -d": errors.New("exit status 1")}
	host.ran = nil
	err = hostprep.Execute(plan, host, ui.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "docker: write "+daemonJSONPath) {
		t.Fatalf("got %v", err)
	}
	if len(host.ran) != 1 || len(host.written) != 0 {
		t.Fatalf("ran %q and wrote %q after a failed step", host.ran, host.written)
	}
}

// The watchdog is read, never planned: each mode accepts what admit left or
// refuses with what the host's owner runs.
func TestWatchdogModes(t *testing.T) {
	const mine = "owner 1000\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	hardware := "device present\nidentity watchdog0 sp5100_tco\nsystemd 0\ndaemon absent\n" + mine
	softdog := "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\n" + mine
	none := "device absent\nsystemd 0\ndaemon absent\nowner \nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	roots := "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 0\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	// Spilo's start hands the device to its postgres user, UID 101 in the
	// container, which is 165536+100 on the host.
	containers := "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 165636\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	beyond := "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 231072\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	noRange := "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid \n"

	cases := []struct {
		name    string
		mode    config.WatchdogMode
		probe   string
		refuse  string // substring of the error, when refused
		present string
		warn    bool
	}{
		{name: "auto uses hardware", mode: config.WatchdogAuto, probe: hardware, present: "watchdog0 is sp5100_tco"},
		{name: "auto accepts softdog with a warning", mode: config.WatchdogAuto, probe: softdog, present: "Software Watchdog", warn: true},
		{name: "required accepts hardware", mode: config.WatchdogRequired, probe: hardware, present: "sp5100_tco"},
		{name: "required refuses softdog", mode: config.WatchdogRequired, probe: softdog, refuse: "has only softdog"},
		{name: "softdog accepts softdog without a warning", mode: config.WatchdogSoftdog, probe: softdog, present: "Software Watchdog"},
		{name: "softdog warns of hardware", mode: config.WatchdogSoftdog, probe: hardware, present: "sp5100_tco", warn: true},
		{name: "no device sends the owner to admit", mode: config.WatchdogAuto, probe: none, refuse: "sudo paisans site admit --request home-a.admit --watchdog again, from paisans site request --site home-a > home-a.admit"},
		{name: "a root device sends the owner to admit", mode: config.WatchdogAuto, probe: roots, refuse: "is not the deployment's user's"},
		{name: "a device the deployment's container took is the deployment's", mode: config.WatchdogAuto, probe: containers, present: "Software Watchdog", warn: true},
		{name: "a device past the user's subordinate range sends the owner to admit", mode: config.WatchdogAuto, probe: beyond, refuse: "is not the deployment's user's"},
		{name: "the user's own device needs no subordinate range", mode: config.WatchdogAuto, probe: noRange, present: "Software Watchdog", warn: true},
		{name: "systemd holds the device", mode: config.WatchdogAuto, probe: "device present\nidentity watchdog0 iTCO_wdt\nsystemd 1min\ndaemon absent\n" + mine, refuse: "RuntimeWatchdogUSec is 1min"},
		{name: "a daemon holds the device", mode: config.WatchdogAuto, probe: "device present\nidentity watchdog0 iTCO_wdt\nsystemd 0\ndaemon running\n" + mine, refuse: "a watchdog daemon is running"},
		{name: "contention refuses required too", mode: config.WatchdogRequired, probe: "device present\nidentity watchdog0 iTCO_wdt\nsystemd 30s\ndaemon absent\n" + mine, refuse: "RuntimeWatchdogUSec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := preparedHost(false)
			host.responses[probeWatchdog] = tc.probe
			cfg := withWatchdog(t, "home-a", tc.mode)
			plan, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a"))
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want a refusal containing %q, got %v", tc.refuse, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := describes(plan); got != "" {
				t.Errorf("want no steps, got:\n%s", got)
			}
			if tc.present != "" && !strings.Contains(strings.Join(plan.Present, "\n"), tc.present) {
				t.Errorf("want %q reported present, got:\n%s", tc.present, strings.Join(plan.Present, "\n"))
			}
			if (len(plan.Warnings) > 0) != tc.warn {
				t.Errorf("warnings: %v, want warning %v", plan.Warnings, tc.warn)
			}
		})
	}
}

func withKeys(t *testing.T, site string, keys ...string) *config.Config {
	t.Helper()
	cfg := fixture(t)
	s := cfg.Sites[site]
	s.SSH.Keys = nil
	for _, k := range keys {
		name, _, _ := strings.Cut(strings.Fields(k)[2], "@")
		s.SSH.Keys = append(s.SSH.Keys, config.NamedKey{Name: name, Line: k})
	}
	cfg.Sites[site] = s
	return cfg
}

// unlocked is a command run under the deployment's lock, without the lock,
// failing the test when the lock is missing.
func unlocked(t *testing.T, command string) string {
	t.Helper()
	prefix := deployment.Deployment{ID: "f2a9"}.StateLockCommand("the keys") + "; "
	inner, ok := strings.CutPrefix(command, prefix)
	if !ok {
		t.Fatalf("not run under the deployment's lock: %s", command)
	}
	return inner
}

func stepsLabelled(plan *hostprep.Plan, label string) []hostprep.Step {
	var out []hostprep.Step
	for _, s := range plan.Steps {
		if s.Label == label {
			out = append(out, s)
		}
	}
	return out
}

// A user with no authorized_keys gets the directory and file, created as
// that user, and then every listed key, recorded in the sidecar.
func TestAUserWithoutAuthorizedKeysGetsThem(t *testing.T) {
	host := preparedHost(false)
	delete(host.files, authorizedKeys)
	delete(host.files, ownedKeys)
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	t.Logf("\n%s", out)
	if len(plan.Steps) != 3 {
		t.Fatalf("want create, add, add; got:\n%s", out)
	}
	create := plan.Steps[0].Command
	for _, want := range []string{"umask 077", "install -d -m 700 '/home/ubuntu/.ssh'", "install -m 600 /dev/null '/home/ubuntu/.ssh/authorized_keys'", "[ -d '/home/ubuntu/.ssh' ] ||", "[ -f '/home/ubuntu/.ssh/authorized_keys' ] ||"} {
		if !strings.Contains(create, want) {
			t.Errorf("create lacks %q: %s", want, create)
		}
	}
	for _, want := range []string{
		"detail: ssh: authorize key " + fingerprint(keyAlice) + " (alice) for the deployment's user authorize ssh key",
		"detail: ssh: authorize key " + fingerprint(keyBob) + " (bob) for the deployment's user authorize ssh key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// The line is appended verbatim, and the sidecar written after it holds
	// both fingerprints once the second key is in.
	last := unlocked(t, plan.Steps[2].Command)
	if !strings.Contains(last, "printf '%s\\n' '"+keyBob+"' >> '/home/ubuntu/.ssh/authorized_keys'") {
		t.Errorf("bob's key is not appended verbatim: %s", last)
	}
	if !strings.Contains(last, "added "+fingerprint(keyAlice)+" alice\nadded "+fingerprint(keyBob)+" bob") &&
		!strings.Contains(last, "added "+fingerprint(keyBob)+" bob\nadded "+fingerprint(keyAlice)+" alice") {
		t.Errorf("the sidecar after the last add does not hold both keys: %s", last)
	}
	if !strings.Contains(last, "chmod 600 '/srv/paisans/f2a9/.paisans/authorized_keys.owned.paisans-tmp' && mv") {
		t.Errorf("the sidecar is not written 0600 and renamed into place: %s", last)
	}
}

// site prepare adds every key ssh.keys lists, whatever a file at
// /etc/paisans/ssh-allow.<user> on the host holds: it reads no such file,
// and no step it plans mentions one.
func TestKeysAreAddedWhateverAnSSHAllowFileHolds(t *testing.T) {
	host := preparedHost(false)
	host.files["/etc/paisans/ssh-allow.ubuntu"] = "# allows nothing\n"
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	add := stepsLabelled(plan, "add")
	if len(add) != 1 || !strings.Contains(add[0].Describe, fingerprint(keyBob)+" (bob)") {
		t.Fatalf("want bob's key added, got:\n%s", printed(plan))
	}
	for _, s := range plan.Steps {
		if strings.Contains(s.Command, "ssh-allow") {
			t.Errorf("%s consults an allowlist: %s", s.Title, s.Command)
		}
	}
	if out := printed(plan); strings.Contains(out, "allowlist") {
		t.Errorf("the plan mentions an allowlist:\n%s", out)
	}
}

// A listed key cloud-init already put there is reported, neither added
// again nor recorded, and a key nobody listed and site prepare never added is
// not touched or mentioned.
func TestAnExistingListedKeyIsLeftUnrecorded(t *testing.T) {
	host := preparedHost(false)
	host.files[authorizedKeys] = keyCloud + "\n" + keyAlice
	delete(host.files, ownedKeys)
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	t.Logf("\n%s", out)
	add := stepsLabelled(plan, "add")
	if len(add) != 1 || len(plan.Steps) != 1 {
		t.Fatalf("want one add:\n%s", out)
	}
	if !strings.Contains(out, "done: ssh: key "+fingerprint(keyAlice)+" (alice) authorized for the deployment's user, not by site prepare, which never removes it") {
		t.Errorf("alice's key reads:\n%s", out)
	}
	addCmd := unlocked(t, add[0].Command)
	if strings.Contains(addCmd, fingerprint(keyAlice)) {
		t.Errorf("the sidecar records a key site prepare did not write: %s", addCmd)
	}
	// The file's last line has no newline; the add ends it first.
	if !strings.Contains(addCmd, `[ -n "$(tail -c1 '/home/ubuntu/.ssh/authorized_keys')" ]; then echo >>`) {
		t.Errorf("the add can glue a key onto an unterminated line: %s", addCmd)
	}
	if strings.Contains(out, fingerprint(keyCloud)) || strings.Contains(out, "cloud-init") {
		t.Errorf("an unlisted, unowned key was mentioned:\n%s", out)
	}
}

// A key that was already authorized is never removed once unlisted, and
// neither is one a sidecar line without the added mark names.
func TestAKeyHostPrepareDidNotWriteIsNeverRemoved(t *testing.T) {
	host := preparedHost(false)
	host.files[authorizedKeys] = keyAlice + "\n" + keyCarol + "\n"
	host.files[ownedKeys] = "added " + fingerprint(keyAlice) + " alice\n" + fingerprint(keyCarol) + " carol\n"
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice), host, admitted(t, withKeys(t, "home-a", keyAlice), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	if strings.Contains(out, fingerprint(keyCarol)) || len(stepsLabelled(plan, "remove")) != 0 {
		t.Errorf("carol's key is planned for:\n%s", out)
	}
}

// A key site prepare added and the configuration no longer lists is removed,
// last, by its exact line; one it never added is not, listed or not.
func TestAnUnlistedOwnedKeyIsRemovedLast(t *testing.T) {
	host := preparedHost(false)
	host.files[authorizedKeys] = keyCloud + "\n" + keyAlice + "\n" + keyCarol + "  \n"
	host.files[ownedKeys] = "added " + fingerprint(keyAlice) + " alice\nadded " + fingerprint(keyCarol) + " carol\n"
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	t.Logf("\n%s", out)
	last := plan.Steps[len(plan.Steps)-1]
	if last.Label != "remove" || !strings.Contains(last.Describe, "ssh: remove key "+fingerprint(keyCarol)+" (carol) from /home/ubuntu/.ssh/authorized_keys, which site prepare or site admit added and ssh.keys no longer lists") {
		t.Fatalf("the last step is not carol's removal:\n%s", out)
	}
	// The exact line, trailing spaces and all, so grep -x matches it.
	lastCmd := unlocked(t, last.Command)
	if !strings.Contains(lastCmd, "grep -vxF -e '"+keyCarol+"  ' '/home/ubuntu/.ssh/authorized_keys'") {
		t.Errorf("the removal does not name carol's exact line: %s", lastCmd)
	}
	if !strings.Contains(lastCmd, "chmod --reference='/home/ubuntu/.ssh/authorized_keys'") || !strings.Contains(lastCmd, "mv '/home/ubuntu/.ssh/authorized_keys.paisans-tmp' '/home/ubuntu/.ssh/authorized_keys'") {
		t.Errorf("the removal does not keep the mode and rename into place: %s", lastCmd)
	}
	if strings.Contains(lastCmd, "chown") {
		t.Errorf("the removal changes an owner, which the user cannot: %s", lastCmd)
	}
	if strings.Contains(lastCmd, "added "+fingerprint(keyCarol)) {
		t.Errorf("the sidecar still lists carol after her removal: %s", lastCmd)
	}
	if strings.Contains(lastCmd, keyCloud) || strings.Contains(lastCmd, keyAlice) {
		t.Errorf("the removal names a line it must keep: %s", lastCmd)
	}
	// The add comes before the removal.
	addAt := -1
	for i, s := range plan.Steps {
		if s.Label == "add" {
			addAt = i
		}
	}
	if addAt < 0 || addAt >= len(plan.Steps)-1 {
		t.Errorf("want add < remove, got add %d of %d", addAt, len(plan.Steps))
	}
}

// A key in the sidecar that someone already deleted is forgotten, so that a
// later hand-added copy is never taken for site prepare's.
func TestAnOwnedKeyGoneFromTheFileIsForgotten(t *testing.T) {
	host := preparedHost(false)
	host.files[ownedKeys] += "added " + fingerprint(keyCarol) + " carol\n"
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || !strings.Contains(plan.Steps[0].Describe, "ssh: forget key "+fingerprint(keyCarol)) {
		t.Fatalf("got:\n%s", printed(plan))
	}
	if strings.Contains(unlocked(t, plan.Steps[0].Command), "authorized_keys'") {
		t.Errorf("forgetting touches authorized_keys: %s", plan.Steps[0].Command)
	}
}

// A listed key present only with options is someone's restriction: it is
// reported and left, and the plain key is not added beside it.
func TestARestrictedListedKeyIsLeftAlone(t *testing.T) {
	host := preparedHost(false)
	restricted := `from="203.0.113.0/24" ` + keyAlice
	host.files[authorizedKeys] = restricted + "\n"
	delete(host.files, ownedKeys)
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("planned over a restricted key:\n%s", printed(plan))
	}
	if !strings.Contains(printed(plan), "done: ssh: key "+fingerprint(keyAlice)+" (alice) authorized for the deployment's user with options, by `"+restricted+"`; left as it is not paisans") {
		t.Errorf("got:\n%s", printed(plan))
	}
}

// A login user without a passwd entry has no home to find its keys in, so
// it is refused before any key is planned.
func TestAMissingUserIsRefused(t *testing.T) {
	host := preparedHost(false)
	host.responses[probePasswd] = ""
	_, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err == nil || !strings.Contains(err.Error(), "the login user has no passwd entry on fake that paisans can read") {
		t.Fatalf("got %v", err)
	}
}

// The login user's passwd entry is the first thing the keys and the store
// need, and its probe refuses a user the host lacks by name: it is the last
// command run, and neither authorized_keys nor the sidecar is read.
func TestAMissingUserIsRefusedBeforeTheHomeOrStoreIsRead(t *testing.T) {
	host := preparedHost(false)
	host.responses[probePasswd] = "__PAISANS_ABSENT__ ubuntu\n"
	_, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err == nil || !strings.Contains(err.Error(), "the login user ubuntu does not exist on fake. site prepare does not create users") {
		t.Fatalf("got %v", err)
	}
	if last := host.ran[len(host.ran)-1]; !strings.Contains(last, probePasswd) {
		t.Errorf("ran %q after the passwd probe", last)
	}
	for _, c := range host.ran {
		if strings.Contains(c, "paisans-f2a9-config-") {
			t.Errorf("read the store before refusing the user: %s", c)
		}
	}
	for _, path := range host.reads {
		if path == authorizedKeys || path == ownedKeys {
			t.Errorf("read %s before refusing the user", path)
		}
	}
}

// A key ssh.keys now calls something else is renamed in the sidecar.
func TestARenamedKeyIsRecordedUnderItsNewName(t *testing.T) {
	host := preparedHost(false)
	host.files[ownedKeys] = "added " + fingerprint(keyAlice) + " alice-old\n"
	plan, err := hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	rename := stepsLabelled(plan, "rename")
	if len(rename) != 1 || len(plan.Steps) != 1 || !strings.Contains(unlocked(t, rename[0].Command), "added "+fingerprint(keyAlice)+" alice\n") {
		t.Fatalf("want alice renamed:\n%s", printed(plan))
	}
}

// The safety property: no plan leaves the user with none of the listed keys.
// Load makes this unreachable through a file, so the configuration is
// emptied by hand to show the check holds on its own.
func TestNoPlanRemovesTheLastListedKey(t *testing.T) {
	host := preparedHost(false)
	host.files[authorizedKeys] = keyCarol + "\n"
	host.files[ownedKeys] = "added " + fingerprint(keyCarol) + " carol\n"
	_, err := hostprep.Build("home-a", withKeys(t, "home-a"), host, admitted(t, fixture(t), "home-a"))
	if err == nil || !strings.Contains(err.Error(), "would leave the deployment's user with none of the keys ssh.keys lists") {
		t.Fatalf("got %v", err)
	}
}

// After the plan's effect, the same host plans nothing for its keys.
func TestPreparedKeysPlanNothing(t *testing.T) {
	host := preparedHost(false)
	host.files[authorizedKeys] = keyCloud + "\n" + keyAlice + "\n" + keyBob + "\n"
	host.files[ownedKeys] = "added " + fingerprint(keyBob) + " bob\nadded " + fingerprint(keyAlice) + " alice\n"
	plan, err := hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("got:\n%s", printed(plan))
	}
	if !strings.Contains(printed(plan), "done: ssh: key "+fingerprint(keyBob)+" (bob) authorized for the deployment's user") {
		t.Errorf("got:\n%s", printed(plan))
	}
}

// Every key step runs as the deployment's user, under the deployment's own
// lock, writing only its own home and its own state directory.
func TestKeyStepsAreTheUsersOwn(t *testing.T) {
	host := freshHost()
	delete(host.files, authorizedKeys)
	delete(host.files, ownedKeys)
	cfg := fixture(t)
	s := cfg.Sites["home-a"]
	s.SSH.Keys = append(s.SSH.Keys, config.NamedKey{Name: "bob", Line: keyBob})
	cfg.Sites["home-a"] = s
	plan, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	lock := cfg.Deployment().StateLockCommand("the keys")
	var keySteps int
	for _, step := range plan.Steps {
		if !slices.Contains(step.Kinds, hostprep.KindSSHKeys) {
			continue
		}
		keySteps++
		for _, root := range []string{"/etc/paisans", "chown", " -o ", "sudo"} {
			if strings.Contains(step.Command, root) {
				t.Errorf("%q reaches %q: %s", step.Title, root, step.Command)
			}
		}
		if step.Title != "create authorized_keys" && !strings.Contains(step.Command, lock) {
			t.Errorf("%q is not under the deployment's lock: %s", step.Title, step.Command)
		}
	}
	if keySteps < 3 {
		t.Errorf("planned %d key steps, want create and two authorizations", keySteps)
	}
}

// expectedKind is the kind a step of each title is, by what the title says
// it changes.
func expectedKind(title string) (family, kind string) {
	switch {
	case title == "write daemon.json":
		return title, hostprep.KindDocker
	case title == "create authorized_keys", strings.HasSuffix(title, " ssh key"):
		return title, hostprep.KindSSHKeys
	case title == "create the configuration repository", title == "start the configuration store":
		return title, hostprep.KindConfigStore
	case strings.HasSuffix(title, ".peers"), title == "write config-sync.sh", strings.HasPrefix(title, "write paisans-f2a9-config-"):
		return "write <configuration store file>", hostprep.KindConfigStore
	}
	return title, ""
}

// Every step says what kind of change it is, so a dry run can sum a plan up
// as the kinds it would change: each step of every title, in plans that
// between them build every one.
func TestEveryStepCarriesItsKind(t *testing.T) {
	plans := map[string]func(t *testing.T) (*hostprep.Plan, error){
		"fresh data site": func(t *testing.T) (*hostprep.Plan, error) {
			return hostprep.Build("home-a", fixture(t), freshHost(), admitted(t, fixture(t), "home-a"))
		},
		"keys added": func(t *testing.T) (*hostprep.Plan, error) {
			host := preparedHost(false)
			delete(host.files, authorizedKeys)
			delete(host.files, ownedKeys)
			return hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
		},
		"key removed": func(t *testing.T) (*hostprep.Plan, error) {
			host := preparedHost(false)
			host.files[authorizedKeys] = keyCloud + "\n" + keyAlice + "\n" + keyCarol + "\n"
			host.files[ownedKeys] = "added " + fingerprint(keyAlice) + " alice\nadded " + fingerprint(keyCarol) + " carol\n"
			return hostprep.Build("home-a", withKeys(t, "home-a", keyAlice, keyBob), host, admitted(t, withKeys(t, "home-a", keyAlice, keyBob), "home-a"))
		},
		"key forgotten": func(t *testing.T) (*hostprep.Plan, error) {
			host := preparedHost(false)
			host.files[ownedKeys] += "added " + fingerprint(keyCarol) + " carol\n"
			return hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
		},
		"key renamed": func(t *testing.T) (*hostprep.Plan, error) {
			host := preparedHost(false)
			host.files[ownedKeys] = "added " + fingerprint(keyAlice) + " alice-old\n"
			return hostprep.Build("home-a", fixture(t), host, admitted(t, fixture(t), "home-a"))
		},
	}
	seen := map[string]bool{}
	for name, build := range plans {
		t.Run(name, func(t *testing.T) {
			plan, err := build(t)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Steps) == 0 {
				t.Fatal("planned nothing")
			}
			for _, s := range plan.Steps {
				family, kind := expectedKind(s.Title)
				seen[family] = true
				if kind == "" {
					t.Errorf("no kind expected for %q; add it to expectedKind", s.Title)
					continue
				}
				if strings.Join(s.Kinds, ",") != kind {
					t.Errorf("%q has kinds %q, want [%s]", s.Title, s.Kinds, kind)
				}
			}
		})
	}
	for _, family := range []string{
		"write daemon.json",
		"create the configuration repository", "write <configuration store file>", "start the configuration store",
		"create authorized_keys", "authorize ssh key", "rename ssh key", "forget ssh key", "remove ssh key",
	} {
		if !seen[family] {
			t.Errorf("no plan here builds %q", family)
		}
	}
}

// A plan's kinds are each kind its steps change, once, in a fixed order, so
// two runs that plan the same kinds say the same thing.
func TestAPlansKindsAreDistinctAndInAFixedOrder(t *testing.T) {
	plan, err := hostprep.Build("home-a", fixture(t), freshHost(), admitted(t, fixture(t), "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Kinds(), ", "); got != "docker, configuration store" {
		t.Errorf("a fresh data site's kinds are %q", got)
	}
	shuffled := &hostprep.Plan{Section: hostprep.Section{Steps: []hostprep.Step{
		{Kinds: []string{"watchdog"}}, {Kinds: []string{"ssh keys"}}, {Kinds: []string{"mystery"}},
		{Kinds: []string{"firewall", "docker"}}, {Kinds: []string{"wireguard"}}, {Kinds: []string{"docker"}},
	}}}
	if got := strings.Join(shuffled.Kinds(), ", "); got != "docker, wireguard, firewall, ssh keys, watchdog, mystery" {
		t.Errorf("kinds %q", got)
	}
	if got := (&hostprep.Plan{}).Kinds(); len(got) != 0 {
		t.Errorf("an empty plan has kinds %q", got)
	}
}

// A site that leaves ssh.user out has its keys managed for the deployment's
// own user: the authorized_keys in the home its passwd entry names, and the
// deployment's sidecar.
func TestKeysGoToTheDefaultUserWhenSSHUserIsLeftOut(t *testing.T) {
	cfg := withKeys(t, "home-a", keyAlice)
	s := cfg.Sites["home-a"]
	s.SSH.User = ""
	cfg.Sites["home-a"] = s
	host := preparedHost(false)
	host.responses[probePasswd] = "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/sh\n"
	delete(host.files, authorizedKeys)
	delete(host.files, ownedKeys)
	plan, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	out := printed(plan)
	if !strings.Contains(out, "ssh: authorize key "+fingerprint(keyAlice)+" (alice) for the deployment's user") {
		t.Fatalf("alice's key is not authorized for the deployment's user:\n%s", out)
	}
	add := stepsLabelled(plan, "add")
	if len(add) != 1 {
		t.Fatalf("want one add, got:\n%s", out)
	}
	cmd := unlocked(t, add[0].Command)
	for _, want := range []string{"'/home/paisans-f2a9/.ssh/authorized_keys'", ownedKeys} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the add lacks %s: %s", want, cmd)
		}
	}
}

// daemonJSONPath is the deployment user's daemon.json on the fake hosts.
const daemonJSONPath = "/home/ubuntu/.config/docker/daemon.json"

// admitted is the record site admit leaves for site with every grant its
// roles need: what site prepare is checked against.
func admitted(t *testing.T, cfg *config.Config, site string) admission.Record {
	t.Helper()
	req, err := admission.Build(cfg, site)
	if err != nil {
		t.Fatal(err)
	}
	rec := admission.NewRecord(req, req.Grants, "", time.Unix(0, 0))
	if cfg.Sites[site].WebServerManagedBy() == config.ManagedByExternal {
		rec.WebServer = &admission.WebServer{Server: "caddy"}
	}
	return rec
}

// site prepare as the deployment's user: nothing it plans installs a
// package, writes outside the user's home or the deployment's root, or runs
// a command only root can.
func TestHostPrepareInstallsNothing(t *testing.T) {
	cfg := fixture(t)
	for _, site := range cfg.SiteNames() {
		host := freshHost()
		plan, err := hostprep.Build(site, cfg, host, admitted(t, cfg, site))
		if err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		for _, s := range plan.Steps {
			if s.File != nil && !strings.HasPrefix(s.File.Path, "/home/ubuntu/") && !strings.HasPrefix(s.File.Path, "/srv/paisans/f2a9/") {
				t.Errorf("%s: %q writes %s", site, s.Title, s.File.Path)
			}
			for _, root := range []string{"apt-get", "modprobe", "ufw ", "/etc/", "sudo ", "systemctl daemon-reload &&", "systemctl enable docker"} {
				if strings.Contains(s.Command, root) {
					t.Errorf("%s: %q runs %q: %s", site, s.Title, root, s.Command)
				}
			}
		}
		for _, c := range host.ran {
			if strings.Contains(c, "ufw") || strings.Contains(c, "dpkg") {
				t.Errorf("%s: probed %q", site, c)
			}
		}
	}
}

// A record that does not match the declaration refuses site prepare, naming
// each difference and the line the owner runs.
func TestARecordThatDiffersIsRefused(t *testing.T) {
	cfg := fixture(t)
	rec := admitted(t, cfg, "home-a")
	rec.Grants.Watchdog = false
	_, err := hostprep.Build("home-a", cfg, freshHost(), rec)
	if err == nil || !strings.Contains(err.Error(), "--watchdog") || !strings.Contains(err.Error(), "sudo paisans site admit --request home-a.admit") {
		t.Errorf("got %v", err)
	}
}

// The watchdog is admit's: site prepare reads that /dev/watchdog is the
// user's, and refuses with the admit line when it is not.
func TestTheWatchdogIsOnlyRead(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	host.responses[probeWatchdog] = "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 0\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	if _, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a")); err == nil || !strings.Contains(err.Error(), "site admit") {
		t.Errorf("a root-owned /dev/watchdog: %v", err)
	}
	host.responses[probeWatchdog] = "device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid paisans-f2a9:165536:65536\n"
	plan, err := hostprep.Build("home-a", cfg, host, admitted(t, cfg, "home-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if slices.Contains(s.Kinds, hostprep.KindWatchdog) {
			t.Errorf("planned a watchdog step: %+v", s)
		}
	}
}

// daemon.json is written as the user when it differs, and the daemon is
// restarted only when it runs no container.
func TestDaemonJSONIsWrittenAndTheDaemonRestartedOnlyWhenIdle(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	host.responses["docker ps -q"] = "3f2a9c1d0b7e\n"
	plan, err := hostprep.Build("vm", cfg, host, admitted(t, cfg, "vm"))
	if err != nil {
		t.Fatal(err)
	}
	var write string
	for _, s := range plan.Steps {
		if s.Title == "write daemon.json" {
			write = s.Command
		}
		if strings.Contains(s.Command, "restart docker") {
			t.Errorf("restarted a daemon running containers: %s", s.Command)
		}
	}
	if !strings.Contains(write, "/home/ubuntu/.config/docker/daemon.json") {
		t.Errorf("write %q", write)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0].Hint, "next restart") {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}

// A daemon that runs no container has daemon.json written and is restarted
// at once, with nothing to warn of.
func TestAnIdleDaemonIsRestartedUnderItsNewDaemonJSON(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	plan, err := hostprep.Build("vm", cfg, host, admitted(t, cfg, "vm"))
	if err != nil {
		t.Fatal(err)
	}
	var write string
	for _, s := range plan.Steps {
		if s.Title == "write daemon.json" {
			write = s.Command
		}
	}
	if !strings.HasSuffix(write, "systemctl --user restart docker") {
		t.Errorf("write %q", write)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}

// A daemon site prepare cannot ask what it runs is never restarted: it
// may be running containers, so daemon.json is written and the plan warns.
func TestADaemonThatCannotBeAskedIsNotRestarted(t *testing.T) {
	cfg := fixture(t)
	host := freshHost()
	host.failures = map[string]error{"docker ps -q": errors.New("exit status 1")}
	plan, err := hostprep.Build("vm", cfg, host, admitted(t, cfg, "vm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range host.ran {
		if strings.Contains(c, "docker ps -q") && c != "docker ps -q" {
			t.Errorf("the count runs in a pipeline, which hides a failed docker ps: %q", c)
		}
	}
	var write string
	for _, s := range plan.Steps {
		if s.Title == "write daemon.json" {
			write = s.Command
		}
	}
	if write == "" || strings.Contains(write, "restart docker") {
		t.Errorf("write %q", write)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0].Hint, "next restart") || !strings.Contains(plan.Warnings[0].Detail, "could not ask") {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}
