# Rootless Phase 2: Transports Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every command that acts on a deployment reaches its sites as the deployment's own user, with `DOCKER_HOST` pointed at that user's rootless daemon and no sudo; a refused default user is reported as a host that has not admitted the deployment; the first login records the user in `sites.<s>.ssh.user`; and the transports the next phases need (a local one for the owner, the WireGuard helper's) exist.

**Architecture:** `apply.SSHTransport` gains `DockerHost`, a shared `*apply.Login` that reads the login user's UID with `id -u` once per destination per run and calls back once the first login has worked, and `DefaultUser`, which turns ssh's `Permission denied` into "not admitted". `RemoteCommand` exports `DOCKER_HOST` inside the encoded command. `siteTransport` in `cmd/paisans` loses its `sudo` parameter, so the compiler finds every caller; the one transport left with sudo is `ownerTransport`, used only by `host deployments`. A test walks the source tree and refuses `Sudo` or `Auth` set anywhere else. The local transport lives in a new package, `internal/hostadmit`, behind `hostadmit.Local()`, and a second source walk refuses any caller outside that package. `apply.WGHelper` builds the helper's `sudo -n` command lines; nothing calls it until phase 3.

**Tech Stack:** Go 1.25, `go/parser` and `go/ast` for the two source walks, `gopkg.in/yaml.v3` splicing in `internal/config`, the existing `apply.FakeSSH` test seam.

**Spec:** `docs/specs/2026-10-15-rootless-and-host-admit.md` (sections *Settled*, *The deployment's user*, *Running on the host*, *The WireGuard helper*, *What runs as the deployment's user*, *Tests*), and the phase index `docs/superpowers/plans/2026-10-15-rootless-and-host-admit.md`, *Phase 2: Transports*. Both live on the `host-admit` branch, at `/Users/wash/Developer/paisans.community/src/paisans-stack/.worktrees/host-admit/docs/`.

## Global Constraints

* "Rootless on every site; no code path keeps the sudo transport for a deployment command." (index, *Global Constraints*)
* "Every command except `host admit`, `host evict` and `host deployments`. `SSHTransport.Sudo` is false for all of them and every `--sudo` flag is removed. `DOCKER_HOST` is set to `unix:///run/user/<uid>/docker.sock` in `RemoteCommand`, so `docker` never falls back to the owner's system socket." (spec, *What runs as the deployment's user*)
* The UID behind `DOCKER_HOST` is read with `id -u`, once per destination per run.
* "Default user `paisans-<token>` (`deployment.Prefix()`); `ssh.user` overrides; written back with a splice that never overwrites." Phase 1 delivered it as `(config.Site).SSHUser() string` (no argument: `config.Load` records the default on each site) and `config.SetSSHUser(path, site, user string) error`.
* A write-back failure is a warning, never fatal to the command that connected.
* "An unadmitted host (ssh auth fails for the default user and no `ssh.user` is declared) reports 'not admitted' with the `host request` line." The line is `paisans host request --site <s>`; that command lands in phase 4, and the message names it now.
* The local transport "is only constructed by `host admit --request` and `host evict --request`, never by a command that changes a deployment". Neither exists yet, so in this phase nothing outside `internal/hostadmit` calls `hostadmit.Local()`.
* WireGuard helper commands are exactly `sudo -n /usr/local/libexec/paisans/wg-<iface> <verb>`, verbs `put`, `up`, `restart`, `sync`, `show <field>`, `log`, fields `peers`, `latest-handshakes`, `public-key`.
* `paisans create`: "the SSH user prompt offers the deployment's default and may be left empty".
* No migration code. Nothing is released from `develop` until phase 6; this phase may leave the toolkit unable to prepare a host (`host prepare` still installs packages, which now fails without sudo), because phases 3 to 6 move those steps to `host admit` and to the user.
* No em dashes anywhere: code, comments, docs, commit messages.
* Docs describe current state only: no "no longer", "used to" or history wording outside `CHANGELOG.md`; no rejected alternatives anywhere. `docs/decisions.md`, `docs/specs/` and `docs/superpowers/` are records and are not edited.
* Commits: `<type>: <lowercase subject>`, a body saying why, `Founder decision.` where it was one, and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
* `CHANGELOG.md`: a behaviour-changing commit adds one bullet under `[Unreleased]`, in its Keep a Changelog category, one sentence, at most 35 words. A commit that changes nothing an operator would notice adds none and says so in its body.
* Never `git stash`; other sessions share the stash. Never push.

## Review Focus

* A command run with `ssh.user` absent before admission: it must say the site is not admitted and print `paisans host request --site <s> > <s>.admit`, not "did not accept your ssh key" (Task 3 transport test; Task 8 wiring test that sets `DefaultUser`).
* A configuration file saved with CRLF line endings: the line `SetSSHUser` adds ends in `\r\n` like its neighbours, so the file is not left with mixed endings (Task 1 splice table).
* Several sites logged in to at once, as `doctor` does: each write-back is serialised, every site's user lands in the file, and the file still loads (Task 9 concurrency test).
* A site written `user:` with no value: the command logs in as the default, says the user could not be recorded, and carries on with the file unchanged (Task 9 test).
* A login whose `id -u` prints anything but a number (a banner from a shell rc file, an empty line): the command fails naming the probe and never builds a `DOCKER_HOST` from it (Task 2 test).

---

## File Structure

| File | Change | Responsibility after the change |
|---|---|---|
| `internal/config/ssh_user.go` | modify | `setSSHUser` keeps the file's line ending |
| `internal/config/ssh.go`, `internal/config/config.go` | modify | `SSH.file`, `(Site).SSHUserFile()`; `LoadBytes` records no file |
| `internal/apply/transport.go` | modify | `SSHTransport.DockerHost`, `.Login`, `.DefaultUser`; `RemoteCommand(dockerHost, command)`; Login resolved in `run`; the not-admitted problem |
| `internal/apply/login.go` | create | `Login`, `NewLogin`, `RootlessDockerHost` |
| `internal/apply/helper.go` | create | `WGHelper`, `HelperDir` |
| `internal/hostadmit/local.go` | create | package doc, `Local()`, the unexported local transport |
| `cmd/paisans/main.go` | modify | `siteTransport(name, site, override)`; no `--sudo` on `init`, `apply`, `storage init`, `host prepare` |
| `cmd/paisans/hostdeployments.go` | modify | `ownerTransport`, `reachOwner`, `destinationArg`, `reachDestination(dest)` |
| `cmd/paisans/login.go` | create | `loginFor`, `recordSSHUser`, `routeLoginNotes` |
| `cmd/paisans/sudo.go` | modify | `settleSudo` deleted; the prompt machinery stays for `ownerTransport` |
| every other `cmd/paisans/*.go` with a `--sudo` flag or a `sudo bool` parameter | modify | listed in Task 6 |
| `internal/preflight/preflight.go`, `internal/doctor/reach.go`, `internal/hostcheck/inventory.go`, `internal/webserver/detect.go` | modify | messages and checks that named the sudo transport |
| `internal/create/wizard.go`, `internal/create/paisans-stack.yaml.tmpl`, `cmd/paisans/create.go` | modify | optional SSH user offering the deployment's own |
| `README.md`, `docs/commands.md`, `docs/guides/behind-your-own-web-server.md`, `CHANGELOG.md`, `cmd/paisans/help.go` | modify | current state |

---

### Task 1: `SetSSHUser` edge cases, and where a site's default user is recorded

**Files:**
- Modify: `internal/config/ssh_user.go:88-90` (block insertion)
- Modify: `internal/config/ssh.go:48-51` (`SSH` fields), `internal/config/ssh.go:101-111` (after `SSHUser`), `internal/config/ssh.go:323-331` (`resolveUsers`)
- Modify: `internal/config/config.go:844-851` (`LoadBytes`)
- Test: `internal/config/ssh_user_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: phase 1's `setSSHUser(data []byte, site, user string) ([]byte, error)`, `(Site).SSHUser() string`, `(*Config).resolveUsers()`.
- Produces: `func (s Site) SSHUserFile() string` in `internal/config/ssh.go`: the declaration's path when the site leaves `ssh.user` out and was loaded from a file, else `""`. Task 9 uses it.

- [ ] **Step 1: Write the failing splice cases**

In `internal/config/ssh_user_test.go`, append these entries to the `cases` slice of `TestSetSSHUser`, after `"only the named site"`:

```go
		{
			"written empty, in a flow mapping",
			"sites:\n  home-a:\n    ssh: {user: \"\", host: home-a.local, keys: [alice]}\n",
			"sites:\n  home-a:\n    ssh: {user: \"paisans-f2a9\", host: home-a.local, keys: [alice]}\n",
		},
		{
			"written empty, quoted, with a trailing comment",
			"sites:\n  home-a:\n    ssh:\n      user: \"\"   # set on first login\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
		},
		{
			"present, quoted, with a trailing comment",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
		},
		{
			"absent, with CRLF line endings",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      keys: [alice]\r\n",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: paisans-f2a9\r\n      keys: [alice]\r\n",
		},
		{
			"written empty, with CRLF line endings",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: \"\"\r\n      keys: [alice]\r\n",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: \"paisans-f2a9\"\r\n      keys: [alice]\r\n",
		},
		{
			"ssh last, with no final newline",
			"sites:\n  home-a:\n    roles: [data]\n    ssh:\n      keys: [alice]",
			"sites:\n  home-a:\n    roles: [data]\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]",
		},
		{
			"ssh last, in a flow mapping, with no final newline",
			"sites:\n  home-a:\n    ssh: {keys: [alice]}",
			"sites:\n  home-a:\n    ssh: {user: paisans-f2a9, keys: [alice]}",
		},
```

and append this entry to the `cases` slice of `TestSetSSHUserRefuses`:

```go
		{"a user written with no value, in a flow mapping", "sites:\n  home-a:\n    ssh: {user: , keys: [alice]}\n", "home-a", "paisans-f2a9", "written with no value"},
```

- [ ] **Step 2: Run the splice test to verify it fails**

Run: `go test ./internal/config/ -run TestSetSSHUser`
Expected: FAIL, only `TestSetSSHUser/absent,_with_CRLF_line_endings`, whose `got:` has `user: paisans-f2a9\n` between `\r\n` lines. Every other new case passes already; they pin shapes the write-back meets.

- [ ] **Step 3: Keep the file's line ending in the splice**

In `internal/config/ssh_user.go` (`bytes` is already imported), replace the last two lines of `setSSHUser` (lines 89-90):

```go
	at := lines.start(first.Line)
	return splice(data, edit{at, at, fmt.Sprintf("%*suser: %s\n", first.Column-1, "", plainOrQuoted(user))}), nil
```

with

```go
	at := lines.start(first.Line)
	return splice(data, edit{at, at, fmt.Sprintf("%*suser: %s%s", first.Column-1, "", plainOrQuoted(user), lineEnding(data))}), nil
}

// lineEnding is the line ending data is written with: CRLF when any line
// ends in one, so a file saved on Windows gets no bare LF line among its
// own, and LF otherwise.
func lineEnding(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
```

(The closing brace of `setSSHUser` that followed line 90 now closes `lineEnding`.)

Run: `go test ./internal/config/ -run TestSetSSHUser`
Expected: PASS.

- [ ] **Step 4: Write the failing `SSHUserFile` test**

Append to `internal/config/config_test.go`, after `TestSSHUserIsTheDeclaredName`:

```go
// A site that leaves ssh.user out names the file it was loaded from, where a
// command records the user it logged in as. A site naming its user names
// none, and so does one loaded from bytes or built by hand, which have no
// file to record it in.
func TestSSHUserFileIsWhereTheDefaultUserIsRecorded(t *testing.T) {
	path := write(t, withSSH("    ssh:\n      host: home-a.local\n      keys: [alice]\n"))
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sites["home-a"].SSHUserFile(); got != path {
		t.Errorf("SSHUserFile() = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fromBytes, err := config.LoadBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromBytes.Sites["home-a"].SSHUserFile(); got != "" {
		t.Errorf("loaded from bytes: SSHUserFile() = %q, want none", got)
	}
	declared, err := config.Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := declared.Sites["home-a"].SSHUserFile(); got != "" {
		t.Errorf("a declared user: SSHUserFile() = %q, want none", got)
	}
	if got := (config.Site{}).SSHUserFile(); got != "" {
		t.Errorf("a site built by hand: SSHUserFile() = %q, want none", got)
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestSSHUserFileIsWhereTheDefaultUserIsRecorded`
Expected: FAIL to compile, `cfg.Sites["home-a"].SSHUserFile undefined (type config.Site has no field or method SSHUserFile)`.

- [ ] **Step 6: Record the file on each site**

In `internal/config/ssh.go`, replace the `defaultUser` field (lines 48-51):

```go
	// defaultUser is the deployment's own user, paisans-<token>, which Load
	// records on every site so that a site can name its login without the
	// configuration it came from.
	defaultUser string
```

with

```go
	// defaultUser is the deployment's own user, paisans-<token>, which Load
	// records on every site so that a site can name its login without the
	// configuration it came from.
	defaultUser string
	// file is the declaration Load read the site from, "" for one loaded
	// from bytes, for SSHUserFile.
	file string
```

After `SSHUser` (after line 111), add:

```go
// SSHUserFile is the file a command records the site's login user in, with
// SetSSHUser, once it has logged in as that user: the declaration the site
// was loaded from, when the site leaves ssh.user out. It is "" for a site
// that names its user, which is recorded already, and for a site not loaded
// from a file, which has nowhere to record it.
func (s Site) SSHUserFile() string {
	if s.SSH.User != "" || s.SSH.defaultUser == "" {
		return ""
	}
	return s.SSH.file
}
```

Replace `resolveUsers` (lines 323-331):

```go
// resolveUsers records the deployment's own user on every site's ssh section,
// for SSHUser.
func (c *Config) resolveUsers() {
	user := c.Deployment().Prefix()
	for name, site := range c.Sites {
		site.SSH.defaultUser = user
		c.Sites[name] = site
	}
}
```

with

```go
// resolveUsers records the deployment's own user on every site's ssh section,
// for SSHUser, and the file the configuration was read from, for
// SSHUserFile.
func (c *Config) resolveUsers() {
	user := c.Deployment().Prefix()
	for name, site := range c.Sites {
		site.SSH.defaultUser = user
		site.SSH.file = c.Path
		c.Sites[name] = site
	}
}
```

In `internal/config/config.go`, `LoadBytes` (lines 844-851), replace

```go
	cfg.Path = ""
	return cfg, nil
```

with

```go
	cfg.Path = ""
	cfg.resolveUsers()
	return cfg, nil
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 8: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

No `CHANGELOG.md` bullet: `SetSSHUser` is unreleased and nothing calls it yet.

```bash
git add internal/config/ssh_user.go internal/config/ssh_user_test.go internal/config/ssh.go internal/config/config.go internal/config/config_test.go
git commit -m "fix: setsshuser keeps a crlf file's line endings

SetSSHUser added its line with a bare LF, so a declaration saved with CRLF
line endings came back with one line unlike the rest, which some editors
then normalise and a diff shows as a whole-file change. The line now ends
the way the file's lines do. Tests pin the other shapes a write-back meets:
a flow section with an empty user, a quoted value with a trailing comment,
and an ssh section last in a file with no final newline.

Each site also records the file it was loaded from, so a command that logs
in as the deployment's default user knows where to record it. A site loaded
from bytes, as a configuration store version is, records none. No changelog
bullet: SetSSHUser is unreleased and nothing calls it yet.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `DOCKER_HOST` in the remote command, and the login's UID read once

**Files:**
- Create: `internal/apply/login.go`
- Modify: `internal/apply/transport.go:86-92` (fields after `Auth`), `internal/apply/transport.go:174-186` (`SSHArgs`), `internal/apply/transport.go:188-208` (`RemoteCommand`), `internal/apply/transport.go:557-558` (top of `run`)
- Modify tests that call `apply.RemoteCommand(cmd)`: `internal/apply/transport_test.go:29,49,71,79`, `internal/apply/sshmux_test.go:81`, `internal/apply/remote_shell_test.go:17,60`
- Test: `internal/apply/login_test.go` (create), `internal/apply/remote_shell_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `SSHTransport.DockerHost string`, `SSHTransport.Login *Login`.
  - `func RemoteCommand(dockerHost, command string) string`.
  - `type Login struct { Connected func(); ... }`, `func NewLogin(connected func()) *Login`.
  - `func RootlessDockerHost(uid string) string` returning `"unix:///run/user/" + uid + "/docker.sock"`.
  - Task 8 builds one `*Login` per destination in `cmd/paisans`; Task 9 passes `connected`.

- [ ] **Step 1: Write the failing tests**

Create `internal/apply/login_test.go`:

```go
package apply_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// loginScript stands in for ssh: it answers id -u with uid and every other
// command with nothing, and keeps each command as the remote sh runs it.
func loginScript(t *testing.T, uid string, exit int) *[]string {
	t.Helper()
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	blanks := 0
	t.Cleanup(apply.FakeHostKeys(func(apply.SSHTransport) bool { return true }, &blanks))
	var ran []string
	t.Cleanup(apply.FakeSSH(func(args []string, _ string) (string, int) {
		command := args[len(args)-1]
		ran = append(ran, command)
		if command == "id -u" {
			return uid, exit
		}
		return "", 0
	}, nil, func(time.Duration) {}, io.Discard))
	return &ran
}

const rootlessSocket = "unix:///run/user/1000/docker.sock"

// The login user's UID is asked once, on the first command, and every
// command runs with DOCKER_HOST at that user's rootless socket, so docker
// never reaches the host's system daemon. A copy of the transport shares the
// answer, as a second transport built for the same site does.
func TestALoginAsksItsUIDOnceAndPointsDockerAtIt(t *testing.T) {
	ran := loginScript(t, "1000\n", 0)
	connected := 0
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected++ })}
	if _, err := tr.Run("docker ps"); err != nil {
		t.Fatal(err)
	}
	copied := tr
	if _, err := copied.Run("docker version"); err != nil {
		t.Fatal(err)
	}
	export := "export DOCKER_HOST='" + rootlessSocket + "'\n"
	want := []string{"id -u", export + "docker ps", export + "docker version"}
	if strings.Join(*ran, "|") != strings.Join(want, "|") {
		t.Errorf("ran %q, want %q", *ran, want)
	}
	if connected != 1 {
		t.Errorf("Connected called %d times, want once", connected)
	}
}

// What id -u printed is a UID or nothing is built from it: a login that
// prints a banner first, or nothing, fails naming the probe, and runs no
// command without its DOCKER_HOST. The failure is not kept, so the next
// command asks again.
func TestALoginWhoseUIDCannotBeReadRunsNothing(t *testing.T) {
	for _, out := range []string{"Welcome to home-a\n1000\n", "\n"} {
		ran := loginScript(t, out, 0)
		connected := false
		tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected = true })}
		_, err := tr.Run("docker ps")
		if err == nil || !strings.Contains(err.Error(), "id -u printed") {
			t.Errorf("%q: err = %v", out, err)
		}
		if _, err := tr.Run("docker ps"); err == nil {
			t.Errorf("%q: a second command ran", out)
		}
		if strings.Join(*ran, "|") != "id -u|id -u" || connected {
			t.Errorf("%q: ran %q, connected %v", out, *ran, connected)
		}
	}
}

// A login ssh never made is ErrUnreachable to the caller, as any command's
// is, and counts as no login.
func TestALoginThatNeverConnectedIsUnreachable(t *testing.T) {
	loginScript(t, "ssh: connect to host 203.0.113.10 port 22: Connection refused\n", 255)
	connected := false
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, Login: apply.NewLogin(func() { connected = true })}
	if _, err := tr.Run("docker ps"); !errors.Is(err, apply.ErrUnreachable) || connected {
		t.Errorf("err = %v, connected %v", err, connected)
	}
}

// A DockerHost given outright is used as given, with no id -u.
func TestAGivenDockerHostAsksNoUID(t *testing.T) {
	ran := loginScript(t, "1000\n", 0)
	tr := apply.SSHTransport{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DockerHost: "unix:///run/user/1001/docker.sock", Login: apply.NewLogin(nil)}
	if _, err := tr.Run("docker ps"); err != nil {
		t.Fatal(err)
	}
	if want := "export DOCKER_HOST='unix:///run/user/1001/docker.sock'\ndocker ps"; strings.Join(*ran, "|") != want {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}

func TestRootlessDockerHost(t *testing.T) {
	if got := apply.RootlessDockerHost("1000"); got != rootlessSocket {
		t.Errorf("RootlessDockerHost(1000) = %q", got)
	}
}
```

Append to `internal/apply/remote_shell_test.go`:

```go
// With a docker host, the command runs after DOCKER_HOST is exported, inside
// the one encoded word, so every docker the command starts sees it. Without
// one, the command is sent exactly as given.
func TestRemoteCommandExportsTheDockerHost(t *testing.T) {
	remote := apply.RemoteCommand("unix:///run/user/1000/docker.sock", `printf %s "$DOCKER_HOST"`)
	out, err := exec.Command("sh", "-c", remote).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "unix:///run/user/1000/docker.sock" {
		t.Errorf("DOCKER_HOST was %q", out)
	}
	if got, ok := apply.InnerCommand(apply.RemoteCommand("", "uptime")); !ok || got != "uptime" {
		t.Errorf("without a docker host: %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/apply/ -run 'Login|RootlessDockerHost|RemoteCommandExportsTheDockerHost|AGivenDockerHost'`
Expected: FAIL to compile: `unknown field Login in struct literal`, `undefined: apply.NewLogin`, `undefined: apply.RootlessDockerHost`, `too many arguments in call to apply.RemoteCommand`.

- [ ] **Step 3: Create `internal/apply/login.go`**

```go
package apply

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// uidProbe asks the login user's UID, from which its rootless Docker
// daemon's socket is known.
const uidProbe = "id -u"

// RootlessDockerHost is DOCKER_HOST for the rootless daemon of the user with
// uid: its socket in the user's runtime directory, which systemd makes at
// /run/user/<uid> and keeps while the user lingers.
func RootlessDockerHost(uid string) string { return "unix:///run/user/" + uid + "/docker.sock" }

// Login is what a run learns the first time it logs in to one destination,
// shared by every copy of the transport to it, as SudoAuth is: the login
// user's DOCKER_HOST, from its UID. Every command runs as that user, against
// its own rootless daemon, so the UID is asked once a run and every command
// after it carries the answer.
type Login struct {
	// Connected, when set, is called once, after the first command run
	// through this login has succeeded: the user exists on the host and
	// accepted the operator's key.
	Connected func()

	mu         sync.Mutex
	dockerHost string
}

// NewLogin returns a Login that calls connected after its first successful
// command, or nothing for nil.
func NewLogin(connected func()) *Login { return &Login{Connected: connected} }

// resolve returns t's DOCKER_HOST, asking the host for the login user's UID
// the first time. A failure is not kept: it is either ssh's, which the next
// command meets again, or an answer that was not a UID, which a second
// command may find fixed.
func (l *Login) resolve(t SSHTransport) (string, error) {
	l.mu.Lock()
	host, first, err := l.resolveLocked(t)
	l.mu.Unlock()
	// Outside the lock, so whatever Connected does cannot wait on a
	// command through this login.
	if first && l.Connected != nil {
		l.Connected()
	}
	return host, err
}

func (l *Login) resolveLocked(t SSHTransport) (host string, first bool, err error) {
	if l.dockerHost != "" {
		return l.dockerHost, false, nil
	}
	bare := t
	bare.Login, bare.DockerHost = nil, ""
	out, err := bare.run(uidProbe, nil)
	if err != nil {
		var p *ui.Problem
		if errors.As(err, &p) {
			return "", false, err
		}
		return "", false, fmt.Errorf("%s: %s: %w\n%s", t.Describe(), uidProbe, err, strings.TrimRight(out, "\n"))
	}
	uid := strings.TrimSpace(out)
	if uid == "" || strings.Trim(uid, "0123456789") != "" {
		return "", false, fmt.Errorf("%s: %s printed %q, not a user ID, so the user's Docker daemon cannot be found. A shell startup file that prints on a command without a terminal does this", t.Describe(), uidProbe, uid)
	}
	l.dockerHost = RootlessDockerHost(uid)
	return l.dockerHost, true, nil
}
```

- [ ] **Step 4: Add the fields, export DOCKER_HOST, and resolve the login**

In `internal/apply/transport.go`, after the `Auth` field (line 92, `Auth *SudoAuth`), insert:

```go
	// DockerHost, when set, is exported as DOCKER_HOST ahead of every
	// command (see RemoteCommand), so docker reaches the login user's own
	// rootless daemon and never the host's system socket.
	DockerHost string
	// Login, when set and DockerHost is empty, finds DockerHost from the
	// login user's UID on the transport's first command, and is shared by
	// every copy of the transport to one destination, so the UID is asked
	// once a run. See Login.
	Login *Login
```

In `SSHArgs` (line 176), replace

```go
	command = RemoteCommand(command)
```

with

```go
	command = RemoteCommand(t.DockerHost, command)
```

and in its comment (line 174) replace `// The command is sent as RemoteCommand wraps it, on both routes.` with `// The command is sent as RemoteCommand wraps it, with DockerHost, on both routes.`

Replace `RemoteCommand` and the last paragraph of its comment (lines 203-208):

```go
// The string is a third longer than the command, and it is still one
// argument: Linux allows 128 KiB in one (MAX_ARG_STRLEN), and anything large
// here (rendered files, SQL) goes on stdin, not in the command.
func RemoteCommand(command string) string {
	return `sh -c 'eval "$(printf %s ` + base64.StdEncoding.EncodeToString([]byte(command)) + ` | base64 -d)"'`
}
```

with

```go
// The string is a third longer than the command, and it is still one
// argument: Linux allows 128 KiB in one (MAX_ARG_STRLEN), and anything large
// here (rendered files, SQL) goes on stdin, not in the command.
//
// With dockerHost, the command runs after `export DOCKER_HOST=<dockerHost>`,
// inside the same encoded word, so every docker the command starts, in a
// pipeline or a subshell as much as at its head, reaches the login user's
// own daemon.
func RemoteCommand(dockerHost, command string) string {
	if dockerHost != "" {
		command = "export DOCKER_HOST=" + shellQuote(dockerHost) + "\n" + command
	}
	return `sh -c 'eval "$(printf %s ` + base64.StdEncoding.EncodeToString([]byte(command)) + ` | base64 -d)"'`
}
```

At the top of `run` (line 557, after `func (t SSHTransport) run(command string, stdin *string) (string, error) {`), insert before the `for` loop:

```go
	if t.Login != nil && t.DockerHost == "" {
		host, err := t.Login.resolve(t)
		if err != nil {
			return "", err
		}
		t.DockerHost = host
	}
```

and add one sentence to the end of `run`'s comment (after line 556): `// A transport with a Login asks its UID first, once a run (see Login).`

- [ ] **Step 5: Update the existing callers of `RemoteCommand`**

Every test call `apply.RemoteCommand(X)` becomes `apply.RemoteCommand("", X)`:

```bash
sed -i '' 's/apply\.RemoteCommand(\([^,)]*\))/apply.RemoteCommand("", \1)/g' internal/apply/transport_test.go internal/apply/sshmux_test.go internal/apply/remote_shell_test.go
```

Then check `TestRemoteCommandExportsTheDockerHost` was not rewritten (it already has two arguments, and the pattern does not match a comma): `grep -n 'RemoteCommand(' internal/apply/*_test.go`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/apply/`
Expected: PASS.

- [ ] **Step 7: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

No `CHANGELOG.md` bullet: nothing sets `DockerHost` or `Login` until Task 8.

```bash
git add internal/apply/login.go internal/apply/login_test.go internal/apply/transport.go internal/apply/transport_test.go internal/apply/sshmux_test.go internal/apply/remote_shell_test.go
git commit -m "feat: ssh transports point docker at the login user's rootless daemon

Each deployment runs its containers in its own user's rootless Docker,
whose socket is /run/user/<uid>/docker.sock. The transport now exports
DOCKER_HOST inside the encoded remote command, so every docker it starts
reaches that daemon and none falls back to the host's system socket, which
is the owner's and root's. The UID comes from id -u, asked once per
destination a run through a Login shared by every copy of the transport,
which also calls back once the first login has worked; the next tasks
record the user there. An answer that is not a number is refused rather
than built into a socket path.

No changelog bullet: nothing sets these fields until siteTransport does.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: A refused default user is a host that has not admitted the deployment

**Files:**
- Modify: `internal/apply/transport.go` (field after `Login`; `authProblem` at lines 480-507)
- Test: `internal/apply/transport_test.go`

**Interfaces:**
- Consumes: `SSHTransport` from Task 2.
- Produces: `SSHTransport.DefaultUser bool`. Task 8 sets it in `siteTransport`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/apply/transport_test.go`, after `TestAuthFailuresAreProblems` (the test ending at line 402):

```go
// A site that names no ssh.user is reached as the deployment's own user,
// which exists on a host only once its owner has admitted the deployment.
// A host that refuses that login is said to be one that has not admitted
// it, with the request to send its owner, rather than as a key problem.
func TestARefusedDefaultUserIsNotAdmitted(t *testing.T) {
	apply.ForgetContacts()
	t.Cleanup(apply.ForgetContacts)
	s := &sshScript{answers: []sshAnswer{{"paisans-f2a9@203.0.113.10: Permission denied (publickey).\n", 255}}}
	s.install(t)
	tr := apply.SSHTransport{Site: "home-a", User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true}
	_, err := tr.Run("true")
	if s.calls != 1 || errors.Is(err, apply.ErrUnreachable) {
		t.Errorf("retried or called unreachable: %d calls, %v", s.calls, err)
	}
	p := problemOf(t, err)
	if p.Hint != "site home-a is not admitted on 203.0.113.10" {
		t.Errorf("hint %q", p.Hint)
	}
	for _, want := range []string{"paisans-f2a9", "paisans host request --site home-a > home-a.admit", "sites.home-a.ssh.user"} {
		if !strings.Contains(p.Explain, want) {
			t.Errorf("explain lacks %q:\n%s", want, p.Explain)
		}
	}
}

// A user the site names is the operator's choice, so its refusal is a key
// problem, as before; so is a default user on a transport that names no
// site, which has no request to send.
func TestARefusedDeclaredUserIsAKeyProblem(t *testing.T) {
	for _, tr := range []apply.SSHTransport{
		{Site: "home-a", User: "ubuntu", Host: "203.0.113.10", PublicKeys: []string{keyA}},
		{User: "paisans-f2a9", Host: "203.0.113.10", PublicKeys: []string{keyA}, DefaultUser: true},
	} {
		apply.ForgetContacts()
		s := &sshScript{answers: []sshAnswer{{"Permission denied (publickey).\n", 255}}}
		s.install(t)
		_, err := tr.Run("true")
		if p := problemOf(t, err); !strings.Contains(p.Hint, "did not accept your ssh key") {
			t.Errorf("%+v: hint %q", tr, p.Hint)
		}
	}
	apply.ForgetContacts()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/apply/ -run 'TestARefusedDefaultUserIsNotAdmitted|TestARefusedDeclaredUserIsAKeyProblem'`
Expected: FAIL to compile, `unknown field DefaultUser in struct literal of type apply.SSHTransport`.

- [ ] **Step 3: Add the field and the problem**

In `internal/apply/transport.go`, after the `Login` field Task 2 added, insert:

```go
	// DefaultUser reports that User is the deployment's own user because the
	// site's ssh section names none. That user exists only on a host whose
	// owner has admitted the deployment, so a login it refuses is reported
	// as a host that has not (see authProblem).
	DefaultUser bool
```

In `authProblem`, replace the third case (lines 499-504):

```go
	case strings.Contains(last, "Permission denied (") || strings.Contains(last, "Too many authentication failures"):
		return &ui.Problem{
			Hint:    t.Describe() + " did not accept your ssh key",
			Explain: "Check that your key is loaded in your ssh agent (ssh-add -l), that its public half is under an admin the site's ssh.keys names, and that the host's authorized_keys for that user holds it.",
			Cause:   err,
		}
```

with

```go
	case (strings.Contains(last, "Permission denied (") || strings.Contains(last, "Too many authentication failures")) && t.DefaultUser && t.Site != "":
		return &ui.Problem{
			Hint:    fmt.Sprintf("site %s is not admitted on %s", t.Site, t.Host),
			Explain: fmt.Sprintf("The site names no ssh.user, so paisans logs in as the deployment's own user, %s, and %s refused it. That user exists only once the host's owner has admitted the deployment. Send them a request:\n\n    paisans host request --site %s > %s.admit\n\nIf they have admitted it, check that your key is loaded in your ssh agent (ssh-add -l) and is under an operator the site's ssh.keys names. If the deployment logs in as another user on that host, name it in sites.%s.ssh.user.", t.User, t.Host, t.Site, t.Site, t.Site),
			Cause:   err,
		}
	case strings.Contains(last, "Permission denied (") || strings.Contains(last, "Too many authentication failures"):
		return &ui.Problem{
			Hint:    t.Describe() + " did not accept your ssh key",
			Explain: "Check that your key is loaded in your ssh agent (ssh-add -l), that its public half is under an admin the site's ssh.keys names, and that the host's authorized_keys for that user holds it.",
			Cause:   err,
		}
```

Add to `authProblem`'s comment (after line 479): `// A refusal of the deployment's default user is a host that has not admitted the deployment.`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/apply/`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

No `CHANGELOG.md` bullet: nothing sets `DefaultUser` until Task 8, whose bullet covers it.

```bash
git add internal/apply/transport.go internal/apply/transport_test.go
git commit -m "feat: a refused default user says the host has not admitted the deployment

A site that leaves ssh.user out logs in as paisans-<token>, a user only the
host's owner creates, with host admit. Before that, ssh refuses the login,
and saying the operator's key was not accepted sends them to check a key
that is fine. The transport now knows when its user is that default, and
reports the refusal as a host that has not admitted the deployment, with
the host request command whose file the owner admits from. A declared
user's refusal stays a key problem.

No changelog bullet: siteTransport sets the field in a later commit, whose
bullet covers it.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The owner's local transport, `hostadmit.Local()`

`apply` cannot hand an unexported constructor to another package, so the transport lives in the package that will be its only user: `internal/hostadmit`, which phase 5 fills with `host admit` and `host evict`. In this phase the package holds `Local()` and nothing else, and a source walk refuses any caller outside it.

**Files:**
- Create: `internal/hostadmit/local.go`
- Test: `internal/hostadmit/local_test.go`

**Interfaces:**
- Consumes: `apply.Transport` (`internal/apply/transport.go:33-50`).
- Produces: `func Local() apply.Transport` in `internal/hostadmit`. Phase 5's `cmd/paisans/hostadmit.go` is its first caller and adds itself to the guard test's allowed list.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostadmit/local_test.go`:

```go
package hostadmit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

func TestLocalRunsCommandsWithSh(t *testing.T) {
	l := hostadmit.Local()
	out, err := l.Run("printf '%s' \"$((1 + 2))\"")
	if err != nil || out != "3" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	out, err = l.Run("echo partial; exit 3")
	if err == nil || out != "partial\n" || !strings.Contains(err.Error(), "echo partial; exit 3") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("a failing command: %q, %v", out, err)
	}
	out, err = l.RunInput("cat", "from stdin")
	if err != nil || out != "from stdin" {
		t.Errorf("RunInput = %q, %v", out, err)
	}
	if d := l.Describe(); !strings.HasPrefix(d, "this machine") {
		t.Errorf("Describe() = %q", d)
	}
}

// A write creates the parent directories, sets the mode and replaces the
// file whole, leaving no temporary file beside it; a missing file, and a
// directory, read as absent.
func TestLocalReadsAndWritesFiles(t *testing.T) {
	l := hostadmit.Local()
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "conf")
	if err := l.WriteFile(path, "one\n", 0o640); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteFile(path, "two\n", 0o600); err != nil {
		t.Fatal(err)
	}
	content, found, err := l.ReadFile(path)
	if err != nil || !found || content != "two\n" {
		t.Fatalf("ReadFile = %q, %v, %v", content, found, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", info.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left beside the file: %v", entries)
	}
	for _, absent := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "a")} {
		if _, found, err := l.ReadFile(absent); found || err != nil {
			t.Errorf("%s: found %v, %v", absent, found, err)
		}
	}
}

// The local transport runs a plan against the machine the CLI runs on, so
// it is reached only through host admit and host evict with --request,
// which run on the host itself. Neither exists yet, so nothing outside this
// package calls Local.
func TestOnlyHostAdmitReachesTheLocalTransport(t *testing.T) {
	allowed := map[string]bool{}
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/internal/hostadmit/") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Local" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "hostadmit" && !allowed[strings.TrimPrefix(path, "../../")] {
					t.Errorf("%s calls hostadmit.Local", strings.TrimPrefix(path, "../../"))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/hostadmit/`
Expected: FAIL, `no non-test Go files in .../internal/hostadmit` (or `undefined: hostadmit.Local`).

- [ ] **Step 3: Create `internal/hostadmit/local.go`**

```go
// Package hostadmit holds what runs on a host as its owner, as root, for one
// deployment: the steps that admit it and evict it. Every other command runs
// as the deployment's own user.
package hostadmit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// Local is the transport for a command run on the host itself: each command
// through sh -c on this machine, each file through the filesystem. It is how
// host admit and host evict run from a request, by the owner, with no ssh.
// It is reached only through this function, and only this package's
// commands call it, so a mistyped flag on a workstation cannot run a host's
// plan against the workstation.
func Local() apply.Transport { return local{} }

type local struct{}

func (local) Describe() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return "this machine (" + name + ")"
	}
	return "this machine"
}

func (l local) Run(command string) (string, error) { return l.run(command, nil) }

func (l local) RunInput(command, stdin string) (string, error) { return l.run(command, &stdin) }

// run runs command with sh -c, with stdin when it is not nil, and returns
// its combined output. A non zero exit is an error naming the command's
// first line, as the ssh transport's is.
func (l local) run(command string, stdin *string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %s: %w\n%s", l.Describe(), firstLine(command), err, strings.TrimRight(out.String(), "\n"))
	}
	return out.String(), nil
}

// ReadFile reads a regular file. A missing path, and one that is not a
// regular file, read as absent, as `[ -f ]` does over ssh.
func (local) ReadFile(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// WriteFile writes content to a temporary file beside path, sets its mode
// and renames it over path, creating the parent directories, so a failed
// write leaves the previous file whole.
func (local) WriteFile(path, content string, mode uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".paisans-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, os.FileMode(mode)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/hostadmit/`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

No `CHANGELOG.md` bullet: nothing calls `Local` yet.

```bash
git add internal/hostadmit/local.go internal/hostadmit/local_test.go
git commit -m "feat: hostadmit.Local runs a plan on the host itself

An owner without the deployment's configuration admits it from a request,
on the host, with no ssh. That needs a transport that runs each command
with sh -c on this machine. It is reached only through hostadmit.Local,
and a test refuses any caller outside the package, so no command that
changes a deployment can run a host's plan against the workstation it was
typed on. The package holds only this until host admit lands in it.

No changelog bullet: nothing calls it yet.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `apply.WGHelper`, the WireGuard helper's command lines

**Files:**
- Create: `internal/apply/helper.go`
- Test: `internal/apply/helper_test.go`

**Interfaces:**
- Consumes: `apply.Transport`, `shellQuote` (`internal/apply/transport.go:679-681`).
- Produces: `const HelperDir = "/usr/local/libexec/paisans"`; `type WGHelper struct { T Transport; Iface string }` with `Path() string`, `Put(conf []byte) error`, `Up() error`, `Restart() error`, `Sync() error`, `Show(field string) (string, error)`, `Log() (string, error)`. Phase 3 routes every `wg`, `wg-quick`, `systemctl ... wg-quick@` and `journalctl -u` through it.

- [ ] **Step 1: Write the failing test**

Create `internal/apply/helper_test.go`:

```go
package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// helperHost records each command and its stdin, and answers with out.
type helperHost struct {
	ran, stdin []string
	out        string
}

func (h *helperHost) Run(command string) (string, error) {
	h.ran = append(h.ran, command)
	return h.out, nil
}

func (h *helperHost) RunInput(command, stdin string) (string, error) {
	h.ran = append(h.ran, command)
	h.stdin = append(h.stdin, stdin)
	return h.out, nil
}

func (h *helperHost) ReadFile(string) (string, bool, error) { return "", false, nil }
func (h *helperHost) WriteFile(string, string, uint32) error { return nil }
func (h *helperHost) Describe() string                      { return "home-a" }

// Every verb is the helper host admit installs for the interface, run with
// sudo -n: the one command the deployment's user may run as root, which
// fails at once on a host without the sudoers line rather than waiting for
// a password.
func TestWGHelperCommandLines(t *testing.T) {
	h := &helperHost{out: "peer\n"}
	w := apply.WGHelper{T: h, Iface: apply.Fixture.Interface()}
	if got := w.Path(); got != "/usr/local/libexec/paisans/wg-psns-f2a9" {
		t.Errorf("Path() = %q", got)
	}
	if err := w.Put([]byte("[Interface]\nListenPort = 51820\n")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{w.Up, w.Restart, w.Sync} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"peers", "latest-handshakes", "public-key"} {
		if out, err := w.Show(field); err != nil || out != "peer\n" {
			t.Errorf("Show(%s) = %q, %v", field, out, err)
		}
	}
	if _, err := w.Log(); err != nil {
		t.Fatal(err)
	}
	const helper = "sudo -n '/usr/local/libexec/paisans/wg-psns-f2a9' "
	want := []string{helper + "put", helper + "up", helper + "restart", helper + "sync", helper + "show peers", helper + "show latest-handshakes", helper + "show public-key", helper + "log"}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(h.ran, "\n"), strings.Join(want, "\n"))
	}
	if len(h.stdin) != 1 || h.stdin[0] != "[Interface]\nListenPort = 51820\n" {
		t.Errorf("put's stdin: %q", h.stdin)
	}
}

// show takes only the fields the helper reads; anything else is refused
// here, before it reaches the host.
func TestWGHelperShowRefusesAnotherField(t *testing.T) {
	h := &helperHost{}
	for _, field := range []string{"", "dump", "peers; id", "private-key"} {
		if _, err := (apply.WGHelper{T: h, Iface: "psns-f2a9"}).Show(field); err == nil {
			t.Errorf("Show(%q) was not refused", field)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %q", h.ran)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/apply/ -run WGHelper`
Expected: FAIL to compile, `undefined: apply.WGHelper`.

- [ ] **Step 3: Create `internal/apply/helper.go`**

```go
package apply

import "fmt"

// HelperDir is where host admit installs each deployment's WireGuard helper.
const HelperDir = "/usr/local/libexec/paisans"

// WGHelper runs the WireGuard helper host admit installs for one deployment's
// interface. Creating the interface, writing its conf and syncing its peers
// need CAP_NET_ADMIN in the host's namespace, which the deployment's user
// does not have, so the helper is the one command that user may run as root,
// through one sudoers line, and it takes only these verbs.
//
// Every verb runs with sudo -n, so a host where the sudoers line is missing
// fails at once instead of waiting for a password nobody is there to type.
type WGHelper struct {
	T     Transport
	Iface string
}

// Path is the helper for this interface: HelperDir/wg-<iface>.
func (h WGHelper) Path() string { return HelperDir + "/wg-" + h.Iface }

func (h WGHelper) command(verb string) string {
	return "sudo -n " + shellQuote(h.Path()) + " " + verb
}

// Put sends conf on stdin, for the helper to check and install as the
// interface's wg-quick file.
func (h WGHelper) Put(conf []byte) error {
	_, err := h.T.RunInput(h.command("put"), string(conf))
	return err
}

// Up enables and starts the interface.
func (h WGHelper) Up() error {
	_, err := h.T.Run(h.command("up"))
	return err
}

// Restart restarts the interface.
func (h WGHelper) Restart() error {
	_, err := h.T.Run(h.command("restart"))
	return err
}

// Sync applies the installed conf's peers to the running interface without
// restarting it.
func (h WGHelper) Sync() error {
	_, err := h.T.Run(h.command("sync"))
	return err
}

// showFields are the fields the helper's show verb reads.
var showFields = map[string]bool{"peers": true, "latest-handshakes": true, "public-key": true}

// Show reads one field of the running interface, as `wg show <iface>
// <field>` prints it.
func (h WGHelper) Show(field string) (string, error) {
	if !showFields[field] {
		return "", fmt.Errorf("the WireGuard helper shows peers, latest-handshakes or public-key, not %q", field)
	}
	return h.T.Run(h.command("show " + field))
}

// Log is the interface unit's last journal lines.
func (h WGHelper) Log() (string, error) { return h.T.Run(h.command("log")) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/apply/ -run WGHelper`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

No `CHANGELOG.md` bullet: nothing calls the helper until phase 3.

```bash
git add internal/apply/helper.go internal/apply/helper_test.go
git commit -m "feat: apply.WGHelper builds the wireguard helper's commands

The mesh is the one thing a deployment needs root for after admission.
Host admit installs a helper per deployment that takes a fixed set of
verbs, and its user may run that and nothing else through sudo. WGHelper
is the toolkit's side of it: one command line per verb, each with sudo -n
so a missing sudoers line fails at once, and show limited to the fields the
helper reads. The mesh steps move onto it in the next phase.

No changelog bullet: nothing calls it yet.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Deployment commands run without sudo

`siteTransport` loses its `sudo` parameter, so `go build` lists every caller. `host deployments` is the owner's command and keeps sudo through `ownerTransport`, the one transport left that sets `Sudo` or `Auth`; a source walk refuses them anywhere else.

**The inventory.** Every `--sudo` flag in `cmd/paisans/*.go`, and what happens to it:

| Flag | File:line | Becomes |
|---|---|---|
| `init --sudo` | `cmd/paisans/main.go:247` | removed |
| `apply --sudo` | `cmd/paisans/main.go:511` | removed |
| `storage init --sudo` | `cmd/paisans/main.go:1111` | removed |
| `host prepare --sudo` | `cmd/paisans/main.go:1219` | removed |
| `app remove --sudo` | `cmd/paisans/appremove.go:51` | removed |
| `doctor --sudo` | `cmd/paisans/doctor.go:51` | removed |
| `failover test --sudo` | `cmd/paisans/failover.go:26` | removed |
| `gateway move --sudo` | `cmd/paisans/gateway.go:52` | removed |
| `mesh rekey --sudo` | `cmd/paisans/meshrekey.go:60` | removed |
| `preflight --sudo` | `cmd/paisans/preflight.go:26` | removed |
| `prune --sudo` | `cmd/paisans/prune.go:32` | removed |
| `storage rotate-key --sudo` | `cmd/paisans/rotatekey.go:30` | removed |
| `secrets prune --sudo` | `cmd/paisans/secrets.go:75` | removed |
| `secrets rotate --sudo` | `cmd/paisans/secretsrotate.go:99` | removed |
| `site add --sudo` | `cmd/paisans/site.go:40` | removed |
| `site remove --sudo` | `cmd/paisans/siteremove.go:53` | removed |
| `storage add --sudo` | `cmd/paisans/storage.go:34` | removed |
| `web-server show/check --sudo` | `cmd/paisans/webserver.go:58` | removed |
| `host deployments --sudo` | `cmd/paisans/hostdeployments.go:83` | **kept**: the owner's command, through `ownerTransport` |

Every `--sudo` a command hands another: `convergeFlags` (`cmd/paisans/converge.go:487-503`), `handOffArgs` (`cmd/paisans/gateway.go:156`), `secrets rotate`'s `storage rotate-key` (`cmd/paisans/secretsrotate.go:169`). All removed.

Every `settleSudo` call: `cmd/paisans/config.go:150` (`gatherAt`), `cmd/paisans/meshrekey.go:109`, `cmd/paisans/siteremove.go:179`, `:307`, `:404`, `cmd/paisans/secretsrotate.go:185`, `cmd/paisans/converge.go:688` (the `settle` closure, called at `:700` and `:776`), `cmd/paisans/main.go:1398` (`host prepare --all`). All removed, and `settleSudo` itself (`cmd/paisans/sudo.go:41-82`) is deleted with them, since `host deployments` reaches one host and its sudo is asked by its first command.

Every hard-wired sudo: `configSiteTransport` (`cmd/paisans/config.go:43-45`, `siteTransport(name, site, "", true)`), `config recover`'s `reachDestination(dest, true)` (`cmd/paisans/config.go:801`), `webServerHostVia`'s `h.Sudo` (`cmd/paisans/webserver.go:30-36`). All removed.

**Files:**
- Modify: `cmd/paisans/main.go` (flags at 247, 511, 1111, 1203, 1219; `initHosts` call 276; record hosts 300-305; `runConverge` options 529; `archHost`/`transport`/`applyRecordHosts`/`readPeers`/`siteTransport` calls 587, 616, 620, 702, 741, 860, 868, 883; `readPeers` 918-928; `siteApplied` 948-950; `storage init` 1151; `host prepare` 1287; `hostPrepareAll` 1381-1402; `siteTransport` 1624-1649)
- Modify: `internal/doctor/reach.go:10-44`, `cmd/paisans/doctor.go:313-320`
- Modify: `cmd/paisans/hostdeployments.go:19-30,83,98`, `cmd/paisans/sudo.go:41-82`, `cmd/paisans/claim.go:57-67`, `cmd/paisans/record.go:21-33,37-43,70-84,201-217`, `cmd/paisans/mesh.go:24-25,256-259`, `cmd/paisans/converge.go:377-386,403-434,455-465,473,487-503,671-703,776-790,811-825,905,1813-1814`, `cmd/paisans/converge_review.go:14-22,65,75`, `cmd/paisans/converge_operators.go:107,133`, `cmd/paisans/converge_pull.go:115`, `cmd/paisans/converge_webserver.go:60-62,112,172`, `cmd/paisans/converge_webserver_files.go:16-19,64-142`, `cmd/paisans/webserver.go:18-36,58,92,110`, `cmd/paisans/config.go:40-45,150,801`, `cmd/paisans/appremove.go:25-26,51,105,175-216`, `cmd/paisans/secrets.go:75,96-97`, `cmd/paisans/secretsrotate.go:29-30,99,169,183-187`, `cmd/paisans/gateway.go:24-25,52,85,88,120,143-156`, `cmd/paisans/meshrekey.go:17-18,60,107-111,126`, `cmd/paisans/siteremove.go:28-29,53,87,96,174-181,209,256,264-320,362,377-406`, `cmd/paisans/failover.go:26,37,41`, `cmd/paisans/preflight.go:26,43,72-75`, `cmd/paisans/prune.go:32,54`, `cmd/paisans/rotatekey.go:30,80,93`, `cmd/paisans/site.go:40,91,150`, `cmd/paisans/storage.go:34,71,83`, `cmd/paisans/doctor.go:51,81`, `cmd/paisans/operatormagiclink.go:70`, `cmd/paisans/app.go:179`, `cmd/paisans/mail.go:23`, `cmd/paisans/clients.go:83,599`, `cmd/paisans/help.go:137,194,260,361,386,393`
- Delete: `cmd/paisans/sudo_upfront_test.go`, `cmd/paisans/sudo_redact_test.go`
- Create: `cmd/paisans/sudo_guard_test.go`
- Modify tests: `cmd/paisans/transport_test.go`, `cmd/paisans/hostdeployments_test.go:72-74,320-340`, `cmd/paisans/webserver_test.go:74-92,115-133`, `cmd/paisans/site_webserver_test.go:236-252`, `cmd/paisans/converge_webserver_files_test.go:64-95,137,155,311,674-697`, `cmd/paisans/converge_webserver_test.go:49,59,361-366`, `cmd/paisans/claim_test.go:16,39-40`, `cmd/paisans/config_test.go:187-191,437`, `cmd/paisans/siteremove_test.go:28-33`, and every `--sudo=` in `cmd/paisans/*_test.go`
- Modify docs: `docs/commands.md`, `README.md`, `docs/guides/behind-your-own-web-server.md:406`, `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing new.
- Produces (in `cmd/paisans`):
  - `func siteTransport(name string, site config.Site, override string) apply.SSHTransport`
  - `func ownerTransport(dest config.Destination, sudo bool) apply.SSHTransport`, `var reachOwner func(dest config.Destination, sudo bool) apply.Transport`, `func destinationArg(dest config.Destination) string`
  - `var reachDestination func(dest config.Destination) apply.Transport`
  - `type recordHosts func(gw string) registry.Runner`
  - Every host-building var loses its `sudo bool` parameter: `registryHost(name, site, destination)`, `initHost(name, site)`, `convergeHost(name, site)`, `convergeGrantHost(cfg, site)`, `convergeRecord(cfg, site, g)`, `convergeReadInitial(cfg, member)`, `convergeHoldsGateway(cfg, site)`, `gatewayTransport(name, site)`, `removeHost(name, site)`, `rekeyHost(name, site)`, `rotateHost(name, site)`, `removeSiteHost(name, site)`, `webServerFilesHost(cfg, site)`, `webServerHost(cfg, site)`, `webServerHostVia(cfg, site, ssh)`.
  - Task 8 adds `Login` and `DefaultUser` inside `siteTransport`; Task 9 the write-back.

- [ ] **Step 1: Write the failing guard test**

Create `cmd/paisans/sudo_guard_test.go`:

```go
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// ownerSudo is every function allowed to give a transport sudo: the
// owner's own commands. Every command that acts on a deployment logs in as
// the deployment's user, which may run nothing through sudo but the
// WireGuard helper.
var ownerSudo = map[string]bool{
	"cmd/paisans/hostdeployments.go ownerTransport": true,
}

// Only the owner's transport runs through sudo. A Sudo or Auth field set on
// an SSHTransport anywhere else in the CLI's code is a deployment command
// reaching for root.
func TestOnlyTheOwnersTransportRunsThroughSudo(t *testing.T) {
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, "../../")
			for _, decl := range file.Decls {
				where := rel + " " + declName(decl)
				ast.Inspect(decl, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.CompositeLit:
						if !isSSHTransport(n.Type) {
							return true
						}
						for _, e := range n.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Sudo" || key.Name == "Auth") && !ownerSudo[where] {
									t.Errorf("%s sets %s on an SSHTransport", where, key.Name)
								}
							}
						}
					case *ast.AssignStmt:
						for _, lhs := range n.Lhs {
							if sel, ok := lhs.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Sudo" || sel.Sel.Name == "Auth") && !ownerSudo[where] {
								t.Errorf("%s assigns .%s", where, sel.Sel.Name)
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// declName is a declaration's name: a function's, or the first name a var
// declares, which is how this package names a replaceable function.
func declName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) > 0 {
				return v.Names[0].Name
			}
		}
	}
	return ""
}

func isSSHTransport(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name == "SSHTransport"
	case *ast.Ident:
		return e.Name == "SSHTransport"
	}
	return false
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/paisans/ -run TestOnlyTheOwnersTransportRunsThroughSudo`
Expected: FAIL: `cmd/paisans/main.go siteTransport sets Sudo on an SSHTransport` (twice), `cmd/paisans/main.go siteTransport assigns .Auth`, `cmd/paisans/webserver.go webServerHostVia assigns .Sudo` and `cmd/paisans/doctor.go Reach assigns .Sudo`. The last is doctor's record that sudo could not be used, which no doctor transport can meet once this task lands, so Step 3 removes it too.

- [ ] **Step 3: Make `siteTransport` sudo-free and add the owner's transport**

In `cmd/paisans/main.go`, replace `siteTransport` and its comment (lines 1624-1649) with:

```go
// siteTransport is how a command reaches a site: its ssh section, or the
// --ssh override verbatim. The override replaces the whole section rather
// than one part of it, so what is used is always either everything the file
// says or exactly what the operator typed, never a blend of the two.
//
// Every command that acts on a deployment reaches its sites this way, as the
// login user and without sudo: that user is the deployment's own, and the
// most a command can reach is what that user owns. name is the site's name
// in paisans-stack.yaml, for messages; it plays no part in reaching the host.
func siteTransport(name string, site config.Site, override string) apply.SSHTransport {
	if override != "" {
		return apply.SSHTransport{Site: name, Destination: override}
	}
	// validate has already refused a bad key, so problems are empty here.
	keys, _ := site.SSH.AuthorizedKeys()
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k.Line
	}
	return apply.SSHTransport{Site: name, User: site.SSHUser(), Host: site.SSHHost(), Port: site.SSH.PortOrDefault(), PublicKeys: lines}
}
```

In `cmd/paisans/hostdeployments.go`, replace `reachDestination` and its comment (lines 19-30) with:

```go
// destinationArg is dest as ssh takes it: user@host, or an ssh:// URL for a
// port other than 22, the one destination form that carries it.
func destinationArg(dest config.Destination) string {
	if dest.Port != 0 && dest.Port != config.DefaultSSHPort {
		return "ssh://" + dest.User + "@" + net.JoinHostPort(dest.Host, strconv.Itoa(dest.Port))
	}
	return dest.User + "@" + dest.Host
}

// reachDestination is how a command with no configuration reaches the host
// --ssh names as the deployment's user: the way the operator's own ssh
// reaches it, with their agent, keys and ssh config, since there are no
// declared keys to offer. Tests replace it.
var reachDestination = func(dest config.Destination) apply.Transport {
	return siteTransport("", config.Site{}, destinationArg(dest))
}

// ownerTransport is how host deployments reaches a host as its owner: the
// operator's own ssh to the destination --ssh names, through sudo unless
// --sudo=false, since the registry and every deployment's directories are
// root's. It is the one transport the CLI gives sudo: every other command
// logs in as the deployment's user, which has none.
func ownerTransport(dest config.Destination, sudo bool) apply.SSHTransport {
	t := apply.SSHTransport{Destination: destinationArg(dest), Sudo: sudo}
	if sudo {
		t.Auth = sudoAuthFor(t.Describe())
	}
	return t
}

// reachOwner is ownerTransport for host deployments. Tests replace it.
var reachOwner = func(dest config.Destination, sudo bool) apply.Transport { return ownerTransport(dest, sudo) }
```

and in `runHostDeployments` replace `t := reachDestination(dest, *sudo)` (line 98) with `t := reachOwner(dest, *sudo)`.

Doctor's sudo finding: `internal/doctor/reach.go`'s `ReachCommand` comment (lines 10-12) becomes `// ReachCommand is the trivial command that proves a site answers: ssh connected and the login worked.`; delete the `Sudo` field of `SiteReach` and its comment (lines 22-24) and the `case r.Sudo:` branch of `Reach` (lines 40-43), whose advice names `--sudo=false`. In `cmd/paisans/doctor.go`, delete `r.Sudo = errors.Is(err, apply.ErrSudo)` (line 317), and `errors` from its imports if unused.

In `cmd/paisans/sudo.go`, delete `settleSudo` and its comment (lines 41-82), and drop `"errors"` from the imports if the compiler says it is unused. Rewrite the comment on `sudoAuths` (lines 18-20) to: `// sudoAuths holds one SudoAuth per destination for the whole run, so the owner's transport (ownerTransport), built more than once, asks for its sudo password once.`

- [ ] **Step 4: Remove the flags and the `sudo` parameters**

Run `go build ./cmd/paisans/` and fix each error by the rules below; repeat until it builds. Every rule removes something; nothing is added.

1. A `sudo := fs.Bool("sudo", ...)` line from the inventory table: delete it, and every `*sudo` argument it fed. For `host prepare`, delete the `sudo` field from `hostPrepareOptions` (`main.go:1203`, `execute, sudo bool` becomes `execute bool`) and the `fs.BoolVar(&o.sudo, ...)` line (`main.go:1219`).
2. A host-building var or function with a `sudo bool` (or `sudo` among `execute, sudo bool`) parameter, listed under **Interfaces**, `claimSites(r, cfg, execute, sites...)`, `initHosts(cfg)`, `allSiteTransports(cfg)`, `readPeers(r, cfg, site, only)`, `siteApplied(r, cfg, configPath, site, ssh, written, err)`, `forgetInRecords(r, cfg, names, what, execute)`, `convergeGatewayMove(cfg)`, `convergeFounded(r, cfg)`, `convergeSecrets(r, cfg, path)`, `writeWebServerFiles(r, cfg, configPath, scope)`, `webServerStep(r, cfg, configPath, scope)`, `convergeWebServer(r, cfg, files, scope)`, `(webServerScope).recordHost(cfg, site)`, `(webServerScope).serverHost(cfg, site)`: drop the parameter and the argument at every call.
3. A `siteTransport(..., false)` or `siteTransport(..., true)` call (`operatormagiclink.go:70`, `app.go:179`, `mail.go:23`, `clients.go:83`, `clients.go:599`, `config.go:44`, `converge_operators.go:107,133`, `converge_pull.go:115`): drop the last argument.
4. Struct fields that only carried the flag: `convergeOptions.Sudo` (`converge.go:473`, `Execute, Sudo bool` becomes `Execute bool`; and `Sudo: *sudo` in `main.go:529`), `handOffFlags.sudo` (`gateway.go:145`, `sudo, verbose, keepImages bool` becomes `verbose, keepImages bool`; and `sudo: *sudo` at `gateway.go:88`), `forcedArgs.sudo` and `byIDArgs.sudo` (`siteremove.go:256`, `:362`; and `sudo: *sudo` at `:87`, `:96`): delete.
5. Every `settleSudo(...)` call in the inventory: delete the `if err := settleSudo(...); err != nil { return err }` statement. In `runConverge` (`converge.go:671-703`) delete `sudo` from `configPath, secretsPath, execute, sudo := o.Config, o.Secrets, o.Execute, o.Sudo`, the `settled`/`settle` closure and its comment (lines 680-690), the whole `if pre, err := config.LoadForInit(configPath); err == nil && ... { if err := settle(pre) ... }` block and the comment above it (it exists only to settle sudo before init), and `if err := settle(cfg); err != nil { return err }` (line 776).

Then these replacements, exactly:

`cmd/paisans/converge.go`, `convergeFlags` (lines 484-503): replace the comment's `--execute to all but init, --sudo to all but dns, and --min-free and` with `--execute to all but init, and --min-free and`, and delete

```go
	takesSudo := !(args[0] == "dns")
```

and

```go
	if takesSudo {
		out = append(out, fmt.Sprintf("--sudo=%t", o.Sudo))
	}
```

`cmd/paisans/gateway.go`, `handOffArgs`: delete `args = append(args, "--sudo="+strconv.FormatBool(f.sudo))` (line 156), and `strconv` from the imports if unused.

`cmd/paisans/secretsrotate.go:169`: replace

```go
			return runStorageRotateKey(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--execute", fmt.Sprintf("--sudo=%t", *sudo)))
```

with

```go
			return runStorageRotateKey(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--execute"))
```

`cmd/paisans/record.go`: replace the `recordHosts` type and its comment (lines 30-33) with

```go
// recordHosts reaches a gateway to read its deployment record, as the ssh
// user.
type recordHosts func(gw string) registry.Runner
```

`recordReaders`' loop body (line 40) with

```go
		h := hosts(gw)
		out[gw] = deployrecord.ReadAsUser(h, func() registry.Runner { return h })
```

and its comment (lines 35-36) with `// recordReaders is every gateway paisans-stack.yaml declares, each read as the ssh user.`; `applyRecordHosts` and its comment (lines 70-84) with

```go
// applyRecordHosts is how an apply of site reaches each gateway to read its
// deployment record: site itself through destination, the --ssh the apply
// was given, and every other gateway through its ssh section.
func applyRecordHosts(cfg *config.Config, site, destination string) recordHosts {
	return func(gw string) registry.Runner {
		if gw == site {
			return registryHost(gw, cfg.Sites[gw], destination)
		}
		return registryHost(gw, cfg.Sites[gw], "")
	}
}
```

and every `func(gw string, elevated bool) registry.Runner { return registryHost(gw, cfg.Sites[gw], "", elevated && X) }` (`appremove.go:175-176`, `converge.go:464-465`, `secrets.go:96-97`, `record.go:216-217`) with `func(gw string) registry.Runner { return registryHost(gw, cfg.Sites[gw], "") }`. In `runInit` (`main.go:300-305`) replace

```go
	warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, func(gw string, elevated bool) registry.Runner {
		if elevated {
			return hosts[gw]
		}
		return initHost(gw, cfg.Sites[gw], false)
	}))
```

with

```go
	warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, func(gw string) registry.Runner { return hosts[gw] }))
```

and at `main.go:620` call `applyRecordHosts(cfg, *site, *destination)`.

`cmd/paisans/webserver.go`: replace lines 18-36 with

```go
// webServerHost reaches a monitor site to read its web server, as the ssh
// user.
func webServerHost(cfg *config.Config, site string) webserver.Host {
	return webServerHostVia(cfg, site, "")
}

// webServerHostVia is webServerHost through ssh, verbatim, in place of the
// site's ssh section when it is set, as apply --site --ssh reaches it.
func webServerHostVia(cfg *config.Config, site, ssh string) webserver.Host {
	return webserver.Host{Plain: siteTransport(site, cfg.Sites[site], ssh)}
}
```

`cmd/paisans/converge_webserver_files.go:16-18`: the comment on `webServerFilesHost` becomes `// webServerFilesHost reaches a monitor site to write the toolkit's files for its web server, as the ssh user. Tests replace it.`

`cmd/paisans/config.go:40-45`: replace with

```go
// configSiteTransport is how a config command reaches a site paisans-stack.yaml
// declares: its ssh section. Tests replace it.
var configSiteTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}
```

and at `config.go:801` call `reachDestination(dest)`; in `siteremove.go:403` call `reachDestination(dest)`.

`cmd/paisans/help.go`: delete ` [--sudo=false]` from the usage strings at lines 137 (`init`), 194 (`site remove`), 361 (`secrets rotate`), 386 and 393 (`web-server show`, `web-server check`), and ` [--sudo]` at line 260 (`doctor`). Keep line 174 (`host deployments`).

Comments that say a deployment command's transport runs through sudo are rewritten to say it runs as the ssh user; find them with `grep -n sudo cmd/paisans/*.go | grep -v _test`. They are at `converge.go` (the comments around the removed `settle`, and `The deployment and the version open the run, ahead of sudo.` becomes `The deployment and the version open the run.`; `The read has a section of its own, as the sudo before it does, rather than ending whichever section came last.` becomes `The read has a section of its own, rather than ending whichever section came last.`), `main.go:1381-1382` (`Every sudo password is asked before the first site is reached.` is deleted), `doctor.go:203` and `doctor.go:239` (`through sudo` becomes `as the ssh user`), and `app.go:147-149`, whose `It asks without sudo, as the API calls themselves run:` becomes `It asks as the ssh user, as the API calls themselves run:`. Comments about `ownerTransport`, `sudoAuthFor`, `promptOnTerminal`, `readPassword`, `askPassword` and the web server's own reload commands (`sudo systemctl reload`) stay.

Run: `go build ./... && go vet ./cmd/paisans/`
Expected: builds; `go vet` lists the test files still using the old signatures.

- [ ] **Step 5: Update the tests**

Delete the two test files that exist only for the settled-up-front sudo, which no deployment command has:

```bash
git rm cmd/paisans/sudo_upfront_test.go cmd/paisans/sudo_redact_test.go
```

Remove every `--sudo=` argument a deployment command was given in a test (`host deployments`' at `hostdeployments_test.go:129` stays, so it is restored after):

```bash
sed -i '' -e 's/, "--sudo=false"//g' -e 's/"--sudo=false", //g' -e 's/, "--sudo=true"//g' -e 's/"--sudo=true", //g' cmd/paisans/*_test.go
git checkout cmd/paisans/hostdeployments_test.go
```

Multi-line argument lists the sed cannot see (an argument alone on its line, Eg: in `gateway_handoff_args_test.go`, or a `fmt.Sprintf("--sudo=%t", ...)`) remain: `grep -n -- '--sudo' cmd/paisans/*_test.go | grep -v hostdeployments_test.go` and delete each by hand.

Then `go vet ./cmd/paisans/` and fix each remaining error by the same rules as Step 4: a stub of a host-building var drops its `sudo bool` parameter and any assertion on it (Eg: in `claim_test.go`, `registryHost = func(_ string, _ config.Site, _ string) registry.Runner {` and delete the `sudo` field of the fake and `fake.sudo = sudo`); a `savedX(name, site, sudo)` call drops `sudo`; `reachDestination = func(dest config.Destination) apply.Transport` in `config_test.go:191,437` and `siteremove_test.go:29`.

These tests change in substance; replace them exactly.

`cmd/paisans/transport_test.go`, `TestSiteTransportUsesTheSectionOrTheOverride`, replace the body from `site := cfg.Sites["home-a"]` to the end with:

```go
	site := cfg.Sites["home-a"]
	got := siteTransport("home-a", site, "")
	keys, _ := site.SSH.AuthorizedKeys()
	var lines []string
	for _, k := range keys {
		lines = append(lines, k.Line)
	}
	want := apply.SSHTransport{Site: "home-a", User: "ubuntu", Host: "home-a.local", Port: 22, PublicKeys: lines}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("section transport:\n got %+v\nwant %+v", got, want)
	}
	if got := siteTransport("home-a", site, "admin@jump-alias"); !reflect.DeepEqual(got, apply.SSHTransport{Site: "home-a", Destination: "admin@jump-alias"}) {
		t.Fatalf("override transport: %+v", got)
	}
}
```

and change its comment's last sentence to `Either way the transport keeps the site's name, for messages.` In `TestSiteTransportDefaultsToTheDeploymentsUser`, `siteTransport("home-a", site, "", false)` becomes `siteTransport("home-a", site, "")`.

`cmd/paisans/hostdeployments_test.go`: at lines 72-74 stub `reachOwner` in place of `reachDestination`:

```go
	saved := reachOwner
	reachOwner = func(config.Destination, bool) apply.Transport { return h }
	t.Cleanup(func() { reachOwner = saved })
```

and replace `TestReachDestinationUsesTheOperatorsSSH` (lines 320-340) with:

```go
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
		if plain := ownerTransport(dest, false); plain.Sudo || plain.Auth != nil {
			t.Errorf("%s: owner with --sudo=false %+v", in, plain)
		}
	}
}
```

`cmd/paisans/webserver_test.go`: replace `TestWebServerHostSharesTheRunsSudo` and its comment (lines 74-92) with

```go
// The monitor's host is read as the ssh user, never through sudo.
func TestWebServerHostReadsAsTheSSHUser(t *testing.T) {
	h := webServerHost(fixture(t), "watch")
	if plain, ok := h.Plain.(apply.SSHTransport); !ok || plain.Sudo {
		t.Fatalf("plain %#v", h.Plain)
	}
	if h.Sudo != nil {
		t.Errorf("read through sudo: %#v", h.Sudo)
	}
}
```

(keep whatever loads `cfg` in the old test if `fixture(t)` is not the helper it used; the old body's first lines show it). In `TestWebServerShowPrintsTheDetectedServer` (lines 115-133), `webServerAsks{"health sudo=true"}` becomes `webServerAsks{"health sudo=false"}`, and delete the second `runWebServer` run and its assertion (the `if out2 := captureStdout(...); ... { ... }` statement).

`cmd/paisans/site_webserver_test.go`, replace `TestApplySiteWebServerStepReachesTheSiteAsTheApplyDid` and its comment (lines 236-252) with

```go
// apply --site --ssh's web server step reaches the monitor to read and write
// its site files through the destination it was given, as the ssh user.
func TestApplySiteWebServerStepReachesTheSiteAsTheApplyDid(t *testing.T) {
	cfg := fixture(t)
	scope := webServerScope{site: "watch", ssh: "root@203.0.113.9"}
	applied := siteTransport("watch", cfg.Sites["watch"], scope.ssh)
	record, ok := scope.recordHost(cfg, "watch").(apply.SSHTransport)
	if !ok || record.Describe() != applied.Describe() || record.Sudo {
		t.Errorf("record through %#v", record)
	}
	h := scope.serverHost(cfg, "watch")
	plain, _ := h.Plain.(apply.SSHTransport)
	if plain.Describe() != applied.Describe() || plain.Sudo || h.Sudo != nil {
		t.Errorf("web server through %#v, %#v", h.Plain, h.Sudo)
	}
}
```

`cmd/paisans/converge_webserver_files_test.go`: in `filesFake` rename `sudo []string` to `reached []string` with the comment `// reached is each site the web server was reached on.`, and replace the `webServerFilesHost` and `convergeHost` stubs (lines 79-92) with

```go
	webServerFilesHost = func(_ *config.Config, site string) webserver.Host {
		f.reached = append(f.reached, site)
		return webserver.Host{Plain: f.server.Plain()}
	}
	convergeHost = func(name string, site config.Site) apply.Transport {
		if name == "watch" {
			return f.manifest
		}
		return savedConverge(name, site)
	}
```

change `len(f.sudo)` to `len(f.reached)` at lines 137, 155 and 311, and replace `TestApplyWritesTheSiteFileWithTheRunsSudo` and its comment (lines 674-697) with

```go
// The site files are written as the ssh user, through the host webServerHost
// builds, which has no sudo.
func TestApplyWritesTheSiteFileAsTheSSHUser(t *testing.T) {
	fakeConverge(t, nil)
	path := externalFixture(t)
	f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
	recordConverge(t)
	if err := convergeAt(t, path, "--execute"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.reached, []string{"watch"}) {
		t.Errorf("reached %v", f.reached)
	}
	if got := webServerHost(fixture(t), "watch"); got.Sudo != nil {
		t.Errorf("sudo %#v", got.Sudo)
	}
}
```

`cmd/paisans/converge_webserver_test.go:361-365`: replace the comment

```go
	// The run's --sudo reaches the read: here --sudo=false, as every
	// converge test runs, since a settled sudo would reach a real host.
	// TestWebServerHostSharesTheRunsSudo shows a read with sudo shares the
```

and the line after it with `// The read is the ssh user's, with no sudo.`; and the comment at line 49 that says `through sudo` with `as the ssh user`.

Run: `go vet ./cmd/paisans/ && go test ./cmd/paisans/`
Expected: PASS, including `TestOnlyTheOwnersTransportRunsThroughSudo`.

- [ ] **Step 6: Update the documentation**

`docs/commands.md`:

* Line 45, the `--sudo` row of the global flags table, becomes:
  `| `--sudo` | on | `host deployments` | run its remote commands through sudo, since the registry is root's; `--sudo=false` skips it (see *A sudo that wants a password is asked for once, on a terminal* in `README.md`). Every other command runs as the site's ssh user, without sudo |`
* Delete `, `--sudo`` (or `--sudo` with the comma before it) from each **Flags:** list at lines 265, 304, 560, 619, 651, 732, 803, 824, 886, 923, 943, 959, 992, 1016, 1030 and 1156, keeping each list's final full stop. Line 605 (`host deployments`) keeps `--sudo`.
* Line 137-138: `* **Flags:** `--config`, `--secrets`, `--sudo` (on: each site's registry is root's), `--age-private-key-file <path>`` becomes `* **Flags:** `--config`, `--secrets`, `--age-private-key-file <path>``.
* Lines 266-268: `as the ssh user since it holds only names and is world readable, and through sudo only for one written with other modes. Changes none.` becomes `as the ssh user, since it holds only names and is world readable. Changes none.`
* Lines 561-563: delete `Every sudo password is asked first.`
* Line 770: `reaches the monitor through the same ssh and sudo as the rest of the apply,` becomes `reaches the monitor through the same ssh as the rest of the apply,`.
* Lines 1095-1096: `--sudo` (on: run remote commands through sudo).` is deleted, ending the list at `--execute`.
* Lines 1184-1186: `* **Flags:** `--app <name>` (required), `--config`, `--sudo` (on: read the web server through sudo where the ssh user cannot; `--sudo=false` reads as the ssh user alone).` becomes `* **Flags:** `--app <name>` (required), `--config`. It reads as the ssh user.`

`README.md`:

* Lines 1159-1160: `sudo is asked for once, and only when a command needs it; `--sudo=false` reads as the ssh user alone.` becomes `It reads as the ssh user.`
* Lines 4807-4810 (the opening of *A sudo that wants a password is asked for once, on a terminal*): replace

  ```
  Commands that read or change a host run through sudo, because `/srv`, `/etc`,
  the packages and the firewall are root's (`--sudo`, on by default). Before the
  first one on a site, the toolkit asks the host `sudo -n true`. `-n` makes sudo
  fail at once instead of waiting for a password, so the answer is immediate.
  ```

  with

  ```
  Every command that acts on a deployment logs in as the site's ssh user and
  runs nothing through sudo. `host deployments` is the owner's command: it reads
  a host's registry and every deployment's directories, which are root's, so it
  runs through sudo (`--sudo`, on by default). Before its first command, the
  toolkit asks the host `sudo -n true`. `-n` makes sudo fail at once instead of
  waiting for a password, so the answer is immediate.
  ```
* Lines 4822-4842 (from `A command that reaches several hosts asks every one of them before it reaches` through `asks before its first sudo command, as above.`), including the `sudo` block between them, become one sentence: `It asks before its first sudo command.`
* Lines 4926-4927: `--sudo=false` skips sudo entirely, and with it everything above.` becomes ``host deployments --sudo=false` skips sudo entirely, and with it everything above.`
* Lines 4978-4980: `so `--sudo=false` as a normal user is refused rather than reading every root listener as unowned.` becomes `so a run as a normal user is refused rather than reading every root listener as unowned.`
* Line 9078: `and it runs its commands through sudo unless told `--sudo=false`.` becomes `and it runs its commands as each site's ssh user.`
* Line 9109, the Leftovers row: `skipped under `--sudo=false`, since the inventory reads as root` becomes `skipped when the inventory cannot be read, since it reads as root`.

`docs/guides/behind-your-own-web-server.md:406`: delete the sentence ``--sudo=false` reads as the ssh user alone.`

Check: `grep -rn -- '--sudo' README.md docs/commands.md docs/guides/` prints only `host deployments` lines and the README sudo section's own `host deployments --sudo=false`.

`CHANGELOG.md`, under `[Unreleased]`, `### Removed`, add after the existing bullet:

```markdown
- `--sudo` is removed from every command but `host deployments`: each runs as the site's ssh user, and `apply`, `site remove`, `host prepare --all` and `config` ask no sudo password before they start.
```

- [ ] **Step 7: Run the whole suite**

Run: `gofmt -l cmd internal && go vet ./... && go test ./...`
Expected: `gofmt` prints nothing; PASS.

- [ ] **Step 8: Commit**

```bash
git add -A cmd/paisans internal/doctor/reach.go README.md docs/commands.md docs/guides/behind-your-own-web-server.md CHANGELOG.md
git commit -m "feat: deployment commands run without sudo

Each deployment runs as its own user, admitted once by the host's owner,
so the most a command, a bug in it or a hostile blueprint can reach is
that user. A deployment command that ran through sudo would undo that.
siteTransport loses its sudo parameter, and with it every --sudo flag,
every sudo settled before a run starts, and the hard-wired sudo of the
config commands and the web server read. doctor's finding for a sudo it
could not use goes too, since none of its transports has one. host
deployments is the owner's command and keeps sudo through ownerTransport,
the one transport that sets it; a test walks the source and refuses Sudo
or Auth set anywhere else.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Checks and messages that assumed the sudo transport

**Files:**
- Modify: `internal/preflight/preflight.go:222-249`, `internal/hostcheck/inventory.go:165-167`, `internal/webserver/detect.go:270-272`
- Test: `internal/preflight/preflight_test.go:68,236-248,444`, `internal/hostcheck/inventory_test.go:192`, `internal/webserver/detect_test.go:152-163`
- Modify docs: `README.md:4924-4925,8934`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 6 (no transport but the owner's runs through sudo).
- Produces: `preflight`'s ssh check runs `echo paisans-answers`.

- [ ] **Step 1: Write the failing tests**

In `internal/preflight/preflight_test.go`: in `healthy` (line 68) replace `{match: "sudo -n true"},` with `{match: "echo paisans-answers"},`; in `TestPreflightRefuses` replace the `"ssh unreachable"` case's rule match `"sudo -n true"` with `"echo paisans-answers"` and delete the cases `"sudo wants a password"` and `"sudo cannot be asked"` (lines 241-248); in `TestAnUnreachableNewSiteIsCheckedNoFurther` (line 444) replace `"sudo -n true"` with `"echo paisans-answers"`. Then append:

```go
// The ssh check asks only that the site answers, as its ssh user: no
// command here runs through sudo, so whether sudo works is not this check's
// question.
func TestTheSSHCheckAsksNoSudo(t *testing.T) {
	prepared(t)
	h := hosts()
	r := run(t, fixture(t), h)
	for name, host := range h {
		for _, c := range host.ran {
			if strings.Contains(c, "sudo") {
				t.Errorf("%s: ran %q", name, c)
			}
		}
	}
	if got := find(r, "home-a", "ssh"); len(got) != 1 || !strings.HasSuffix(got[0].Detail, " answers") {
		t.Errorf("ssh check: %+v", got)
	}
}
```

(If `find` returns a different type than `[]Check`, read its definition at the top of the file and compare its `Detail` the way the neighbouring tests do.)

In `internal/hostcheck/inventory_test.go:192` replace

```go
	if err == nil || !strings.Contains(err.Error(), "uid 1000") || !strings.Contains(err.Error(), "--sudo") {
```

with

```go
	if err == nil || !strings.Contains(err.Error(), "uid 1000") || strings.Contains(err.Error(), "--sudo") {
```

In `internal/webserver/detect_test.go`, replace `TestDetectWithoutSudoSaysDockerNeedsIt`'s comment and assertion (lines 152-163):

```go
// docker refused with no sudo to ask leaves the server undetermined, and
// says why, naming no flag.
func TestDetectWithoutSudoSaysDockerNeedsIt(t *testing.T) {
	plain := &fakeHost{answers: []answer{
		{key: "docker", out: "permission denied while trying to connect to the Docker daemon socket", err: errors.New("exit status 1")},
		{key: "ss -ltnp", out: "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"},
	}}
	ws := webserver.Detect(webserver.Host{Plain: plain}, external(t, "127.0.0.1:8480"))
	if ws.Server != "" || !strings.Contains(ws.Why, "docker refuses the ssh user") || strings.Contains(ws.Why, "--sudo") {
		t.Fatalf("got %+v", ws)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/preflight/ ./internal/hostcheck/ ./internal/webserver/`
Expected: FAIL: preflight refuses every site at ssh (the fake answers nothing to `sudo -n true` now, so the check falls to its default rule) or `TestTheSSHCheckAsksNoSudo` reports `ran "sudo -n true"`; hostcheck's message still contains `--sudo`; webserver's `Why` still contains `--sudo`.

- [ ] **Step 3: Change the checks and the messages**

`internal/preflight/preflight.go`: replace the comment and body of `ssh` (lines 222-249) with

```go
// reachProbe proves a site answers: a word only a shell that ran the command
// prints, so a login banner is not taken for an answer.
const reachProbe = "echo paisans-answers"

// ssh checks the site answers, as its ssh user.
func (r *runner) ssh(name string) {
	t, ok := r.transports[name]
	if !ok || t == nil {
		r.refuse(name, "ssh", "no way to reach this site was given, so nothing on it could be checked")
		return
	}
	if out, err := t.Run(reachProbe); err != nil {
		r.refuse(name, "ssh", "%s does not answer: %s", t.Describe(), firstLine(errText(out, err)))
		return
	}
	r.reachable[name] = true
	r.pass(name, "ssh", "%s answers", t.Describe())
}
```

and drop `"errors"` from its imports if the compiler says it is unused.

`internal/hostcheck/inventory.go:166`: replace

```go
		return nil, fail("asking who the probes run as", fmt.Errorf("they run as uid %s, not root, so ss cannot name the process behind another user's listener and every one of them would read as unowned. Run without --sudo=false, or as root", uid))
```

with

```go
		return nil, fail("asking who the probes run as", fmt.Errorf("they run as uid %s, not root, so ss cannot name the process behind another user's listener and every one of them would read as unowned", uid))
```

`internal/webserver/detect.go:271`: replace `"docker refuses the ssh user, and sudo is off (--sudo=false)"` with `"docker refuses the ssh user"`.

`README.md`:

* Lines 4924-4925: `` `preflight` and `doctor` report each of these as a sudo problem (`apply.ErrSudo`), separately from a host that does not answer, so the finding points at sudoers rather than at the network.`` becomes `` `host deployments` reports each of these as a sudo problem (`apply.ErrSudo`), separately from a host that does not answer, so it points at sudoers rather than at the network.``
* Line 8934, the preflight row: replace it with `| SSH | every site | `echo paisans-answers`, as the site's ssh user | the host does not answer, or ssh cannot log in |`.

`CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `preflight`'s ssh check asks only that each site answers as its ssh user, and `doctor` has no sudo finding, since no command they check for runs through sudo.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/preflight/ ./internal/hostcheck/ ./internal/webserver/ ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/preflight internal/hostcheck internal/webserver/detect.go internal/webserver/detect_test.go README.md CHANGELOG.md
git commit -m "feat: preflight stops checking sudo

preflight's ssh check ran sudo -n true, because every command ran through
sudo. None does now, and a deployment's user may run only the WireGuard
helper through sudo, so the check would refuse every admitted host. It
asks only that the site answers, with a word only a shell that ran the
command prints. The host check's refusal and the web server read's reason
named a flag that is gone; each says what it found and nothing more.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Sites are reached with their user's `DOCKER_HOST`, and a refused default user is "not admitted"

**Files:**
- Create: `cmd/paisans/login.go`
- Modify: `cmd/paisans/main.go` (`siteTransport`, as Task 6 left it)
- Test: `cmd/paisans/transport_test.go`, `cmd/paisans/hostdeployments_test.go`
- Modify docs: `README.md:4598-4601`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `apply.NewLogin(connected func()) *apply.Login` and `SSHTransport.Login` (Task 2), `SSHTransport.DefaultUser` (Task 3), `siteTransport(name, site, override)` and `ownerTransport` (Task 6).
- Produces: `func loginFor(destination string, connected func()) *apply.Login` in `cmd/paisans/login.go`; `siteTransport` sets `Login` on every transport and `DefaultUser` on a section transport whose site names no user. Task 9 passes `connected`.

- [ ] **Step 1: Write the failing tests**

In `cmd/paisans/transport_test.go`, add a helper and change `TestSiteTransportUsesTheSectionOrTheOverride` so it compares with `Login` cleared. Replace the test (as Task 6 left it) with:

```go
// resetLogins starts the test with no login known, so a Login from another
// test is not shared into this one.
func resetLogins(t *testing.T) {
	t.Helper()
	loginsMu.Lock()
	saved := logins
	logins = map[string]*apply.Login{}
	loginsMu.Unlock()
	t.Cleanup(func() {
		loginsMu.Lock()
		logins = saved
		loginsMu.Unlock()
	})
}

// A site's section becomes the transport's user, host, port and keys; --ssh
// replaces all four rather than one of them. Either way the transport keeps
// the site's name, for messages, and a Login shared by every transport to
// the same destination, so the user's UID is asked once a run.
func TestSiteTransportUsesTheSectionOrTheOverride(t *testing.T) {
	resetLogins(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	got := siteTransport("home-a", site, "")
	if got.Login == nil || got.Login != siteTransport("home-a", site, "").Login {
		t.Fatalf("want one shared Login per site, got %p", got.Login)
	}
	got.Login = nil
	keys, _ := site.SSH.AuthorizedKeys()
	var lines []string
	for _, k := range keys {
		lines = append(lines, k.Line)
	}
	want := apply.SSHTransport{Site: "home-a", User: "ubuntu", Host: "home-a.local", Port: 22, PublicKeys: lines}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("section transport:\n got %+v\nwant %+v", got, want)
	}
	over := siteTransport("home-a", site, "admin@jump-alias")
	if over.Login == nil {
		t.Fatal("the override has no Login")
	}
	over.Login = nil
	if !reflect.DeepEqual(over, apply.SSHTransport{Site: "home-a", Destination: "admin@jump-alias"}) {
		t.Fatalf("override transport: %+v", over)
	}
}
```

Replace `TestSiteTransportDefaultsToTheDeploymentsUser` with:

```go
// A site that leaves ssh.user out is reached as the deployment's own user,
// and its transport knows that user is the default, so a host that refuses
// it is reported as one that has not admitted the deployment. A declared
// user, and a destination given with --ssh, are the operator's choice.
func TestSiteTransportDefaultsToTheDeploymentsUser(t *testing.T) {
	resetLogins(t)
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	site := cfg.Sites["home-a"]
	if siteTransport("home-a", site, "").DefaultUser {
		t.Error("a declared user is taken for the default")
	}
	site.SSH.User = ""
	got := siteTransport("home-a", site, "")
	if got.User != "paisans-f2a9" || !got.DefaultUser {
		t.Errorf("user %q, default %v; want paisans-f2a9, the default", got.User, got.DefaultUser)
	}
	if siteTransport("home-a", site, "paisans-f2a9@home-a.local").DefaultUser {
		t.Error("an --ssh destination is taken for the default")
	}
	if siteTransport("", config.Site{}, "").DefaultUser {
		t.Error("a site with no deployment behind it is taken for the default")
	}
}
```

In `cmd/paisans/hostdeployments_test.go`, `TestReachDestinationUsesTheOperatorsSSH` (as Task 6 left it), add to the loop after the `reachDestination` check:

```go
		if tr.Login == nil {
			t.Errorf("%s: a deployment command's transport has no Login", in)
		}
		if owner.Login != nil {
			t.Errorf("%s: the owner's transport points docker at a user's daemon", in)
		}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/paisans/ -run 'TestSiteTransport|TestReachDestinationUsesTheOperatorsSSH'`
Expected: FAIL to compile, `undefined: loginsMu` and `undefined: logins`.

- [ ] **Step 3: Create `cmd/paisans/login.go` and wire it**

```go
package main

import (
	"sync"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// logins holds one apply.Login per destination for the whole run, so a site
// whose transport is built several times (a map of every site, then the one
// a stage needs) asks its login user's UID once.
var (
	loginsMu sync.Mutex
	logins   = map[string]*apply.Login{}
)

// loginFor is the Login every transport to destination shares in this run.
// connected is kept from the first call for a destination, which is the
// call that made its Login.
func loginFor(destination string, connected func()) *apply.Login {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if l, ok := logins[destination]; ok {
		return l
	}
	l := apply.NewLogin(connected)
	logins[destination] = l
	return l
}
```

In `cmd/paisans/main.go`, replace the body of `siteTransport` (as Task 6 left it) and add to its comment:

```go
// siteTransport is how a command reaches a site: its ssh section, or the
// --ssh override verbatim. The override replaces the whole section rather
// than one part of it, so what is used is always either everything the file
// says or exactly what the operator typed, never a blend of the two.
//
// Every command that acts on a deployment reaches its sites this way, as the
// login user and without sudo: that user is the deployment's own, and the
// most a command can reach is what that user owns. Its first command asks
// the user's UID, once a run (loginFor), and every command runs with
// DOCKER_HOST at that user's rootless daemon. A site that names no user is
// reached as the deployment's default, and its transport says so, so a
// host that refuses it is reported as not admitted. name is the site's name
// in paisans-stack.yaml, for messages; it plays no part in reaching the host.
func siteTransport(name string, site config.Site, override string) apply.SSHTransport {
	var t apply.SSHTransport
	if override != "" {
		t = apply.SSHTransport{Site: name, Destination: override}
	} else {
		// validate has already refused a bad key, so problems are empty here.
		keys, _ := site.SSH.AuthorizedKeys()
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k.Line
		}
		user := site.SSHUser()
		t = apply.SSHTransport{Site: name, User: user, Host: site.SSHHost(), Port: site.SSH.PortOrDefault(), PublicKeys: lines, DefaultUser: site.SSH.User == "" && user != ""}
	}
	t.Login = loginFor(t.Describe(), nil)
	return t
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/paisans/`
Expected: PASS. If a test elsewhere compares a whole `apply.SSHTransport` from `siteTransport` with `reflect.DeepEqual`, clear `Login` on the value first, as `TestSiteTransportUsesTheSectionOrTheOverride` does.

- [ ] **Step 5: Document it**

`README.md`, after the paragraph ending `The user must exist on the host either way.` (lines 4598-4601), add:

```markdown
A host refuses that user until its owner has admitted the deployment, so a
command whose login as the default user is refused says the site is not
admitted there, with the `paisans host request --site <site>` line whose file
the owner admits it from, rather than that a key was not accepted.

Every command runs docker against the login user's own rootless daemon: its
first command on a site asks `id -u`, once a run, and every command after it
runs with `DOCKER_HOST=unix:///run/user/<uid>/docker.sock`, so docker never
reaches the host's system socket.
```

`CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- Docker commands on a site reach the login user's rootless daemon at `/run/user/<uid>/docker.sock`, the uid read once a run, and a refused default user is reported as a site not admitted.
```

- [ ] **Step 6: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/paisans/login.go cmd/paisans/main.go cmd/paisans/transport_test.go cmd/paisans/hostdeployments_test.go README.md CHANGELOG.md
git commit -m "feat: every site is reached with its user's docker host

siteTransport now gives every transport a Login shared by every transport
to the same destination, so a command's first ssh to a site asks the login
user's UID once and every command after it runs docker against that user's
rootless daemon. A site that names no ssh.user is marked as reached by the
deployment's default user, so a host that refuses the login says the site
is not admitted, with the host request line, rather than blaming a key.
The owner's transport has no Login: it reads the host as root.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: The first login records the default user in `ssh.user`

**Files:**
- Modify: `cmd/paisans/login.go`, `cmd/paisans/main.go` (`siteTransport`), `cmd/paisans/flags.go:47-60` (`commonFlags`)
- Test: `cmd/paisans/login_test.go` (create)
- Modify docs: `README.md:4567,4598-4601`, `examples/paisans-stack.example.yaml:98`, `CHANGELOG.md`

**Interfaces:**
- Consumes: `(config.Site).SSHUserFile() string` (Task 1), `config.SetSSHUser(path, site, user string) error` (phase 1), `loginFor` (Task 8), `apply.Login.Connected` (Task 2).
- Produces: `func recordSSHUser(path, site, user string) func()`, `func routeLoginNotes(r ui.Reporter)`, `var loginReporter ui.Reporter` in `cmd/paisans/login.go`.

The write happens at the site's first login in the run. In `apply` without `--site` that is the init read or the architecture read, both before the run gathers and publishes the file, so every step applies the bytes the run published.

- [ ] **Step 1: Write the failing tests**

Create `cmd/paisans/login_test.go`:

```go
package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// twoDataSitesWithoutUsers is twoDataSites with both sites leaving ssh.user
// out, so each is reached as paisans-f2a9.
var twoDataSitesWithoutUsers = strings.ReplaceAll(twoDataSites, "user: ubuntu, ", "")

// loginNotes routes the login notes to a recorder for the test.
func loginNotes(t *testing.T) *ui.Recorder {
	t.Helper()
	rec := &ui.Recorder{}
	saved := loginReporter
	routeLoginNotes(rec)
	t.Cleanup(func() { loginReporter = saved })
	return rec
}

// The first login as the deployment's default user writes the name into
// the site's ssh.user, and says so, so the file names who logs in. Only
// that site is written.
func TestTheFirstLoginRecordsTheDefaultUser(t *testing.T) {
	resetLogins(t)
	rec := loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	tr := siteTransport("home-a", cfg.Sites["home-a"], "")
	if tr.Login == nil || tr.Login.Connected == nil {
		t.Fatal("the default user's login records nothing")
	}
	tr.Login.Connected()
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reread.Sites["home-a"].SSH.User; got != "paisans-f2a9" {
		t.Errorf("home-a's ssh.user is %q, want paisans-f2a9", got)
	}
	if got := reread.Sites["home-b"].SSH.User; got != "" {
		t.Errorf("home-b's ssh.user is %q, want it left out", got)
	}
	if !rec.Has("note", "home-a: ssh.user paisans-f2a9 recorded in paisans-stack.yaml") {
		t.Errorf("no note: %+v", rec.Events)
	}
}

// A declared user is recorded already, and a destination typed with --ssh
// is not the site's section, so neither writes anything.
func TestOnlyTheDefaultUsersLoginIsRecorded(t *testing.T) {
	resetLogins(t)
	_, cfg := archFixture(t, twoDataSites)
	if l := siteTransport("home-a", cfg.Sites["home-a"], "").Login; l.Connected != nil {
		t.Error("a declared user's login records")
	}
	_, cfg = archFixture(t, twoDataSitesWithoutUsers)
	if l := siteTransport("home-b", cfg.Sites["home-b"], "paisans-f2a9@203.0.113.7").Login; l.Connected != nil {
		t.Error("an --ssh login records")
	}
}

// Sites logged in to at once, as doctor reaches them, each record their
// user: the writes take turns, and the file loads with both.
func TestLoginsAtOnceEachRecordTheirUser(t *testing.T) {
	resetLogins(t)
	loginNotes(t)
	path, cfg := archFixture(t, twoDataSitesWithoutUsers)
	var wg sync.WaitGroup
	for _, name := range []string{"home-a", "home-b"} {
		connected := siteTransport(name, cfg.Sites[name], "").Login.Connected
		wg.Add(1)
		go func() {
			defer wg.Done()
			connected()
		}()
	}
	wg.Wait()
	reread, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home-a", "home-b"} {
		if got := reread.Sites[name].SSH.User; got != "paisans-f2a9" {
			t.Errorf("%s's ssh.user is %q", name, got)
		}
	}
}

// A user written with no value cannot be filled in place. The login it
// follows has worked, so the command is told and goes on, and the file is
// left as it was.
func TestAUserThatCannotBeRecordedIsAWarning(t *testing.T) {
	resetLogins(t)
	rec := loginNotes(t)
	body := strings.Replace(twoDataSitesWithoutUsers, "ssh: {host: home-a.local, keys: [alice]}", "ssh: {host: home-a.local, user: , keys: [alice]}", 1)
	path, cfg := archFixture(t, body)
	before, _ := os.ReadFile(path)
	siteTransport("home-a", cfg.Sites["home-a"], "").Login.Connected()
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("the file changed:\n%s", after)
	}
	i := rec.Index("note", "home-a: ssh.user paisans-f2a9 could not be recorded in paisans-stack.yaml")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "written with no value") {
		t.Errorf("no warning: %+v", rec.Events)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/paisans/ -run 'Login|Recorded'`
Expected: FAIL to compile, `undefined: loginReporter` and `undefined: routeLoginNotes`.

- [ ] **Step 3: Write the recording**

Append to `cmd/paisans/login.go` (adding `"fmt"`, `config` and `ui` imports):

```go
// loginReporter is the reporter a recorded login user is told through: the
// running command's, set by commonFlags (routeLoginNotes), as the retry lines
// are. Nil says nothing.
var loginReporter ui.Reporter

// routeLoginNotes sends the notes recordSSHUser makes to r.
func routeLoginNotes(r ui.Reporter) { loginReporter = r }

// recordMu takes turns between recordSSHUser's writes: a command that
// reaches several sites at once, as doctor does, can log in to two of them
// together, and SetSSHUser reads the file and writes it whole.
var recordMu sync.Mutex

// recordSSHUser is what a transport calls once it has logged in to site as
// user, the deployment's own, which the declaration at path leaves out of
// ssh.user: it writes the name there, so the file says who logs in, and a
// later run reads it as declared. The login it records has already worked,
// so a write that fails is said and is never the command's failure.
func recordSSHUser(path, site, user string) func() {
	return func() {
		recordMu.Lock()
		defer recordMu.Unlock()
		err := config.SetSSHUser(path, site, user)
		r := loginReporter
		if r == nil {
			return
		}
		if err != nil {
			r.Note(fmt.Sprintf("%s: ssh.user %s could not be recorded in %s", site, user, config.FileName),
				err.Error()+". The command goes on as "+user+"; write user: "+user+" under sites."+site+".ssh yourself, or leave it for the next command to try.")
			return
		}
		r.Note(fmt.Sprintf("%s: ssh.user %s recorded in %s", site, user, config.FileName), "")
	}
}
```

In `siteTransport` (`cmd/paisans/main.go`), replace

```go
	t.Login = loginFor(t.Describe(), nil)
	return t
```

with

```go
	var connected func()
	if path := site.SSHUserFile(); override == "" && name != "" && path != "" {
		connected = recordSSHUser(path, name, site.SSHUser())
	}
	t.Login = loginFor(t.Describe(), connected)
	return t
```

and add to its comment, before `name is the site's name`: `Its first login as that default writes the name into the site's ssh.user (recordSSHUser).`

In `cmd/paisans/flags.go`, `commonFlags`, after `routeKeyNotices(r)` add `routeLoginNotes(r)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 5: Document it**

`README.md:4567` and `examples/paisans-stack.example.yaml:98`: the comment `# optional: defaults to paisans-<token>; must already exist on the host` becomes `# optional: defaults to paisans-<token>, written here on its first login`. Keep the column the comment starts at.

`README.md`, in the paragraph Task 8 added after `The user must exist on the host either way.`, after its first sentence, add: `The first command that logs in as the default writes it into `user`, so the file names who logs in; a name already there is never changed, and a write that fails is a warning, since the login it records has worked.`

`CHANGELOG.md`, under `[Unreleased]`, `### Added`, add:

```markdown
- The first command that logs in to a site leaving `ssh.user` out writes the deployment's user there; a write that fails is a warning, and the command carries on.
```

- [ ] **Step 6: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/paisans/login.go cmd/paisans/login_test.go cmd/paisans/main.go cmd/paisans/flags.go README.md examples/paisans-stack.example.yaml CHANGELOG.md
git commit -m "feat: the first login records the deployment's user in ssh.user

A name in the file is the name used, so once a command has logged in as
the deployment's default user the CLI writes it into sites.<site>.ssh.user.
It happens at the site's first login in the run, which in apply is before
the file is published, and it takes turns with other sites' writes so a
command reaching several at once loses none. The login has already worked,
so a write that fails is a note and the command goes on. A destination
typed with --ssh is not the site's section and records nothing.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: `paisans create` offers the deployment's own user

**Files:**
- Modify: `internal/create/wizard.go:71` (`Wizard` and `NewWizard`), `:245`, `:299`, `:337-349` (`ssh`), `:566`
- Modify: `internal/create/paisans-stack.yaml.tmpl:66`, `cmd/paisans/create.go:54`
- Test: `internal/create/wizard_test.go`; regenerate `internal/create/testdata/*.yaml`
- Modify docs: `CHANGELOG.md`

**Interfaces:**
- Consumes: `deployment.Deployment{ID: id}.Prefix()`.
- Produces: `Wizard.DefaultUser string`, set by `runCreate`; `Site.SSHUser` may be `""`, which renders no `user:` line.

- [ ] **Step 1: Write the failing tests**

Append to `internal/create/wizard_test.go`:

```go
// The SSH user may be left empty: the machine is then reached as the
// deployment's own user, which the question names, and the file writes no
// user for it. A name given is written.
func TestTheSSHUserMayBeLeftEmpty(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, appAnswers(false)...)
	lines = append(lines, mailAnswers...)
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(script(lines...)), &out))
	w.DefaultUser = "paisans-f2a9"
	if err := w.Ask(SectionAll); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "SSH user (blank: paisans-f2a9, the deployment's own): ") {
		t.Errorf("the question does not offer the deployment's user:\n%s", out.String())
	}
	if strings.Contains(out.String(), "must already exist") {
		t.Errorf("the question still asks for an existing user:\n%s", out.String())
	}
	data, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user:") {
		t.Errorf("a user was written:\n%s", data)
	}
	for _, f := range check(t, data).Refusals() {
		t.Errorf("refused %s: %s: %s", f.Rule, f.Key, f.Message)
	}

	w.Answers.Sites[0].SSHUser = "admin"
	data, err = Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "      user: admin\n") {
		t.Errorf("a given user was not written:\n%s", data)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/create/ -run TestTheSSHUserMayBeLeftEmpty`
Expected: FAIL to compile, `w.DefaultUser undefined`.

- [ ] **Step 3: Make the user optional**

`internal/create/wizard.go`: add the field to `Wizard` (its struct is above `NewWizard`, line 71; read it and add):

```go
	// DefaultUser is the deployment's own user, paisans-<token>, which a
	// machine whose SSH user is left empty is reached as. create sets it
	// from the id it made; "" names it generically.
	DefaultUser string
```

Replace `SSHUser: "ubuntu"` with nothing in the three defaults: `d := Site{Name: ss.name, SSHUser: "ubuntu"}` (line 245) becomes `d := Site{Name: ss.name}`, `d := Site{Name: "site-" + strconv.Itoa(i+1), SSHUser: "ubuntu"}` (line 299) becomes `d := Site{Name: "site-" + strconv.Itoa(i+1)}`, and `d := Site{Name: monitorSite, SSHUser: "ubuntu"}` (line 566) becomes `d := Site{Name: monitorSite}`.

Replace the end of `ssh` (lines 343-348):

```go
	user := d.SSHUser
	if user == "" {
		user = "ubuntu"
	}
	site.SSHUser, err = w.p.Text("SSH user (must already exist)", user, checkUnixUser)
	return err
```

with

```go
	own := "the deployment's own user"
	if w.DefaultUser != "" {
		own = w.DefaultUser + ", the deployment's own"
	}
	site.SSHUser, err = w.p.Optional("SSH user (blank: "+own+")", d.SSHUser, checkUnixUser)
	return err
```

and add to `ssh`'s comment: `The user may be left empty: the machine is then reached as the deployment's own user, which host admit creates.`

`internal/create/paisans-stack.yaml.tmpl`, replace line 66:

```
      user: {{q .SSHUser}}
```

with

```
{{- if .SSHUser}}
      user: {{q .SSHUser}}
{{- end}}
```

`cmd/paisans/create.go`, after `w := create.NewWizard(p)` (line 54), add:

```go
	w.DefaultUser = deployment.Deployment{ID: id}.Prefix()
```

(`deployment` is already imported there for `deployment.NewID`.)

- [ ] **Step 4: Regenerate the golden files and read the diff**

Run: `go test ./internal/create/ -run TestWizardWritesEachNamedShape -update && git diff internal/create/testdata/`
Expected: the only change in each golden file is the removal of every `      user: ubuntu` line, since the scripted answers press Enter at the SSH user.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/create/ ./cmd/paisans/`
Expected: PASS.

- [ ] **Step 6: Changelog**

`CHANGELOG.md`, under `[Unreleased]`, `### Changed`, add:

```markdown
- `paisans create` asks each machine's SSH user as optional, offering the deployment's own user, `paisans-<token>`, when left blank, rather than defaulting to `ubuntu`.
```

- [ ] **Step 7: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/create cmd/paisans/create.go CHANGELOG.md
git commit -m "feat: create offers the deployment's own user for ssh

Each deployment logs in as its own user, paisans-<token>, which host admit
creates; a site that names none is reached as it. create asked for a user
that must already exist and suggested ubuntu, a shared login with sudo,
which is what this design moves away from. The question now offers the
deployment's own user by name and may be left blank, and the file then
writes no user for the machine.

Founder decision.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-Review Notes

* **Spec coverage.** `DockerHost` and `RemoteCommand` (Task 2); the local transport and its "only admit and evict" test (Task 4); `siteTransport` with `SSHUser`, the UID once a run, no sudo, every `--sudo` and `settleSudo` (Tasks 6 and 8); `WGHelper` (Task 5); not admitted (Tasks 3 and 8); write-back and the four `SetSSHUser` shapes (Tasks 1 and 9); the create prompt (Task 10); the guard test that walks the transports built with sudo (Task 6). The spec's "Admit plans the same steps from `--request` as from `--site`" and every admit, evict, request and helper-script item belong to phases 3 to 5.
* **Left to later phases, on purpose.** `deployrecord.ReadAsUser` keeps its elevated reader, now given the same transport, until phase 6 moves the record under the deployment's directory. `internal/webserver`'s `Host.Sudo` stays, unset by every command, until the founder's answer on `web_server.managed_by: external` (phase 6). `host prepare`, `hostcheck.Inspect` and the paths under `/var/lib/paisans` and `/etc` still assume root; phases 3 to 6 move them.
