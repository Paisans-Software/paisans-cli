package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-cli/internal/ui"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/doctor"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/ownership"
	"github.com/paisans-software/paisans-cli/internal/patroni"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// doctorTransport is how doctor reaches a site. Tests replace it.
var doctorTransport = func(t apply.SSHTransport) apply.Transport { return t }

// doctorNow is the workstation's clock, for the clock check. Tests replace it.
var doctorNow = time.Now

// runDoctor reads every site, or the ones --site names, and prints what is
// stuck and how to recover. It changes nothing and has no --execute: see
// internal/doctor for why. Its checks are doctor.Checks' graph, run at once
// and printed as each ends. A site that does not answer is a finding, nothing
// more is asked of it, and every other check runs with the sites that did.
// It exits non zero when any finding is FAIL, so a script can tell a healthy
// deployment from one that needs a human.
//
// It takes no --ssh, like preflight: it reaches several sites, and one
// override cannot name them all.
func runDoctor(args []string) error {
	fs := newFlagSet("doctor", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	var only pathList
	fs.Var(&only, "site", "look at this site only (repeatable); every check then runs on the named sites alone")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since Docker and /srv are root's")
	execute := fs.Bool("execute", false, "remove the config: keys in "+config.FileName+" that only repeat a value gating requires, before reading any site; no site is changed")
	secretsPath := fs.String("secrets", "", "path to the secrets, which hold the DNS token the workstation's check uses (default: secrets.enc.yaml beside the config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("doctor takes flags only. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if *execute {
		if err := removeRedundantGateConfig(r, *configPath); err != nil {
			return err
		}
	}
	cfg, err := loadChecked(r, *configPath)
	if err != nil {
		return err
	}
	sites := cfg.SiteNames()
	if len(only) > 0 {
		for _, name := range only {
			if _, ok := cfg.Sites[name]; !ok {
				return fmt.Errorf("doctor: %s declares no site %q. Declared sites are %s", *configPath, name, strings.Join(cfg.SiteNames(), ", "))
			}
		}
		sites = only
	}
	transports := map[string]apply.Transport{}
	for _, name := range sites {
		transports[name] = newBoundedTransport(doctorTransport(siteTransport(name, cfg.Sites[name], "", *sudo)), doctorSiteCommands)
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	graph := doctor.Checks(cfg, sites, doctorReads{cfg: cfg, transports: transports, token: &workstationToken{path: *secretsPath}})
	report, _ := graph.Run(r, doctorAtOnce)
	report.ShowEnd(r)
	if report.Failed() {
		return fmt.Errorf("doctor: %d finding(s) marked FAIL", report.Count(doctor.Fail))
	}
	return nil
}

// doctorAtOnce is how many of doctor's checks run at once: 0, every one
// whose needs are met. Tests set it to one, to run them in the graph's
// order.
var doctorAtOnce = 0

// doctorSiteCommands is how many commands doctor runs on one site at once.
// Paisans shares one ssh connection per destination (internal/apply,
// sshmux.go), and sshd allows ten sessions on one connection by default
// (MaxSessions, sshd_config(5)); a command past that fails as if the host
// had refused it, which doctor would report as a failed check, so the
// bound stays under it. Sites are not bounded against each other: each is
// a machine of its own, since validate refuses two sites at one ssh
// destination.
var doctorSiteCommands = 8

// boundedTransport is a site's transport that runs at most as many
// commands at once as it has slots, each waiting for one.
type boundedTransport struct {
	apply.Transport
	slots chan struct{}
}

func newBoundedTransport(t apply.Transport, n int) boundedTransport {
	return boundedTransport{Transport: t, slots: make(chan struct{}, max(n, 1))}
}

func (b boundedTransport) take() (release func()) {
	b.slots <- struct{}{}
	return func() { <-b.slots }
}

func (b boundedTransport) Run(command string) (string, error) {
	defer b.take()()
	return b.Transport.Run(command)
}

func (b boundedTransport) RunInput(command, stdin string) (string, error) {
	defer b.take()()
	return b.Transport.RunInput(command, stdin)
}

func (b boundedTransport) ReadFile(path string) (string, bool, error) {
	defer b.take()()
	return b.Transport.ReadFile(path)
}

func (b boundedTransport) WriteFile(path, content string, mode uint32) error {
	defer b.take()()
	return b.Transport.WriteFile(path, content, mode)
}

// timed runs command with the workstation's clock read just before and just
// after it, once it holds its slot, so time spent waiting for one does not
// widen the round trip the clock check judges by.
func (b boundedTransport) timed(command string) (out string, before, after time.Time, err error) {
	defer b.take()()
	before = doctorNow()
	out, err = b.Transport.Run(command)
	after = doctorNow()
	return out, before, after, err
}

// doctorReads makes doctor's reads over each site's transport. Every
// command here is one of internal/doctor's read commands, patroni's
// ClusterCommand, apply.LookAtInstances' question, or the host check's
// inventory (hostcheck.Inspect); none writes.
type doctorReads struct {
	cfg        *config.Config
	transports map[string]apply.Transport
	// token is the workstation's DNS token, once Secrets has read it.
	token *workstationToken
}

// workstationToken is the DNS token from the secrets file. It stays here,
// out of doctor.Input, and is registered as a secret the moment it is read,
// so that no line doctor prints can carry it.
type workstationToken struct {
	path  string
	mu    sync.Mutex
	value string
}

func (w *workstationToken) get() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.value
}

// redact is text without the token.
func (w *workstationToken) redact(text string) string {
	if t := w.get(); t != "" {
		return strings.ReplaceAll(text, t, "[redacted]")
	}
	return text
}

// doctorDNSFor is dns.For, which tests replace with a provider held in
// memory, since no test may reach a real one.
var doctorDNSFor = dns.For

// doctorSiteToken is the token check on a site. A package variable so that
// a test can stand in for the provider.
var doctorSiteToken = func(ctx context.Context, check dns.TokenCheck, host dns.Runner, envFile string) dns.TokenResult {
	return check.OnSite(ctx, host, envFile)
}

// CaddyLog reads Caddy's certificate log lines over the site's transport,
// through sudo as every other read, since Docker is root's.
func (d doctorReads) CaddyLog(site string) doctor.CaddyLogProbe {
	out, err := d.transports[site].Run(doctor.CaddyLogCommand(d.cfg.Deployment()))
	p := doctor.CaddyLogProbe{Site: site, Out: out}
	if err != nil {
		p.Err = errText(out, err)
	}
	return p
}

// Secrets opens the secrets file as apply does, with the age key from the
// workstation's keystore or a SOPS_AGE_* variable.
func (d doctorReads) Secrets() doctor.SecretsProbe {
	p := doctor.SecretsProbe{Path: ui.ShortPath(d.token.path)}
	secrets, err := config.LoadDeploymentSecrets(d.token.path, d.cfg.ID)
	if err != nil {
		var problem *ui.Problem
		if errors.As(err, &problem) {
			p.Err = problem.Hint
			if problem.Explain != "" {
				p.More = strings.Split(problem.Explain, "\n")
			}
		} else {
			p.Err = firstOf(err.Error())
		}
		return p
	}
	token := secrets.External["acme_dns_token"]
	ui.Secret(token)
	d.token.mu.Lock()
	d.token.value = token
	d.token.mu.Unlock()
	return p
}

// SiteToken checks the token in the site's caddy.env, from the site, over
// its transport: the file is root's and 0600, so it is read through sudo
// as every other read here.
func (d doctorReads) SiteToken(site string) dns.TokenResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	check := dns.TokenCheck{Provider: d.cfg.ACME.Provider, Domain: d.cfg.Community.Domain, Place: site}
	return doctorSiteToken(ctx, check, d.transports[site], dns.CaddyEnvFile(d.cfg.Deployment()))
}

// WorkstationToken checks the secrets' token from this workstation, as dns
// init does before its first records call.
func (d doctorReads) WorkstationToken() dns.TokenResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := workstationTokenCheck(ctx, dns.TokenCheck{
		Provider: d.cfg.ACME.Provider,
		Domain:   d.cfg.Community.Domain,
		Place:    dns.WorkstationPlace,
		From:     "this workstation",
	}, d.token.get())
	r.Finding, r.Advice = d.token.redact(r.Finding), d.token.redact(r.Advice)
	return r
}

// DNSProvider is the provider's API with the secrets' token, for the
// records check, which only reads.
func (d doctorReads) DNSProvider() (dns.Provider, error) {
	p, err := doctorDNSFor(d.cfg.ACME.Provider, d.token.get())
	if err != nil {
		return nil, errors.New(d.token.redact(err.Error()))
	}
	return readOnlyProvider{p, d.token}, nil
}

// readOnlyProvider is the provider as doctor holds it: it refuses every
// write, since doctor changes nothing, and strips the token from every
// error, beside the provider's own redaction, so that a provider that
// echoed it could not put it in a finding.
type readOnlyProvider struct {
	dns.Provider
	token *workstationToken
}

func (r readOnlyProvider) err(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(r.token.redact(err.Error()))
}

func (r readOnlyProvider) LookupZone(ctx context.Context, name string) (string, bool, error) {
	id, found, err := r.Provider.LookupZone(ctx, name)
	return id, found, r.err(err)
}

func (r readOnlyProvider) Records(ctx context.Context, zoneID, name string) ([]dns.Record, error) {
	records, err := r.Provider.Records(ctx, zoneID, name)
	return records, r.err(err)
}

func (r readOnlyProvider) AllRecords(ctx context.Context, zoneID string) ([]dns.Record, error) {
	records, err := r.Provider.AllRecords(ctx, zoneID)
	return records, r.err(err)
}

func (readOnlyProvider) Create(context.Context, string, dns.Record) error {
	return errors.New("doctor writes nothing at the DNS provider")
}

func (readOnlyProvider) Delete(context.Context, string, string) error {
	return errors.New("doctor writes nothing at the DNS provider")
}

func (d doctorReads) Reach(site string) doctor.SiteReach {
	t := d.transports[site]
	r := doctor.SiteReach{Site: site, Destination: t.Describe()}
	if out, err := t.Run(doctor.ReachCommand); err != nil {
		r.Err = errText(out, err)
		r.Sudo = errors.Is(err, apply.ErrSudo)
	}
	return r
}

func (d doctorReads) Mesh(site string) doctor.MeshProbe {
	t := d.transports[site]
	p := doctor.MeshProbe{Site: site}
	if out, err := t.Run(mesh.LinkCommand(d.cfg.Deployment().Interface())); err != nil {
		p.LinkErr = errText(out, err)
	} else {
		p.Link = out
	}
	if probed, err := mesh.Probe(t); err != nil {
		p.ProbeErr = err.Error()
	} else {
		p.Probed = probed
	}
	return p
}

func (d doctorReads) read(site, command string) (string, string) {
	out, err := d.transports[site].Run(command)
	return out, errText(out, err)
}

func (d doctorReads) EtcdHealth(member string) (string, string) {
	return d.read(member, doctor.EtcdHealthCommand(d.cfg))
}

func (d doctorReads) EtcdVersion(member string) (string, string) {
	return d.read(member, doctor.EtcdVersionCommand)
}

func (d doctorReads) PatroniCluster(site string) (string, string) {
	api := fmt.Sprintf("%s:%d", d.cfg.Sites[site].Address, render.PatroniAPIPort)
	return d.read(site, patroni.ClusterCommand(d.cfg.Deployment(), api))
}

func (d doctorReads) Sync(member string) (string, string) {
	return d.read(member, doctor.SyncCommand(d.cfg.Deployment()))
}

func (d doctorReads) PatroniLog(site string) string {
	out, _ := d.transports[site].Run(doctor.PatroniLogCommand(d.cfg.Deployment()))
	return out
}

func (d doctorReads) Containers(site string) doctor.SiteContainers {
	return gatherContainers(d.cfg.Deployment(), site, d.transports[site])
}

// Settings asks the running copy of an app using the gate for the settings
// the gate leaves to it, with the read command its blueprint records.
func (d doctorReads) Settings(site, app string) doctor.SettingsProbe {
	declared := d.cfg.Apps[app]
	live, _ := blueprints.LiveSettingsFor(declared.Blueprint)
	var names []string
	for _, s := range blueprints.GateSettingsFor(declared) {
		names = append(names, s.Name)
	}
	p := doctor.SettingsProbe{Site: site, App: app}
	got, err := d.transports[site].Run(doctor.SettingsCommand(d.cfg.Deployment(), app, live.Service(), live.ReadCommand(names)))
	switch {
	case strings.Contains(got, doctor.NotRunningMarker):
		p.NotRunning = true
	case err != nil:
		p.Err = errText(got, err)
	default:
		p.Out = got
	}
	return p
}

// Standby asks Pocket ID only of the sites that answered: a site that did
// not is listed as unreachable rather than asked again.
func (d doctorReads) Standby(app string, reached map[string]bool) doctor.StandbyProbe {
	answering := map[string]apply.Transport{}
	for name := range reached {
		answering[name] = d.transports[name]
	}
	list := apply.LookAtInstances(d.cfg, app, answering)
	for i := range list {
		if list[i].State == apply.Unreachable && list[i].Detail == "no transport" {
			if _, asked := d.transports[list[i].Site]; asked {
				list[i].Detail = "did not answer ssh (see reach)"
			} else {
				list[i].Detail = "not asked (outside --site)"
			}
		}
	}
	return doctor.StandbyProbe{App: app, Instances: list}
}

func (d doctorReads) Clock(site string) doctor.ClockSample {
	t := d.transports[site]
	var out string
	var before, after time.Time
	var err error
	if b, ok := t.(boundedTransport); ok {
		out, before, after, err = b.timed(doctor.ClockCommand)
	} else {
		before = doctorNow()
		out, err = t.Run(doctor.ClockCommand)
		after = doctorNow()
	}
	sample := doctor.ClockSample{Site: site, Before: before, After: after, Out: out}
	if err != nil {
		sample.Err = errText(out, err)
	}
	return sample
}

func (d doctorReads) Leftovers(site string) doctor.LeftoverProbe {
	return gatherLeftovers(d.cfg, site, d.transports[site])
}

func (d doctorReads) Allowlist(site string) doctor.AllowlistProbe {
	t := d.transports[site]
	user := d.cfg.Sites[site].SSH.User
	p := doctor.AllowlistProbe{Site: site}
	content, found, err := t.ReadFile(hostprep.AllowPath(user))
	if err != nil {
		p.Err = err.Error()
	}
	p.Found, p.Content = found, content
	// Only a key host prepare would append is refused, so the warning
	// needs to know which keys authorized_keys holds already.
	if found {
		if fps, err := hostprep.AuthorizedFingerprints(t, user); err == nil {
			p.KeysRead, p.Authorized = true, fps
		}
	}
	return p
}

// gatherLeftovers takes one site's inventory with the host check's probes,
// all reads, and classifies what is left over and what relies on its Caddy.
func gatherLeftovers(cfg *config.Config, site string, t apply.Transport) doctor.LeftoverProbe {
	p := doctor.LeftoverProbe{Site: site}
	inv, err := hostcheck.Inspect(t, cfg.Deployment())
	if err != nil {
		p.Err = firstOf(err.Error())
		return p
	}
	if p.Report, err = ownership.Classify(cfg, site, inv, inv.ManifestFiles); err != nil {
		p.Err = err.Error()
	}
	return p
}

// gatherContainers lists one site's containers and looks closer at each one
// that is down: not running, or running unhealthy.
func gatherContainers(d deployment.Deployment, site string, t apply.Transport) doctor.SiteContainers {
	s := doctor.SiteContainers{Site: site}
	out, err := t.Run(doctor.ContainersCommand(d))
	if err != nil {
		s.PSErr = errText(out, err)
		return s
	}
	s.PS = out
	list, err := doctor.ParsePS(out)
	if err != nil {
		return s
	}
	for _, e := range list {
		if !e.Down() {
			continue
		}
		p := doctor.ContainerProbe{Entry: e}
		if out, err := t.Run(doctor.InspectCommand(e.Names)); err != nil {
			p.InspectErr = errText(out, err)
		} else {
			p.Inspect = out
		}
		p.Logs, _ = t.Run(doctor.LogCommand(e.Names))
		s.Down = append(s.Down, p)
	}
	return s
}

// errText is why a command failed: the last line it printed, which for a
// connection that never opened is ssh's own error and otherwise is usually
// the remote command's, or the error itself when nothing was printed.
func errText(out string, err error) string {
	if err == nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return firstOf(err.Error())
}

func firstOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// removeRedundantGateConfig is doctor --execute's one change: it takes out of
// paisans-stack.yaml every config: key that only repeats a value gating makes the
// template write (blueprints.RedundantGateConfig), which validate refuses as
// config-key-already-rendered. A key with any other value contradicts the
// gate, and changing it would change policy, so it is left for the operator.
// It runs before validate, which would otherwise refuse the file before
// doctor got this far, and it restores the file if the result does not load.
// It touches no site. See docs/specs/2026-10-12-gate-settings.md.
func removeRedundantGateConfig(r ui.Reporter, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		// A file that does not load is reported by loadChecked, next.
		return nil
	}
	keys := blueprints.RedundantGateConfig(cfg)
	if len(keys) == 0 {
		return nil
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r.Section(path)
	for _, k := range keys {
		st := r.Step(fmt.Sprintf("remove apps.%s.config.%s, which repeats what gating renders", k.App, k.Key))
		if err := config.RemoveConfigKey(path, k.App, k.Key); err != nil {
			st.Fail(err)
			return restore(path, original, err)
		}
		st.Done("")
	}
	if _, err := config.Load(path); err != nil {
		return restore(path, original, fmt.Errorf("the edited file no longer loads: %w", err))
	}
	return nil
}

// restore puts paisans-stack.yaml back as it was before doctor --execute edited it.
func restore(path string, original []byte, cause error) error {
	if err := os.WriteFile(path, original, 0o644); err != nil {
		return fmt.Errorf("%w; restoring %s also failed: %v", cause, path, err)
	}
	return fmt.Errorf("%w; %s is restored as it was", cause, path)
}
