package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// removeSiteHost is how `site remove` reaches a site. Tests replace it.
var removeSiteHost = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// dnsProviderFor makes the provider site remove's DNS stage deletes
// through, the one dns init and dns prune use. Tests replace it with a fake:
// none may reach a real provider.
var dnsProviderFor = dns.For

// runSiteRemove takes one site out of the running deployment, stage by stage:
// its data moved off, its cluster memberships removed, its host cleaned and
// its entry taken out of paisans-stack.yaml. See internal/siteremove and README's
// *`site remove` takes a site out*.
//
// Like every command that changes a host, it plans from what it reads and
// changes nothing without --execute. --delete-data asks for the site's name
// at a terminal, for the reason app remove does.
func runSiteRemove(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("site remove", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually run the stages, stopping at the first gate that fails")
	hostGone := fs.Bool("host-gone", false, "the site's host is not reached, dead or no longer the deployment's: cut it out of the mesh first, then take it out of the cluster without reaching it; asks for the site's name at a terminal")
	deleteData := fs.Bool("delete-data", false, "also delete this deployment's data on the host (its directory and named volumes); asks for the site's name at a terminal")
	force := fs.Bool("force", false, "clean this deployment off one host and nothing else: no cluster stage or refusal, and neither "+config.FileName+" nor the secrets file edited")
	sshFlag := fs.String("ssh", "", "with --force: the host to clean, user@host[:port] or an alias of your ssh config, any host; only what carries this deployment's id or token is removed")
	idFlag := fs.String("id", "", "with --force and --ssh, and no "+config.FileName+": the deployment to clean off the host, by its full id or its token, as "+progname.Cmd()+" site deployments lists it")
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
	// Once every flag is read, those after the site's name included, as
	// the reporter and the login recording take --execute and -v from them.
	r := reporter()
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "site remove takes one site", Explain: "Got extra argument(s): " + strings.Join(fs.Args(), " ") + ". Usage: " + progname.Cmd() + " site remove <site> [--execute] [--host-gone] [--delete-data] [--force [--ssh user@host[:port] or ssh alias]]"}
	}
	if *hostGone {
		switch {
		case *force || *idFlag != "" || *sshFlag != "":
			return &ui.Problem{Hint: "--host-gone cleans no host", Explain: "--host-gone never reaches the site's host, and --force, --ssh and --id name a host to clean. Drop them."}
		case *deleteData:
			return &ui.Problem{Hint: "--host-gone and --delete-data cannot be used together", Explain: "--delete-data deletes data on the host, and --host-gone never reaches it. Drop --delete-data."}
		}
	}
	if *idFlag != "" {
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		return runSiteRemoveByID(r, site, byIDArgs{id: *idFlag, ssh: *sshFlag, force: *force, config: set["config"], secrets: set["secrets"], execute: *execute, deleteData: *deleteData, answers: ans}, stdin, stdout)
	}
	if site == "" {
		return &ui.Problem{Hint: "site remove needs a site", Explain: "Name the site to remove, Eg: " + progname.Cmd() + " site remove home-b"}
	}
	if *sshFlag != "" && !*force {
		return &ui.Problem{Hint: "--ssh is only for --force", Explain: "--ssh names the host to clean, which only --force takes. A full removal reaches the site through its ssh section."}
	}
	if *force {
		return runSiteRemoveForced(r, site, forcedArgs{config: *configPath, secrets: *secretsPath, ssh: *sshFlag, execute: *execute, deleteData: *deleteData, answers: ans}, stdin, stdout)
	}
	if _, err := ensureDraft(*configPath, ""); err != nil {
		if errors.Is(err, errSeveralDeployments) {
			return &ui.Problem{Hint: "this workstation holds more than one deployment", Explain: "There is no draft beside " + *configPath + " to say which, and --id here names a host to clean. Run site remove with --config in the folder of a draft, started with " + progname.Cmd() + " deployment checkout --id <id> --config <folder>/" + config.FileName + " --execute."}
		}
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	// Refused before any host is read, so an unattended run with
	// --delete-data stops with nothing asked of anything.
	if *execute && *deleteData && !ans.canAnswer(stdin) {
		return noTerminal("--delete-data deletes member data, which nothing brings back")
	}
	if *execute && *hostGone && !ans.canAnswer(stdin) {
		return noTerminal("Cutting a site off is not undone by running it again")
	}
	opts := siteremove.Options{HostGone: *hostGone, DeleteData: *deleteData, ConfigPath: *configPath, DNSProvider: dnsProviderFor}
	if err := siteremove.Refusal(cfg, site, opts); err != nil {
		return err
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	// One reader for every answer a removal with --host-gone asks for, so none
	// is read ahead and lost between questions.
	term := &terminal{answers: ans, stdin: stdin, out: stdout}
	// A gateway's DNS token is revoked and replaced while `revoked` waits,
	// with `secrets set`, which writes the secrets file. So it is asked
	// before the file is read: the token checked and every secret written
	// back are the ones the operator has just set.
	asked := false
	if *execute && *hostGone && cfg.Sites[site].Has(config.RoleGateway) {
		if err := term.ask([]question{revokedQuestion(cfg, site)}); err != nil {
			return err
		}
		asked = true
	}
	loaded, err := os.ReadFile(*secretsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	if err := refuseMidRotation(secrets, "site remove"); err != nil {
		return err
	}
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	// A replacement declared in the same edit has no secrets yet. They are
	// generated here, on the workstation, by the same code init runs, so the
	// remaining sites can be rendered with it; they are written only once the
	// removal is confirmed.
	var filled secretsgen.Result
	if *hostGone {
		if filled, err = secretsgen.Fill(cfg, secrets); err != nil {
			return err
		}
	}
	if err := requireACMEToken(cfg, secrets, cfg.SiteNames()); err != nil {
		return err
	}

	transports := map[string]apply.Transport{}
	var written []string
	for _, name := range cfg.SiteNames() {
		if name == site && *hostGone {
			continue
		}
		transports[name] = removeSiteHost(name, cfg.Sites[name])
		written = append(written, name)
	}
	// A site declared for the first time is neither checked nor written to:
	// the removal leaves its host to the apply that brings it up.
	monitors := cfg.MonitorSites()
	if *hostGone {
		joining, err := siteremove.JoiningSites(cfg, secrets, site, transports)
		if err != nil {
			return err
		}
		written, monitors = withoutNames(written, joining), withoutNames(monitors, joining)
	}
	// The monitor's stack is applied last, with the site out of its seed,
	// so each monitor site is host checked as apply would check it.
	if _, err := gateSites(r, cfg, monitors, func(site string) hostcheck.Transport { return transports[site] }); err != nil {
		return err
	}
	plan, err := ui.Get(r, "read every site", func() (*siteremove.Plan, error) {
		return siteremove.Build(cfg, secrets, site, transports, opts)
	})
	if err != nil {
		return err
	}
	if !*execute || r.Verbose() {
		plan.Show(r)
	}
	// Every site it reaches is written to, so it checks the admission of
	// each: the cluster and the mesh change on each remaining one, and the
	// leaving host is cleaned.
	if _, err := admittedSites(r, cfg, written...); err != nil {
		return err
	}
	if filled.Changed() {
		verb := "will be generated by --execute"
		if *execute {
			verb = "are generated before the site is cut off"
		}
		r.Note("secrets: "+strings.Join(filled.Generated, ", ")+" "+verb, "The configuration declares them and the secrets file does not hold them yet; "+progname.Cmd()+" init generates them the same way")
	}
	if !*execute {
		reportRemains(r, plan.Remains())
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	if *deleteData {
		if err := confirmSite(ans, stdin, stdout, site); err != nil {
			return err
		}
	}
	if *hostGone {
		var questions []question
		for _, q := range cutoffQuestions(cfg, site, plan) {
			if !asked || q.word != "revoked" {
				questions = append(questions, q)
			}
		}
		if err := term.ask(questions); err != nil {
			return err
		}
		if filled.Changed() {
			if err := writeFilled(r, *secretsPath, loaded, secrets, filled); err != nil {
				return err
			}
		}
	}
	plan.Report = r
	if err := siteremove.Execute(plan); err != nil {
		return err
	}
	reportRemains(r, plan.Remains())
	r.Result("%s is removed: every gate passed, and %s no longer declares it.", site, *configPath)
	noteEvict(r, plan)
	return nil
}

type forcedArgs struct {
	config, secrets, ssh string
	execute, deleteData  bool
	answers              answerer
}

// runSiteRemoveForced is `site remove --force`: one host cleaned of this
// deployment, nothing else read or changed. See siteremove.BuildForced.
//
// The host's admission is not checked, as other commands that write to a
// host check it: cleaning it removes this deployment from the host.
func runSiteRemoveForced(r ui.Reporter, site string, a forcedArgs, stdin io.Reader, stdout io.Writer) error {
	cfg, err := config.Load(a.config)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, a.config, result)
	if result.Refused() {
		return refused(a.config, len(result.Refusals()), "")
	}
	dest, err := chooseHost(cfg, site, a.ssh)
	if err != nil {
		return err
	}
	declared, isDeclared := cfg.Sites[site]
	own := isDeclared && declared.Destination().Same(dest)
	// Refused before any host is read, so an unattended run stops with
	// nothing asked of anything.
	if a.execute && (own || a.deleteData) && !a.answers.canAnswer(stdin) {
		why := "--delete-data deletes member data, which nothing brings back"
		if own {
			why = "it cleans the host " + site + " runs on, out from under its cluster"
		}
		return noTerminal(strings.ToUpper(why[:1]) + why[1:])
	}
	if a.secrets == "" {
		a.secrets = filepath.Join(filepath.Dir(a.config), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(a.secrets, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, a.secrets)
	}
	// The declared entry's public keys, if any, still say which key the
	// operator logs in with; only where is dest. An alias stays the host ssh
	// is given, so its ProxyJump and the rest of its ssh config still apply.
	reach := declared
	reach.SSH.User, reach.SSH.Host, reach.SSH.Port = dest.User, dest.SSHHost(), dest.Port
	t := removeSiteHost(site, reach)
	if st, ok := t.(apply.SSHTransport); ok {
		st.KnownHost = dest.KnownHostsName()
		t = st
	}
	opts := siteremove.Options{DeleteData: a.deleteData, ConfigPath: a.config, DNSProvider: dnsProviderFor}
	plan, err := ui.Get(r, "read "+dest.String(), func() (*siteremove.Plan, error) {
		return siteremove.BuildForced(cfg, secrets, site, dest, t, opts)
	})
	if err != nil {
		return err
	}
	// A site no longer declared leaves every gateway's deployment record,
	// whatever its host held, so secrets prune may remove its secrets.
	forget := func(execute bool) bool {
		return !isDeclared && forgetInRecords(r, cfg, deployrecord.Record{Sites: []string{site}}, site, execute)
	}
	recordLeft := false
	if !plan.Pending() {
		recordLeft = forget(a.execute)
	}
	if forcedNothingToDo(r, plan, dest, recordLeft) {
		return nil
	}
	if !a.execute || r.Verbose() {
		plan.Show(r)
	}
	if !a.execute {
		forget(false)
		reportRemains(r, plan.Remains())
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	var what []string
	if plan.Current {
		what = append(what, fmt.Sprintf("This cleans %s, the host %s runs on, out from under its cluster.", dest, site))
	}
	if a.deleteData {
		what = append(what, deletesData(stdout, dest.String()))
	}
	if len(what) > 0 {
		if err := confirmSiteFor(a.answers, stdin, stdout, site, strings.Join(what, "\n")); err != nil {
			return err
		}
	}
	plan.Report = r
	if err := siteremove.Execute(plan); err != nil {
		return err
	}
	forget(true)
	reportRemains(r, plan.Remains())
	r.Result("%s is cleaned of this deployment. %s and the secrets file are unchanged.", dest, a.config)
	noteEvict(r, plan)
	return nil
}

type byIDArgs struct {
	id, ssh                                     string
	force, config, secrets, execute, deleteData bool
	answers                                     answerer
}

// tokenShape is a token as --id takes it: deployment.TokenLength lowercase
// hex digits.
var tokenShape = regexp.MustCompile(fmt.Sprintf("^[0-9a-f]{%d}$", deployment.TokenLength))

// runSiteRemoveByID is `site remove --force --ssh <dest> --id <id or token>`:
// one host cleaned of the deployment the login's admission record names, with no
// paisans-stack.yaml and no secrets file. See siteremove.BuildForcedByID and
// docs/specs/2026-10-10-remove-without-config.md.
//
// Nothing says whether the deployment still runs elsewhere, or whether
// someone still holds its paisans-stack.yaml, so --execute always asks for the
// site's name at a terminal.
func runSiteRemoveByID(r ui.Reporter, site string, a byIDArgs, stdin io.Reader, stdout io.Writer) error {
	switch {
	case !a.force:
		return &ui.Problem{Hint: "--id needs --force", Explain: "--id names a deployment for --force to clean off one host, so add --force."}
	case site != "":
		return &ui.Problem{Hint: "--id takes no site", Explain: "The login's admission record on the host names the site, so drop " + site + "."}
	case a.config:
		return &ui.Problem{Hint: "--id takes no --config", Explain: "--id reads no " + config.FileName + ", and a configuration names its own id. Drop --config, or --id."}
	case a.secrets:
		return &ui.Problem{Hint: "--id takes no --secrets", Explain: "--id reads no secrets file. Drop --secrets."}
	case a.ssh == "":
		return &ui.Problem{Hint: "--id needs --ssh", Explain: "With no configuration there is no ssh section to reach the host through, so name it with --ssh user@host[:port] or an alias of your ssh config."}
	case !deployment.ValidID(a.id) && !tokenShape.MatchString(a.id):
		return &ui.Problem{Hint: fmt.Sprintf("--id %q is not a deployment id or token", a.id), Explain: fmt.Sprintf("Give the full id or its %d hex digit token, as %s site deployments lists them, Eg: --id f2a9", deployment.TokenLength, progname.Cmd())}
	}
	dest, err := parseDestination(a.ssh)
	if err != nil {
		return sshFlagProblem(a.ssh, err)
	}
	// Refused before any host is read, so an unattended run stops with
	// nothing asked of anything.
	if a.execute && !a.answers.canAnswer(stdin) {
		return noTerminal("Nothing says whether this deployment still runs")
	}
	t := reachDestination(dest)
	plan, err := ui.Get(r, "read "+dest.String(), func() (*siteremove.Plan, error) {
		return siteremove.BuildForcedByID(dest, t, a.id, siteremove.Options{DeleteData: a.deleteData})
	})
	if err != nil {
		return err
	}
	if !plan.Pending() {
		reportRemains(r, plan.DNSWarning())
	}
	if forcedNothingToDo(r, plan, dest, false) {
		return nil
	}
	if !a.execute || r.Verbose() {
		plan.Show(r)
	}
	if !a.execute {
		reportRemains(r, plan.Remains())
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	what, deletes := plan.Confirmation(dest)
	if deletes != "" {
		what += "\n" + ui.Danger(stdout, deletes)
	}
	if err := confirmSiteFor(a.answers, stdin, stdout, plan.Site, what); err != nil {
		return err
	}
	plan.Report = r
	if err := siteremove.Execute(plan); err != nil {
		return err
	}
	reportRemains(r, plan.Remains())
	r.Result("%s is cleaned of deployment %s.", dest, plan.DeploymentID())
	noteEvict(r, plan)
	return nil
}

// noteEvict ends a run that reached or skipped a host with the line its
// owner runs to remove what is root's, which site remove cannot.
func noteEvict(r ui.Reporter, plan *siteremove.Plan) {
	if plan.Evict == "" {
		return
	}
	r.Note("the host's owner finishes on "+plan.Site, "Send them this line, which removes the deployment's user and everything root's that admit made for it:\n\n    "+plan.Evict)
}

// forcedNothingToDo ends a forced run on a host that holds nothing of this
// deployment, dry run or not: there is nothing to run again, and what the
// host's owner keeps there is not this command's to list.
func forcedNothingToDo(r ui.Reporter, plan *siteremove.Plan, dest config.Destination, recordLeft bool) bool {
	if plan.Pending() {
		return false
	}
	if len(plan.CaddyKept) > 0 {
		r.Result("%s holds nothing of this deployment but its Caddy, kept because it serves %s. Run this again once they have moved, and it goes.", dest, strings.Join(plan.CaddyKept, ", "))
		noteEvict(r, plan)
		return true
	}
	if plan.DataKept {
		reportRemains(r, plan.Remains())
		r.Result("%s holds nothing of this deployment but its data. Run this again with --delete-data, and it goes.", dest)
		noteEvict(r, plan)
		return true
	}
	if recordLeft {
		r.Result("%s holds nothing of this deployment. Re-run with --execute to take it out of the deployment record.", dest)
		noteEvict(r, plan)
		return true
	}
	// The deployment's user, its root and its admission record may still
	// be there from an earlier run, whose evict line this repeats.
	r.Result("%s holds nothing of this deployment. Nothing to do.", dest)
	noteEvict(r, plan)
	return true
}

// chooseHost is the host --force cleans: --ssh when given, any host, else the
// declared site's own. Any host is safe to name, since only what carries this
// deployment's id or token is removed.
func chooseHost(cfg *config.Config, site, ssh string) (config.Destination, error) {
	if ssh != "" {
		d, err := parseDestination(ssh)
		if err != nil {
			return config.Destination{}, sshFlagProblem(ssh, err)
		}
		return d, nil
	}
	if s, ok := cfg.Sites[site]; ok {
		return s.Destination(), nil
	}
	return config.Destination{}, &ui.Problem{Hint: site + " is not declared", Explain: "Name its host with --ssh user@host[:port] or an alias of your ssh config."}
}

// noTerminal refuses a removal that must ask for the site's name, for why,
// when stdin is not a terminal to ask at.
func noTerminal(why string) error {
	return &ui.Problem{
		Hint:    "the site's name must be typed, and stdin is not a terminal",
		Explain: why + ", so it asks for the site's name at a terminal. Run it from an interactive shell, or answer it with --yes and PAISANS_ALLOW_YES=1. Nothing was changed.",
	}
}

// sshFlagProblem is the Problem for an --ssh value that is not a
// destination.
func sshFlagProblem(value string, err error) error {
	return &ui.Problem{Hint: "--ssh is not a destination", Explain: err.Error() + ".", Cause: err}
}

// confirmSite asks for the site's name before --delete-data deletes its data.
func confirmSite(a answerer, stdin io.Reader, stdout io.Writer, site string) error {
	return confirmSiteFor(a, stdin, stdout, site, deletesData(stdout, site))
}

// deletesData is the sentence --delete-data's question sets off: what it
// deletes, on where, and that nothing brings it back.
func deletesData(stdout io.Writer, where string) string {
	return ui.Danger(stdout, fmt.Sprintf("This permanently deletes this deployment's data on %s.", where))
}

// confirmSiteFor says what is about to happen, asks for the site's name at
// the terminal, and refuses anything else. --yes, through a, answers it.
func confirmSiteFor(a answerer, stdin io.Reader, stdout io.Writer, site, what string) error {
	if err := confirmWord(a, stdin, stdout, site, what); err != nil {
		return fmt.Errorf("site remove: %w", err)
	}
	return nil
}

// withoutNames is list without any of names.
func withoutNames(list, names []string) []string {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	var out []string
	for _, n := range list {
		if !drop[n] {
			out = append(out, n)
		}
	}
	return out
}

// question is one word an operator types at the terminal, and what it
// agrees to. A question whose answer loses data is a danger, and says so in
// bold red.
type question struct {
	word, what string
	danger     bool
}

// cutoffQuestions are the words a removal with --host-gone asks for, in order:
// revoked, for a gateway, whose DNS token can change the community's records
// from anywhere; the site's name, since cutting a site off is not undone by
// running it again; lose writes, for a leader with no streaming Sync Standby;
// and lose objects, for a Garage node whose objects nothing else holds.
func cutoffQuestions(cfg *config.Config, site string, plan *siteremove.Plan) []question {
	var out []question
	if plan.Gateway() {
		out = append(out, revokedQuestion(cfg, site))
	}
	out = append(out, question{word: site, what: fmt.Sprintf("This cuts %s out of the mesh and takes it out of the cluster without reaching its host. Cutting a site off is not undone by running it again.", site)})
	if text := plan.LoseWritesText(); text != "" {
		out = append(out, question{"lose writes", text, true})
	}
	if plan.LoseObjects() {
		out = append(out, question{"lose objects", fmt.Sprintf("Skipping %s's Garage node permanently loses the objects only it held: storage.garage.replication is %d, and the nodes that remain hold no other copy of them.", site, cfg.Storage.Garage.Replication), true})
	}
	return out
}

// revokedQuestion is `revoked`, asked when the cut-off site holds the
// gateway.
func revokedQuestion(cfg *config.Config, site string) question {
	provider := cfg.ACME.Provider
	if provider == "" {
		provider = "the DNS provider"
	}
	return question{word: "revoked", what: fmt.Sprintf("%s holds the gateway, and the DNS token it was rendered can change the community's records from anywhere. Revoke that token at %s, and set a new one with `%s secrets set external.acme_dns_token` before you proceed. This command cannot revoke it: it holds the token, not a credential that manages tokens.", site, provider, progname.Cmd())}
}

// terminal is where the questions of a removal with --host-gone are asked: one
// reader for all of them, however many times it asks, or --yes, which answers
// each.
type terminal struct {
	answers answerer
	stdin   io.Reader
	in      *bufio.Reader
	out     io.Writer
}

// confirmCutoff asks each question in turn, from one reader so that no
// answer is read ahead and lost, and refuses at the first wrong answer.
// --yes, through a, answers each with its word.
func confirmCutoff(a answerer, stdin io.Reader, stdout io.Writer, questions []question) error {
	return (&terminal{answers: a, stdin: stdin, out: stdout}).ask(questions)
}

func (t *terminal) ask(questions []question) error {
	if t.answers.yes {
		for _, q := range questions {
			what := q.what
			if q.danger {
				what = ui.Danger(t.out, what)
			}
			if err := confirmWord(t.answers, nil, t.out, q.word, what); err != nil {
				return err
			}
		}
		return nil
	}
	if !stdinIsTerminal(t.stdin) {
		return noTerminal("Cutting a site off is not undone by running it again")
	}
	if t.in == nil {
		t.in = bufio.NewReader(t.stdin)
	}
	in, stdout := t.in, t.out
	for _, q := range questions {
		what := q.what
		if q.danger {
			what = ui.Danger(stdout, what)
		}
		prompt(stdout, what, q.word)
		answer, err := in.ReadString('\n')
		if err != nil && answer == "" {
			return fmt.Errorf("site remove: no answer read. Nothing was changed")
		}
		if strings.TrimSpace(answer) != q.word {
			return fmt.Errorf("site remove: %q is not %s. Nothing was changed", strings.TrimSpace(answer), q.word)
		}
	}
	return nil
}

// writeFilled writes the secrets init's generation filled in, encrypted to
// the recipients beside the file, before the removal renders anything from
// them. The whole file is written back from what was read, so a file that
// changed since, loaded as its bytes then, is refused rather than
// overwritten: the change would be lost, and the plan was rendered without
// it.
func writeFilled(r ui.Reporter, path string, loaded []byte, secrets *config.Secrets, filled secretsgen.Result) error {
	now, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(now, loaded) {
		return fmt.Errorf("site remove: %s changed after it was read, so writing the generated secrets back would lose that change, and the plan was made without it. Nothing was written or changed: run site remove --host-gone again, which reads it afresh", path)
	}
	recipients, err := config.Recipients(filepath.Dir(path))
	if err != nil {
		return err
	}
	if secrets.Encrypted && len(recipients) == 0 {
		return config.NoRecipientProblem(path)
	}
	s := r.Step("generate secrets")
	if err := config.WriteSecrets(path, secrets, recipients); err != nil {
		s.Fail(err)
		return err
	}
	s.Done(strings.Join(filled.Generated, ", "))
	if len(recipients) == 0 {
		warnUnencrypted(r, path)
	}
	return nil
}
