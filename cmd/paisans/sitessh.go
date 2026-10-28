package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// runSiteSSH is `paisans site ssh`: the operator's own ssh session on a
// site's host, logged in as every command logs in there. That is the site's
// ssh.user, or the deployment's own user when it names none, with the keys
// its ssh.keys lists, so a shell lands where a command's work lands: in the
// deployment user's home, with its rootless Docker, rather than in the
// operator's own account, which a plain `ssh host` reaches.
//
// Words after `--` are a command to run there instead of a shell, as for
// ssh. They are split off before the flags are read, so a flag of the
// command (Eg: docker ps --all) is never taken for one of site ssh's.
//
// It loads the declaration without validating it, as site list does: a
// session on the host is how a broken deployment is looked at, and a
// declaration validate refuses for something else still says how to reach
// its sites. It writes nothing, here or on the host; what the session does
// is the operator's.
func runSiteSSH(args []string) error {
	var command []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, command = args[:i], args[i+1:]
	}
	fs := newFlagSet("site ssh", flag.ContinueOnError)
	verboseFlags(fs, &verboseRun)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	usage := "Usage: " + progname.Cmd() + " site ssh <site> [--config " + config.FileName + "] [-- command...]"
	site := ""
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		site = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	if site == "" {
		return &ui.Problem{Hint: "site ssh takes one site", Explain: "Name it as site list shows it. " + usage}
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "site ssh takes one site", Explain: "Got extra argument(s): " + quoteAll(fs.Args()) + ". A command to run goes after --. " + usage}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	s, ok := cfg.Sites[site]
	if !ok {
		return fmt.Errorf("site ssh: %s declares no site %q. Declared sites are %s", *configPath, site, strings.Join(cfg.SiteNames(), ", "))
	}
	if s.SSHHost() == "" {
		return fmt.Errorf("site ssh: %s gives site %q no host to reach: set its ssh.host, public_address or endpoint", *configPath, site)
	}
	if _, problems := s.SSH.AuthorizedKeys(); len(problems) > 0 {
		return &ui.Problem{Hint: "site ssh: site " + site + "'s ssh.keys cannot be offered", Explain: strings.Join(problems, " ")}
	}
	err = shellSite(siteTransport(site, s, ""), command)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return remoteExit(exit.ExitCode())
	}
	return err
}

// shellSite runs the session; a test replaces it to see the transport.
var shellSite = func(t apply.SSHTransport, command []string) error {
	return t.Shell(command, os.Stdin, os.Stdout, os.Stderr)
}

// remoteExit is the status a site ssh session ended with: the remote
// command's, or 255 for ssh's own failure. ssh has already said what went
// wrong, so the process exits with it and prints nothing more, as a plain
// ssh does.
type remoteExit int

func (e remoteExit) Error() string { return fmt.Sprintf("ssh exited with status %d", int(e)) }
