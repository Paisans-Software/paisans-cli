package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// localTransport is the transport for --request, on the host itself.
// Tests replace it.
var localTransport = hostadmit.Local

// reachOwnerAdmit is the owner's own ssh, through sudo, for --site. Tests
// replace it.
var reachOwnerAdmit = func(dest config.Destination) apply.Transport { return ownerTransport(dest, true) }

// reachSiteUser is how admit --site logs in as the deployment's user once
// it is admitted, as the operators' commands will. Tests replace it.
var reachSiteUser = func(name string, site config.Site) apply.Transport { return siteTransport(name, site, "") }

// executeAdmit runs the approved plan. Tests replace it.
var executeAdmit = hostadmit.Execute

// runningAsRootOnLinux is whether this process may run a host's plan on
// the machine it runs on: --request runs only there, as root.
var runningAsRootOnLinux = func() bool { return runtime.GOOS == "linux" && os.Geteuid() == 0 }

// runSiteAdmit is `paisans site admit`: the one root step for a deployment
// on a host, run by its owner, from a request on the host or from the
// configuration over the owner's own ssh.
func runSiteAdmit(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("site admit", flag.ContinueOnError)
	reporter := commonFlags(fs)
	var (
		request, site, sshFlag, configPath, digestFlag string
		flags                                          hostadmit.Flags
		execute                                        bool
	)
	fs.StringVar(&request, "request", "", "the request file the operator sent, on this host, as root")
	fs.StringVar(&site, "site", "", "the site to admit, from the configuration, from a workstation")
	fs.StringVar(&sshFlag, "ssh", "", "your own login on the host, with --site: user, for the site's declared host, or user@host[:port] or an alias of your ssh config; asked for at a terminal when left out")
	fs.StringVar(&configPath, "config", config.FileName, "path to the deployment declaration, with --site")
	fs.BoolVar(&flags.Watchdog, "watchdog", false, "grant the host's watchdog, which can reboot it, to a data site's Patroni")
	fs.BoolVar(&flags.PublicPorts, "public-ports", false, "let every local user bind ports 80 to 1023, and open 80 and 443, for the toolkit's Caddy")
	fs.StringVar(&flags.DataDir, "data-dir", "", "keep the deployment's Docker data under this directory instead of the user's home")
	fs.BoolVar(&flags.BridgeNetfilter, "bridge-netfilter", false, "load br_netfilter although this host has bridges of its own")
	fs.BoolVar(&flags.MeshLAN, "mesh-lan", false, "let the mesh dial this host's own LAN")
	fs.BoolVar(&flags.EnableUFW, "enable-ufw", false, "enable ufw when it is installed and not active: allow every port sshd listens on, arm a timer that turns ufw off again in 10 minutes, enable it, and stop the timer once a new SSH login gets through")
	fs.BoolVar(&flags.EnableUFWBlockListed, "enable-ufw-block-listed", false, "with --enable-ufw: enable ufw although it blocks the ports admit lists, which answer from off the host now and no ufw rule allows, once you have checked each may be blocked")
	fs.BoolVar(&flags.Swapfile, "swapfile", false, "make a swapfile at /swapfile, twice the host's RAM up to 16 GiB, when the host has no swap in any form but zram; asked at a terminal with --execute")
	fs.BoolVar(&execute, "execute", false, "make the changes the plan shows")
	fs.StringVar(&digestFlag, "digest", "", "with --request: the request's digest the operator gave you, compared as the one typed at a terminal is")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("site admit takes no argument. Got: %s", quoteAll(fs.Args()))
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	switch {
	case request == "" && site == "":
		return fmt.Errorf("site admit: give --request <file> on the host, or --site <name> from a workstation")
	case request != "" && site != "":
		return fmt.Errorf("site admit: --request or --site, not both")
	case request != "" && sshFlag != "":
		return fmt.Errorf("site admit: --request runs on the host itself and takes no --ssh")
	case request != "" && !runningAsRootOnLinux():
		return fmt.Errorf("site admit: --request runs as root on the host it admits to: copy the request there and run sudo %s site admit --request <file>", progname.Name())
	case request == "" && digestFlag != "":
		return fmt.Errorf("site admit: --digest goes with --request: with --site admit builds the request itself from the configuration")
	case flags.EnableUFWBlockListed && !flags.EnableUFW:
		return fmt.Errorf("site admit: --enable-ufw-block-listed goes with --enable-ufw: it lets admit enable ufw over the ports it lists, and enables nothing alone. Nothing was read or changed")
	case request != "" && ans.yes && flags.EnableUFW:
		return ufwNeedsAPerson()
	case request != "" && execute && flags.EnableUFW && !stdinIsTerminal(stdin):
		return ufwNeedsAPerson()
	case request != "" && execute && ans.yes && digestFlag == "":
		return fmt.Errorf("site admit: --yes never answers the request's digest: give it as --digest <digest>, the one the operator gave you over a channel you both trust. Nothing was read or changed")
	case request != "" && execute && digestFlag == "" && !stdinIsTerminal(stdin):
		return fmt.Errorf("site admit: --execute with --request asks you to type the request's digest at a terminal, and stdin is not one. Run it from an interactive shell, or give the digest as --digest <digest>. Nothing was read or changed")
	case site != "" && ans.yes && sshFlag == "":
		return fmt.Errorf("site admit: --yes does not choose a login: give --ssh <user>, your own login on the site's declared host, or --ssh user@host[:port]. Nothing was read or changed")
	}
	return admit(reporter(), admitRun{
		request: request, site: site, sshFlag: sshFlag, configPath: configPath, digestFlag: digestFlag,
		flags: flags, execute: execute,
	}, ans, stdin, stdout)
}

// admitRun is one site admit: its flags, as runSiteAdmit has checked
// them, and whether the owner approves the plan at a terminal before it
// runs.
type admitRun struct {
	request, site, sshFlag, configPath, digestFlag string
	flags                                          hostadmit.Flags
	execute                                        bool
	// approve asks the owner, once the plan is shown, whether to make its
	// changes, and stops with errAdmitDeclined, nothing changed, on
	// anything but a yes. init sets it when it admits a site at the
	// operator's word (see admitFromInit).
	approve bool
	// movePort, when set, lets a claim refused for the site's WireGuard
	// listen port alone stop with a free port instead (see
	// hostadmit.PortMoveError), for init to move the endpoint to. init sets
	// it for a site whose port it may move (see admitSite).
	movePort io.Reader
}

// portMoved is admit's refusal of a WireGuard listen port another
// deployment holds, with a free port it found (see admitRun.movePort). It
// is named here because only this file reaches the hostadmit package.
type portMoved = hostadmit.PortMoveError

// errAdmitDeclined is a plan the owner did not approve (admitRun.approve).
var errAdmitDeclined = errors.New("site admit: the plan was not approved. Nothing was changed")

// admitStoppedError is admit stopping before it ran its plan: a declined
// grant or ufw question, no login, or a refusal of the host, read only.
// Nothing on the host was changed. Its text is the stop's own.
type admitStoppedError struct{ err error }

func (e *admitStoppedError) Error() string { return e.err.Error() }
func (e *admitStoppedError) Unwrap() error { return e.err }

// admitStopped reports whether err is admit stopping with nothing changed:
// a declined plan, or any stop before the plan ran.
func admitStopped(err error) bool {
	var stopped *admitStoppedError
	return errors.Is(err, errAdmitDeclined) || errors.As(err, &stopped)
}

// admitFromInit is site admit --site site --execute, for init: admit's own
// flow, asking at stdin's terminal for the owner's login on the host and
// for each grant the site's roles need, then showing the plan and asking
// before it runs. A plan the operator declines is errAdmitDeclined, and any
// other stop before the plan runs is an admitStoppedError (see
// admitStopped). movePort is admitRun.movePort. A test replaces it.
var admitFromInit = func(r ui.Reporter, configPath, site string, movePort io.Reader, stdin io.Reader, stdout io.Writer) error {
	return admit(r, admitRun{site: site, configPath: configPath, execute: true, approve: true, movePort: movePort}, answerer{}, stdin, stdout)
}

// admit is site admit once its flags are checked: from a request on the
// host, or from the configuration over the owner's own ssh.
func admit(r ui.Reporter, run admitRun, ans answerer, stdin io.Reader, stdout io.Writer) (err error) {
	// Everything before executeAdmit reads the host and changes nothing,
	// so a stop there is an admitStoppedError.
	ran := false
	defer func() {
		if err != nil && !ran && !errors.Is(err, errAdmitDeclined) {
			err = &admitStoppedError{err}
		}
	}()
	request, site, sshFlag, configPath, digestFlag, flags, execute := run.request, run.site, run.sshFlag, run.configPath, run.digestFlag, run.flags, run.execute
	in := hostadmit.Input{Flags: flags, Now: time.Now()}
	var declared config.Site
	if request != "" {
		data, err := readRequest("site admit", request)
		if err != nil {
			return err
		}
		if in.Request, err = admission.Parse(data); err != nil {
			return fmt.Errorf("site admit: %s: %w", request, err)
		}
		in.Digest = admission.Digest(data)
		if digestFlag != "" {
			if err := checkDigest(digestFlag, in.Digest); err != nil {
				return err
			}
		}
	} else {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		if result := validate.Check(cfg); result.Refused() {
			reportFindings(r, configPath, result)
			return refused(configPath, len(result.Refusals()), "Nothing was read or changed.")
		}
		if in.Request, err = admission.Build(cfg, site); err != nil {
			return fmt.Errorf("site admit: %w", err)
		}
		declared = cfg.Sites[site]
		in.MovePort = run.movePort
	}
	// The grants first, before any transport exists: the owner's decision
	// needs nothing from the host. At a terminal, a grant the site's roles
	// need and the flags leave out is asked for, with what it gives; --yes
	// grants each one, as typing y would.
	if ans.canAnswer(stdin) {
		for _, g := range hostadmit.Missing(in) {
			yes, err := askGrant(ans, stdin, stdout, in.Request, g)
			if err != nil {
				return err
			}
			if yes {
				g.Give(&in.Flags)
			}
		}
	}
	if err := hostadmit.Early(in); err != nil {
		return err
	}
	var t apply.Transport
	var dest config.Destination
	if request != "" {
		// The owner's environment never points admit's Docker commands
		// at another daemon; over ssh, sudo's env_reset does the same.
		os.Unsetenv("DOCKER_HOST")
		os.Unsetenv("DOCKER_CONTEXT")
		t = localTransport()
		r.Result("request digest %s: compare it with the one the operator gave you, over a channel you both trust, such as a call", in.Digest)
	} else {
		var err error
		if dest, err = ownerDestination("site admit", sshFlag, site, declared, stdin, stdout); err != nil {
			return err
		}
		t = reachOwnerAdmit(dest)
	}
	r.Section(fmt.Sprintf("admit %s on %s", ui.Visible(in.Request.User), t.Describe()))
	// An inactive ufw is a question at a terminal, as a missing grant is:
	// the plan is drawn again with --enable-ufw on a yes. Off a terminal it
	// refuses, naming --enable-ufw and the commands that enable it safely.
	// --yes answers it with --site, whose own login proves SSH gets
	// through; with --request only a person can, so it refuses.
	// What enabling ufw would block, of what answers from off the host,
	// is a second question at a terminal, which --yes never answers.
	var inactive *hostadmit.UFWInactiveError
	var blocks *hostadmit.UFWBlocksError
	var refusal error
	plan, err := ui.Get(r, "read the host", func() (*hostadmit.Plan, error) {
		p, err := hostadmit.Build(t, in)
		if errors.As(err, &inactive) && len(inactive.SSHPorts) > 0 && ans.canAnswer(stdin) {
			if ans.yes && request != "" {
				return nil, ufwNeedsAPerson()
			}
			refusal = err
			return nil, nil
		}
		inactive = nil
		if errors.As(err, &blocks) && !ans.yes && stdinIsTerminal(stdin) {
			refusal = err
			return nil, nil
		}
		return p, err
	})
	if err != nil {
		return err
	}
	if plan == nil {
		shown, listed := []hostadmit.Blocked(nil), false
		if inactive != nil {
			if ans.yes && len(inactive.Blocked) > 0 {
				return &hostadmit.UFWBlocksError{Blocked: inactive.Blocked}
			}
			yes, err := askEnableUFW(ans, stdin, stdout, inactive.SSHPorts, inactive.Blocked, site != "")
			if err != nil {
				return err
			}
			if !yes {
				return refusal
			}
			in.Flags.EnableUFW = true
			shown, listed = inactive.Blocked, true
		} else {
			shown = blocks.Blocked
		}
		if len(shown) > 0 {
			yes, err := askBlockListed(stdin, stdout, shown, listed)
			if err != nil {
				return err
			}
			if !yes && blocks != nil {
				return refusal
			}
			if !yes {
				return &hostadmit.UFWBlocksError{Blocked: shown}
			}
			in.Flags.EnableUFWBlockListed = true
		}
		if plan, err = ui.Get(r, "read the host again, to enable ufw", func() (*hostadmit.Plan, error) { return hostadmit.Build(t, in) }); err != nil {
			return err
		}
		if more := unshown(plan.UFWBlocks, shown); len(more) > 0 {
			return fmt.Errorf("site admit: the host changed since admit asked: enabling ufw now blocks %s as well, which you were not shown. Run admit again to see what it blocks. Nothing was changed", strings.Join(more, ", "))
		}
	}
	// A host with no swap: with --execute, the owner is asked whether to
	// make a swapfile, and the plan is read again to make it.
	if execute && plan.SwapOffer > 0 && ans.canAnswer(stdin) {
		yes, err := askSwapfile(ans, stdin, stdout, plan.SwapOffer)
		if err != nil {
			return err
		}
		if yes {
			in.Flags.Swapfile = true
			if plan, err = ui.Get(r, "read the host again, to make a swapfile", func() (*hostadmit.Plan, error) { return hostadmit.Build(t, in) }); err != nil {
				return err
			}
		}
	}
	// The whole plan, always: with --execute the owner approves it here,
	// before the digest and before anything runs, and its rows then show
	// the run.
	if !execute {
		plan.Show(r)
		r.Result("Nothing was changed. Run it again with --execute to make these changes.")
		return nil
	}
	plan.ShowToRun(r)
	if run.approve {
		resume := ui.Hold(r)
		yes, err := ans.yesNo(stdin, stdout, "", fmt.Sprintf("Make these changes on %s?", t.Describe()), "admit's plan")
		resume()
		if err != nil {
			return fmt.Errorf("site admit: reading your answer: %w. Nothing was changed", err)
		}
		if !yes {
			return errAdmitDeclined
		}
	}
	if request != "" {
		// The prompt is held as one, so the run draws the plan's rows
		// again below it rather than reach above it.
		resume := ui.Hold(r)
		err := confirmDigest(stdin, stdout, in.Digest, digestFlag)
		resume()
		if err != nil {
			return err
		}
	}
	ran = true
	if err := executeAdmit(t, in, plan, r); err != nil {
		return ufwFallbackNote(err, plan, false)
	}
	user := ui.Visible(in.Request.User)
	version, err := ui.Get(r, "check "+user+"'s Docker", func() (string, error) { return hostadmit.CheckUser(t, in.Request) })
	if err != nil {
		return ufwFallbackNote(err, plan, true)
	}
	if site == "" {
		if plan.EnablesUFW {
			return confirmUFW(stdin, stdout, r, t, user, version)
		}
		r.Result("Admitted: %s runs Docker %s. The operators' next command logs in as %s.", user, version, user)
		return nil
	}
	// The operators' first login as the user, with the operator's own ssh,
	// which records ssh.user where the declaration leaves it out. It goes
	// to the declared host, which an alias or an address in --ssh may name
	// otherwise, so a failure names both.
	login := reachSiteUser(site, declared)
	if err := ui.Run(r, "log in as "+user, func() error {
		_, err := login.Run("docker version --format '{{.Server.Version}}'")
		return err
	}); err != nil {
		where := ""
		if h := declared.SSHHost(); h != dest.Host && h != dest.Alias {
			where = fmt.Sprintf(" The login went to %s, the site's declared ssh host, and admit to %s: check that they are the same machine.", ui.Visible(h), ui.Visible(dest.Host))
		}
		return ufwFallbackNote(fmt.Errorf("site admit: %s is admitted and its Docker answers, but logging in as it failed: %w. Check that your key is one of sites.%s.ssh.keys.%s", user, err, site, where), plan, true)
	}
	// That login was a new SSH connection, made after ufw was enabled, so
	// ufw lets SSH in: the fallback is stopped, through the owner's login.
	if plan.EnablesUFW {
		if err := ui.Run(r, "stop the ufw fallback", func() error { return hostadmit.Disarm(t) }); err != nil {
			return fmt.Errorf("site admit: %s logs in through ufw, but %w. ufw turns itself off again by %s: run sudo %s on the host to keep it on", user, err, ufwOffAt(), hostadmit.DisarmUFW)
		}
	}
	r.Result("Admitted: %s runs Docker %s, and logs in with the operators' keys.", user, version)
	return nil
}

// ownerDestination is where command (site admit or site evict) --site
// logs in as the owner: --ssh as
// user@host[:port] or an alias of the operator's ssh config (see
// parseDestination), or a login alone for the site's declared ssh host and
// port, or, with no --ssh, a login asked for at a terminal. Off a terminal
// with no --ssh there is no one to ask, so it refuses.
//
// A word alone, with no @, is a login or an alias, and is never guessed: an
// ssh config that expands short names (Host * with HostName %h.example.org)
// maps every word to some host. With a port (alias:port), which no login
// carries, it is an alias. Otherwise it is an alias only where ssh -G maps
// it to the site's declared host and port, as ssh -G resolves those; a word
// mapped to any other host is refused, naming both spellings; a word mapped
// to none is a login. A typed answer at the terminal is always a login, as
// the question asks for one.
//
// A login alone goes to the declared host on the declared port, written, as
// every command reaches a site: ssh.port, or 22.
func ownerDestination(command, sshFlag, site string, declared config.Site, stdin io.Reader, stdout io.Writer) (config.Destination, error) {
	host, port := declared.SSHHost(), declared.SSH.PortOrDefault()
	declaredAt := net.JoinHostPort(host, strconv.Itoa(port))
	if sshFlag != "" && !strings.Contains(sshFlag, "@") {
		if strings.Contains(sshFlag, ":") {
			dest, err := parseDestination(sshFlag)
			if err != nil {
				return config.Destination{}, sshFlagProblem(sshFlag, err)
			}
			return dest, nil
		}
		if dest, err := parseDestination(sshFlag); err == nil && dest.Alias != "" {
			target, err := parseDestination("root@" + declaredAt)
			if err == nil && strings.EqualFold(dest.Host, target.Host) && dest.Port == target.Port {
				return dest, nil
			}
			return config.Destination{}, &ui.Problem{
				Hint:    fmt.Sprintf("--ssh %s is an alias or a login", sshFlag),
				Explain: fmt.Sprintf("Your ssh config maps %s to %s, which is not %s, site %s's declared ssh host and port. Write --ssh user@%s for that alias, or --ssh %s@%s for a login on the declared host. Nothing was read or changed.", ui.Visible(sshFlag), ui.Visible(dest.Host+":"+strconv.Itoa(dest.Port)), ui.Visible(declaredAt), site, ui.Visible(sshFlag), ui.Visible(sshFlag), ui.Visible(declaredAt)),
			}
		}
		if !config.ValidLogin(sshFlag) {
			return config.Destination{}, &ui.Problem{
				Hint:    fmt.Sprintf("--ssh %s is not a login", sshFlag),
				Explain: fmt.Sprintf("A word alone is your login on site %s's declared host, %s, or an alias of your ssh config for that host. Write --ssh user@%s to reach %s as a host. Nothing was read or changed.", site, ui.Visible(declaredAt), ui.Visible(sshFlag), ui.Visible(sshFlag)),
			}
		}
	}
	verb := strings.TrimPrefix(command, "host ")
	login := sshFlag
	if login == "" {
		if !stdinIsTerminal(stdin) {
			return config.Destination{}, fmt.Errorf("%s: --site needs --ssh off a terminal, your own login on %s, which runs %s through sudo: --ssh <user>, or --ssh user@host[:port] for another address. Nothing was read or changed", command, ui.Visible(host), verb)
		}
		fmt.Fprintf(stdout, "Existing username on %s (site %s) to connect under, with sudo: ", ui.Visible(host), site)
		line, err := readTypedLine(stdin)
		if err != nil {
			return config.Destination{}, fmt.Errorf("%s: reading your login: %w. Nothing was read or changed", command, err)
		}
		if login = strings.TrimSpace(line); login == "" {
			return config.Destination{}, fmt.Errorf("%s: no login was given. Nothing was read or changed", command)
		}
	}
	if !strings.Contains(login, "@") {
		login += "@" + declaredAt
	}
	dest, err := parseDestination(login)
	if err != nil {
		return config.Destination{}, sshFlagProblem(login, err)
	}
	return dest, nil
}

// askEnableUFW asks the owner at a terminal whether admit may enable ufw,
// saying what it does, in order, and the risk. Only y or yes says so, or
// --yes, through a, which only --site reaches.
func askEnableUFW(a answerer, stdin io.Reader, stdout io.Writer, ports []int, blocked []hostadmit.Blocked, bySite bool) (bool, error) {
	var list []string
	for _, port := range ports {
		list = append(list, strconv.Itoa(port)+"/tcp")
	}
	confirm := "you open a new SSH session to this host when admit asks, and say it logged in"
	if bySite {
		confirm = "admit's own login as the deployment's user, at its end"
	}
	explain := fmt.Sprintf("\nufw is installed but not active on this host. Admit can enable it, in this order:\n"+
		"  1. allow %s, every port sshd listens on, tagged paisans: ssh, which evict never removes\n"+
		"  2. arm a timer that runs ufw disable in 10 minutes\n"+
		"  3. enable ufw, the deployment's own rules already in place\n"+
		"  4. stop the timer once a new SSH login gets through: %s\n"+
		"The risk: something admit cannot see, such as a cloud firewall, could still block SSH. You are then locked out until the timer turns ufw off again, at most 10 minutes later. The timer does not survive a reboot: a reboot before it fires leaves ufw enabled, so wait for it rather than reboot.\n"+hostadmit.CannotSee+"\n", strings.Join(list, ", "), confirm)
	if len(blocked) > 0 {
		explain += "Enabling ufw also blocks these, which answer from off this host now and no ufw rule allows:\n" + hostadmit.BlockedList(blocked) +
			"On a yes, admit asks next whether every one of them may be blocked.\n"
	}
	yes, err := a.yesNo(stdin, stdout, explain, "Enable ufw (--enable-ufw)?", "enable ufw (--enable-ufw)")
	if err != nil {
		return false, fmt.Errorf("site admit: reading your answer: %w. Nothing was changed", err)
	}
	return yes, nil
}

// askSwapfile asks the owner at a terminal whether admit may make a
// swapfile of size bytes on a host with no swap. Only y or yes says so, or
// --yes, through a. A no admits the host without one.
func askSwapfile(a answerer, stdin io.Reader, stdout io.Writer, size int64) (bool, error) {
	explain := fmt.Sprintf("\nThis host has no swap, or only zram, which adds nothing to its memory. %s\n"+
		"Admit can make a swapfile of %s at /swapfile, twice the host's RAM up to %s, and turn it on at boot through /etc/fstab.\n", hostadmit.SwapNeeded, hostadmit.GiB(size), hostadmit.GiB(hostadmit.SwapMax))
	yes, err := a.yesNo(stdin, stdout, explain, "Make the swapfile (--swapfile)?", "make a swapfile (--swapfile)")
	if err != nil {
		return false, fmt.Errorf("site admit: reading your answer: %w. Nothing was changed", err)
	}
	return yes, nil
}

// askBlockListed asks the owner at a terminal whether admit may enable ufw
// over blocked, what answers from off the host and no ufw rule allows,
// listing them unless the question before did (listed). Only y or yes
// says so; --yes never reaches it.
func askBlockListed(stdin io.Reader, stdout io.Writer, blocked []hostadmit.Blocked, listed bool) (bool, error) {
	explain := "\n"
	if !listed {
		explain += "Enabling ufw blocks these, which answer from off this host now and no ufw rule allows:\n" + hostadmit.BlockedList(blocked)
	}
	explain += hostadmit.BlockedHelp(blocked) + "\n"
	yes, err := answerer{}.yesNo(stdin, stdout, explain, "Block every one listed (--enable-ufw-block-listed)? Answer no to allow them first", "")
	if err != nil {
		return false, fmt.Errorf("site admit: reading your answer: %w. Nothing was changed", err)
	}
	return yes, nil
}

// unshown is each of keys that is no Key of shown.
func unshown(keys []string, shown []hostadmit.Blocked) []string {
	var out []string
	for _, k := range keys {
		if !slices.ContainsFunc(shown, func(b hostadmit.Blocked) bool { return b.Key == k }) {
			out = append(out, k)
		}
	}
	return out
}

// ufwNeedsAPerson refuses --request where admit would enable ufw and no
// person is there to confirm a new SSH login: with --yes, or with
// --execute off a terminal. Once ufw is on, only a person opening a new
// SSH session shows that SSH gets through.
func ufwNeedsAPerson() error {
	return fmt.Errorf("site admit: with --request, enabling ufw needs a person at a terminal: once ufw is on, admit waits for you to open a new SSH session to this host and say it logged in, before it stops the timer that turns ufw off again, and neither --yes nor a pipe answers that. Run %s site admit --site <site> --ssh <your login> from a workstation instead, whose own login as the deployment's user proves SSH gets through; or run this at a terminal without --yes. Nothing was changed", progname.Name())
}

// ufwOffAt is the latest the fallback armed by now turns ufw off.
func ufwOffAt() string { return time.Now().Add(hostadmit.FallbackAfter).Format("15:04 MST") }

// ufwFallbackNote is err, from a plan that enables ufw, with what the
// owner needs to know of the fallback: armed is whether admit is known to
// have armed it.
func ufwFallbackNote(err error, plan *hostadmit.Plan, armed bool) error {
	if !plan.EnablesUFW {
		return err
	}
	when := "ufw turns itself off again by " + ufwOffAt() + ", since no new SSH login has shown that it lets SSH in"
	if !armed {
		when = "If admit armed the ufw fallback before it stopped, " + when
	}
	return fmt.Errorf("%w\n%s. Once a new SSH login to this host works, run sudo %s to keep ufw on; or let it turn off, and run admit again with --enable-ufw. The timer does not survive a reboot: a reboot before it fires leaves ufw enabled, so wait for it rather than reboot", err, when, hostadmit.DisarmUFW)
}

// confirmUFW, on the host with --request once admit has enabled ufw, asks
// the owner to open a new SSH session and stops the fallback only on a
// yes typed at a terminal. Anything else, and any stdin that is not a
// terminal, leaves the timer to turn ufw off, and says so.
func confirmUFW(stdin io.Reader, stdout io.Writer, r ui.Reporter, t apply.Transport, user, version string) error {
	off := ufwOffAt()
	line, err := "", error(nil)
	if stdinIsTerminal(stdin) {
		fmt.Fprintf(stdout, "\nufw is on, and turns itself off again by %s unless a new SSH login gets through. Open a new SSH session to this host now. Type yes once it logs in: ", off)
		line, err = readTypedLine(stdin)
	}
	if err != nil || strings.ToLower(strings.TrimSpace(line)) != "yes" {
		r.Warn("ufw turns itself off again by "+off,
			"No new SSH login was confirmed, so the timer admit armed is left to run ufw disable. If a new login works before then, run sudo "+hostadmit.DisarmUFW+" to keep ufw on; otherwise run admit again with --enable-ufw once you know why SSH was blocked. The timer does not survive a reboot: a reboot before it fires leaves ufw enabled, so wait for it rather than reboot.")
		r.Result("Admitted: %s runs Docker %s. The operators' next command logs in as %s. ufw turns itself off again by %s.", user, version, user, off)
		return nil
	}
	if err := ui.Run(r, "stop the ufw fallback", func() error { return hostadmit.Disarm(t) }); err != nil {
		return fmt.Errorf("site admit: %w. ufw turns itself off again by %s: run sudo %s to keep it on", err, off, hostadmit.DisarmUFW)
	}
	r.Result("Admitted: %s runs Docker %s, and ufw is on. The operators' next command logs in as %s.", user, version, user)
	return nil
}

// askGrant puts g to the owner at a terminal: what it gives, and whether to
// grant it. Only y or yes grants it, or --yes, through a.
func askGrant(a answerer, stdin io.Reader, stdout io.Writer, req admission.Request, g hostadmit.Grant) (bool, error) {
	// Unless is a clause after Early's "or do not admit it"; here it is a
	// sentence of its own.
	unless := ""
	if u := strings.TrimSpace(strings.TrimPrefix(g.Unless, ";")); u != "" {
		unless = " " + strings.ToUpper(u[:1]) + u[1:] + "."
	}
	explain := fmt.Sprintf("\nSite %s (roles: %s): %s.\n%s%s\n", ui.Visible(req.Site), strings.Join(req.Roles, ", "), g.Hint, g.Explain, unless)
	yes, err := a.yesNo(stdin, stdout, explain, "Grant "+g.Flag+"?", "grant "+g.Flag)
	if err != nil {
		return false, fmt.Errorf("site admit: reading your answer: %w. Nothing was read or changed", err)
	}
	return yes, nil
}

// readTypedLine reads one line from stdin a byte at a time, so that nothing
// typed after it, such as the digest admit asks for next, is read ahead and
// lost.
func readTypedLine(stdin io.Reader) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for {
		n, err := stdin.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(one[0])
		}
		if err == io.EOF {
			return b.String(), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// readRequest reads a request file, refusing one larger than any request.
func readRequest(command, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", command, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, admission.MaxRequestSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", command, path, err)
	}
	if len(data) > admission.MaxRequestSize {
		return nil, fmt.Errorf("%s: %s is larger than any request (%d bytes). Nothing was read or changed", command, path, admission.MaxRequestSize)
	}
	return data, nil
}

// confirmDigest asks for the request's digest at a terminal and refuses
// anything that is not it, read as ParseDigest reads it. given is --digest,
// checked already when the request was read, which takes the place of the
// typed one. --yes never answers it.
func confirmDigest(stdin io.Reader, stdout io.Writer, digest, given string) error {
	if given != "" {
		return checkDigest(given, digest)
	}
	if !stdinIsTerminal(stdin) {
		return fmt.Errorf("site admit: --execute with --request asks you to type the request's digest at a terminal, and stdin is not one. Run it from an interactive shell, or give the digest as --digest <digest>. Nothing was changed")
	}
	fmt.Fprintf(stdout, "\nThe request carries the keys that will log in to this machine. Type the digest the operator gave you, over a channel you trust: ")
	// A byte at a time, so the confirmation of a new SSH login that admit
	// asks for after it, once it has enabled ufw, is not read ahead.
	line, err := readTypedLine(stdin)
	if err != nil || line == "" {
		return fmt.Errorf("site admit: no digest read. Nothing was changed")
	}
	return checkDigest(line, digest)
}

// checkDigest refuses given, a digest typed or passed as --digest, unless
// it is the request's, read as ParseDigest reads it.
func checkDigest(given, digest string) error {
	typed, err := admission.ParseDigest(strings.TrimSpace(given))
	if err != nil || typed != digest {
		return fmt.Errorf("site admit: that is not this request's digest, %s. The request may have been changed on its way: ask the operator for it again. Nothing was changed", digest)
	}
	return nil
}

// runSiteEvict is `paisans site evict`: site admit undone for one
// deployment, by the host's owner, the same two ways.
func runSiteEvict(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("site evict", flag.ContinueOnError)
	reporter := commonFlags(fs)
	request := fs.String("request", "", "the request the deployment was admitted from, on this host, as root")
	id := fs.String("deployment", "", "the deployment's id, with --user, on this host, as root")
	user := fs.String("user", "", "the deployment's user, with --deployment")
	site := fs.String("site", "", "the site to evict, from the configuration, with --ssh")
	sshFlag := fs.String("ssh", "", "your own login on the host, with --site: user, for the site's declared host, or user@host[:port] or an alias of your ssh config; asked for at a terminal when left out")
	configPath := fs.String("config", config.FileName, "path to the deployment declaration, with --site")
	dataDir := fs.String("data-dir", "", "the --data-dir admit was given, needed only when the host holds no admission record")
	execute := fs.Bool("execute", false, "make the changes the plan shows")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("site evict takes no argument. Got: %s", quoteAll(fs.Args()))
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	modes := 0
	for _, set := range []bool{*request != "", *id != "" || *user != "", *site != ""} {
		if set {
			modes++
		}
	}
	switch {
	case modes != 1:
		return fmt.Errorf("site evict: give one of --request <file>, --deployment <id> --user <name> (both on the host), or --site <name> --ssh <your login>")
	case (*id == "") != (*user == ""):
		return fmt.Errorf("site evict: --deployment and --user go together")
	case *site != "" && *sshFlag == "" && ans.yes:
		return fmt.Errorf("site evict: --yes does not choose a login: give --ssh <user>, your own login on the site's declared host, or --ssh user@host[:port]. Nothing was read or changed")
	case *site != "" && *sshFlag == "" && !stdinIsTerminal(stdin):
		return fmt.Errorf("site evict: --site needs --ssh off a terminal, your own login on the host: --ssh <user> for the site's declared host, or --ssh user@host[:port]. Nothing was read or changed")
	case *site == "" && *sshFlag != "":
		return fmt.Errorf("site evict: --ssh goes with --site")
	case *site == "" && !runningAsRootOnLinux():
		return fmt.Errorf("site evict: --request and --deployment run as root on the host itself: run sudo %s site evict there", progname.Name())
	}
	r := reporter()
	in := hostadmit.EvictInput{DataDir: *dataDir}
	var declared config.Site
	switch {
	case *request != "":
		data, err := readRequest("site evict", *request)
		if err != nil {
			return err
		}
		req, err := admission.Parse(data)
		if err != nil {
			return fmt.Errorf("site evict: %s: %w", *request, err)
		}
		in.Deployment, in.User = req.Deployment, req.User
	case *id != "":
		in.Deployment, in.User = *id, *user
	default:
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		if result := validate.Check(cfg); result.Refused() {
			reportFindings(r, *configPath, result)
			return refused(*configPath, len(result.Refusals()), "Nothing was read or changed.")
		}
		s, ok := cfg.Sites[*site]
		if !ok {
			return fmt.Errorf("site evict: %s declares no site %q", *configPath, *site)
		}
		in.Deployment, in.User = cfg.ID, s.SSHUser()
		declared = s
	}
	var t apply.Transport
	if *site != "" {
		dest, err := ownerDestination("site evict", *sshFlag, *site, declared, stdin, stdout)
		if err != nil {
			return err
		}
		t = reachOwnerAdmit(dest)
	} else {
		os.Unsetenv("DOCKER_HOST")
		os.Unsetenv("DOCKER_CONTEXT")
		t = localTransport()
	}
	r.Section(fmt.Sprintf("evict %s from %s", ui.Visible(in.User), t.Describe()))
	plan, err := ui.Get(r, "read the host", func() (*hostadmit.Plan, error) { return hostadmit.BuildEvict(t, in) })
	if err != nil {
		return err
	}
	if !*execute {
		plan.Show(r)
		r.Result("Nothing was changed. Run it again with --execute to evict.")
		return nil
	}
	plan.ShowToRun(r)
	if len(plan.Steps) == 0 {
		r.Result("Nothing of deployment %s is left on %s.", in.Deployment, t.Describe())
		return nil
	}
	what := ui.Danger(stdout, fmt.Sprintf("This deletes %s with its home, its Docker data and everything of deployment %s on %s. Nothing brings it back.", in.User, in.Deployment, t.Describe()))
	// Held as the digest is in admit: the run draws the rows again below.
	resume := ui.Hold(r)
	err = confirmWord(ans, stdin, stdout, in.User, what)
	resume()
	if err != nil {
		return fmt.Errorf("site evict: %w", err)
	}
	if err := hostadmit.RunSteps(t, plan, r); err != nil {
		return err
	}
	r.Result("Evicted: nothing of deployment %s is left on %s but what the plan said it kept.", in.Deployment, t.Describe())
	return nil
}
