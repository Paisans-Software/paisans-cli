package main

import (
	"flag"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// sshAlias is a Host of the operator's own ssh config: a name ssh resolves
// to its host, user and port. It never starts with a dash, which ssh would
// read as an option.
var sshAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// grantsDestination is --ssh as operator grants takes it, as ssh is given
// it: user@host[:port], or an alias of the operator's own ssh config.
func grantsDestination(s string) (string, error) {
	if strings.Contains(s, "@") {
		dest, err := config.ParseDestination(s)
		if err != nil {
			return "", err
		}
		return destinationArg(dest), nil
	}
	if !sshAlias.MatchString(s) {
		return "", fmt.Errorf("%q is neither user@host[:port] nor an alias of your ssh config, Eg: admin@203.0.113.9 or home-a", s)
	}
	return s, nil
}

// reachGrantsHost is how operator grants reaches the host: the operator's
// own ssh, with their agent, keys and ssh config, and no sudo, since the
// admission records are 644 in a 755 directory. Tests replace it.
var reachGrantsHost = func(dest string) apply.Transport { return grantsTransport(dest) }

// grantsTransport is plain ssh to dest, as whoever dest logs in as: not a
// deployment's user, so nothing points docker at a user's daemon.
func grantsTransport(dest string) apply.SSHTransport { return apply.SSHTransport{Destination: dest} }

// admitFlags are the site admit flags rec records, in the order site admit
// lists them: what admitting the site again takes to plan nothing more.
// --enable-ufw is never one: ufw admit enabled is on.
func admitFlags(rec admission.Record) []string {
	var flags []string
	if rec.Grants.Watchdog {
		flags = append(flags, "--watchdog")
	}
	if rec.Grants.PublicPorts {
		flags = append(flags, "--public-ports")
	}
	if rec.DataDir != "" {
		flags = append(flags, "--data-dir", apply.ShellQuote(path.Dir(rec.DataDir)))
	}
	if rec.BridgeNetfilter {
		flags = append(flags, "--bridge-netfilter")
	}
	if rec.MeshLAN != "" {
		flags = append(flags, "--mesh-lan")
	}
	return flags
}

// runOperatorGrants is `paisans operator grants`: each admission record on
// the host --ssh names, as the site admit flags that admitted it. It needs
// no configuration and changes nothing.
func runOperatorGrants(args []string) error {
	fs := newFlagSet("operator grants", flag.ContinueOnError)
	reporter := commonFlags(fs)
	sshFlag := fs.String("ssh", "", "the host to read, user@host[:port] or an alias of your ssh config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("operator grants takes no argument: paisans operator grants --ssh <user@host[:port] or alias>. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if *sshFlag == "" {
		return fmt.Errorf("operator grants: name the host with --ssh user@host[:port], or an alias of your ssh config, Eg: --ssh admin@203.0.113.9")
	}
	dest, err := grantsDestination(*sshFlag)
	if err != nil {
		return fmt.Errorf("operator grants: --ssh: %w", err)
	}
	r := reporter()
	t := reachGrantsHost(dest)
	r.Section("grants on " + *sshFlag)
	s := r.Step("read " + *sshFlag)
	out, err := t.Run(hostDeploymentsAdmitted)
	if err != nil {
		s.Fail(err)
		return fmt.Errorf("operator grants: %s: listing the admission records in %s failed, so nothing is listed: %w", *sshFlag, admission.Dir, err)
	}
	records, err := parseAdmitted(out)
	if err != nil {
		s.Fail(err)
		return fmt.Errorf("operator grants: %s: listing the admission records in %s: %w. Nothing is listed", *sshFlag, admission.Dir, err)
	}
	s.Done("")
	read := 0
	for _, a := range records {
		if a.err != nil {
			r.Warn(fmt.Sprintf("%s: %v", a.path, a.err), "")
			continue
		}
		rec := a.rec
		roles := strings.Join(rec.Roles, ", ")
		if roles == "" {
			roles = "no role"
		}
		command := strings.Join(append([]string{"paisans site admit --site", apply.ShellQuote(rec.Site)}, admitFlags(rec)...), " ")
		r.Step(fmt.Sprintf("site %s of %s (%s), as %s: %s", ui.Visible(rec.Site), rec.Deployment, roles, rec.User, ui.Visible(command))).End(ui.OK, "")
		read++
	}
	if len(records) == 0 {
		r.Result("No admission record in %s: no deployment is admitted to %s.", admission.Dir, *sshFlag)
		return nil
	}
	r.Result("%d admission record(s) read. Admit a site again with its command above, adding --ssh and anything new, such as --swapfile; its dry run plans nothing else.", read)
	return nil
}
