package main

import (
	"flag"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

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

// ownerTransport is how site deployments reaches a host as its owner: the
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

// reachOwner is ownerTransport for site deployments. Tests replace it.
var reachOwner = func(dest config.Destination, sudo bool) apply.Transport { return ownerTransport(dest, sudo) }

// deploymentsProbe lists, read only, each directory under the deployments'
// base. It fails, saying what, rather than answer partly: a directory it may
// not read (the registry's, which a read without sudo would take for an
// empty registry, or the deployments' base). A deployment's containers are
// in its user's own daemon, which root's docker does not reach, so none is
// listed. Its last line is "end", so an answer cut short is told from a host
// holding nothing.
var deploymentsProbe = fmt.Sprintf(`set -e; for d in %[1]s %[2]s; do if [ -e "$d" ] && ! { [ -r "$d" ] && [ -x "$d" ]; }; then echo "cannot read $d"; exit 3; fi; done; if [ -d %[2]s ]; then for d in %[2]s/*; do if [ -d "$d" ]; then echo "root ${d##*/}"; fi; done; fi; echo end`, registry.Dir, deployment.Base)

// hostDeploymentsAdmitted lists every admission record with its content,
// each preceded by its path, so one read lists them all. Records are 644 in a
// 755 directory: no sudo needed.
var hostDeploymentsAdmitted = `for f in ` + admission.Dir + `/admitted.*.json; do [ -f "$f" ] || continue; echo "record $f"; cat -- "$f"; echo; done; echo end`

// leftoverProbe is what deploymentsProbe found: the directories under the
// base.
type leftoverProbe struct {
	roots []string
}

func parseDeploymentsProbe(out string) (leftoverProbe, error) {
	var p leftoverProbe
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return p, fmt.Errorf("its answer was cut short: %s", strings.TrimSpace(out))
	}
	for _, line := range lines[:len(lines)-1] {
		root, ok := strings.CutPrefix(line, "root ")
		if !ok {
			return p, fmt.Errorf("an unreadable line: %q", line)
		}
		p.roots = append(p.roots, root)
	}
	return p, nil
}

// listedRecord is one admission record the listing found: its path, and
// the record or why it does not parse.
type listedRecord struct {
	path string
	rec  admission.Record
	err  error
}

// parseAdmitted reads hostDeploymentsAdmitted's answer: each record parsed
// by the token in its file's name. A line naming a record's path starts it,
// which no line of a record's indented JSON can look like; the listing's
// echo ends it with an empty line after the record's own final newline.
func parseAdmitted(out string) ([]listedRecord, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "end" {
		return nil, fmt.Errorf("its answer was cut short: %s", strings.TrimSpace(out))
	}
	var found []listedRecord
	var path string
	var content []string
	flush := func() {
		if path == "" {
			return
		}
		if n := len(content); n > 0 && content[n-1] == "" {
			content = content[:n-1]
		}
		token := strings.TrimSuffix(strings.TrimPrefix(path, admission.Dir+"/admitted."), ".json")
		rec, err := admission.ParseRecord([]byte(strings.Join(content, "\n")+"\n"), token)
		found = append(found, listedRecord{path: path, rec: rec, err: err})
	}
	for _, line := range lines[:len(lines)-1] {
		if p, ok := strings.CutPrefix(line, "record "+admission.Dir+"/"); ok {
			flush()
			path, content = admission.Dir+"/"+p, nil
			continue
		}
		if path == "" {
			return nil, fmt.Errorf("an unreadable line: %q", line)
		}
		content = append(content, line)
	}
	flush()
	return found, nil
}

// yesNo is b as the listing says it.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// runSiteDeployments is `paisans site deployments`: every admission record
// on the host --ssh names, then, through sudo, what its registry holds and
// what of a deployment is on it with no registry entry. With --sudo=false it
// lists the records alone, which any user can read. It needs no
// configuration, and changes nothing. See
// docs/specs/2026-10-10-remove-without-config.md.
func runSiteDeployments(args []string) error {
	fs := newFlagSet("site deployments", flag.ContinueOnError)
	reporter := commonFlags(fs)
	sshFlag := fs.String("ssh", "", "the host to read, user@host[:port]")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since the registry is root's; --sudo=false lists admission records only, which are readable without sudo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("site deployments takes no argument: paisans site deployments --ssh user@host[:port]. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if *sshFlag == "" {
		return fmt.Errorf("site deployments: name the host with --ssh user@host[:port], Eg: --ssh admin@203.0.113.9")
	}
	dest, err := config.ParseDestination(*sshFlag)
	if err != nil {
		return fmt.Errorf("site deployments: --ssh: %w", err)
	}
	r := reporter()
	t := reachOwner(dest, *sudo)
	r.Section(fmt.Sprintf("deployments on %s", dest))
	s := r.Step("read " + dest.String())
	out, err := t.Run(hostDeploymentsAdmitted)
	if err != nil {
		s.Fail(err)
		return fmt.Errorf("site deployments: %s: listing the admission records in %s failed, so nothing is listed: %w: %s", dest, admission.Dir, err, strings.TrimSpace(out))
	}
	records, err := parseAdmitted(out)
	if err != nil {
		s.Fail(err)
		return fmt.Errorf("site deployments: %s: listing the admission records in %s: %w. Nothing is listed", dest, admission.Dir, err)
	}
	var found leftoverProbe
	reg := registry.Registry{Deployments: map[string]registry.Entry{}}
	if *sudo {
		// The probe before the registry, since it fails on a registry
		// directory it may not read, which reading the registry would take
		// for an empty one.
		out, err := t.Run(deploymentsProbe)
		if err != nil {
			s.Fail(err)
			return fmt.Errorf("site deployments: %s: listing %s failed, so nothing is listed: %w: %s", dest, deployment.Base, err, strings.TrimSpace(out))
		}
		if found, err = parseDeploymentsProbe(out); err != nil {
			s.Fail(err)
			return fmt.Errorf("site deployments: %s: listing %s: %w. Nothing is listed", dest, deployment.Base, err)
		}
		if reg, err = registry.Read(t); err != nil {
			s.Fail(err)
			return fmt.Errorf("site deployments: %w. Nothing is listed", err)
		}
	}
	s.Done("")

	for _, a := range records {
		if a.err != nil {
			r.Warn(fmt.Sprintf("%s: %v", a.path, a.err), "")
			continue
		}
		rec := a.rec
		roles := strings.Join(rec.Roles, ",")
		if roles == "" {
			roles = "none"
		}
		r.Step(fmt.Sprintf("%s: admitted: %s as %s (%s), watchdog %s, public ports %s, at %s", rec.Deployment, rec.User, rec.Site, roles, yesNo(rec.Grants.Watchdog), yesNo(rec.Grants.PublicPorts), rec.AdmittedAt)).End(ui.OK, "")
	}
	if len(records) == 0 {
		r.Step("no admission record in "+admission.Dir).End(ui.OK, "")
	}
	if !*sudo {
		r.Result("%d admission record(s). The registry and the deployments' directories are root's: run without --sudo=false to list them.", len(records))
		return nil
	}

	ids := make([]string, 0, len(reg.Deployments))
	for id := range reg.Deployments {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tokens, roots := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		e := reg.Deployments[id]
		tokens[e.Token], roots[e.Root] = true, true
		r.Step(registryLine(id, e)).End(ui.OK, "")
	}
	if len(ids) == 0 {
		r.Step("none in "+registry.Path).End(ui.OK, "")
	}

	var left []string
	for _, name := range found.roots {
		dir := deployment.Base + "/" + name
		if !tokens[name] && !roots[dir] {
			left = append(left, dir+": no registry entry names this deployment directory")
		}
	}
	if len(left) > 0 {
		r.Section("not in the registry")
		sort.Strings(left)
		// Each is the operator's to remove by hand, so each is a warning.
		for _, l := range left {
			r.Warn(l, "")
		}
	}

	switch {
	case len(ids) > 0:
		r.Result("%d deployment(s) in the registry. paisans site remove --force --ssh <the deployment's user>@<host> --id <id or token> cleans one off this host.", len(ids))
	case len(left) > 0:
		r.Result("No deployment in the registry; what is listed above is removed by hand.")
	default:
		r.Result("No deployment in the registry, and nothing else of paisans on %s.", dest)
	}
	return nil
}

// registryLine is one registry entry as the listing says it.
func registryLine(id string, e registry.Entry) string {
	roles := e.Roles
	if roles == "" {
		roles = "none"
	}
	return fmt.Sprintf("%s: token %s, %s, site %s, roles %s, root %s", id, e.Token, e.Domain, e.Site, roles, e.Root)
}
