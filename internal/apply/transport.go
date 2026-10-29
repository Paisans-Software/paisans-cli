// Package apply pushes rendered artifacts to a host and takes the narrowest
// action that makes them live.
//
// Everything here is staged and every stage is a gate that stops rather than
// warning, because the blast radius lands on somebody else's stack. The order
// is fixed: read what is on the host, refuse if anybody edited it, write, bring
// up the mesh, check the assembled gateway configuration, act on the
// infrastructure stack, create clustered apps' databases, and only then
// restart or recreate any app. Every stack acted on must come up healthy
// before the next one moves.
package apply

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Transport is the one place this package talks to a machine. Everything else
// works in terms of files and commands, which is what makes the whole planner
// testable without a host in the room.
type Transport interface {
	// Run executes a command and returns its combined output. A non zero exit
	// is an error: a caller that wants to tolerate one says so by inspecting
	// the error, never by ignoring it.
	Run(command string) (string, error)
	// ReadFile returns a file's contents. A missing file is reported through
	// the second result rather than as an error, because "not there yet" is the
	// ordinary case on a first apply.
	ReadFile(path string) (content string, found bool, err error)
	// WriteFile writes content at path with mode, creating parent directories.
	WriteFile(path string, content string, mode uint32) error
	// RunInput is Run with stdin. It exists for input that must not appear
	// in a command line, such as SQL carrying a password: a command line is
	// visible in `ps` to every user on the host, and stdin is not.
	RunInput(command, stdin string) (string, error)
	// Describe names the destination, for messages.
	Describe() string
}

// SSHTransport runs commands through the operator's own ssh binary.
//
// Shelling out rather than embedding an SSH client is deliberate, and it is the
// opposite of the choice made for sops and age. Authentication is the
// operator's: their agent, their keys, their `~/.ssh/config` with its jump
// hosts and per host users, their `known_hosts`. An embedded client would have
// to reimplement all of that or, far worse, invite a toolkit specific way to
// hand it a private key. sops is embedded because its alternative is asking an
// operator to install a binary; ssh is not, because every operator already has
// one and it already knows things this toolkit should never learn.
//
// A site's ssh section supplies User, Host, Port and PublicKeys. Destination
// is the `--ssh` escape hatch: when it is set it is passed to ssh verbatim, and
// the other four are not used at all, so an operator whose route the section
// cannot describe still has one.
type SSHTransport struct {
	// Site is the site's name in paisans-stack.yaml, when the transport reaches
	// one. It is never part of the ssh command; it names the site where the
	// operator is asked something, so a prompt says which site it is for
	// rather than only an address the operator may not recognise.
	Site string
	// Destination, when set, is whatever ssh accepts (a host, a user@host,
	// or an alias from the operator's config), passed as given.
	Destination string
	// DestinationPort, with Destination, is ssh's -p, given with the
	// destination after --, so a port the operator wrote goes ahead of the
	// ssh config's Port. Destination is then user@host, the host unbracketed
	// even for IPv6, as ssh reads a host given alone, and User and Host name
	// the same login, for messages. Zero gives no -p, and Destination stays
	// as given.
	DestinationPort int
	// KnownHost, when set, is the host as known_hosts names it, for a
	// Destination or Host that is an alias of the operator's ssh config:
	// its HostName, with the port in brackets when it is not 22. A changed
	// host key's advice names it, since the alias is not what ssh records.
	KnownHost string
	// User and Host make the destination when Destination is empty.
	User string
	Host string
	// Port is ssh's -p. Zero means 22.
	Port int
	// PublicKeys are authorized_keys lines, one key each. Each is written to
	// a file of its own and given to ssh with -i, under IdentitiesOnly, so
	// ssh offers exactly these keys and finds their private halves in the
	// operator's agent. See SSHArgs.
	PublicKeys []string
	// Sudo prefixes every command. The paths written here are under /srv and
	// /etc, which a deploy user does not own.
	Sudo bool
	// Auth is how sudo is satisfied on this host, shared by every copy of
	// the transport so a site is asked once. Nil runs sudo as it is, which
	// works only where it asks for no password.
	Auth *SudoAuth
	// DockerHost, when set, is exported as DOCKER_HOST ahead of every
	// command (see RemoteCommand), so docker reaches the login user's own
	// rootless daemon and never the host's system socket.
	DockerHost string
	// Login, when set and DockerHost is empty, finds DockerHost from the
	// login user's UID on the transport's first command, and is shared by
	// every copy of the transport to one destination, so the UID is asked
	// once a run. See Login.
	Login *Login
	// DefaultUser reports that User is the deployment's own user because the
	// site's ssh section names none. That user exists only on a host whose
	// owner has admitted the deployment, so a login it refuses is reported
	// as a host that has not (see authProblem).
	DefaultUser bool
	// ConnectTimeout, in seconds, is ssh's -o ConnectTimeout. Zero is
	// DefaultConnectTimeout. ssh's own is the operating system's TCP connect
	// timeout, which is 75 seconds on macOS for a host that is switched off
	// or a VM that has been deleted, and a command's connection is tried
	// three times, so without one a dead site holds a command for minutes before it can
	// say so. It is given on the command line, so it takes precedence over
	// ~/.ssh/config. Like every other field here it is not used with
	// Destination, which stays verbatim.
	ConnectTimeout int
	// RetryLog, when set, receives this transport's retry notices in place
	// of the package's (SetRetryLog), so commands run at once on several
	// hosts each keep their own.
	RetryLog io.Writer

	// once makes a connection that never opened fail on its first attempt,
	// with no retry. Only Reach sets it.
	once bool
}

// Reach logs in to the host and runs nothing there, to find out whether it
// answers. An SSHTransport is tried once, not retried as a command is: a
// host that does not answer is down for this run, and every retry would
// wait out ssh's connect timeout again. A Login whose first probe this was
// remembers it, so no later command through it waits for the host either
// (see Login). A blip that a retry
// would have ridden out reads as the host down, which a run takes as a site
// it does not step on and a later run reaches again. Any other Transport
// runs `true`.
func Reach(t Transport) error {
	if s, ok := t.(SSHTransport); ok {
		s.once = true
		t = s
	}
	_, err := t.Run("true")
	return err
}

func (t SSHTransport) Describe() string {
	if t.Destination != "" && t.DestinationPort == 0 {
		return t.Destination
	}
	out := t.User + "@" + t.Host
	if p := t.port(); p != 22 {
		out += fmt.Sprintf(" port %d", p)
	}
	return out
}

// DefaultConnectTimeout is the ConnectTimeout, in seconds, of a transport
// that sets none. A host that answers at all opens its TCP connection in well
// under a second, so ten is room for a slow link and still lets a dead site be
// reported, after three attempts, within a minute, and after Reach's one
// within seconds.
const DefaultConnectTimeout = 10

func (t SSHTransport) connectTimeout() int {
	if t.ConnectTimeout == 0 {
		return DefaultConnectTimeout
	}
	return t.ConnectTimeout
}

func (t SSHTransport) port() int {
	if t.Destination != "" && t.DestinationPort != 0 {
		return t.DestinationPort
	}
	if t.Port == 0 {
		return 22
	}
	return t.Port
}

// SSHArgs is ssh's argument list for one remote command, with the public key
// files at keyFiles (in the order of PublicKeys).
//
// `-i` names a public key file, not a private one. ssh(1), on -i, and
// ssh_config(5), on IdentityFile, both say a public key file may be given "to
// use the corresponding private key that is loaded in ssh-agent(1) when the
// private key file is not present locally", and ssh_config(5) on IdentityFile
// says it "may be used in conjunction with IdentitiesOnly to select which
// identities in an agent are offered during authentication" (OpenSSH 9.9p2's
// pages). IdentitiesOnly=yes is what makes the selection a selection: without
// it ssh also offers every other key the agent holds, and a server that allows
// six attempts can refuse the operator before reaching the right one. Several
// -i are allowed and tried in order, so every listed key is offered, and the
// operator's private key, whichever of them it is, is never named here.
//
// Keys from the operator's own IdentityFile lines are still offered, since
// IdentitiesOnly restricts to configured files rather than to the command
// line's, and every other option in ~/.ssh/config (ProxyJump, known hosts)
// still applies. They come after the keys given here, because ssh lists -i
// files before any it reads from a config file, so a host that holds an
// operator's key accepts it before reaching them. Only -F leaves those lines
// out, and it leaves out the rest of the file with them. A host that holds
// none of the keys refuses them all, and sshd may say so as "Too many
// authentication failures" (see loginRefused). A -p and a user@ given here take precedence over that file's
// Port and User, because command line options are read first.
//
// BatchMode is not set, as before: a first connection may still ask the
// operator to accept a host key, and refusing that would push them to
// disable host key checking instead.
//
// Both routes share one connection per destination: muxArgs come first, on
// the --ssh route as well, since they name a socket and not the destination
// (see sshmux.go).
//
// -T asks for no terminal on the host, whatever the operator's ssh config
// says (RequestTTY). A command on a terminal has its stdin echoed back into
// its output by the terminal itself, and stdin carries sudo's password and
// WriteFile's content, so with one every command run with a password would
// print it.
//
// The command is sent as RemoteCommand wraps it, with DockerHost, on both routes.
func (t SSHTransport) SSHArgs(keyFiles []string, command string) []string {
	command = RemoteCommand(t.DockerHost, command)
	args := append(muxArgs(), "-T")
	if t.Destination != "" && t.DestinationPort != 0 {
		return append(args, "-p", strconv.Itoa(t.DestinationPort), "--", t.Destination, command)
	}
	if t.Destination != "" {
		return append(args, t.Destination, command)
	}
	args = append(args, t.sectionOptions()...)
	for _, f := range keyFiles {
		args = append(args, "-i", f)
	}
	return append(args, t.User+"@"+t.Host, command)
}

// sectionOptions are the options a transport built from a site's ssh
// section gives ssh before its key files.
//
// The deployment's own user (DefaultUser) is offered public keys alone. It
// has no password, and on a host that has not admitted the deployment it
// does not exist, where sshd asks for a password all the same: a prompt
// nothing can answer, drawn over the command's output. With public keys
// alone that host refuses the login, which authProblem reports, on a
// transport naming its site, as ErrLoginNotAdmitted.
func (t SSHTransport) sectionOptions() []string {
	opts := []string{"-p", strconv.Itoa(t.port()), "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=" + strconv.Itoa(t.connectTimeout())}
	if t.DefaultUser {
		opts = append(opts, "-o", "PreferredAuthentications=publickey")
	}
	return opts
}

// RemoteCommand is what ssh sends for command: command run by sh, whatever
// the login user's shell is.
//
// sshd hands the string ssh sends to the login shell (`$SHELL -c`), and the
// commands built here are sh syntax, quoted for sh. A login shell with other
// quoting rules misreads them: inside single quotes fish reads `\'` as an
// escaped quote, so the way shellQuote closes, escapes and reopens a quote
// leaves fish's quotes unbalanced, and it fails before anything runs. So the
// command travels as base64, which has no quote, backslash or space, inside a
// string every common shell parses the same way: `sh -c`, then one single
// quoted word holding only `eval`, a double quoted `$(...)`, `printf %s`, the
// base64 and `base64 -d`. sh decodes it and evaluates it as the command
// itself, so its output and exit status are the command's, and stdin reaches
// the command untouched, which sudo's password and WriteFile's content need.
//
// The string is a third longer than the command, and it is still one
// argument: Linux allows 128 KiB in one (MAX_ARG_STRLEN), and anything large
// here (rendered files, SQL) goes on stdin, not in the command.
//
// With dockerHost, the command runs after `export DOCKER_HOST=<dockerHost>`,
// inside the same encoded word, so every docker the command starts, in a
// pipeline or a subshell as much as at its head, reaches the login user's
// own daemon. A unix:// host's socket path is exported beside it, on the same
// line, as PAISANS_DOCKER_SOCKET: the infrastructure stack's Beszel agent
// mounts that socket, and its compose file names the variable because the
// user's UID, and so the path, is learnt over ssh after rendering.
func RemoteCommand(dockerHost, command string) string {
	if dockerHost != "" {
		export := "export DOCKER_HOST=" + shellQuote(dockerHost)
		if socket, ok := strings.CutPrefix(dockerHost, "unix://"); ok {
			export += " PAISANS_DOCKER_SOCKET=" + shellQuote(socket)
		}
		command = export + "\n" + command
	}
	return `sh -c 'eval "$(printf %s ` + base64.StdEncoding.EncodeToString([]byte(command)) + ` | base64 -d)"'`
}

// ssh builds the command for one remote command, with the key files it
// needs, and a cleanup that removes them.
//
// The files go in a fresh directory per invocation, made by os.MkdirTemp,
// which creates it 0700; each file is 0600. A public key is not a secret, but
// a world writable path ssh reads an identity from would let another local
// user choose which key it offers. Writing them per invocation rather than
// once per transport means nothing outlives the command that used it, even
// when the caller never calls a Close.
func (t SSHTransport) ssh(command string) (*exec.Cmd, func(), error) {
	if t.Destination != "" {
		args := t.SSHArgs(nil, command)
		rememberMaster(args[:len(args)-1])
		return exec.Command("ssh", args...), func() {}, nil
	}
	if len(t.PublicKeys) == 0 {
		return nil, nil, fmt.Errorf("%s: no public key to offer. Name an admin with a key in the site's ssh.keys, or pass --ssh", t.Describe())
	}
	// The master is named again without key files, which are gone by the
	// time it is closed and do not change its socket.
	named := t.SSHArgs(nil, command)
	rememberMaster(named[:len(named)-1])
	dir, err := os.MkdirTemp("", "paisans-ssh-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	files, err := writeKeyFiles(dir, t.PublicKeys)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return exec.Command("ssh", t.SSHArgs(files, command)...), cleanup, nil
}

// writeKeyFiles writes one public key per file, 0600, named in order.
func writeKeyFiles(dir string, keys []string) ([]string, error) {
	var files []string
	for i, key := range keys {
		path := filepath.Join(dir, fmt.Sprintf("key-%d.pub", i+1))
		if err := os.WriteFile(path, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, nil
}

// Run runs command on the host, through sudo when the transport says to.
//
// Its error, like every error the transport returns, is redacted of the
// sudo password (see ui.RedactError): it carries the host's output, which
// callers quote to the operator, and a host can print what it was sent.
// The output itself is returned as the host printed it, since it is data a
// caller parses; what is shown of it is redacted where it is shown.
func (t SSHTransport) Run(command string) (out string, err error) {
	defer t.logged(command, time.Now(), &err)
	if err := t.refuseSudoDocker(); err != nil {
		return "", err
	}
	wrapped, stdin, err := t.sudo(command, nil)
	if err != nil {
		return "", ui.RedactError(err)
	}
	out, err = t.run(wrapped, stdin)
	if err != nil {
		return out, ui.RedactError(fmt.Errorf("%s: %s: %w\n%s", t.Describe(), t.named(command), err, strings.TrimRight(out, "\n")))
	}
	return out, nil
}

// SSHLog, when set, is told of each command a transport sends: the site, the
// command as its caller gave it, how long it took, and its error. A run log
// sets it (see internal/runlog).
var SSHLog func(site, command string, took time.Duration, err error)

// logged tells SSHLog of command, started at start, ending with *err.
func (t SSHTransport) logged(command string, start time.Time, err *error) {
	if SSHLog == nil {
		return
	}
	site := t.Site
	if site == "" {
		site = t.Describe()
	}
	SSHLog(site, command, time.Since(start), *err)
}

// named is how an error names command: its first line, after sudo when it
// ran through sudo. It is the command as the caller gave it rather than as
// sudo wraps it, which says what failed without the wrapping.
func (t SSHTransport) named(command string) string {
	if t.Sudo {
		return "sudo " + firstLine(command)
	}
	return firstLine(command)
}

// How often a connection that never opened is tried again, and how long to
// wait before each retry: three attempts in all, but one for Reach. Package
// variables so that a test can shrink them.
var sshRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second}

// SetSleep makes every wait in this package, the one before an ssh retry
// among them, call sleep instead, and returns a function restoring the one
// before. It is for tests in other packages, which drive a retry to see how
// it is reported and need not wait the seconds it waits.
func SetSleep(sleeper func(time.Duration)) (restore func()) {
	old := sleep
	sleep = sleeper
	return func() { sleep = old }
}

// runSSH runs one built ssh command. A package variable so that a test can
// stand in for the ssh binary and choose its output and exit status.
var runSSH = func(cmd *exec.Cmd) error { return cmd.Run() }

// SetRunSSH runs each built ssh command with run instead, or with ssh itself
// for nil, and returns a function restoring the one before. It is runSSH for
// tests in other packages: a fake host answered in process costs nothing,
// where an ssh script on PATH costs a fork and an exec for every command.
func SetRunSSH(run func(cmd *exec.Cmd) error) (restore func()) {
	old := runSSH
	if run == nil {
		run = func(cmd *exec.Cmd) error { return cmd.Run() }
	}
	runSSH = run
	return func() { runSSH = old }
}

// retryLog receives one line per retry. Nothing by default: a retry that
// then succeeds is not news, and one that runs out is reported in full by the
// error that ends the command, so the lines are only worth showing to
// someone who asked for detail. SetRetryLog lets a command route them there.
var retryLog io.Writer = io.Discard

// SetRetryLog sends the notice of each ssh retry to w, or nowhere for nil.
func SetRetryLog(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	retryLog = w
}

// promptHold pauses the operator's progress display while ssh may be asking
// on the terminal, and returns what resumes it. Nothing by default, since a
// run with nothing drawn has nothing to pause. SetPromptHold lets a command
// connect it to the reporter that draws.
var promptHold = func() (resume func()) { return func() {} }

// SetPromptHold pauses with hold around an ssh that may ask the operator
// something, or with nothing for nil.
func SetPromptHold(hold func() (resume func())) {
	if hold == nil {
		hold = func() (resume func()) { return func() {} }
	}
	promptHold = hold
}

// contacted is every destination ssh has reached in this run. ssh asks on
// the terminal itself, not through the toolkit, when it meets a host key it
// does not know, and that happens on a first connection. Until a host has
// answered once, each attempt is made with the display held, so a spinner
// redrawing its line cannot erase the question; after that the spinner runs.
var (
	contactedMu sync.Mutex
	contacted   = map[string]bool{}
)

// AskMu is held while anything asks the operator on the terminal: ssh about
// a host key on a first connection, here, and a sudo password prompt, in
// cmd/paisans. ssh asks on the terminal itself, and two questions at once,
// from commands run on several hosts together, would garble each other, so
// every question takes this one lock rather than one of its own kind.
var AskMu sync.Mutex

func firstContact(destination string) bool {
	contactedMu.Lock()
	defer contactedMu.Unlock()
	return !contacted[destination]
}

func markContacted(destination string) {
	contactedMu.Lock()
	defer contactedMu.Unlock()
	contacted[destination] = true
}

// hostKeyKnown reports whether ssh already knows t's host key, so connecting
// asks nothing. It asks ssh -G where the host and its known_hosts files are,
// then ssh-keygen -F in each file. Anything it cannot tell counts as known:
// the first attempt is then made in batch mode, and one that fails at the
// host key is made again with the display held (batchWouldAsk). Tests
// replace it.
var hostKeyKnown = func(t SSHTransport) bool {
	args := t.SSHArgs(nil, "true")
	out, err := exec.Command("ssh", append([]string{"-G"}, args[:len(args)-1]...)...).Output()
	if err != nil {
		return true
	}
	host, port := "", "22"
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "hostname":
			host = value
		case "port":
			port = value
		case "userknownhostsfile", "globalknownhostsfile":
			files = append(files, strings.Fields(value)...)
		}
	}
	if host == "" {
		return true
	}
	name := host
	if port != "22" {
		name = "[" + host + "]:" + port
	}
	home, _ := os.UserHomeDir()
	for _, f := range files {
		if strings.HasPrefix(f, "~/") && home != "" {
			f = filepath.Join(home, f[2:])
		}
		if exec.Command("ssh-keygen", "-F", name, "-f", f).Run() == nil {
			return true
		}
	}
	return false
}

// quietLogin reports whether ssh can reach t with nothing that asks on the
// terminal behind BatchMode's back: no ProxyCommand or ProxyJump, whose own
// ssh or command may ask, and no security key, whose touch or PIN ssh waits
// for whatever the mode. It reads ssh -G, and anything it cannot read
// counts as a login that may ask. Tests replace it.
var quietLogin = func(t SSHTransport) bool {
	args := t.SSHArgs(nil, "true")
	out, err := exec.Command("ssh", append([]string{"-G"}, args[:len(args)-1]...)...).Output()
	if err != nil {
		return false
	}
	home, _ := os.UserHomeDir()
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "proxycommand", "proxyjump":
			if value != "" && value != "none" {
				return false
			}
		case "identityfile", "certificatefile":
			if strings.HasPrefix(value, "~/") && home != "" {
				value = filepath.Join(home, value[2:])
			}
			if securityKey(value) {
				return false
			}
		}
	}
	return true
}

// securityKey reports whether path, an identity ssh may offer, is a FIDO
// key that exists: by its type in the public half, sk-ssh-ed25519@openssh.com
// or sk-ecdsa-sha2-nistp256@openssh.com, or by the _sk in its default name.
func securityKey(path string) bool {
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		pub, err = os.ReadFile(path)
	}
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(pub)), "sk-") || strings.HasSuffix(path, "_sk")
}

// batchWouldAsk reports whether a batch-mode attempt failed at what ssh
// would have asked about, before any command ran: ssh's own exit, 255, with
// a login refused where a password or a key's passphrase might have let it
// in, or a host key ssh would have asked to accept (one hostKeyKnown took
// for known: a key of another type, say). A refusal is no question where
// the transport offers declared keys alone, which have no passphrase to
// ask for, nor is too many authentication failures, nor a host key that
// changed, which ssh refuses whatever the mode.
func batchWouldAsk(t SSHTransport, err error, out string) bool {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 255 {
		return false
	}
	last := lastLine(out)
	switch {
	case strings.Contains(last, "Host key verification failed"):
		return !strings.Contains(out, "REMOTE HOST IDENTIFICATION HAS CHANGED")
	case len(t.PublicKeys) > 0 && t.Destination == "":
		return false
	}
	return loginRefused(out) && strings.Contains(out, "Permission denied (")
}

// blankLineOnTerminal writes an empty line to the controlling terminal, after
// ssh's question about a host key, so what follows starts apart from the
// answer. Without a terminal there was no question. Tests replace it.
var blankLineOnTerminal = func() {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer tty.Close()
	fmt.Fprintln(tty)
}

// ErrUnreachable marks a command that never reached the host: ssh could not
// connect, on every attempt. A caller asking the host a question can tell
// "the host said no" from "the host was never asked" with errors.Is, and must
// never read the second as an answer.
var ErrUnreachable = errors.New("ssh could not connect to the host")

// ErrLoginRefused is a host that answered and would not let the login in:
// no key it accepts, or no such user, which for the deployment's own user is
// a host that has not admitted it.
var ErrLoginRefused = errors.New("the host refused the login")

// ErrLoginNotAdmitted is ErrLoginRefused for the deployment's own user on a
// transport naming its site: that user exists only once the host's owner has
// admitted the deployment, so the refusal is most likely a host that has
// not, and otherwise a key of the operator's the host was never given. A
// refusal of a user the site names is ErrLoginRefused alone, a key problem.
var ErrLoginNotAdmitted = fmt.Errorf("%w: the deployment's user is not admitted there", ErrLoginRefused)

// NoLogin reports whether err is a host that could not be logged into at
// all, refused or not answering, rather than a command that failed there.
func NoLogin(err error) bool {
	return errors.Is(err, ErrLoginRefused) || errors.Is(err, ErrUnreachable)
}

// UnreachableError is ErrUnreachable for one site, so a command that reads
// several can say which one did not answer and refer the operator to
// doctor for it. Site is the transport's, empty when it reaches no site.
type UnreachableError struct {
	Site string
	Err  error
}

func (e *UnreachableError) Error() string { return e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// UnreachableSite is the site an ssh failure in err's chain names, or ""
// when there is none or it names no site.
func UnreachableSite(err error) string {
	var u *UnreachableError
	if errors.As(err, &u) {
		return u.Site
	}
	return ""
}

// connectionFailures are what ssh prints when the connection itself failed,
// before any command could run. "Connection closed by" is the pre
// authentication form ("Connection closed by 203.0.113.10 port 22"); once a
// session is open ssh says "Connection to <host> closed by remote host"
// instead, which is not matched, because by then the command may have run.
var connectionFailures = []string{
	"Operation timed out",
	"Connection timed out",
	"Connection refused",
	"Connection reset",
	"No route to host",
	"kex_exchange_identification",
	"Connection closed by",
}

// connectionFailed reports whether a failed ssh never reached the remote
// command. Both halves are needed. ssh exits 255 on its own errors, but a
// remote command may exit 255 too, so the status alone proves nothing. And
// the strings alone prove nothing either, since a remote command can print
// "Connection refused" about some other connection. ssh's own error is its
// last line, after anything the remote side printed, so only that line is
// read, and a session that had opened (client_loop, which ssh prints only
// once one exists) is never a connection failure.
func connectionFailed(err error, out string) bool {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 255 {
		return false
	}
	last := lastLine(out)
	if strings.Contains(last, "client_loop") {
		return false
	}
	// A login refused before the connection closed is a refusal, which
	// authProblem reports, and is never retried.
	if loginRefused(out) {
		return false
	}
	for _, s := range connectionFailures {
		if strings.Contains(last, s) {
			return true
		}
	}
	return false
}

// authProblem is the Problem for a connection ssh opened and then would not
// use: the host's key is not the one known, or is not known and ssh could
// not ask, or the host accepted none of the operator's keys. ssh says the
// first two on its own last line and exits 255, as for connectionFailed.
// A refused login is found anywhere in ssh's closing lines (see
// loginRefused), since sshd may hang up after it. Nil for anything else. A
// refusal of the deployment's default user is a host that has not admitted
// the deployment.
func (t SSHTransport) authProblem(err error, out string) error {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 255 {
		return nil
	}
	last := lastLine(out)
	user, host := t.User, t.Host
	if t.Destination != "" && t.DestinationPort == 0 {
		if u, h, found := strings.Cut(t.Destination, "@"); found {
			user, host = u, h
		} else {
			host = t.Destination
		}
	}
	switch {
	case strings.Contains(last, "Host key verification failed") && strings.Contains(out, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		return &ui.Problem{
			Hint:    t.Describe() + " offered a host key other than the one known",
			Explain: fmt.Sprintf("Either the host was reinstalled, or something is answering in its place. Find out which before you proceed. Once you know it is the same host, remove the old key with ssh-keygen -R %s, connect once with %s to accept the new one, and run again.", shellQuote(t.knownHostsName()), t.connectCommand()),
			Cause:   err,
		}
	case strings.Contains(last, "Host key verification failed"):
		return &ui.Problem{
			Hint:    t.Describe() + "'s host key is not known",
			Explain: fmt.Sprintf("ssh could not ask whether to trust it. Connect once with %s, check the fingerprint it shows, accept it, and run again.", t.connectCommand()),
			Cause:   err,
		}
	case loginRefused(out) && t.DefaultUser && t.Site != "":
		return &ui.Problem{
			Hint:    fmt.Sprintf("site %s is not admitted on %s", t.Site, host),
			Explain: fmt.Sprintf("The site names no ssh.user, so paisans logs in as the deployment's own user, %s, and %s refused it. That user exists only once the host's owner has admitted the deployment. Send them a request:\n\n    %s site request --site %s > %s.admit\n\nIf they have admitted it, check that your key is loaded in your ssh agent (ssh-add -l) and is under an operator the site's ssh.keys names. If the deployment logs in as another user on that host, name it in sites.%s.ssh.user.", user, host, progname.Cmd(), t.Site, t.Site, t.Site),
			Cause:   fmt.Errorf("%w: %w", ErrLoginNotAdmitted, err),
		}
	case loginRefused(out):
		return &ui.Problem{
			Hint:    t.Describe() + " did not accept your ssh key",
			Explain: "Check that your key is loaded in your ssh agent (ssh-add -l), that its public half is under an admin the site's ssh.keys names, and that the host's authorized_keys for that user holds it.",
			Cause:   fmt.Errorf("%w: %w", ErrLoginRefused, err),
		}
	}
	return nil
}

// connectCommand is the ssh command an operator runs to reach the host by
// hand.
func (t SSHTransport) connectCommand() string {
	if t.Destination != "" && t.DestinationPort == 0 {
		return "ssh " + t.Destination
	}
	if p := t.port(); p != 22 {
		return fmt.Sprintf("ssh -p %d %s@%s", p, t.User, t.Host)
	}
	return "ssh " + t.User + "@" + t.Host
}

// knownHostsName is the host as known_hosts names it.
func (t SSHTransport) knownHostsName() string {
	if t.KnownHost != "" {
		return t.KnownHost
	}
	if t.Destination != "" && t.DestinationPort == 0 {
		dest, uri := strings.CutPrefix(t.Destination, "ssh://")
		if _, host, found := strings.Cut(dest, "@"); found {
			dest = host
		}
		if uri {
			// ssh://[user@]host[:port], the one form with a port.
			if h, p, err := net.SplitHostPort(dest); err == nil {
				if p == "22" {
					return h
				}
				return "[" + h + "]:" + p
			}
		}
		return dest
	}
	if p := t.port(); p != 22 {
		return fmt.Sprintf("[%s]:%d", t.Host, p)
	}
	return t.Host
}

// loginRefusals are what ssh prints when the host accepted none of the keys
// offered: "Permission denied (publickey)." once every key was tried, or
// sshd's "Too many authentication failures" once more keys were offered than
// its MaxAuthTries allows.
var loginRefusals = []string{"Permission denied (", "Too many authentication failures"}

// sshDisconnects begin the lines ssh prints as the connection ends, after
// the reason for it: "Received disconnect from <host> port 22:2: <reason>"
// carries sshd's reason, and "Disconnected from <host> port 22" follows it.
// A server that hangs up after refusing the login without a disconnect
// message leaves "Connection closed by <host> port 22", or "Connection
// reset by <host> port 22" when it resets the connection. Those two are
// connectionFailures as well, and connectionFailed leaves them to
// authProblem when a refusal comes before them. The other
// connectionFailures (a timeout, a refused or unroutable connection, a
// failed key exchange) all end a connection before authentication, so none
// of them can follow a refusal.
var sshDisconnects = []string{"Received disconnect from ", "Disconnected from ", "Connection closed by ", "Connection reset by "}

// loginRefused reports whether ssh's diagnostic output ends in a refused
// login. That is the closing run of out's lines: its last line, and the
// lines before it while each is a refusal or one of ssh's disconnect
// notices. A refusal anywhere in that run counts, so sshd hanging up with
// "Too many authentication failures" and ssh then printing "Disconnected
// from" reads as the refusal it is. Anything above that run is the remote
// command's or the session's, never read here.
func loginRefused(out string) bool {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		for _, r := range loginRefusals {
			if strings.Contains(line, r) {
				return true
			}
		}
		closing := false
		for _, d := range sshDisconnects {
			if strings.HasPrefix(line, d) {
				closing = true
			}
		}
		if !closing {
			return false
		}
	}
	return false
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// run runs one remote command, with stdin when it is not nil, and retries a
// connection that never opened.
//
// Only that failure is retried. A command that ran and failed is reported at
// once, whatever its status: running it again could repeat whatever it did
// before failing, and the failure is the host's answer. A connection that
// never opened ran nothing, so trying again cannot do anything twice. The
// first real storage add met exactly this twice, a connect timeout on a host
// that answered seconds later, and stopped a multi stage operation half way
// for it. WriteFile is retried the same way and is safe to be: it writes a
// temporary file and renames it, so even a transfer cut off part way leaves
// the old file whole and a second attempt starts over.
//
// A transport with a Login asks its UID first, once a run (see Login).
//
// Three attempts, two and four seconds apart, is enough for a blip and short
// enough that a host which is really down is reported within seconds rather
// than hidden behind a long wait. A transport Reach made tries once.
func (t SSHTransport) run(command string, stdin *string) (string, error) {
	if err := t.refuseSudoDocker(); err != nil {
		return "", err
	}
	if t.Login != nil && t.DockerHost == "" {
		host, err := t.Login.resolve(t)
		if err != nil {
			return "", err
		}
		t.DockerHost = host
	}
	retries := sshRetryDelays
	if t.once {
		retries = nil
	}
	// interactive is a first contact whose login, tried in batch mode, was
	// refused: ssh needs to ask for a password or a key's passphrase.
	interactive := false
	for attempt := 1; ; attempt++ {
		cmd, cleanup, err := t.ssh(command)
		if err != nil {
			return "", err
		}
		if stdin != nil {
			cmd.Stdin = strings.NewReader(*stdin)
		}
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		resume := func() {}
		asks, batch := false, false
		if firstContact(t.Describe()) {
			asks = !hostKeyKnown(t)
			// A host whose key is known, reached with nothing else that may
			// ask (quietLogin), is tried first in batch mode, where ssh asks
			// nothing, so the spinner runs; only a login that needs a
			// question holds the display.
			if batch = !asks && !interactive && quietLogin(t); batch {
				cmd.Args = slices.Insert(cmd.Args, 1, "-o", "BatchMode=yes")
			} else {
				resume = promptHold()
			}
		}
		if asks || interactive {
			AskMu.Lock()
		}
		err = runSSH(cmd)
		if batch && batchWouldAsk(t, err, out.String()) {
			// Stopped before any command ran, at what ssh would have asked
			// about: the same attempt again, with the display held.
			cleanup()
			interactive = true
			attempt--
			continue
		}
		if asks && (err == nil || !connectionFailed(err, out.String())) {
			// ssh asked about the host's key and was answered. The blank
			// line goes before the display resumes, so the spinner redraws
			// below it rather than leaving a frame above it.
			blankLineOnTerminal()
		}
		if asks || interactive {
			AskMu.Unlock()
		}
		resume()
		cleanup()
		if err == nil {
			markContacted(t.Describe())
			return out.String(), nil
		}
		if !connectionFailed(err, out.String()) {
			// The host answered, so any question about its key is settled.
			markContacted(t.Describe())
			if p := t.authProblem(err, out.String()); p != nil {
				return out.String(), p
			}
			return out.String(), err
		}
		if attempt > len(retries) {
			return out.String(), &ui.Problem{
				Hint:    t.Describe() + " cannot be reached over ssh",
				Explain: fmt.Sprintf("ssh could not connect in %s: %s. Check that the host is up, and that %s from this machine reaches it.", attempts(attempt), strings.TrimSuffix(lastLine(out.String()), "."), t.connectCommand()),
				Cause:   &UnreachableError{Site: t.Site, Err: fmt.Errorf("%w after %s: %w", ErrUnreachable, attempts(attempt), err)},
			}
		}
		delay := retries[attempt-1]
		log := retryLog
		if t.RetryLog != nil {
			log = t.RetryLog
		}
		fmt.Fprint(log, ui.Redact(fmt.Sprintf("%s: ssh could not connect (%s), retrying in %s (attempt %d of %d)\n",
			t.Describe(), lastLine(out.String()), delay, attempt+1, len(retries)+1)))
		sleep(delay)
	}
}

// attempts is n attempts, in words.
func attempts(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}

func (t SSHTransport) ReadFile(path string) (string, bool, error) {
	out, err := t.Run(readFileCommand(path))
	if err != nil {
		return "", false, err
	}
	return parseReadFile(out)
}

// WriteFile sends content over stdin rather than as part of the command.
//
// A rendered file carries credentials, and a command line is visible in `ps`
// to every user on the host for as long as it runs. Feeding it through stdin
// keeps it out of the process table, and writing to a temporary file first
// means a failed transfer leaves the previous file intact rather than a
// truncated one that an application will happily read.
//
// The temporary file is made by mktemp beside the target, which creates it
// exclusively with mode 600. A fixed, guessable name would let anyone who can
// write the directory plant a link there for a privileged write to follow, and
// would make two writes of one path share a file. The rename onto the target
// replaces a link at the target instead of writing through it, and the trap
// removes the temporary file if any step before the rename fails.
//
// A target that is a link or a directory is refused before the rename, since
// mv would move the temporary file into a linked directory. The check and the
// mv are two steps, so a link planted between them is not caught. That is
// acceptable because this transport is never used under sudo to write into a
// directory another user can write: files in the deployment user's own tree
// are written as that user.
func (t SSHTransport) WriteFile(path, content string, mode uint32) (err error) {
	defer t.logged("write "+path, time.Now(), &err)
	if err := t.refuseSudoDocker(); err != nil {
		return err
	}
	dir := parentDir(path)
	template := strings.TrimSuffix(dir, "/") + "/." + pathpkg.Base(path) + ".XXXXXX"
	script := fmt.Sprintf(
		`set -e; mkdir -p %[1]s; tmp=$(mktemp %[2]s); trap 'rm -f "$tmp"' EXIT; cat > "$tmp"; chmod %04[3]o "$tmp"; `+
			`if [ -L %[4]s ]; then printf '%%s is a symbolic link, not a file\n' %[4]s >&2; exit 1; fi; `+
			`if [ -d %[4]s ]; then printf '%%s is a directory, not a file\n' %[4]s >&2; exit 1; fi; `+
			`mv -f "$tmp" %[4]s; trap - EXIT`,
		shellQuote(dir), shellQuote(template), mode, shellQuote(path))
	script, stdin, err := t.sudo(script, &content)
	if err != nil {
		return ui.RedactError(err)
	}
	if out, err := t.run(script, stdin); err != nil {
		return ui.RedactError(fmt.Errorf("%s: writing %s: %w\n%s", t.Describe(), path, err, strings.TrimRight(out, "\n")))
	}
	return nil
}

// RunInput runs a command with stdin, for the same reason WriteFile sends
// content that way: what goes in carries credentials, and only the command
// line is visible in the process table.
func (t SSHTransport) RunInput(command, stdin string) (out string, err error) {
	defer t.logged(command, time.Now(), &err)
	if err := t.refuseSudoDocker(); err != nil {
		return "", err
	}
	wrapped, in, err := t.sudo(command, &stdin)
	if err != nil {
		return "", ui.RedactError(err)
	}
	out, err = t.run(wrapped, in)
	if err != nil {
		return out, ui.RedactError(fmt.Errorf("%s: %s: %w\n%s", t.Describe(), t.named(command), err, strings.TrimRight(out, "\n")))
	}
	return out, nil
}

func parentDir(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "/"
}

// shellQuote wraps a value in single quotes for /bin/sh, escaping any single
// quote inside it. Paths here come from a configuration an operator wrote, so
// they are not hostile, but they are not guaranteed to be free of spaces
// either.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuote is shellQuote, for a command another package builds to run on
// a host.
func ShellQuote(s string) string { return shellQuote(s) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

// refuseSudoDocker refuses a transport that both sets DOCKER_HOST and runs
// through sudo: sudo resets the environment, so docker would reach the
// host's system daemon instead of the user's own. It names the destination
// itself, because Dial and the probes report run's error unwrapped.
func (t SSHTransport) refuseSudoDocker() error {
	if t.Sudo && (t.DockerHost != "" || t.Login != nil) {
		return fmt.Errorf("%s: a transport with a Docker host cannot use sudo: sudo resets the environment, so docker would reach the host's system daemon instead of the user's own", t.Describe())
	}
	return nil
}
