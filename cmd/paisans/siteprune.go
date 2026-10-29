package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runSitePrune lists what one site holds that this deployment no longer
// runs there and, with --execute, removes it: the containers of each left
// over service, then the dangling volumes a paisans container left behind.
// It is modelled on runSitePrepare: one site, a dry run by default, and the
// plan reported before anything happens.
//
// deployment apply never removes what it stops rendering, since that is a
// decision with member data in it; this is the command that makes it, on
// purpose. `docker volume prune` was rejected for the volumes because it
// removes every unused volume whoever made it, with no list first, and on
// Docker 23 and later only anonymous ones unless given --all, which is two
// different surprises depending on the engine (Docker Engine 23.0 release
// notes).
func runSitePrune(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("site prune", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "actually remove the containers and volumes marked remove")
	breakLease := breakLeaseFlag(fs)
	answers := yesFlag(fs)
	var site string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		site, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if site == "" && fs.NArg() > 0 {
		site = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "site prune takes one site", Explain: "Got extra argument(s): " + strings.Join(fs.Args(), " ") + ". Usage: " + progname.Cmd() + " site prune <site> [--ssh <destination>] [--execute]"}
	}
	if site == "" {
		return &ui.Problem{Hint: "site prune needs a site", Explain: "Name the site to prune, Eg: " + progname.Cmd() + " site prune home-b. " + progname.Cmd() + " deployment prune prunes every site."}
	}
	cfg, err := loadForPrune(r, *configPath)
	if err != nil {
		return err
	}
	declared, ok := cfg.Sites[site]
	if !ok {
		return fmt.Errorf("site prune: %s declares no site %q. Declared sites are %s", *configPath, site, strings.Join(cfg.SiteNames(), ", "))
	}
	transports := map[string]apply.SSHTransport{site: siteTransport(site, declared, *destination)}
	return prune(r, cfg, []string{site}, transports, "site prune "+site, *execute, leaseOptions{breakLease: *breakLease, answers: ans, stdin: stdin, stdout: stdout})
}

// runDeploymentPrune is site prune on every site the configuration declares.
// Every site is checked and leased before any is changed, so a host that
// refuses stops the run with nothing removed anywhere.
func runDeploymentPrune(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("deployment prune", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	execute := fs.Bool("execute", false, "actually remove the containers and volumes marked remove")
	breakLease := breakLeaseFlag(fs)
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment prune takes no site", Explain: "Got extra argument(s): " + strings.Join(fs.Args(), " ") + ". It prunes every site; " + progname.Cmd() + " site prune <site> prunes one."}
	}
	cfg, err := loadForPrune(r, *configPath)
	if err != nil {
		return err
	}
	sites := cfg.SiteNames()
	transports := map[string]apply.SSHTransport{}
	for _, name := range sites {
		transports[name] = siteTransport(name, cfg.Sites[name], "")
	}
	return prune(r, cfg, sites, transports, "deployment prune", *execute, leaseOptions{breakLease: *breakLease, answers: ans, stdin: stdin, stdout: stdout})
}

// loadForPrune loads the configuration and refuses one validation refuses.
func loadForPrune(r ui.Reporter, path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	result := validate.Check(cfg)
	reportFindings(r, path, result)
	if result.Refused() {
		return nil, refused(path, len(result.Refusals()), "")
	}
	return cfg, nil
}

// prune runs the host check on every site, then leases them all, then plans
// and with execute removes each site's leftovers in turn. A site that fails
// does not stop the rest, since each site's removals stand alone; the run
// fails naming each one.
func prune(r ui.Reporter, cfg *config.Config, sites []string, transports map[string]apply.SSHTransport, command string, execute bool, lease leaseOptions) error {
	inventories := map[string]*hostcheck.Inventory{}
	runners := map[string]registry.Runner{}
	for _, site := range sites {
		t := transports[site]
		r.Section(fmt.Sprintf("%s (%s)", site, t.Describe()))
		report, err := hostGate(r, cfg, site, t)
		if err != nil {
			return err
		}
		inventories[site] = report.Inventory
		runners[site] = t
	}
	if _, err := admittedHosts(r, cfg, runners); err != nil {
		return err
	}
	// Which volumes are dangling is this release's reading of their
	// labels, so a newer one's volumes are not judged by it.
	release, err := guardSites(r, cfg, sites, func(name string) apply.Transport { return transports[name] }, command, guard{execute: execute, lease: lease})
	if err != nil {
		return err
	}
	defer release()

	var failed []string
	containers, volumes := 0, 0
	for _, site := range sites {
		if len(sites) > 1 {
			r.Section(site)
		}
		c, v, err := pruneSite(r, cfg, site, transports[site], inventories[site], execute)
		containers += c
		volumes += v
		if err != nil {
			if len(sites) == 1 {
				return err
			}
			r.Warn("prune "+site+" failed", err.Error())
			failed = append(failed, site)
		}
	}
	switch {
	case len(failed) > 0:
		return fmt.Errorf("%s: %s failed; every other site was pruned", command, strings.Join(failed, ", "))
	case containers+volumes == 0:
		r.Result("Nothing to remove.")
	case !execute:
		r.Result("Nothing changed. Re-run with --execute to apply.")
	default:
		r.Result("Removed %s and %s.", plural(containers, "container"), plural(volumes, "volume"))
	}
	return nil
}

// pruneSite plans one site's prune and, with execute, carries it out: the
// left over services' containers first, then the volumes, read after them so
// that what removing the containers leaves dangling is judged too. It
// returns how many containers and volumes it removed, or with execute false
// would remove.
func pruneSite(r ui.Reporter, cfg *config.Config, site string, t apply.SSHTransport, inv *hostcheck.Inventory, execute bool) (containers, volumes int, err error) {
	d := cfg.Deployment()
	services, err := ui.Get(r, "read left over services", func() ([]ownership.Service, error) {
		composes := map[string]string{}
		for _, stack := range ownership.Stacks(d, inv) {
			content, found, err := t.ReadFile(d.Compose(stack))
			if err != nil {
				return nil, err
			}
			if found {
				composes[stack] = content
			}
		}
		return ownership.PruneServices(d, inv, composes)
	})
	if err != nil {
		return 0, 0, err
	}
	var names []string
	for _, s := range services {
		if s.Kept == "" {
			names = append(names, s.Containers...)
		}
	}
	if !execute || r.Verbose() {
		showServicePrune(r, services)
	}
	if execute && len(names) > 0 {
		s := r.Step("remove left over containers")
		s.Detail("from %s", t.Describe())
		if err := apply.RemoveContainers(site, names, t); err != nil {
			s.Fail(err)
			return 0, 0, err
		}
		s.Done(plural(len(names), "container"))
	}

	plan, err := ui.Get(r, "read volumes", func() (*apply.VolumePrune, error) {
		return apply.BuildVolumePrune(d, site, t)
	})
	if err != nil {
		return len(names), 0, err
	}
	if !execute || r.Verbose() {
		showVolumePrune(r, plan)
	}
	removals := plan.Removals()
	if execute && len(removals) > 0 {
		s := r.Step("remove volumes")
		s.Detail("from %s", plan.Transport)
		if err := apply.ExecuteVolumePrune(plan, t); err != nil {
			s.Fail(err)
			return len(names), 0, err
		}
		s.Done(plural(len(removals), "volume"))
	}
	return len(names), len(removals), nil
}

// showServicePrune reports each left over service as site prepare marks its
// plan: one it would remove as pending, one it keeps as a warning with why.
func showServicePrune(r ui.Reporter, services []ownership.Service) {
	if len(services) == 0 {
		r.Detail("no left over services")
		return
	}
	lines := ownership.Report{Services: services}.LeftoverLines()
	for i, s := range services {
		if s.Kept != "" {
			r.Warn("keep service "+s.Name+" of stack "+s.Stack, lines[i]+". It is "+s.Kept+".")
			continue
		}
		step := r.Step("remove service " + s.Name + " of stack " + s.Stack)
		step.Detail("%s", lines[i])
		step.End(ui.Pending, "")
	}
}

// showVolumePrune reports the plan, marked the way site prepare marks its
// plan: each volume it would remove as pending under its title, and under
// --verbose each one kept as done. Every verdict carries the volume's whole
// name, size, contents and reason as details, since the reason is what an
// operator reads before agreeing.
func showVolumePrune(r ui.Reporter, plan *apply.VolumePrune) {
	r.Detail("%s", apply.PruneHeader)
	if len(plan.Volumes) == 0 {
		r.Detail("no dangling volumes")
		return
	}
	var total int64
	unknown := 0
	for _, v := range plan.Volumes {
		if v.Remove {
			if v.Size >= 0 {
				total += v.Size
			} else {
				unknown++
			}
			showVolume(r, "remove", v, ui.Pending)
		}
	}
	if r.Verbose() {
		for _, v := range plan.Volumes {
			if !v.Remove {
				showVolume(r, "keep", v, ui.OK)
			}
		}
	}
	size := apply.FormatSize(total)
	if unknown > 0 {
		size += fmt.Sprintf(" (plus %d of unknown size)", unknown)
	}
	r.Detail("total: %d of %d dangling volume(s), %s", len(plan.Removals()), len(plan.Volumes), size)
}

// showVolume is one verdict: verb and the volume's name as the title, its
// whole name, size and contents, then its reason, as details.
func showVolume(r ui.Reporter, verb string, v apply.DanglingVolume, m ui.Mark) {
	// Only an anonymous volume's name is shortened, since it is 64 hex
	// characters whose front tells one from another. A named volume's name
	// is what an operator recognises, and this list is the consent to delete
	// its data, so it is shown whole.
	name := v.Name
	if apply.IsAnonymousVolume(name) {
		name = name[:12]
	}
	entries := "empty"
	if len(v.Entries) > 0 {
		entries = strings.Join(v.Entries, " ")
	}
	s := r.Step(verb + " volume " + name)
	s.Detail("%s (%s): %s", v.Name, apply.FormatSize(v.Size), entries)
	if v.Reason != "" {
		s.Detail("%s", v.Reason)
	}
	s.End(m, "")
}
