package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/appremove"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Default output is plain when piped: no colour and no redrawn lines. Lines
// are not wrapped there, since whatever shows them wraps them. Run over the
// dry runs and executions the command tests already drive against their
// fakes.
func TestDefaultOutputIsPlain(t *testing.T) {
	runs := fixtureRuns(t)
	var names []string
	for name := range runs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out := runs[name]
		if testing.Verbose() {
			t.Logf("%s:\n%s", name, out)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s: printed nothing, so the run was not driven", name)
		}
		if strings.ContainsAny(out, "\x1b\r") {
			t.Errorf("%s: escapes in piped output", name)
		}
	}
}

// runDefault runs fn with the default reporter (not verbose) writing to a
// buffer, and returns what that reporter and any direct print to stdout
// showed. A direct print is measured too, so a command that skips the
// reporter cannot hide a long line.
func runDefault(t *testing.T, name string, fn func() error) string {
	t.Helper()
	var b strings.Builder
	reporterOverride = ui.NewPlain(&b, false)
	defer func() { reporterOverride = nil; apply.SetRetryLog(nil) }()
	var err error
	direct := captureStdout(t, func() { err = fn() })
	if err != nil {
		// A refusal is output too, and the error itself is printed whole by
		// main; the lines measured here are the reporter's.
		t.Logf("%s ended with: %v", name, err)
	}
	return b.String() + direct
}

// quietSSH puts an ssh on PATH that connects and answers as a clean, empty
// host, so a command that reaches one reports what it would do there.
func quietSSH(t *testing.T) { answerQuietly(t, "", "") }

// answerQuietly is quietSSH that logs each remote command, decoded, to log
// when log is set, and fails one containing fail when fail is set.
//
// The script still goes on PATH, for what runs ssh itself (ssh -G, a
// master's -O exit), but each remote command is answered in process, by
// quietAnswer, as the script would answer it: a test sends hundreds, and the
// script costs a fork and several execs for each.
func answerQuietly(t *testing.T, log, fail string) {
	t.Helper()
	script := quietHost
	if log != "" {
		script = strings.Replace(script, "case \"$cmd\" in\n", "printf '%s\\n' \"$cmd\" >> '"+log+"'\ncase \"$cmd\" in\n", 1)
	}
	if fail != "" {
		script = strings.Replace(script, "case \"$cmd\" in\n", "case \"$cmd\" in\n*'"+fail+"'*) echo failed; exit 1;;\n", 1)
	}
	sshAnswering(t, script)
	t.Cleanup(apply.SetRunSSH(func(cmd *exec.Cmd) error {
		out, exit, ok := quietAnswer(cmd.Args, log, fail)
		if !ok {
			return cmd.Run()
		}
		io.WriteString(cmd.Stdout, out)
		if exit != 0 {
			return sshExit(exit)
		}
		return nil
	}))
}

// quietAnswer is what loginProbe and then quietHost, with answerQuietly's
// log and fail, print and exit with for an ssh given args, read the way the
// script reads them. It is not ok for a last argument that is not a
// RemoteCommand, which is left to the script.
func quietAnswer(args []string, log, fail string) (string, int, bool) {
	if strings.Contains(strings.Join(args[1:], " "), "printf %s "+base64.StdEncoding.EncodeToString([]byte("id -u"))+" |") {
		return "1000\n", 0, true
	}
	b := args[len(args)-1]
	if _, after, found := strings.Cut(b, "printf %s "); found {
		b = after
	}
	b, _, _ = strings.Cut(b, " |")
	decoded, err := base64.StdEncoding.DecodeString(b)
	if err != nil {
		return "", 0, false
	}
	// $(...) drops the trailing newlines.
	cmd := strings.TrimRight(string(decoded), "\n")
	if strings.HasPrefix(cmd, "export DOCKER_HOST=") {
		if _, after, found := strings.Cut(cmd, "\n"); found {
			cmd = after
		}
	}
	if log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", 0, false
		}
		fmt.Fprintf(f, "%s\n", cmd)
		f.Close()
	}
	// words is the list between `in ` and `; do`, split as sh splits it,
	// with the quotes tr -d takes out taken out.
	words := func(in string) []string {
		l := cmd
		if _, after, found := strings.Cut(l, in); found {
			l = after
		}
		l, _, _ = strings.Cut(l, "; do")
		var out []string
		for _, w := range strings.Fields(l) {
			out = append(out, strings.ReplaceAll(w, "'", ""))
		}
		return out
	}
	var out strings.Builder
	switch {
	case fail != "" && strings.Contains(cmd, fail):
		return "failed\n", 1, true
	case strings.Contains(cmd, "for n in "):
		for _, n := range words("for n in ") {
			out.WriteString("absent " + n + "\n")
		}
	case strings.Contains(cmd, "paisans-sweep-end"):
		for _, r := range words("for r in ") {
			out.WriteString("present sha256:0123456789abcdef " + r + "\n")
		}
		out.WriteString("paisans-sweep-end\n")
	case strings.Contains(cmd, "for r in "):
		for _, r := range words("for r in ") {
			if strings.Contains(cmd, ".Config.Volumes") {
				out.WriteString("volumes " + r + " null\n")
			} else {
				out.WriteString("present sha256:0123456789abcdef " + r + "\n")
			}
		}
	case strings.Contains(cmd, "getent passwd"):
		out.WriteString("ubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n")
	// The watchdog probe asks who it runs as too, so it is matched first.
	case strings.Contains(cmd, "RuntimeWatchdogUSec"):
		out.WriteString("device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid ubuntu:165536:65536\n")
	case strings.Contains(cmd, `echo "uid`):
		out.WriteString("uid 1000\nsubuid ubuntu:165536:65536\n")
	case cmd == "dpkg --print-architecture":
		out.WriteString("amd64\n")
	case strings.Contains(cmd, "print-architecture"):
		out.WriteString("arch amd64\n")
	case strings.Contains(cmd, "/etc/os-release"):
		out.WriteString("ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n")
	case strings.Contains(cmd, "docker volume ls"):
		out.WriteString("end\n")
	case strings.Contains(cmd, "__PAISANS_NO_ETCD__"):
		out.WriteString("__PAISANS_NO_ETCD__\n")
	case strings.Contains(cmd, "__PAISANS_ABSENT__"):
		out.WriteString("__PAISANS_ABSENT__\n")
	}
	return out.String(), 0, true
}

// sshExit is an ssh that exited with a status, as *exec.ExitError reports
// one.
type sshExit int

func (e sshExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e sshExit) ExitCode() int { return int(e) }

// The remote command travels base64 encoded (apply.RemoteCommand), so the
// script decodes it, drops the DOCKER_HOST export a site transport puts
// first, and answers the few probes whose silence would be read as a
// failure: who it runs as, which OS, what volumes there are, that a file
// is absent, and that the watchdog admit gave the user is there.
const quietHost = `#!/bin/sh
for last; do :; done
b=${last#*printf %s }
b=${b%% |*}
cmd=$(printf %s "$b" | base64 -d 2>/dev/null)
case $cmd in "export DOCKER_HOST="*) cmd=${cmd#*"
"};; esac
case "$cmd" in
*"for n in "*) l=${cmd#*for n in }; l=${l%%; do*}; for n in $l; do echo "absent $(printf %s "$n" | tr -d "'")"; done;;
*paisans-sweep-end*) l=${cmd#*for r in }; l=${l%%; do*}
	for r in $l; do echo "present sha256:0123456789abcdef $(printf %s "$r" | tr -d "'")"; done
	echo paisans-sweep-end;;
*"for r in "*) l=${cmd#*for r in }; l=${l%%; do*}
	for r in $l; do
		r=$(printf %s "$r" | tr -d "'")
		case "$cmd" in *".Config.Volumes"*) echo "volumes $r null";; *) echo "present sha256:0123456789abcdef $r";; esac
	done;;
*"getent passwd"*) echo "ubuntu:x:1000:1000::/home/ubuntu:/bin/bash";;
# site admit loaded softdog and gave /dev/watchdog to the user. It asks who
# it runs as too, so it is matched first.
*RuntimeWatchdogUSec*) printf 'device present\nidentity watchdog0 Software Watchdog\nsystemd 0\ndaemon absent\nowner 1000\nuid 1000\nsubuid ubuntu:165536:65536\n';;
# The host check's inventory asks who it runs as. The login probe, a bare
# id -u, is answered 1000 before this script runs.
*'echo "uid'*) printf 'uid 1000\nsubuid ubuntu:165536:65536\n';;
"dpkg --print-architecture") echo amd64;;
*"print-architecture"*) echo "arch amd64";;
*"/etc/os-release"*) printf 'ID=ubuntu\nVERSION_ID="24.04"\nVERSION_CODENAME=noble\n';;
*"docker volume ls"*) echo end;;
*__PAISANS_NO_ETCD__*) echo __PAISANS_NO_ETCD__;;
*__PAISANS_ABSENT__*) echo __PAISANS_ABSENT__;;
esac
exit 0
`

// sshAnswering puts an ssh on PATH that runs script for each connection.
// The login probe a site transport sends first, a bare id -u, is answered
// ahead of script, in the same file, as a login user with UID 1000, so
// script answers only the commands it is about, and the test starts with no
// Login from an earlier one.
func sshAnswering(t *testing.T, script string) {
	t.Helper()
	resetLogins(t)
	body, ok := strings.CutPrefix(script, "#!/bin/sh\n")
	if !ok {
		t.Fatal("a fake ssh script does not start #!/bin/sh")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(loginProbe+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Closed while the fake is still first on PATH, as main closes them.
	t.Cleanup(apply.CloseConnections)
	// Every command reaches the script, whatever an earlier fake answered.
	t.Cleanup(apply.SetRunSSH(nil))
}

// loginProbe answers the login probe, a remote command that is exactly
// id -u, with UID 1000, by matching its base64 as apply.RemoteCommand
// sends it, so no other command is decoded twice.
var loginProbe = "#!/bin/sh\ncase \"$*\" in *'printf %s " + base64.StdEncoding.EncodeToString([]byte("id -u")) + " |'*) echo 1000; exit 0;; esac\n"

// runningHost is a host whose stacks come up: `ps` finds a running container,
// so the gate after each stack passes and an execution carries on.
type runningHost struct{ failingHost }

func (h runningHost) Run(command string) (string, error) {
	if strings.HasSuffix(command, " ps --all --format json") {
		return `{"Service":"app","Name":"app-1","State":"running","Health":""}` + "\n", nil
	}
	if strings.Contains(command, "ip -j ") {
		return "[]\n", nil
	}
	if strings.Contains(command, `echo "uid`) {
		return "uid 1000\nsubuid ubuntu:165536:65536\n", nil
	}
	if strings.Contains(command, "getent passwd") {
		return "ubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n", nil
	}
	if strings.HasSuffix(command, "id -u") {
		return "0\n", nil
	}
	if strings.Contains(command, "__PAISANS_NO_ETCD__") {
		return "__PAISANS_NO_ETCD__\n", nil
	}
	if strings.Contains(command, ":8008/cluster") {
		return `{"members":[{"name":"home-a","role":"leader","state":"running"}]}`, nil
	}
	return h.failingHost.Run(command)
}

// fixtureRuns drives the commands against their fakes, by default verbosity,
// and returns what each showed. Commands run from a directory of their own
// with short file names, as an operator runs them, so a long temporary path
// is not mistaken for a long sentence.
func fixtureRuns(t *testing.T) map[string]string {
	t.Helper()
	runs := map[string]string{}
	fixture, secretsFixture := mustAbs(t, fixtureConfig()), mustAbs(t, fixtureSecretsPath())

	// init reads the example declaration relative to the package, so it
	// runs first, in a subtest whose directory change ends with it.
	t.Run("init", func(t *testing.T) {
		path, _ := initWorld(t, nil, fakeSites(), make([]byte, 64))
		t.Chdir(filepath.Dir(path))
		runs["init"] = runDefault(t, "init", func() error { return runInit([]string{"--config", "paisans-stack.yaml"}) })
	})

	work := t.TempDir()
	t.Chdir(work)
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secretsBody, err := os.ReadFile(secretsFixture)
	if err != nil {
		t.Fatal(err)
	}
	write("paisans-stack.yaml", string(body))
	write("secrets.yaml", string(secretsBody))
	cfg, secrets := "paisans-stack.yaml", "secrets.yaml"

	// With a public address declared, dns init gets as far as the provider
	// check, which is the last place it can refuse offline.
	withAddress := strings.Replace(string(body), "    endpoint: vm.example.org:51820\n", "    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	write("dns.yaml", withAddress)

	// site add joins a site with the data role only, and every site dials
	// the others directly, so the fixture is declared that way for it.
	dialled := strings.Replace(withAddress, "  home-a:\n    roles: [data, apps]\n", "  home-a:\n    roles: [data, apps]\n    endpoint: 192.0.2.1:51820\n", 1)
	joined := strings.Replace(dialled, "  home-b:\n    roles: [data, apps]", "  home-b:\n    roles: [data]", 1)
	if joined == dialled || dialled == withAddress {
		t.Fatal("the fixture no longer declares the sites this edit expects")
	}
	write("join.yaml", joined)

	runs["validate"] = runDefault(t, "validate", func() error { return runValidate([]string{"--config", cfg}) })
	runs["dns init"] = runDefault(t, "dns init", func() error { return runDNSInit([]string{"--config", "dns.yaml", "--secrets", secrets}) })

	{
		// failingHost with a match nothing contains answers everything an
		// empty host would, ps included.
		host := runningHost{failingHost{match: "\x00"}}
		var b strings.Builder
		r := ui.NewPlain(&b, false)
		presentPlan(r, freshPlan(t, r, host), false, map[string]bool{})
		runs["apply dry run"] = b.String()
		b.Reset()
		executed := freshPlan(t, r, host)
		presentPlan(r, executed, true, map[string]bool{})
		if err := apply.Execute(executed, host); err != nil {
			t.Fatal(err)
		}
		runs["apply execute"] = b.String()
		b.Reset()
		failed := freshPlan(t, r, failOn("infra/compose.yaml up -d", "Error response from daemon: port is already allocated"))
		_ = apply.Execute(failed, failOn("infra/compose.yaml up -d", "Error response from daemon: port is already allocated"))
		runs["apply execute failing"] = b.String()
	}

	{
		host := danglingHost{listing: `[{"Name":"3a37a98261c4f658850d43b3d0ddc746ae25d9ec6bb58e83132662b7ea646191","Size":"48.234MB"},{"Name":"other_db","Size":"9.999kB"}]` + "\nvolumes\nvolume\t3a37a98261c4f658850d43b3d0ddc746ae25d9ec6bb58e83132662b7ea646191\t{\"community.paisans.deployment\":\"f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01\"}\tcache log\n" +
			"volume\tother_db\t{\"com.docker.compose.project\":\"other\"}\tpgdata\nend\n"}
		plan, err := apply.BuildVolumePrune(deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}, "home-a", host)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		showVolumePrune(ui.NewPlain(&b, false), plan)
		runs["prune plan"] = b.String()
	}

	{
		fake := withIDPFake(t)
		_ = fake
		runs["oidc client create dry run"] = runDefault(t, "oidc client create", func() error {
			return runOIDCClientCreate([]string{"--config", cfg, "--secrets", secrets, "--app", "talk"})
		})
	}

	{
		withLinkFake(t)
		runs["operator magic-link dry run"] = runDefault(t, "operator magic-link", func() error {
			return runOperatorMagicLink([]string{"--config", cfg, "--secrets", secrets, "alice"})
		})
	}

	{
		var sent, writes []string
		loaded := mustLoad(t, cfg)
		withDoctorHosts(t, loaded, stuckHosts(loaded, &sent, &writes))
		runs["doctor"] = runDefault(t, "doctor", func() error { return runDoctor([]string{"--config", cfg}) })
	}

	{
		savedSite, savedApp, savedReg := removeSiteHost, removeHost, registryHost
		t.Cleanup(func() { removeSiteHost, removeHost, registryHost = savedSite, savedApp, savedReg })
		quiet := runningHost{failingHost{match: "\x00"}}
		removeSiteHost = func(string, config.Site) apply.Transport { return quiet }
		removeHost = func(string, config.Site) appremove.Host { return quiet }
		registryHost = func(string, config.Site, string) registry.Runner { return quiet }
		runs["site remove dry run"] = runDefault(t, "site remove", func() error {
			return runSiteRemove([]string{"watch", "--config", cfg, "--secrets", secrets}, strings.NewReader(""), io.Discard)
		})
		runs["app remove dry run"] = runDefault(t, "app remove", func() error {
			return runAppRemove([]string{"old", "--config", cfg, "--secrets", secrets}, strings.NewReader(""), io.Discard)
		})
	}

	// The commands that reach a host through ssh meet an ssh that answers
	// as a clean, empty host.
	quietSSH(t)
	for name, args := range map[string][]string{
		"site prepare dry run": {"site prepare", "--config", cfg, "--site", "home-a"},
		"site add dry run":     {"site add", "home-b", "--config", "join.yaml", "--secrets", secrets},
		"storage add dry run":  {"storage add", "--config", cfg, "--secrets", secrets},
		"prune dry run":        {"prune", "--config", cfg, "--site", "home-a"},
	} {
		run := map[string]func([]string) error{
			"site prepare": runSitePrepare, "site add": func(a []string) error { return runSiteAdd(a, strings.NewReader(""), io.Discard) }, "storage add": runStorageAdd, "prune": runPrune,
		}[args[0]]
		runs[name] = runDefault(t, name, func() error { return run(args[1:]) })
	}
	return runs
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
