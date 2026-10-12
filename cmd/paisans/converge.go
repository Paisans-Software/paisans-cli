package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/gatewaymove"
	"github.com/paisans-software/paisans-stack/internal/pocketid"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/storageadd"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
	"gopkg.in/yaml.v3"
)

// convergeState is what decides the plan of `paisans apply` without --site:
// whether init has work, and each etcd member's infra/etcd-initial record,
// for the members that hold one. See docs/specs/2026-10-09-apply-converge.md.
type convergeState struct {
	NeedsInit bool
	// InitWhy is what init has to do, by name, for the plan to say.
	InitWhy string
	// SecretsOnly is init's work being only generated secrets missing from
	// a secrets file that reads, which a dry run generates itself.
	SecretsOnly bool
	Initial     map[string]render.EtcdInitial
	// MoveGateway is a site without the gateway role still holding this
	// deployment's gateway Caddy or record: a gateway move to run once
	// every site is applied.
	MoveGateway bool
}

// clusterNames is the member names an --initial-cluster lists.
func clusterNames(in render.EtcdInitial) []string {
	var out []string
	for _, pair := range strings.Split(in.Cluster, ",") {
		if name, _, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}

// stage is where in the order a step falls. The operator sees four of
// them: prepare, pull, dns, and install, which is every stage from founding
// to pass two as a line per site and one for storage.
type stage int

const (
	stageInit stage = iota
	stagePrepare
	// stagePull is each site's images, pulled on every host at once ahead
	// of the install. No command runs it alone; see converge_pull.go.
	stagePull
	stageDNS
	// stageFounding is an etcd member's first apply. Its founding stop is
	// expected: pass two applies it again.
	stageFounding
	stageJoining
	stageOthers
	stageStorage
	// stagePassTwo is every site's apply once more, after the rest.
	stagePassTwo
	// stageGateway is a gateway move, once the new gateway is applied.
	stageGateway
)

// installs reports whether a step of st is one of the install lines'.
func (st stage) installs() bool { return st >= stageFounding && st <= stagePassTwo }

// convergeStep is one existing command the plan runs, or one site's pull,
// which the run does itself.
type convergeStep struct {
	Stage stage
	// Title is the command as typed less its common flags: what a stop
	// names and what -v lists.
	Title string
	// Args is the command and its own flags; the run adds the common ones,
	// by convergeFlags.
	Args []string
}

func step(st stage, args ...string) convergeStep {
	return convergeStep{Stage: st, Title: strings.Join(args, " "), Args: args}
}

func applyStep(st stage, site string) convergeStep {
	return step(st, "apply", "--site", site)
}

// initWhy is what init has to do, as its line says it.
func initWhy(st convergeState) string {
	if st.InitWhy == "" {
		return "the deployment id, the mesh subnet or a generated secret is missing"
	}
	return st.InitWhy
}

// convergePlan is every step that takes cfg to a complete, running stack from
// what st read, in the order the deployment needs them.
func convergePlan(cfg *config.Config, st convergeState) []convergeStep {
	var steps []convergeStep
	if st.NeedsInit {
		steps = append(steps, step(stageInit, "init"))
	}
	// Docker, WireGuard and the firewall on every site; a prepared host
	// plans nothing.
	for _, s := range cfg.SiteNames() {
		steps = append(steps, step(stagePrepare, "host", "prepare", "--site", s))
	}
	// Every site's images, on every host at once: an install applies one
	// site at a time, and each apply would otherwise wait on its own host's
	// downloads while the rest sat idle. A pull needs Docker, which prepare
	// installs, and nothing else, so it follows prepare directly. Ahead of
	// dns, a host short of space for its images stops the run before the
	// provider is asked to publish names for a deployment that cannot start.
	for _, s := range cfg.SiteNames() {
		steps = append(steps, step(stagePull, "pull", "--site", s))
	}
	// dns creates the records that are missing; one pointing elsewhere
	// stops it. It comes before install: each site's wg-quick resolves the
	// endpoint names it dials as it starts (a site's endpoint written as a
	// name with no public_address beside it), and one that does not resolve
	// fails the mesh interface, so the names must exist before any site is
	// applied. It needs only the provider's API and the configuration, and
	// nothing waits for the stack: Caddy's certificates use DNS challenges.
	steps = append(steps, step(stageDNS, "dns", "init"))

	// A member with no record is still being founded when no member has a
	// record, or when a record's --initial-cluster lists it: the founding
	// set is fixed by the first apply that wrote one, and its members are
	// applied, witness first. One no record lists joins the running cluster
	// through site add.
	members := cfg.Etcd.Members
	listed := map[string]bool{}
	for _, in := range st.Initial {
		for _, n := range clusterNames(in) {
			listed[n] = true
		}
	}
	founding := func(m string) bool {
		_, recorded := st.Initial[m]
		return !recorded && (len(st.Initial) == 0 || listed[m])
	}
	// Every witness is applied before a data member is founded, since that
	// member's gate refuses it while a witness's etcd does not run: one
	// still being founded is founded there, and one with its record is
	// applied, which plans nothing when its etcd already runs. A data
	// member's apply may stop until the other members run, and pass two
	// resumes it.
	witness := func(m string) bool { return cfg.Sites[m].Has(config.RoleWitness) }
	dataFounding := slices.ContainsFunc(members, func(m string) bool { return founding(m) && !witness(m) })
	for _, m := range members {
		switch {
		case !witness(m):
		case founding(m):
			steps = append(steps, applyStep(stageFounding, m))
		case dataFounding:
			steps = append(steps, applyStep(stageOthers, m))
		}
	}
	for _, m := range members {
		if founding(m) && !witness(m) {
			steps = append(steps, applyStep(stageFounding, m))
		}
	}
	// The cluster runs and these members have not joined it. A data member
	// joins through its site add. A witness has no site add of its own: it
	// has no replica, and site add's etcd stage brings every configured
	// member that is not yet a voter in, witnesses first and one learner at
	// a time, each added before its etcd starts. So a witness joining beside
	// a data site is joined by that site's site add, after its mesh gate has
	// passed with the witness in it. A witness joining with no data site
	// would make the voters even, which validate refuses; its site add is
	// planned so that the run stops there, saying what to do.
	var joining, witnesses []string
	for _, m := range members {
		if _, recorded := st.Initial[m]; recorded || founding(m) {
			continue
		}
		if witness(m) {
			witnesses = append(witnesses, m)
		} else {
			joining = append(joining, m)
		}
	}
	if len(joining) == 0 {
		joining = witnesses
	}
	for _, m := range joining {
		steps = append(steps, step(stageJoining, "site", "add", m))
	}

	// A monitor checks every other site and app, so it is applied after
	// them, among the sites outside etcd and in pass two. One in
	// etcd.members is founded with the other members, since etcd needs it,
	// and is still last in pass two.
	monitors := cfg.MonitorSites()
	ordered := func(include func(string) bool) []string {
		var first, last []string
		for _, s := range cfg.SiteNames() {
			switch {
			case !include(s):
			case slices.Contains(monitors, s):
				last = append(last, s)
			default:
				first = append(first, s)
			}
		}
		return append(first, last...)
	}
	for _, s := range ordered(func(s string) bool { return !slices.Contains(members, s) }) {
		steps = append(steps, applyStep(stageOthers, s))
	}

	// Garage's layout, across its sites when there are several, and each
	// app's bucket and key.
	switch garage := cfg.Storage.Garage.Sites; len(garage) {
	case 0:
	case 1:
		steps = append(steps, step(stageStorage, "storage", "init", "--site", garage[0]))
	default:
		steps = append(steps, step(stageStorage, "storage", "add"))
	}

	// Pass two resumes a founding stop and moves what earlier steps
	// changed; an applied site plans nothing.
	for _, s := range ordered(func(string) bool { return true }) {
		steps = append(steps, applyStep(stagePassTwo, s))
	}

	// A gateway the configuration moved away from still serves until the
	// move has checked the new one, moved DNS and waited out the old
	// records: last, since the new gateway has to be applied and answering
	// before any of that.
	if st.MoveGateway {
		steps = append(steps, step(stageGateway, "gateway", "move"))
	}
	return steps
}

// convergeRun runs one step: the existing command, in this process, so it
// plans, gates, claims and reports as typed by hand, and the sudo password
// each host asked for is asked once for the whole run. Tests replace it.
var convergeRun func(args []string) error

// The step runner and the check are set here rather than where they are
// declared: each reaches runApply, which reaches it back.
func init() {
	convergeRun = runStep
	convergeCheck = func(r ui.Reporter, args []string) (string, []checkReport, error) {
		if isPrepare(args) {
			return checkPrepare(r, args)
		}
		return checkStep(r, args, runStep)
	}
}

// isPrepare reports whether args are host prepare's.
func isPrepare(args []string) bool {
	return len(args) > 1 && args[0] == "host" && args[1] == "prepare"
}

// convergePrepare runs args, a host prepare step's command, through env:
// apply without --site runs every host's at once, each on its own env.
// Tests replace it.
var convergePrepare = func(env prepareEnv, args []string) error {
	return runHostPrepareIn(env, args[2:])
}

// defaultPrepareAtOnce is how many hosts are prepared at the same time.
const defaultPrepareAtOnce = 8

// prepareAtOnce is how many hosts are prepared at the same time. Tests set
// it to one, to run them in order.
var prepareAtOnce = defaultPrepareAtOnce

// atOnce runs work for every index below n, up to prepareAtOnce at a time,
// and returns once each has. Each index is a site, and a site is a machine of
// its own (validate refuses two sites at one ssh destination), so no two of
// them wait on one host's package manager. With prepareAtOnce one, every
// index runs in order.
func atOnce(n int, work func(i int)) {
	slots := make(chan struct{}, max(prepareAtOnce, 1))
	var wg sync.WaitGroup
	for i := range n {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			work(i)
		}()
	}
	wg.Wait()
}

// prepareRun runs one host's host prepare, args, through convergePrepare,
// quietly, as convergeQuiet runs a step: its warnings, refusals and ssh
// retries come back as reports and its notes for after the line, and a hold
// for a prompt reaches r, which draws the line. With verbose its own output
// comes back too. found is whether it told a dry run's summary, and stop is
// what it found that --execute would stop at. While a step of its own is
// open, line shows it (ui.Nest); line is nil for none.
//
// It touches none of the process's globals, so several run at once: the
// holds already reach r, which the caller routed them to before starting
// them, and nothing here routes them anywhere else.
func prepareRun(r ui.Reporter, line ui.Step, args []string, verbose bool) (output, summary string, found bool, stop error, reports []checkReport, err error) {
	var b strings.Builder
	inner := ui.Discard
	if verbose {
		inner = ui.NewPlain(&b, true)
	}
	nested, done := ui.Nest(line, inner)
	defer done()
	env := prepareEnv{
		r:       checkReporter{nested, r, &reports},
		found:   func(s string, e error) { found, summary, stop = true, s, e },
		retries: retryCollector{&reports},
	}
	err = convergePrepare(env, args)
	return b.String(), summary, found, stop, reports, err
}

// checkPrepare is a host prepare step's check, as checkStep is any other's.
func checkPrepare(r ui.Reporter, args []string) (string, []checkReport, error) {
	_, summary, found, stop, reports, err := prepareRun(r, nil, args, false)
	switch {
	case err != nil:
		return "", reports, err
	case stop != nil:
		return "", reports, stop
	case !found:
		return "", reports, fmt.Errorf("host prepare ended without saying what it would change")
	}
	return summary, reports, nil
}

func runStep(args []string) error {
	switch {
	case args[0] == "init":
		return runInit(args[1:])
	case args[0] == "host" && args[1] == "prepare":
		return runHostPrepare(args[2:])
	case args[0] == "apply":
		return runApply(args[1:], os.Stdin, os.Stdout)
	case args[0] == "site" && args[1] == "add":
		return runSiteAdd(args[2:], os.Stdin, os.Stdout)
	case args[0] == "storage" && args[1] == "init":
		return runStorageInit(args[2:])
	case args[0] == "storage" && args[1] == "add":
		return runStorageAdd(args[2:])
	case args[0] == "dns" && args[1] == "init":
		return runDNSInit(args[2:])
	case args[0] == "gateway" && args[1] == "move":
		return runGatewayMove(args[2:])
	}
	return fmt.Errorf("apply: no command %q", strings.Join(args, " "))
}

// convergeReadInitial reads one etcd member's infra/etcd-initial record
// through its ssh section. Tests replace it.
var convergeReadInitial = func(cfg *config.Config, member string, sudo bool) (render.EtcdInitial, bool, error) {
	return apply.ReadEtcdInitial(siteTransport(member, cfg.Sites[member], "", sudo), cfg.Deployment())
}

// convergeHoldsGateway reads whether a site's host holds this deployment's
// gateway Caddy, record or move marker. Tests replace it.
var convergeHoldsGateway = func(cfg *config.Config, site string, sudo bool) (bool, error) {
	return gatewaymove.Holds(siteTransport(site, cfg.Sites[site], "", sudo), cfg.Deployment())
}

// convergeGatewayMove reports whether a gateway move is still to run: a
// site that neither holds the gateway role nor runs Caddy for a monitor
// still holds what a gateway holds. Only with exactly one gateway declared,
// which is what a move goes to. A site that does not answer is an error,
// as for the etcd reads: a move left unfinished keeps an old gateway
// running.
func convergeGatewayMove(cfg *config.Config, sudo bool) (bool, error) {
	gateways := cfg.GatewaySites()
	if len(gateways) != 1 {
		return false, nil
	}
	for _, site := range cfg.SiteNames() {
		if site == gateways[0] || cfg.Sites[site].RunsCaddy() {
			continue
		}
		held, err := convergeHoldsGateway(cfg, site, sudo)
		if err != nil {
			return false, convergeStop("apply could not read "+site, err, "A site that was the gateway keeps serving until paisans gateway move stops it, so every site is asked. Make it reachable and run again.")
		}
		if held {
			return true, nil
		}
	}
	return false, nil
}

// convergeFounded reads which etcd members hold infra/etcd-initial, each in
// a step of its own so a spinner shows while ssh works. A member that does
// not answer is an error: whether the cluster is founded decides between
// founding and joining.
func convergeFounded(r ui.Reporter, cfg *config.Config, sudo bool) (map[string]render.EtcdInitial, error) {
	records := map[string]render.EtcdInitial{}
	if len(cfg.Etcd.Members) > 0 {
		r.Section("etcd members")
	}
	for _, m := range cfg.Etcd.Members {
		s := r.Step("read " + m + "'s etcd record")
		in, recorded, err := convergeReadInitial(cfg, m, sudo)
		if err != nil {
			s.Fail(err)
			return nil, fmt.Errorf("apply: reading whether %s's etcd member has been founded: %w. Every etcd member is read before anything runs", m, err)
		}
		if recorded {
			records[m] = in
			s.Done("founded")
		} else {
			// Not a success: founding the member is work the plan does.
			s.End(ui.Pending, "not founded yet")
		}
	}
	return records, nil
}

// convergeSecrets is the secrets check apply without --site makes once for
// the whole run, under a section of its own: each gateway's deployment record
// read once, and each secret and recorded name paisans.yaml no longer uses
// warned about once. The applies it runs as steps skip theirs (a
// checkReporter). A secrets file not written yet has nothing to check.
func convergeSecrets(r ui.Reporter, cfg *config.Config, path string, sudo bool) {
	secrets, err := config.LoadDeploymentSecrets(path, cfg.ID)
	if err != nil {
		return
	}
	if len(cfg.GatewaySites()) == 0 && len(secretsgen.Orphans(cfg, secrets, nil)) == 0 {
		return
	}
	r.Section("secrets")
	warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, func(gw string, elevated bool) registry.Runner {
		return registryHost(gw, cfg.Sites[gw], "", elevated && sudo)
	}))
}

// convergeOptions is what apply without --site hands on to its steps: the
// flags every command takes, and apply's own that are not about one site.
type convergeOptions struct {
	Config, Secrets string
	Execute, Sudo   bool
	// Verbose is -v, which every command takes.
	Verbose bool
	// KeepImages and MinFree are apply's, so each apply step takes them.
	KeepImages bool
	MinFree    string
	// stdin and stdout are where the run's review asks for approval. No
	// step is handed them: the run asks once for every site.
	stdin  io.Reader
	stdout io.Writer
}

// convergeFlags is args with the flags o hands on, each to the commands that
// take it: --config and -v to every one, --secrets to all but host prepare,
// --execute to all but init, --sudo to all but dns, and --min-free and
// --keep-images to apply alone.
func convergeFlags(args []string, o convergeOptions) []string {
	out := append([]string(nil), args...)
	out = append(out, "--config", o.Config)
	takesSecrets := !(args[0] == "host")
	takesExecute := args[0] != "init"
	takesSudo := !(args[0] == "dns")
	if takesSecrets && o.Secrets != "" {
		out = append(out, "--secrets", o.Secrets)
	}
	if takesExecute && o.Execute {
		out = append(out, "--execute")
	}
	if takesSudo {
		out = append(out, fmt.Sprintf("--sudo=%t", o.Sudo))
	}
	if o.Verbose {
		out = append(out, "-v")
	}
	if args[0] == "apply" {
		if o.MinFree != "" {
			out = append(out, "--min-free", o.MinFree)
		}
		if o.KeepImages {
			out = append(out, "--keep-images")
		}
	}
	return out
}

// convergeRead reads what decides the plan on this machine. Tests replace
// it.
var convergeRead = readConvergeState

// readConvergeState reads what decides the plan without reaching a host:
// whether init has work (no id, no mesh subnet, no secrets file, or a
// generated secret missing). With no id there is no deployment to read on a
// host yet, and the configuration is nil. Each etcd member's record is read
// by convergeFounded, once the configuration is validated.
func readConvergeState(configPath, secretsPath string) (*config.Config, convergeState, error) {
	var st convergeState
	cfg, err := config.Load(configPath)
	if err != nil {
		if !missingID(configPath) {
			return nil, st, err
		}
		st.NeedsInit = true
		return nil, st, nil
	}
	var why []string
	if cfg.Mesh.Subnet == "" {
		why = append(why, "no mesh subnet")
	}
	if secretsPath == "" {
		secretsPath = filepath.Join(filepath.Dir(configPath), "secrets.enc.yaml")
	}
	if secrets, err := config.LoadDeploymentSecrets(secretsPath, cfg.ID); errors.Is(err, fs.ErrNotExist) {
		why = append(why, "no secrets file yet")
	} else if err != nil {
		// A file that is there and does not read (no age key, most often)
		// is not init's to fix: init needs the same key. So nothing is
		// planned and no host is read.
		return nil, st, fmt.Errorf("apply: %w", err)
	} else if filled, err := secretsgen.Fill(cfg, secrets); err != nil {
		return nil, st, fmt.Errorf("apply: generating the secrets %s lacks: %w", secretsPath, err)
	} else if filled.Changed() {
		why = append(why, missingSecrets(filled.Generated))
		st.SecretsOnly = len(why) == 1
	}
	if len(why) > 0 {
		st.NeedsInit = true
		st.InitWhy = strings.Join(why, "; ")
	}
	return cfg, st, nil
}

// convergeSecretsPath is the secrets file apply without --site reads: the
// one --secrets names, or secrets.enc.yaml beside the configuration.
func convergeSecretsPath(configPath, secretsPath string) string {
	if secretsPath == "" {
		return filepath.Join(filepath.Dir(configPath), "secrets.enc.yaml")
	}
	return secretsPath
}

// convergeGenerate is the dry run generating the secrets init would: the
// generated secrets the file at path lacks are filled and the file written,
// by fillSecrets, as init does. It returns their names and the recipients
// the file was encrypted to. A file writing would harm is left to init, and
// left says why: one holding a key this toolkit does not read, which writing
// would drop, and one encrypted with no .sops.yaml recipient beside it,
// which writing would leave in plaintext. Why it failed is a ui.Problem.
func convergeGenerate(cfg *config.Config, path string) (names, recipients []string, left string, err error) {
	secrets, err := config.LoadDeploymentSecrets(path, cfg.ID)
	if err != nil {
		return nil, nil, "", generateProblem(path, err)
	}
	if secrets.UnknownKeys {
		return nil, nil, "it holds keys this toolkit does not read, which writing it would drop", nil
	}
	if secrets.Encrypted {
		if r, err := config.Recipients(filepath.Dir(path)); err == nil && len(r) == 0 {
			return nil, nil, "no " + config.SOPSConfigName + " recipient beside it, so it would be written in plaintext", nil
		}
	}
	filled, recipients, err := fillSecrets(cfg, path, secrets)
	if err != nil {
		return nil, nil, "", generateProblem(path, err)
	}
	return filled.Generated, recipients, "", nil
}

// generateProblem is why the dry run could not generate the secrets the file
// at path lacks: writing it, or reading what writing it needs.
func generateProblem(path string, err error) error {
	hint := "could not generate the secrets " + ui.ShortPath(path) + " lacks"
	var failed *secretsWriteError
	if errors.As(err, &failed) {
		hint = "could not write the generated secrets to " + ui.ShortPath(path)
	}
	return &ui.Problem{
		Hint:    hint,
		Explain: errLine(err) + ".\nNothing on a server changed. Once that is fixed, run paisans apply again.",
		Cause:   err,
	}
}

// missingSecrets names the generated secrets init would create: the first
// three, and how many more. Names only; a value never reaches the terminal.
func missingSecrets(names []string) string {
	if len(names) == 1 {
		return "generated secret missing (" + names[0] + ")"
	}
	return "generated secrets missing (" + fewNames(names) + ")"
}

// generatedSecrets is init's line once the dry run has written names into
// the file at path. Names only, as missingSecrets.
func generatedSecrets(names []string, path string) string {
	return "generated " + plural(len(names), "secret") + " into " + filepath.Base(path) + " (" + fewNames(names) + ")"
}

// fewNames is the first three names, and how many more.
func fewNames(names []string) string {
	shown := names
	if len(names) > 3 {
		shown = names[:3]
	}
	out := strings.Join(shown, ", ")
	if len(names) > 3 {
		out += fmt.Sprintf(" and %d more", len(names)-3)
	}
	return out
}

// missingID reports whether the file at path declares no deployment id.
func missingID(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		ID string `yaml:"id"`
	}
	return yaml.Unmarshal(data, &doc) == nil && doc.ID == ""
}

// runConverge is `paisans apply` without --site: every step that takes
// paisans.yaml to a complete, running stack, run in the order the deployment
// needs them and stopped at the first failure. Run again, it reads live state
// again and carries on. See docs/specs/2026-10-09-apply-converge.md.
func runConverge(r ui.Reporter, o convergeOptions) error {
	defer planEverySite()()
	// The run reviews every site itself; its steps check what it approved
	// rather than asking again. See converge_review.go.
	savedReview := reviewedByRun
	reviewedByRun = &runReview{}
	defer func() { reviewedByRun = savedReview }()
	configPath, secretsPath, execute, sudo := o.Config, o.Secrets, o.Execute, o.Sudo
	cfg, st, err := convergeRead(configPath, secretsPath)
	if err != nil {
		return err
	}
	// Every sudo password is asked before the first step that reaches a
	// host, once the configuration is one a host may be reached for.
	settled := false
	settle := func(cfg *config.Config) error {
		if settled {
			return nil
		}
		settled = true
		return settleSudo(r, cfg.SiteNames(), func(name string) apply.Transport {
			return convergeHost(name, cfg.Sites[name], sudo)
		})
	}
	initRan := false
	if st.NeedsInit && execute {
		// init reads every host for the mesh subnet, so the passwords are
		// asked ahead of it. A file init cannot read yet, with no id, or
		// one it would refuse, reaches no host here: init asks for them in
		// its own first read of the hosts.
		if pre, err := config.LoadForInit(configPath); err == nil && !validate.Check(pre).Refused() {
			if err := settle(pre); err != nil {
				return err
			}
		}
		// init writes the id, the subnet and the secrets the rest is
		// planned from, so the state is read again once it has run.
		if err := convergeRun(convergeFlags([]string{"init"}, o)); err != nil {
			return convergeStop("apply stopped at init", err, "Run paisans apply --execute again to resume.")
		}
		// init reported through its own reporter, and pointed the retries
		// and holds at it; the reads below draw on r.
		routeRetries(r)
		routeHolds(r)
		if cfg, st, err = convergeRead(configPath, secretsPath); err != nil {
			return err
		}
		st.NeedsInit = false
		initRan = true
	}
	if cfg == nil {
		r.Section("configuration")
		r.Step("init").End(ui.Pending, "the deployment id, the mesh subnet and every generated secret")
		r.Result("Nothing changed. %s has no deployment id yet, so the rest is planned once init has run: re-run with --execute, or run paisans init.", configPath)
		return nil
	}
	// A configuration validate refuses reaches no host: a typo in
	// etcd.members would otherwise read as a host that did not answer.
	// The findings open the configuration section, which init's line
	// belongs to, so the section is headed once, ahead of the etcd reads.
	// An init that has just run reported them itself.
	result := validate.Check(cfg)
	headed := false
	if !initRan {
		headed = reportFindingsUnder(r, "configuration", configPath, result)
	}
	if result.Refused() {
		return refused(configPath, len(result.Refusals()), "")
	}
	// A dry run generates the secrets init would, when that is all init
	// has to do, so the steps after it are checked against them. The rest
	// of init's work is init's.
	var generated []string
	if st.NeedsInit && !execute {
		if !headed {
			r.Section("configuration")
		}
		line := r.Step("init")
		path := convergeSecretsPath(configPath, secretsPath)
		var recipients []string
		var left string
		var err error
		if st.SecretsOnly {
			generated, recipients, left, err = convergeGenerate(cfg, path)
		}
		switch {
		case err != nil:
			hint, explain := ui.Describe(err)
			line.End(ui.Failed, hint)
			r.Explain(explain)
		case len(generated) > 0:
			line.Done(generatedSecrets(generated, path))
			if len(recipients) == 0 {
				warnPlaintext(r, path)
			}
			st.NeedsInit = false
		case left != "":
			line.End(ui.Pending, initWhy(st)+"; left to init: "+left)
		default:
			line.End(ui.Pending, initWhy(st))
		}
	}
	if err := settle(cfg); err != nil {
		return err
	}
	// An init that has just run warned about the secrets itself.
	if !initRan {
		convergeSecrets(r, cfg, convergeSecretsPath(configPath, secretsPath), sudo)
	}
	if st.Initial, err = convergeFounded(r, cfg, sudo); err != nil {
		return err
	}
	if st.MoveGateway, err = convergeGatewayMove(cfg, sudo); err != nil {
		return err
	}
	steps := convergePlan(cfg, st)
	// The version the run applies is gathered once, before the review, so
	// a fork or a stale base is refused before the operator is asked
	// anything; its steps do not gather again.
	store, err := applyStore(r, configPath, convergeSecretsPath(configPath, secretsPath), nil, cfg, execute, o.stdin, o.stdout)
	if err != nil {
		return err
	}
	if !execute {
		marks := convergeStatus(r, cfg, steps, o)
		// A dry run's admin problem is shown on its line; it stops nothing.
		_, _ = convergeAdmins(r, cfg, o, false, func(app string) bool { return convergeUp(cfg, steps, marks, app) })
		convergeWebServerPending(r, cfg, webServerScope{})
		// The review ends the plan, with what --execute asks approval for.
		// A site it could not read is shown on its line; it stops nothing.
		reviews, pending, _ := convergeReview(r, cfg, o)
		// What --execute checks every host's record against before its
		// first step, and publishes once host prepare has made the stores.
		if err := store.check(r, convergeRecords(reviews), false, o.stdin, o.stdout); err != nil {
			return err
		}
		if _, err := store.publish(r, false, publishWhen); err != nil {
			return err
		}
		convergeLeft(r, cfg, secretsPath, configPath)
		next := "Re-run with --execute to apply. Each step's own dry run, Eg: paisans apply --site <name>, shows its detail."
		if pending > 0 {
			next = "Re-run with --execute to apply; it asks you to type " + approveWord + " for the review above before its first step. Each step's own dry run, Eg: paisans apply --site <name>, shows its detail."
		}
		if len(generated) > 0 {
			r.Result("Nothing changed on the servers. Added %s to %s. %s", plural(len(generated), "generated secret"), filepath.Base(convergeSecretsPath(configPath, secretsPath)), next)
			return nil
		}
		r.Result("Nothing changed. %s", next)
		return nil
	}
	// One review for every site, asked once, before the first step that
	// changes a host. init, which may have run above, writes only local
	// files, and the images are rendered from the secrets it generates.
	reviews, _, err := convergeReview(r, cfg, o)
	if err != nil {
		return err
	}
	// Approved, and before the first step: every host's record is checked
	// against the version, which only reads. The version is published once
	// for the run, after the host prepare steps, which make every site's
	// store, and before any other step changes a host, so the store holds
	// what is applied even if the run stops part way. Each apply step
	// records it, and refuses files that are not it.
	if err := store.check(r, convergeRecords(reviews), true, o.stdin, o.stdout); err != nil {
		return err
	}
	publish := func() (err error) {
		reviewedByRun.version, err = store.publish(r, true, publishWhen)
		return err
	}
	if err := convergeExecute(r, cfg, steps, o, publish); err != nil {
		return err
	}
	// Each step pointed the retries and holds at its own reporter; the
	// check draws on r.
	routeRetries(r)
	routeHolds(r)
	links, adminErr := convergeAdmins(r, cfg, o, true, func(string) bool { return true })
	// The links are printed last, after the result, whatever it is: a run
	// that fails after issuing them has still issued them.
	defer printLoginLinks(links)
	// An app the operator's own web server serves is running, but not yet
	// known to be reachable: the run is complete only once it is.
	end, broken := webServerStep(r, cfg, configPath, sudo, webServerScope{})
	if end == nil {
		if err := convergeRecordWebServer(r, cfg, reviews); err != nil {
			return err
		}
	}
	convergeLeft(r, cfg, secretsPath, configPath)
	if end != nil || adminErr != nil {
		return webServerEnd(end, broken, adminErr)
	}
	r.Result("Deployment complete.")
	return nil
}

// convergeStop is the error a run stops with at a step: the step named in
// the hint, and the step's own problem, its hint and explanation, ahead of
// next, how to go on. The step's error is the cause, so -v shows it whole and
// errors.Is still finds what it wraps.
// sentences is err's hint as a sentence, then its explanation.
func sentences(err error) string {
	hint, explain := ui.Describe(err)
	lines := []string{sentence(hint)}
	if explain != "" {
		lines = append(lines, explain)
	}
	return strings.Join(lines, "\n")
}

func convergeStop(hint string, err error, next string) error {
	inner, explain := ui.Describe(err)
	// A wait's own advice is storage add's, which a run of apply replaces
	// with next: only what it waits on is kept.
	var w *storageadd.Waiting
	if errors.As(err, &w) {
		inner, explain = w.Hint()+": "+w.Detail, ""
	}
	lines := []string{sentence(inner)}
	if explain != "" {
		lines = append(lines, explain)
	}
	return &ui.Problem{Hint: hint, Explain: strings.Join(append(lines, next), "\n"), Cause: err}
}

// sentence is hint ended with one period, or as it is when it ends with a
// colon, which introduces the lines after it.
func sentence(hint string) string {
	if strings.HasSuffix(hint, ":") {
		return hint
	}
	return strings.TrimSuffix(hint, ".") + "."
}

// stepStatus is one step's status in a dry run.
type stepStatus struct {
	mark   ui.Mark
	result string
	// after is the title of the step it waits on, when it waits.
	after string
	// explain is what to do about a failed check, and problem the check's
	// hint and explanation as sentences. Neither repeats on a later step
	// that takes an earlier one's status.
	explain, problem string
	reports          []checkReport
}

// convergeStatus is the dry run's plan: a prepare line per site, a pull
// line per site, dns, and an install line per site and one for storage, each marked with its
// status; init's line is runConverge's, drawn with the findings. A step
// is checked by its own dry run once every step it waits on is up to date,
// and is otherwise marked as waiting on the first that is not; until that
// one runs, its dry run would describe a host about to change. A check that
// fails is marked with its error, and the rest are still checked. Each
// command is checked once. It returns each command's mark, by title.
func convergeStatus(r ui.Reporter, cfg *config.Config, steps []convergeStep, o convergeOptions) map[string]ui.Mark {
	// done is the status of each command, by title, as its latest step was
	// marked.
	done := map[string]stepStatus{}
	// settled is step i's status when it needs no check of its own: it
	// waits on a step not up to date, or its command was checked already.
	settled := func(i int) (stepStatus, bool) {
		s := steps[i]
		after := ""
		for _, w := range convergeWaits(cfg, steps, i) {
			if done[w].mark != ui.OK {
				after = w
				break
			}
		}
		prior, checked := done[s.Title]
		switch {
		case after == s.Title:
			// An apply in pass two waits on the same command earlier.
			return stepStatus{mark: ui.Waiting, result: "after the earlier " + after, after: after}, true
		case after != "":
			return stepStatus{mark: ui.Waiting, result: "after " + after, after: after}, true
		case checked:
			return stepStatus{mark: prior.mark, result: prior.result, after: prior.after}, true
		}
		return stepStatus{}, false
	}
	// checked is s's status from its own dry run, or for a pull, from the
	// plan its site's apply would make. It reads and writes nothing shared,
	// so the prepare and pull lines run it on every host at once.
	pulls := newPuller(cfg, o)
	checked := func(s convergeStep) stepStatus {
		if s.Stage == stagePull {
			return pulls.pullStatus(s)
		}
		var st stepStatus
		summary, reports, err := convergeCheck(r, convergeFlags(s.Args, o))
		st.reports = reports
		switch {
		case err != nil:
			// The hint is the line's result; what to do follows it at
			// every verbosity, as a failure always says.
			st.mark = ui.Failed
			st.result, st.explain = ui.Describe(err)
			st.problem = sentences(err)
		case summary == "":
			st.mark = ui.OK
		default:
			st.mark, st.result = ui.Pending, summary
		}
		return st
	}
	check := func(i int) stepStatus {
		st, ok := settled(i)
		if !ok {
			st = checked(steps[i])
		}
		done[steps[i].Title] = st
		return st
	}
	for i := 0; i < len(steps); {
		s := steps[i]
		switch {
		case s.Stage == stageInit:
			// Its line is drawn with the findings, under configuration,
			// before the etcd reads; every other step waits on it.
			done[s.Title] = stepStatus{mark: ui.Pending}
			i++
		case s.Stage.installs():
			j := installEnd(steps, i)
			installStatus(r, steps[i:j], func(k int) stepStatus { return check(i + k) })
			i = j
		case s.Stage == stagePrepare || s.Stage == stagePull:
			// Every host's check runs at once, each on its own line.
			j := stageEnd(steps, i)
			group := steps[i:j]
			r.Section(map[stage]string{stagePrepare: "prepare", stagePull: "pull"}[s.Stage])
			lines := ui.Group(r, siteTitles(group)...)
			statuses := make([]stepStatus, len(group))
			var unsettled []int
			for k := range group {
				st, ok := settled(i + k)
				if ok {
					statuses[k] = st
					endStatus(lines[k], st)
					continue
				}
				unsettled = append(unsettled, k)
			}
			atOnce(len(unsettled), func(n int) {
				k := unsettled[n]
				statuses[k] = checked(group[k])
				endStatus(lines[k], statuses[k])
			})
			for k, s := range group {
				done[s.Title] = statuses[k]
			}
			// Each check pointed nothing elsewhere, but a check of a step
			// that is not host prepare may have: the reads after draw on r.
			routeRetries(r)
			routeHolds(r)
			i = j
		default:
			title := s.Title
			if i == 0 || steps[i-1].Stage != s.Stage {
				r.Section(map[stage]string{stageDNS: "dns", stageGateway: "gateway"}[s.Stage])
			}
			line := r.Step(title)
			st := check(i)
			notes := lineDetails(line, "", "", st.reports)
			line.End(st.mark, st.result)
			if st.explain != "" {
				r.Explain(st.explain)
			}
			showNotes(r, notes)
			i++
		}
	}
	marks := map[string]ui.Mark{}
	for title, st := range done {
		marks[title] = st.mark
	}
	return marks
}

// installStatus is the dry run's install lines: a line per site and one
// for storage, in the order the steps first reach them, each step checked
// by check, by index, in the plan's order. A line's mark is its steps'
// together: a refusal, which --execute would stop at, with its hint as the
// result and its explanation below the line; then a wait on prepare or
// init, which leaves the rest unchecked; then any work, named by what each
// step found; and up to date only when every step is. Each step is listed
// under its line with its own status, which -v shows, and its notes follow
// the line at every verbosity.
func installStatus(r ui.Reporter, steps []convergeStep, check func(int) stepStatus) {
	r.Section("install")
	places := installPlaces(steps)
	lines, done := ui.Ordered(r, places...)
	defer done()
	type combined struct {
		failed           bool
		result, waits    string
		pending          bool
		found, explained []string
	}
	all := make([]combined, len(places))
	last := lastVisits(steps)
	for i, s := range steps {
		k := slices.Index(places, installPlace(s))
		line, c := lines[k], &all[k]
		line.Start()
		st := check(i)
		line.Detail("%s", innerLine(st.mark, s.Title, st.result))
		for _, n := range lineDetails(line, "  ", "", st.reports) {
			line.Note(n.Text, n.Detail)
		}
		switch st.mark {
		case ui.Failed:
			if !c.failed {
				c.failed, c.result = true, st.result
				c.explained = append(c.explained, st.explain)
			} else if st.problem != "" {
				c.explained = append(c.explained, st.problem)
			}
		case ui.Waiting:
			switch {
			case st.after == "init":
				c.waits = "init"
			case strings.HasPrefix(st.after, "host prepare ") && c.waits == "":
				c.waits = "prepare"
			default:
				// It waits on another install step, which is work.
				c.pending = true
			}
		case ui.Pending:
			c.pending = true
			if st.result != "" && !slices.Contains(c.found, st.result) {
				c.found = append(c.found, st.result)
			}
		}
		if last[k] != i {
			line.Pause("")
			continue
		}
		for _, e := range c.explained {
			line.Explain(e)
		}
		switch {
		case c.failed:
			line.End(ui.Failed, c.result)
		case c.waits != "":
			line.End(ui.Waiting, "after "+c.waits)
		case c.pending:
			line.End(ui.Pending, strings.Join(c.found, "; "))
		default:
			line.End(ui.OK, "")
		}
	}
}

// endStatus ends a dry run's prepare line with its status: the check's
// details under it, with -v, then what to do about a failure and its notes.
func endStatus(line ui.Line, st stepStatus) {
	for _, n := range lineDetails(line, "", "", st.reports) {
		line.Note(n.Text, n.Detail)
	}
	line.Explain(st.explain)
	line.End(st.mark, st.result)
}

// stageEnd is the index after the steps of the stage steps[from] is in,
// starting at from.
func stageEnd(steps []convergeStep, from int) int {
	to := from
	for to < len(steps) && steps[to].Stage == steps[from].Stage {
		to++
	}
	return to
}

// siteTitles are the prepare or pull lines' titles: each step's site.
func siteTitles(steps []convergeStep) []string {
	titles := make([]string, len(steps))
	for i, s := range steps {
		titles[i] = stepSite(s)
	}
	return titles
}

// installEnd is the index after the install steps that start at from.
func installEnd(steps []convergeStep, from int) int {
	to := from
	for to < len(steps) && steps[to].Stage.installs() {
		to++
	}
	return to
}

// installPlace is the install line a step is on: its site's, or storage's
// for storage init and storage add.
func installPlace(s convergeStep) string {
	if s.Stage == stageStorage {
		return "storage"
	}
	return stepSite(s)
}

// installPlaces is the install lines' titles: each place the steps reach,
// in the order they first reach it.
func installPlaces(steps []convergeStep) []string {
	var places []string
	for _, s := range steps {
		if p := installPlace(s); !slices.Contains(places, p) {
			places = append(places, p)
		}
	}
	return places
}

// lastVisits is, for each install line, the index of the last of steps on
// it: the step after which the line ends.
func lastVisits(steps []convergeStep) []int {
	places := installPlaces(steps)
	last := make([]int, len(places))
	for i, s := range steps {
		last[slices.Index(places, installPlace(s))] = i
	}
	return last
}

// innerLine is a step of an install line as its detail lists it.
func innerLine(m ui.Mark, title, result string) string {
	line := map[ui.Mark]string{ui.OK: "✓", ui.Failed: "✗", ui.Pending: "○", ui.Waiting: "·"}[m] + " " + title
	if result != "" {
		line += ": " + result
	}
	return line
}

// lineDetails attaches a step's own output and its reports but its notes to
// line as details, each line behind indent, and returns the notes, which
// are shown after the line at every verbosity.
func lineDetails(line ui.Step, indent, output string, reports []checkReport) []checkReport {
	for _, l := range strings.Split(output, "\n") {
		if strings.TrimSpace(l) != "" {
			line.Detail("%s%s", indent, l)
		}
	}
	var notes []checkReport
	for _, c := range reports {
		if c.Note {
			notes = append(notes, c)
			continue
		}
		line.Detail("%s%s", indent, strings.ReplaceAll(c.Text, "\n", "\n"+indent))
	}
	return notes
}

func showNotes(r ui.Reporter, notes []checkReport) {
	for _, n := range notes {
		r.Note(n.Text, n.Detail)
	}
}

// resumeAdvice is what a stop says to do, but at a DNS conflict or a wait.
const resumeAdvice = "Run paisans apply --execute again to resume."

// convergeExecute runs every step but init, which runConverge has run: a
// prepare line per site, as it runs; a pull line per site, likewise; dns init, which reports as typed by
// hand, so a conflicting record it lists is what the stop's advice points
// at; and an install line per site and one for storage, each spinning while
// one of its steps runs. It stops at the first failure, so a DNS conflict
// stops the run before any site is applied.
//
// publish is called once, after the prepare steps and before the first
// other step that runs, or at the end when none does.
func convergeExecute(r ui.Reporter, cfg *config.Config, steps []convergeStep, o convergeOptions, publish func() error) error {
	published := false
	for i := 0; i < len(steps); {
		s := steps[i]
		if !published && s.Stage != stageInit && s.Stage != stagePrepare {
			published = true
			if err := publish(); err != nil {
				return err
			}
		}
		switch {
		case s.Stage == stageInit:
			i++
		case s.Stage == stagePrepare:
			j := stageEnd(steps, i)
			if err := convergePrepareAll(r, cfg, steps[i:j], o); err != nil {
				return err
			}
			i = j
		case s.Stage == stagePull:
			j := stageEnd(steps, i)
			if err := convergePullAll(r, cfg, steps[i:j], o); err != nil {
				return err
			}
			i = j
		case s.Stage.installs():
			j := installEnd(steps, i)
			if err := convergeInstall(r, steps[i:j], o); err != nil {
				return err
			}
			i = j
		default:
			r.Section(map[stage]string{stageDNS: "dns", stageGateway: "gateway"}[s.Stage])
			insideApply = true
			err := convergeRun(convergeFlags(s.Args, o))
			insideApply = false
			if err != nil {
				if errors.Is(err, dns.ErrConflict) {
					return convergeStop("apply stopped at "+s.Title, err, "Resolve each DNS conflict above as it says, then run paisans apply --execute again.")
				}
				return convergeStop("apply stopped at "+s.Title, err, resumeAdvice)
			}
			i++
		}
	}
	if !published {
		return publish()
	}
	return nil
}

// publishWhen is when apply without --site publishes the version it
// applies, as its plan says.
const publishWhen = "after host prepare, which makes every site's store, and before any other step"

// convergePrepareAll runs steps, the prepare lines', every host at once,
// each on its own line, which ends with its host. A host that fails stops
// none of the others; once every one has ended, the run stops if any failed,
// naming each.
func convergePrepareAll(r ui.Reporter, cfg *config.Config, steps []convergeStep, o convergeOptions) error {
	r.Section("prepare")
	lines := ui.Group(r, siteTitles(steps)...)
	errs := make([]error, len(steps))
	atOnce(len(steps), func(k int) {
		output, _, _, _, reports, err := prepareRun(r, lines[k], convergeFlags(steps[k].Args, o), o.Verbose)
		for _, n := range lineDetails(lines[k], "", output, reports) {
			lines[k].Note(n.Text, n.Detail)
		}
		errs[k] = err
		if err != nil {
			lines[k].Fail(err)
			return
		}
		lines[k].Done("")
	})
	// A host prepare a test runs through the step runner may have pointed
	// the retries and holds elsewhere; what follows draws on r.
	routeRetries(r)
	routeHolds(r)
	return groupStop("host prepare", steps, errs)
}

// groupStop is how a stage whose hosts ran at once stops, once every one
// has ended: not at all when none failed, at the one step that did, or at
// the stage, what, on every site that failed, each named with why.
func groupStop(what string, steps []convergeStep, errs []error) error {
	var failed []int
	for k, err := range errs {
		if err != nil {
			failed = append(failed, k)
		}
	}
	switch len(failed) {
	case 0:
		return nil
	case 1:
		return convergeStop("apply stopped at "+steps[failed[0]].Title, errs[failed[0]], resumeAdvice)
	}
	var sites, explain []string
	var causes []error
	for _, k := range failed {
		site := stepSite(steps[k])
		sites = append(sites, site)
		explain = append(explain, site+": "+strings.ReplaceAll(sentences(errs[k]), "\n", " "))
		causes = append(causes, errs[k])
	}
	return &ui.Problem{
		Hint:    "apply stopped at " + what + " on " + strings.Join(sites, ", "),
		Explain: strings.Join(append(explain, resumeAdvice), "\n"),
		Cause:   errors.Join(causes...),
	}
}

// convergeInstall runs steps, the install lines', one at a time in their
// order, quietly. Each site has a line, and storage one, in the order the
// steps first reach them: it spins while one of its steps runs, waits
// between them with the time they took, and ends once its last step has.
// A data member's founding stop is expected, and the run carries on: pass
// two applies it again. So is a replica's stop for the app databases, on any
// of its site's applies, since only the Patroni leader's site creates them
// and which site leads is known only once Patroni runs. A later visit to the
// site resumes it; when the stop is on the site's last visit, the site is
// applied once more after every other step, by which time the leader's site
// has had its own last visit, and a stop then ends the run. Each step is
// listed under its line with its own result, which -v shows with the step's
// own output; its notes follow the line at every verbosity. A failure ends
// its line and the run, leaving the other lines as they stand.
func convergeInstall(r ui.Reporter, steps []convergeStep, o convergeOptions) error {
	r.Section("install")
	places := installPlaces(steps)
	lines, done := ui.Ordered(r, places...)
	defer done()
	last := lastVisits(steps)
	// again is each step whose site waited for its databases on its last
	// visit, to be run once more at the end.
	var again []int
	run := func(i int, final bool) error {
		s := steps[i]
		k := slices.Index(places, installPlace(s))
		line := lines[k]
		line.Start()
		start := convergeNow()
		output, reports, err := convergeQuiet(r, line, convergeFlags(s.Args, o), o.Verbose)
		founding := err != nil && s.Stage == stageFounding && errors.Is(err, apply.ErrFoundingWait)
		databases := err != nil && !final && errors.Is(err, apply.ErrDatabasesWait)
		var why string
		switch {
		case founding:
			why = foundingPause(err)
		case databases:
			why = databasesPause(err)
		case err == nil && !final && last[k] != i:
			why = revisitPause(steps, i)
		}
		switch {
		case err == nil:
			line.Detail("%s", innerLine(ui.OK, s.Title, since(start)))
		case founding || databases:
			line.Detail("%s", innerLine(ui.OK, s.Title, why))
			line.Detail("  %s", strings.ReplaceAll(sentences(err), "\n", "\n  "))
		default:
			line.Detail("%s", innerLine(ui.Failed, s.Title, since(start)))
		}
		for _, n := range lineDetails(line, "  ", output, reports) {
			line.Note(n.Text, n.Detail)
		}
		switch {
		case databases && last[k] == i:
			again = append(again, i)
			line.Pause(why)
		case (err == nil || founding) && (final || last[k] == i):
			line.Done("")
		case err == nil || founding || databases:
			line.Pause(why)
		case errors.Is(err, storageadd.ErrWaiting):
			// Not a failure, and main exits 75 for it as storage add does.
			line.End(ui.Pending, "waiting on Garage")
			return convergeStop("apply is waiting at "+s.Title, err, "Nothing failed: Garage is still moving data. Run paisans apply --execute again later.")
		default:
			line.Fail(err)
			return convergeStop("apply stopped at "+s.Title, err, resumeAdvice)
		}
		return nil
	}
	for i := range steps {
		if err := run(i, false); err != nil {
			return err
		}
	}
	for _, i := range again {
		if err := run(i, true); err != nil {
			return err
		}
	}
	return nil
}

// revisitPause is why the line of steps[i], which is visited again, pauses
// after it: until the places of the steps between it and its next visit
// are done, in the order they run.
func revisitPause(steps []convergeStep, i int) string {
	place := installPlace(steps[i])
	var between []string
	for _, s := range steps[i+1:] {
		p := installPlace(s)
		if p == place {
			break
		}
		if !slices.Contains(between, p) {
			between = append(between, p)
		}
	}
	switch len(between) {
	case 0:
		return ""
	case 1:
		return "paused until " + between[0] + " is done"
	}
	return "paused until " + andList(between) + " are done"
}

// foundingPause is why a founding stop pauses its line: until the founding
// members err names as not started run etcd.
func foundingPause(err error) string {
	var wait *apply.FoundingWaitError
	switch {
	case !errors.As(err, &wait) || len(wait.Unstarted) == 0:
		return "paused until the other founding members run etcd"
	case len(wait.Unstarted) == 1:
		return "paused until " + wait.Unstarted[0] + " runs etcd"
	}
	return "paused until " + andList(wait.Unstarted) + " run etcd"
}

// databasesPause is why a replica's stop pauses its line: until the
// leader's site err names creates the app databases.
func databasesPause(err error) string {
	leader := "the leader"
	var wait *apply.DatabasesWaitError
	if errors.As(err, &wait) && wait.Leader != "" {
		leader = wait.Leader
	}
	return "paused until " + leader + " creates its databases"
}

// andList is names as a sentence lists them, "a, b and c"; past four, the
// first three and how many more.
func andList(names []string) string {
	if len(names) > 4 {
		names = append(names[:3:3], fmt.Sprintf("%d more", len(names)-3))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// convergeNow is the clock an inner step of an install line is timed by.
// Tests replace it.
var convergeNow = time.Now

// since is the time from start, when a second or more, as a step's result.
func since(start time.Time) string {
	if d := convergeNow().Sub(start); d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return ""
}

// convergeQuiet runs args, a step's command, through convergeRun with its
// report kept from the run's output, as checkStep keeps a check's: its
// warnings, refusals and ssh retries come back as details and its notes for
// after the line, and a hold for a prompt reaches r, which draws the line's
// spinner. While a step of its own is open, line shows it (ui.Nest). With
// verbose the command's own output, as -v prints it, comes back too.
func convergeQuiet(r ui.Reporter, line ui.Step, args []string, verbose bool) (output string, reports []checkReport, err error) {
	var b strings.Builder
	inner := ui.Discard
	if verbose {
		inner = ui.NewPlain(&b, true)
	}
	nested, done := ui.Nest(line, inner)
	saved := reporterOverride
	reporterOverride = checkReporter{nested, r, &reports}
	defer func() {
		done()
		reporterOverride = saved
		routeRetries(r)
		routeHolds(r)
	}()
	err = convergeRun(args)
	return b.String(), reports, err
}

// convergeUp reports whether every step that brings a Pocket ID app up was
// marked up to date: init, and each step about a site the app runs on.
func convergeUp(cfg *config.Config, steps []convergeStep, marks map[string]ui.Mark, app string) bool {
	sites := render.AppSites(cfg)[app]
	// A title's mark is its latest step's: an apply's in pass two, which is
	// checked only once its earlier apply or site add is up to date.
	for _, s := range steps {
		about := s.Args[0] != "storage" && slices.Contains(sites, stepSite(s))
		if (s.Title == "init" || about) && marks[s.Title] != ui.OK {
			return false
		}
	}
	return true
}

// convergeWaits is the titles of the earlier steps that steps[i] waits on,
// in the order they ran: init, for every step once it has work; the site's
// host prepare, for every step on a site, its pull included; each witness's earlier apply, for
// a founding member that is not one, whose gate refuses it until the witness
// runs; the site's earlier apply or site add and the storage step, for an
// apply in pass two; and the host prepare and first apply of each Garage
// site, for storage, since Garage has to be running to be asked.
func convergeWaits(cfg *config.Config, steps []convergeStep, i int) []string {
	var waits []string
	if steps[0].Title == "init" && i > 0 {
		waits = append(waits, "init")
	}
	s := steps[i]
	earlier := steps[:i]
	// first is the title of the first earlier apply or site add of site.
	first := func(site string) []string {
		for _, e := range earlier {
			if e.Stage.installs() && e.Stage != stagePassTwo && stepSite(e) == site {
				return []string{e.Title}
			}
		}
		return nil
	}
	prepare := func(site string) string { return "host prepare --site " + site }
	switch {
	case s.Args[0] == "pull":
		waits = append(waits, prepare(stepSite(s)))
	case s.Args[0] == "apply" || s.Args[0] == "site":
		site := stepSite(s)
		waits = append(waits, prepare(site))
		if s.Stage == stageFounding && !cfg.Sites[site].Has(config.RoleWitness) {
			for _, e := range earlier {
				if e.Stage != stagePassTwo && e.Args[0] == "apply" && cfg.Sites[stepSite(e)].Has(config.RoleWitness) {
					waits = append(waits, e.Title)
				}
			}
		}
		if s.Stage == stagePassTwo {
			waits = append(waits, first(site)...)
			for _, e := range earlier {
				if e.Stage == stageStorage {
					waits = append(waits, e.Title)
				}
			}
		}
	case s.Args[0] == "gateway":
		// The move checks the new gateway answers, so it waits on that
		// gateway's applies.
		for _, g := range cfg.GatewaySites() {
			waits = append(waits, prepare(g))
			waits = append(waits, "apply --site "+g)
		}
	case s.Args[0] == "storage":
		for _, g := range cfg.Storage.Garage.Sites {
			waits = append(waits, prepare(g))
			waits = append(waits, first(g)...)
		}
	}
	ran := func(title string) int {
		return slices.IndexFunc(earlier, func(e convergeStep) bool { return e.Title == title })
	}
	slices.SortStableFunc(waits, func(a, b string) int { return ran(a) - ran(b) })
	return waits
}

// stepSite is the site a step is about, or empty for one about none. Each
// step about a site names it last.
func stepSite(s convergeStep) string {
	switch strings.Join(s.Args[:min(2, len(s.Args))], " ") {
	case "apply --site", "pull --site", "site add", "host prepare", "storage init":
		return s.Args[len(s.Args)-1]
	}
	return ""
}

// dryRunFound is told by the dry run of each command apply without --site
// runs as a step what it would change: a summary such as "3 changes", or
// host prepare's kinds of change such as "docker, firewall", empty
// when there is nothing to do, and stop when it found something --execute
// would stop at. checkStep sets it; otherwise it does nothing.
var dryRunFound = func(summary string, stop error) {}

// count is n of unit, Eg: "3 changes", or empty for none, as dryRunFound
// takes it.
func count(n int, unit string) string {
	if n == 0 {
		return ""
	}
	return plural(n, unit)
}

// convergeCheck is one step's status in a dry run: what its own dry run
// would change, empty for nothing. Tests replace it.
var convergeCheck func(r ui.Reporter, args []string) (string, []checkReport, error)

// checkReport is one thing a step's own dry run reported while it was
// checked. A note is shown after the step's line at every verbosity, since
// it is something left for the operator; the rest is a detail of the line,
// shown with -v.
type checkReport struct {
	Note   bool
	Text   string
	Detail string
}

// checkReporter is what a step's own command reports through while it is
// checked, or run under an install line (convergeQuiet). Its plan is not
// shown, since the step's line says what it found;
// its warnings, notes, refusals and ssh retries are kept for that line, and a
// hold for a prompt reaches r, which draws the line's spinner.
type checkReporter struct {
	ui.Reporter
	r       ui.Reporter
	reports *[]checkReport
}

func (c checkReporter) Hold() (resume func()) { return ui.Hold(c.r) }

func (c checkReporter) Warn(hint, detail string) {
	*c.reports = append(*c.reports, checkReport{Text: joinDetail("! "+hint, detail)})
}

func (c checkReporter) Note(hint, detail string) {
	*c.reports = append(*c.reports, checkReport{Note: true, Text: hint, Detail: detail})
}

func (c checkReporter) Refuse(hint, explanation string) {
	*c.reports = append(*c.reports, checkReport{Text: joinDetail("✗ "+hint, explanation)})
}

// reportOwedApplies notes each apply a command leaves owed on a site, as
// reportRemains does, but not for a step of converge (a checkReporter):
// converge's pass two applies every site again, which makes each of them,
// and a note naming another site would read as a second line for it.
func reportOwedApplies(r ui.Reporter, lines []string) {
	if _, step := r.(checkReporter); step {
		return
	}
	reportRemains(r, lines)
}

// RetryLog is where the check's ssh retries go: kept, as a detail of the
// step's line.
func (c checkReporter) RetryLog() io.Writer { return retryCollector{c.reports} }

type retryCollector struct{ reports *[]checkReport }

func (w retryCollector) Write(p []byte) (int, error) {
	*w.reports = append(*w.reports, checkReport{Text: strings.TrimRight(string(p), "\n")})
	return len(p), nil
}

func joinDetail(hint, detail string) string {
	if detail == "" {
		return hint
	}
	return hint + ": " + detail
}

// checkStep runs args, a step's command without --execute, through run, in
// this process and quietly, and returns what its dry run told dryRunFound.
// Its reports, retries and holds are routed back to r once it returns.
func checkStep(r ui.Reporter, args []string, run func([]string) error) (string, []checkReport, error) {
	found := false
	var summary string
	var stop error
	var reports []checkReport
	savedFound, savedOverride := dryRunFound, reporterOverride
	dryRunFound = func(s string, err error) { found, summary, stop = true, s, err }
	reporterOverride = checkReporter{ui.Discard, r, &reports}
	defer func() {
		dryRunFound, reporterOverride = savedFound, savedOverride
		routeRetries(r)
		routeHolds(r)
	}()
	if err := run(args); err != nil {
		return "", reports, err
	}
	if stop != nil {
		return "", reports, stop
	}
	if !found {
		name := args[0]
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			name += " " + args[1]
		}
		return "", reports, fmt.Errorf("%s ended without saying what it would change", name)
	}
	return summary, reports, nil
}

// convergeLeft names each credential the toolkit cannot generate, which only
// the operator can provide.
func convergeLeft(r ui.Reporter, cfg *config.Config, secretsPath, configPath string) {
	if secretsPath == "" {
		secretsPath = filepath.Join(filepath.Dir(configPath), "secrets.enc.yaml")
	}
	if secrets, err := config.LoadDeploymentSecrets(secretsPath, cfg.ID); err == nil {
		for _, key := range secretsgen.OwedNames(cfg, secrets) {
			r.Note("secrets: "+key+" is owed and only you can provide it", "paisans secrets set "+key+" < value")
		}
	}
}

// convergeAnyAdmin names each Pocket ID app that has no admin, for a
// deployment that declares no admins, so only the operator can create one. An app is asked, in a step of its own, only once up
// says it is running; one that is not is named by nothing, since the plan
// already says what brings it up. A check that fails names the app with why,
// and fails nothing else.
func convergeAnyAdmin(r ui.Reporter, cfg *config.Config, o convergeOptions, up func(app string) bool) {
	section := false
	for _, app := range cfg.AppNames() {
		if cfg.Apps[app].Kind != config.KindPocketID || !up(app) {
			continue
		}
		if !section {
			r.Section("pocket id")
			section = true
		}
		command := "paisans app admin create --app " + app + " --username <name> --email <address>"
		s := r.Step(adminCheckTitle(app))
		has, err := convergeAdminCheck(cfg, app, o)
		switch {
		case err != nil:
			s.End(ui.Failed, "could not check")
			r.Note("Pocket ID "+app+": could not check for an admin ("+errLine(err)+")", command)
		case has:
			s.Done("has one")
		default:
			s.End(ui.Pending, "none yet")
			r.Note("Pocket ID "+app+" has no admin yet", command)
		}
	}
}

func adminCheckTitle(app string) string { return "check " + app + " for an admin" }

// errLine is err's first line, without the colon or full stop it ends on.
func errLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimRight(strings.TrimSpace(line), ":.")
}

// convergeAdminCheck asks a Pocket ID app whether any user is an admin, read
// only, with the API key, the site and the API address `app admin create`
// uses, reaching the host as every step of the run does, with its sudo. Why
// it fails is one line. Tests replace it.
var convergeAdminCheck = func(cfg *config.Config, app string, o convergeOptions) (bool, error) {
	// The admin check's own step is open, so the search reports nothing.
	where, err := pocketIDSite(ui.Discard, cfg, app, "", "apply")
	if err != nil {
		line, _, _ := strings.Cut(err.Error(), "\n")
		return false, &adminCheckFailure{strings.TrimPrefix(line, "apply: "), err}
	}
	key, err := pocketIDKey(cfg, o.Config, o.Secrets, app)
	if err != nil {
		return false, err
	}
	c := &pocketid.Client{Transport: adminTransport(siteTransport(where, cfg.Sites[where], "", o.Sudo)),
		BaseURL: pocketIDBase(cfg, where), APIKey: key}
	has, err := c.HasAdmin()
	if err != nil {
		// A host that ran curl and failed says so on the first line, the
		// command and its exit status, and why after it, as curl put it.
		lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
		return false, &adminCheckFailure{strings.TrimSpace(lines[len(lines)-1]), err}
	}
	return has, nil
}

// adminCheckFailure is why convergeAdminCheck failed, on one line, with the
// error in full as its cause.
type adminCheckFailure struct {
	reason string
	cause  error
}

func (f *adminCheckFailure) Error() string { return f.reason }
func (f *adminCheckFailure) Unwrap() error { return f.cause }

// convergeHost is how apply without --site reaches a site to settle its
// sudo: the transport every step builds for it, so the answer is the one
// they find. Tests replace it.
var convergeHost = func(name string, site config.Site, sudo bool) apply.Transport {
	return siteTransport(name, site, "", sudo)
}
