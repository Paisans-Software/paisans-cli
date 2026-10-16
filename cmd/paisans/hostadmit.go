package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
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

// runHostAdmit is `paisans host admit`: the one root step for a deployment
// on a host, run by its owner, from a request on the host or from the
// configuration over the owner's own ssh.
func runHostAdmit(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("host admit", flag.ContinueOnError)
	reporter := commonFlags(fs)
	var (
		request, site, sshFlag, configPath string
		flags                              hostadmit.Flags
		execute                            bool
	)
	fs.StringVar(&request, "request", "", "the request file the operator sent, on this host, as root")
	fs.StringVar(&site, "site", "", "the site to admit, from the configuration, with --ssh")
	fs.StringVar(&sshFlag, "ssh", "", "your own login on the host, user@host[:port], with --site")
	fs.StringVar(&configPath, "config", config.FileName, "path to the deployment declaration, with --site")
	fs.BoolVar(&flags.Watchdog, "watchdog", false, "grant the host's watchdog, which can reboot it, to a data site's Patroni")
	fs.BoolVar(&flags.PublicPorts, "public-ports", false, "let every local user bind ports 80 to 1023, and open 80 and 443, for the toolkit's Caddy")
	fs.StringVar(&flags.DataDir, "data-dir", "", "keep the deployment's Docker data under this directory instead of the user's home")
	fs.BoolVar(&flags.BridgeNetfilter, "bridge-netfilter", false, "load br_netfilter although this host has bridges of its own")
	fs.BoolVar(&flags.MeshLAN, "mesh-lan", false, "let the mesh dial this host's own LAN")
	fs.BoolVar(&execute, "execute", false, "make the changes the plan shows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host admit takes no argument. Got: %s", quoteAll(fs.Args()))
	}
	switch {
	case request == "" && site == "":
		return fmt.Errorf("host admit: give --request <file> on the host, or --site <name> --ssh <your login> from a workstation")
	case request != "" && site != "":
		return fmt.Errorf("host admit: --request or --site, not both")
	case site != "" && sshFlag == "":
		return fmt.Errorf("host admit: --site needs --ssh, your own login on the host (user@host[:port]), which runs admit through sudo")
	case request != "" && sshFlag != "":
		return fmt.Errorf("host admit: --request runs on the host itself and takes no --ssh")
	case request != "" && !runningAsRootOnLinux():
		return fmt.Errorf("host admit: --request runs as root on the host it admits to: copy the request there and run sudo paisans host admit --request <file>")
	}
	r := reporter()
	in := hostadmit.Input{Flags: flags, Now: time.Now()}
	var declared config.Site
	if request != "" {
		data, err := readRequest("host admit", request)
		if err != nil {
			return err
		}
		if in.Request, err = admission.Parse(data); err != nil {
			return fmt.Errorf("host admit: %s: %w", request, err)
		}
		in.Digest = admission.Digest(data)
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
			return fmt.Errorf("host admit: %w", err)
		}
		declared = cfg.Sites[site]
	}
	// The grants first, before any transport exists: the owner's decision
	// needs nothing from the host.
	if err := hostadmit.Early(in); err != nil {
		return err
	}
	var t apply.Transport
	if request != "" {
		// The owner's environment never points admit's Docker commands
		// at another daemon; over ssh, sudo's env_reset does the same.
		os.Unsetenv("DOCKER_HOST")
		os.Unsetenv("DOCKER_CONTEXT")
		t = localTransport()
		r.Result("request digest %s: compare it with the one the operator gave you, over a channel you both trust, such as a call", in.Digest)
	} else {
		dest, err := config.ParseDestination(sshFlag)
		if err != nil {
			return sshFlagProblem(sshFlag, err)
		}
		t = reachOwnerAdmit(dest)
	}
	r.Section(fmt.Sprintf("admit %s on %s", ui.Visible(in.Request.User), t.Describe()))
	plan, err := ui.Get(r, "read the host", func() (*hostadmit.Plan, error) { return hostadmit.Build(t, in) })
	if err != nil {
		return err
	}
	// The whole plan, always: with --execute the owner approves it here,
	// before the digest and before anything runs.
	plan.Show(r)
	if !execute {
		r.Result("Nothing was changed. Run it again with --execute to make these changes.")
		return nil
	}
	if request != "" {
		if err := confirmDigest(stdin, stdout, in.Digest); err != nil {
			return err
		}
	}
	if err := executeAdmit(t, in, plan, r); err != nil {
		return err
	}
	user := ui.Visible(in.Request.User)
	version, err := ui.Get(r, "check "+user+"'s Docker", func() (string, error) { return hostadmit.CheckUser(t, in.Request) })
	if err != nil {
		return err
	}
	if site == "" {
		r.Result("Admitted: %s runs Docker %s. The operators' next command logs in as %s.", user, version, user)
		return nil
	}
	// The operators' first login as the user, with the operator's own ssh,
	// which records ssh.user where the declaration leaves it out.
	login := reachSiteUser(site, declared)
	if err := ui.Run(r, "log in as "+user, func() error {
		_, err := login.Run("docker version --format '{{.Server.Version}}'")
		return err
	}); err != nil {
		return fmt.Errorf("host admit: %s is admitted and its Docker answers, but logging in as it failed: %w. Check that your key is one of sites.%s.ssh.keys", user, err, site)
	}
	r.Result("Admitted: %s runs Docker %s, and logs in with the operators' keys.", user, version)
	return nil
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
// anything that is not it, read as ParseDigest reads it. There is no flag
// to answer it, so it cannot be scripted past.
func confirmDigest(stdin io.Reader, stdout io.Writer, digest string) error {
	if !stdinIsTerminal(stdin) {
		return fmt.Errorf("host admit: --execute with --request asks you to type the request's digest at a terminal, and stdin is not one. Run it from an interactive shell. Nothing was changed")
	}
	fmt.Fprintf(stdout, "\nThe request carries the keys that will log in to this machine. Type the digest the operator gave you, over a channel you trust: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("host admit: no digest read. Nothing was changed")
	}
	typed, err := admission.ParseDigest(strings.TrimSpace(line))
	if err != nil || typed != digest {
		return fmt.Errorf("host admit: that is not this request's digest, %s. The request may have been changed on its way: ask the operator for it again. Nothing was changed", digest)
	}
	return nil
}

// runHostEvict is `paisans host evict`: host admit undone for one
// deployment, by the host's owner, the same two ways.
func runHostEvict(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("host evict", flag.ContinueOnError)
	reporter := commonFlags(fs)
	request := fs.String("request", "", "the request the deployment was admitted from, on this host, as root")
	id := fs.String("deployment", "", "the deployment's id, with --user, on this host, as root")
	user := fs.String("user", "", "the deployment's user, with --deployment")
	site := fs.String("site", "", "the site to evict, from the configuration, with --ssh")
	sshFlag := fs.String("ssh", "", "your own login on the host, user@host[:port], with --site")
	configPath := fs.String("config", config.FileName, "path to the deployment declaration, with --site")
	dataDir := fs.String("data-dir", "", "the --data-dir admit was given, needed only when the host holds no admission record")
	execute := fs.Bool("execute", false, "make the changes the plan shows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host evict takes no argument. Got: %s", quoteAll(fs.Args()))
	}
	modes := 0
	for _, set := range []bool{*request != "", *id != "" || *user != "", *site != ""} {
		if set {
			modes++
		}
	}
	switch {
	case modes != 1:
		return fmt.Errorf("host evict: give one of --request <file>, --deployment <id> --user <name> (both on the host), or --site <name> --ssh <your login>")
	case (*id == "") != (*user == ""):
		return fmt.Errorf("host evict: --deployment and --user go together")
	case *site != "" && *sshFlag == "":
		return fmt.Errorf("host evict: --site needs --ssh, your own login on the host (user@host[:port])")
	case *site == "" && *sshFlag != "":
		return fmt.Errorf("host evict: --ssh goes with --site")
	case *site == "" && !runningAsRootOnLinux():
		return fmt.Errorf("host evict: --request and --deployment run as root on the host itself: run sudo paisans host evict there")
	}
	r := reporter()
	in := hostadmit.EvictInput{DataDir: *dataDir}
	switch {
	case *request != "":
		data, err := readRequest("host evict", *request)
		if err != nil {
			return err
		}
		req, err := admission.Parse(data)
		if err != nil {
			return fmt.Errorf("host evict: %s: %w", *request, err)
		}
		in.Deployment, in.User = req.Deployment, req.User
	case *id != "":
		in.Deployment, in.User = *id, *user
	default:
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		s, ok := cfg.Sites[*site]
		if !ok {
			return fmt.Errorf("host evict: %s declares no site %q", *configPath, *site)
		}
		in.Deployment, in.User = cfg.ID, s.SSHUser()
	}
	var t apply.Transport
	if *site != "" {
		dest, err := config.ParseDestination(*sshFlag)
		if err != nil {
			return sshFlagProblem(*sshFlag, err)
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
	plan.Show(r)
	if !*execute {
		r.Result("Nothing was changed. Run it again with --execute to evict.")
		return nil
	}
	if len(plan.Steps) == 0 {
		r.Result("Nothing of deployment %s is left on %s.", in.Deployment, t.Describe())
		return nil
	}
	what := ui.Danger(stdout, fmt.Sprintf("This deletes %s with its home, its Docker data and everything of deployment %s on %s. Nothing brings it back.", in.User, in.Deployment, t.Describe()))
	if err := confirmWord(stdin, stdout, in.User, what); err != nil {
		return fmt.Errorf("host evict: %w", err)
	}
	if err := hostadmit.RunSteps(t, plan, r); err != nil {
		return err
	}
	r.Result("Evicted: nothing of deployment %s is left on %s but what the plan said it kept.", in.Deployment, t.Describe())
	return nil
}
