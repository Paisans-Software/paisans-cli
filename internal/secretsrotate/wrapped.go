package secretsrotate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// dnsTokenPath is the pasted DNS token's path in the secrets file.
const dnsTokenPath = "external.acme_dns_token"

// pasted rotates a secret issued elsewhere: a DNS token or an SMTP password.
// No next value is made here: a person replaces it at its issuer, and the
// rotation hands the new value to every stack that renders it, one site at a
// time, each healthy before the next.
//
// The next value comes from one of two places. When the secrets file holds the
// value every host using it already runs, nothing new is in it, so stage 1
// asks the operator for the replacement at --execute (Asker), and it goes
// under rotations like a generated secret's next value. When the file holds a
// value some host does not run, it was replaced with `paisans secrets set`,
// and that value is the next one.
//
// Which stacks render it is found by rendering: the configuration is
// rendered with the file as it is and again with the secret replaced by a
// probe, and every file that differs carries it. That is the same rendering
// apply does, so it cannot name a stack apply would not hand the value to.
type pasted struct {
	path string
	// issuer is where a human replaces the secret.
	issuer func(cfg *config.Config) string
	// dns is whether stage 2 tries the next value against the zone, and
	// smtp whether it logs in to the mail server with it (smtpCheck).
	dns, smtp bool
}

// DNSToken is the rotation of external.acme_dns_token, issued by the DNS
// provider: every site whose Caddy is the toolkit's, the gateway and each
// monitor serving its own apps, renders it into caddy.env, so each is a
// scoped apply of the site's infrastructure stack, in which caddy.env changes
// and so recreates Caddy behind the gateway's gates, as any apply that
// changes it does (internal/apply/apply.go, isEnvironment and isRouting).
// Stage 2 tries the next token against the community's zone at the provider,
// read only, so a token that cannot see the zone reaches no Caddy.
func DNSToken() Rotator {
	return pasted{path: dnsTokenPath, issuer: func(cfg *config.Config) string { return cfg.ACME.Provider }, dns: true}
}

// SMTPPassword is the rotation of a password the mail provider issued:
// external.smtp_password, the deployment's, or apps.<app>.smtp_password, an
// app's own. Every app stack that sends mail with it renders it. Stage 2
// logs in to the mail server with the next password, from the machine the
// command runs on, and a pasted one also sends the operators a test
// message before it is taken (smtpCheck).
func SMTPPassword(path string) Rotator {
	return pasted{path: path, issuer: func(*config.Config) string { return "the mail provider" }, smtp: true}
}

func (p pasted) Path() string { return p.path }
func (p pasted) Cost() Cost   { return Cost{} }

// Issuer is where a person replaces the secret, Eg: the DNS provider.
func (p pasted) Issuer(cfg *config.Config) string { return p.issuer(cfg) }

// Next is the value the file holds, which `secrets set` put there. A run
// that asks for the next value does not call it.
func (p pasted) Next(e Env, s *config.Secrets) (string, error) {
	return p.current(e, s)
}

// current is the value the secrets file holds, registered as a secret.
func (p pasted) current(e Env, s *config.Secrets) (string, error) {
	v, _ := s.Get(p.path)
	if v == "" {
		return "", fmt.Errorf("the secrets file holds no %s. Set the one %s issued with `paisans secrets set %s`", p.path, p.issuer(e.Cfg), p.path)
	}
	ui.Secret(v)
	return v, nil
}

// Checked is where a value that does not work is stopped, before any host
// is handed it, and the error says why it failed: the DNS token is tried
// against the zone (dnsCheck), and an SMTP password logs in to the mail
// server and sends the operators a test message (smtpCheck). It is what a
// pasted value is tried with (Run.Answer).
func (p pasted) Checked(e Env, next string) error {
	switch {
	case p.dns:
		return dnsCheck(e, next)
	case p.smtp:
		return smtpCheck(e, p.path, next, true)
	}
	return nil
}

// Accepts is whether the next value works where it is checked. A token the
// provider refuses is not an error here but a stage still ahead, so a
// resumed run plans it and Checked says why. An SMTP password is only logged
// in with here: the plan reads it, and stage 2 reads it again, and neither
// sends mail. The one test message is sent where the value was given, by
// `secrets set` or when it was pasted. A server this machine cannot reach is
// accepted with a warning (smtpCheck).
func (p pasted) Accepts(e Env, next string) (bool, error) {
	switch {
	case p.dns:
		return dnsCheck(e, next) == nil, nil
	case p.smtp:
		return smtpCheck(e, p.path, next, false) == nil, nil
	}
	return true, nil
}

// CheckText is what the plan says stage 2 does for the secret.
func (p pasted) CheckText(cfg *config.Config) string {
	if p.smtp {
		return "the next password logs in to the mail server from this machine; a pasted one also sends the operators a test message when pasted, as `paisans secrets set` does. Each site tries it again before it is handed the password, and a server this machine cannot reach is tried from the sites alone"
	}
	if !p.dns {
		return "nothing checks it here: " + p.issuer(cfg) + " issued it"
	}
	if why := dnsUnchecked(cfg); why != "" {
		return "nothing checks it here: " + why
	}
	return fmt.Sprintf("the next token looks up %s's zone at %s, read only", dnsDomain(cfg), cfg.ACME.Provider)
}

// dnsTimeout bounds stage 2's lookup at the provider.
const dnsTimeout = time.Minute

// dnsUnchecked is why the DNS token cannot be tried here, or "".
func dnsUnchecked(cfg *config.Config) string {
	switch name := cfg.ACME.Provider; {
	case name == "":
		return "no DNS provider is declared"
	case !slices.Contains(dns.Implemented(), name):
		return name + " record management is not implemented"
	}
	return ""
}

func dnsDomain(cfg *config.Config) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.Community.Domain)), ".")
}

// dnsCheck tries token against the community's zone at the provider: the
// zone lookup dns init makes, which reads and changes nothing. A token that
// cannot see the zone cannot answer an ACME challenge in it either. A
// provider whose record management is not implemented is not checked.
func dnsCheck(e Env, token string) error {
	if dnsUnchecked(e.Cfg) != "" {
		return nil
	}
	providerFor := e.DNSProvider
	if providerFor == nil {
		providerFor = dns.For
	}
	ui.Secret(token)
	provider, err := providerFor(e.Cfg.ACME.Provider, token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	if _, err := dns.FindZone(ctx, provider, dnsDomain(e.Cfg), nil); err != nil {
		return fmt.Errorf("the DNS token does not work against %s: %v, so no host is handed it. Check its zone and permissions at %s", e.Cfg.ACME.Provider, err, e.Cfg.ACME.Provider)
	}
	return nil
}

// smtpCheck tries password as the SMTP password at path, from the machine
// the command runs on, for each sender that logs in with it, with send also
// sending the operators the test message (smtpcheck.TryPassword). A failure
// names the server's reply, never the password. Settings nothing can be
// tried from, Eg: a relay only the app's own network resolves, are not
// tried. A server this machine cannot reach is warned about and left
// untried, as `secrets set` leaves it: the scoped apply that hands the
// password to each site tries it from there first.
func smtpCheck(e Env, path, password string, send bool) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	tried, err := smtpcheck.TryPassword(e.Mail, e.Cfg, s, path, password, "paisans secrets rotate", send)
	if err != nil {
		var failed *smtpcheck.Error
		if errors.As(err, &failed) {
			return fmt.Errorf("the SMTP password does not work for %s: %w, so no app is handed it. Check it at the mail provider", failed.Check.Whose(), failed)
		}
		return err
	}
	for _, u := range tried.Unreached {
		c := u.Check
		e.warn(path+" is not tried for "+c.Whose()+" from "+c.From(),
			fmt.Sprintf("%s could not be reached from %s (%s), so %s goes on untried. Each site running %s tries it from there before it is handed the password.", c.Address(), c.From(), strings.TrimSuffix(ui.Redact(u.Reply()), "."), path, strings.TrimSuffix(c.Whose(), "'s mail")))
	}
	return nil
}

// Asks reports whether the next value has to be asked for: every host using
// the secret runs the value the file holds, so the file has nothing new to
// hand out. It is read per secret, not per file (hostsRun), so a carrying
// file that differs only elsewhere, Eg: a comment an upgrade changed, or an
// app setting not yet applied, does not pass for a replaced secret. One file
// whose carrying lines the host does not hold is enough to rotate from the
// file. A site with no copy of such a file was never applied, and what it
// holds cannot be told, so it is refused, whatever the other sites hold.
func (p pasted) Asks(e Env) (bool, error) {
	unchanged, _, err := p.hostsRun(e)
	return unchanged, err
}

// Promote makes next the file's own value. When the next value came from the
// file, it is the value already there, and this changes nothing.
func (p pasted) Promote(s *config.Secrets, next string) error {
	return s.Set(p.path, next)
}

// Users is every site that renders the secret, in site order, with the
// stacks there whose files carry it. One that cannot be worked out is a
// step that fails, so the rotation stops there rather than hand the value to
// nobody and promote it.
func (p pasted) Users(e Env) []Use {
	s, err := e.Store.Load()
	var files []carrier
	if err == nil {
		files, err = carriers(e.Cfg, s, p.path)
	}
	if err != nil {
		return []Use{{Site: "every site", Do: func(Env, *config.Secrets) error {
			return fmt.Errorf("finding what renders %s: %w", p.path, err)
		}}}
	}
	var out []Use
	for _, site := range e.Cfg.SiteNames() {
		var stacks []string
		for _, f := range files {
			if f.site == site && !slices.Contains(stacks, f.stack) {
				stacks = append(stacks, f.stack)
			}
		}
		if len(stacks) > 0 {
			out = append(out, Use{Site: site, Stacks: stacks})
		}
	}
	return out
}

// Pasted reports whether r rotates a pasted secret: the DNS token or an SMTP
// password.
func Pasted(r Rotator) bool {
	_, ok := r.(pasted)
	return ok
}

// HostsRun reports whether every host using the pasted secret r rotates runs
// the value the secrets file holds, read the way Asks reads it: each
// carrying line rendered here, and its hash looked for among the hashes of
// the lines of the host's copy of its file. It is what `secrets rotate
// --replaced` accepts a path on, since a secret replaced and applied before
// has nothing left to hand out. A rotator of a generated secret is an error.
func HostsRun(e Env, r Rotator) (bool, error) {
	if p, ok := r.(pasted); ok {
		ok, _, err := p.hostsRun(e)
		return ok, err
	}
	return false, fmt.Errorf("%s is not a pasted secret", r.Path())
}

// hostsRun is whether every host using the secret runs the file's value,
// and the sites read. Each carrying file is rendered here, and the lines
// that carry the secret are the ones a render with any other value changes
// (carriers). The host prints the SHA-256 of every line of its own copy of
// that file (lineHashes), so only hashes come back over ssh and nothing
// secret is on its command line. The host runs the value when each carrying
// line's hash is among them, whatever else in the file differs. A site with
// no copy of a carrying file is an error, since which value it holds cannot
// be told.
func (p pasted) hostsRun(e Env) (bool, []string, error) {
	s, err := e.Store.Load()
	if err != nil {
		return false, nil, err
	}
	// Registered as a secret, since an error from a host below may quote it.
	if _, err := p.current(e, s); err != nil {
		return false, nil, err
	}
	files, err := carriers(e.Cfg, s, p.path)
	if err != nil {
		return false, nil, err
	}
	if len(files) == 0 {
		return false, nil, fmt.Errorf("nothing the configuration renders carries %s, so no host uses it", p.path)
	}
	var sites []string
	changed := false
	for _, f := range files {
		t := e.Sites[f.site]
		if t == nil {
			return false, nil, fmt.Errorf("%s renders %s, and there is no way to reach it", f.site, p.path)
		}
		if !slices.Contains(sites, f.site) {
			sites = append(sites, f.site)
		}
		held, found, err := lineHashes(t, "/"+f.rel)
		if err != nil {
			return false, nil, fmt.Errorf("%s: reading the lines of %s: %w", f.site, f.rel, err)
		}
		if !found {
			return false, nil, fmt.Errorf("%s renders %s into %s and has no copy of it, so which value it holds cannot be told. Run `paisans apply --site %s` first", f.site, p.path, f.rel, f.site)
		}
		for _, line := range f.lines {
			if !held[line] {
				changed = true
			}
		}
	}
	return !changed, sites, nil
}

// lineHashesAbsent is what lineHashCommand prints for a file the host does
// not have.
const lineHashesAbsent = "absent"

// lineHashCommand prints the SHA-256 of each line of path, without its
// newline, one per line in sha256sum's form, or lineHashesAbsent when there
// is no such file. printf is the shell's own, so no line reaches an argv.
func lineHashCommand(path string) string {
	return fmt.Sprintf(`f=%s; if [ -f "$f" ]; then while IFS= read -r l || [ -n "$l" ]; do printf '%%s' "$l" | sha256sum; done < "$f"; else echo %s; fi`,
		"'"+strings.ReplaceAll(path, "'", `'\''`)+"'", lineHashesAbsent)
}

// lineHashes is the set of the SHA-256 of each line of path on t, and
// whether t has the file.
func lineHashes(t interface{ Run(string) (string, error) }, path string) (map[string]bool, bool, error) {
	out, err := t.Run(lineHashCommand(path))
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(out) == lineHashesAbsent {
		return nil, false, nil
	}
	held := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields[0]) != sha256.Size*2 {
			return nil, false, fmt.Errorf("unexpected answer: a line is not a SHA-256")
		}
		held[fields[0]] = true
	}
	return held, true, nil
}

// Carries reports whether anything cfg renders with s carries the secret at
// path: what `secrets rotate --all` reads to leave out a pasted secret that
// no stack uses, Eg: an SMTP password on a deployment that sends no mail.
func Carries(cfg *config.Config, s *config.Secrets, path string) (bool, error) {
	files, err := carriers(cfg, s, path)
	return len(files) > 0, err
}

// carrier is a rendered file that carries a secret: its site, its stack, its
// path relative to the host's /, and the SHA-256 of each of its lines that
// carries the secret, as rendered.
type carrier struct {
	site, stack, rel string
	lines            []string
}

// probeValue stands in for a secret to find the files that carry it. It is
// long and plain so that no renderer refuses it.
const probeValue = "paisans-rotate-probe-0123456789abcdef0123456789abcdef"

// carriers renders cfg with s and with the secret at path replaced by a
// probe, and returns every file whose content differs, in render's order.
// The lines that carry the secret are those of the first render the probe's
// lacks: found by rendering rather than by looking for the value, they are
// found however the template quotes or encodes it.
func carriers(cfg *config.Config, s *config.Secrets, path string) ([]carrier, error) {
	plan, err := render.Build(cfg, s)
	if err != nil {
		return nil, fmt.Errorf("rendering to find what carries %s: %w", path, err)
	}
	probe := cloneSecrets(s)
	if err := probe.Set(path, probeValue); err != nil {
		return nil, err
	}
	alt, err := render.Build(cfg, probe)
	if err != nil {
		return nil, fmt.Errorf("rendering to find what carries %s: %w", path, err)
	}
	other := map[string]string{}
	for _, f := range alt.Files {
		other[f.Path] = f.Content
	}
	var out []carrier
	for _, f := range plan.Files {
		if c, ok := other[f.Path]; ok && c == f.Content {
			continue
		}
		site, rel, _ := strings.Cut(f.Path, "/")
		_, stack, _, ok := deployment.SplitRel(rel)
		if !ok {
			return nil, fmt.Errorf("%s carries %s outside any stack, and no scoped apply could hand it on", f.Path, path)
		}
		probed := map[string]bool{}
		for _, line := range strings.Split(other[f.Path], "\n") {
			probed[line] = true
		}
		var lines []string
		for _, line := range strings.Split(f.Content, "\n") {
			if !probed[line] {
				digest := sha256.Sum256([]byte(line))
				lines = append(lines, hex.EncodeToString(digest[:]))
			}
		}
		if len(lines) == 0 {
			return nil, fmt.Errorf("%s carries %s, and no line of it does alone, so whether a host runs it cannot be read line by line", f.Path, path)
		}
		out = append(out, carrier{site: site, stack: stack, rel: rel, lines: lines})
	}
	return out, nil
}

// delegated is a rotation an existing command stages itself: the engine runs
// it in its place in the order and skips its own stages. Its Rotator methods
// for those stages are never called, and refuse when they are.
type delegated struct {
	path string
}

func (d delegated) Path() string { return d.path }
func (d delegated) Cost() Cost   { return Cost{} }
func (d delegated) Next(e Env, s *config.Secrets) (string, error) {
	return "", d.ownStages()
}
func (d delegated) Checked(e Env, next string) error { return d.ownStages() }
func (d delegated) Accepts(e Env, next string) (bool, error) {
	return false, d.ownStages()
}
func (d delegated) Users(e Env) []Use                            { return nil }
func (d delegated) Promote(s *config.Secrets, next string) error { return d.ownStages() }

func (d delegated) ownStages() error {
	return fmt.Errorf("%s is rotated by its own staged command, not by the stages of secrets rotate", d.path)
}

// oidcClient rotates an app's OIDC client secret.
type oidcClient struct {
	delegated
	app         string
	run, retire func(app string) error
}

// OIDCClient is the rotation of oidc_clients.<app>.client_secret. run is the
// existing `oidc client create --app <app> --rotate-secret --execute`, which
// records a new secret in the secrets file before Pocket ID is sent it, and
// leaves the old one valid. Then each site running the app is applied with
// the file as run left it, one at a time, each healthy before the next. That
// command renders nothing itself, so these applies are the only ones. Last,
// retire deletes every other secret of the client at Pocket ID
// (oidcclient.Retire), since an old secret that stays valid is a secret not
// rotated: it runs only once every site presents the new one, and only inside
// the --execute run the operator started. A run stopped part way is run again
// whole: the command adds another secret, and the retirement at the end
// deletes every one but that.
func OIDCClient(app string, run, retire func(app string) error) Rotator {
	return oidcClient{delegated: delegated{path: "oidc_clients." + app + ".client_secret"}, app: app, run: run, retire: retire}
}

// Precheck refuses an app that is not declared, runs nowhere, or has no
// client recorded: creating one is `oidc client create`'s, not a rotation.
func (r oidcClient) Precheck(e Env) error {
	if _, ok := e.Cfg.Apps[r.app]; !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if err := runsSomewhere(e, r.app); err != nil {
		return err
	}
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	id, _ := s.Get("oidc_clients." + r.app + ".client_id")
	secret, _ := s.Get(r.path)
	if id == "" || secret == "" {
		return fmt.Errorf("the secrets file records no OIDC client for %s, so there is no secret to rotate. `paisans oidc client create --app %s` creates one", r.app, r.app)
	}
	return nil
}

func (r oidcClient) RunWhole(e Env) error {
	if r.run == nil || r.retire == nil {
		return fmt.Errorf("no way to rotate %s's client secret at Pocket ID", r.app)
	}
	if err := r.run(r.app); err != nil {
		return err
	}
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	for _, u := range appUses(e, r.app) {
		if e.Apply == nil {
			return fmt.Errorf("no apply to hand %s the new client secret with", u.Site)
		}
		if err := e.Apply(u.Site, s, u.Stacks); err != nil {
			return fmt.Errorf("%s on %s: %w. The old client secret is still valid", r.app, u.Site, err)
		}
		if err := e.Healthy(u.Site, u.Stacks); err != nil {
			return fmt.Errorf("gate: %s on %s: %w. The old client secret is still valid", r.app, u.Site, err)
		}
	}
	return r.retire(r.app)
}

// appS3 rotates an app's Garage S3 key.
type appS3 struct {
	delegated
	app string
	run func(app string) error
}

// AppS3 is the rotation of an app's S3 key pair, apps.<app>.s3_access_key_id
// and apps.<app>.s3_secret_access_key, named by the secret half. run is the
// existing `storage rotate-key --app <app> --execute` (internal/rotatekey),
// which writes the new pair first, imports it beside the old one, switches
// every site running the app by a scoped apply behind apply's health gate,
// proves the new key, and deletes the old one from Garage. It resumes from
// the previous pair it leaves in the secrets file. Since it applies the
// app's sites itself, nothing is applied here.
func AppS3(app string, run func(app string) error) Rotator {
	return appS3{delegated: delegated{path: "apps." + app + ".s3_secret_access_key"}, app: app, run: run}
}

// Precheck refuses an app that is not declared or holds no S3 key.
func (r appS3) Precheck(e Env) error {
	if _, ok := e.Cfg.Apps[r.app]; !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	if id, _ := s.Get("apps." + r.app + ".s3_access_key_id"); id == "" {
		return fmt.Errorf("apps.%s holds no S3 key, so there is none to rotate", r.app)
	}
	return nil
}

func (r appS3) RunWhole(e Env) error {
	if r.run == nil {
		return fmt.Errorf("no way to rotate %s's S3 key", r.app)
	}
	return r.run(r.app)
}
