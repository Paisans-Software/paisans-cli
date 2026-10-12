package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// dnsTokenPath is the pasted DNS token's path in the secrets file.
const dnsTokenPath = "external.acme_dns_token"

// pasted rotates a secret issued elsewhere: a DNS token, an SMTP password, an
// OIDC client secret minted at Pocket ID by hand. No next value is made here.
// The operator replaces it at its issuer and sets it with `paisans secrets set
// <path>`, and the rotation hands the value the file then holds to every
// stack that renders it, one site at a time, each healthy before the next.
// The issuer checks it, and it already accepts the new one.
//
// Which stacks render it is found by rendering: the configuration is
// rendered with the file as it is and again with the secret replaced by a
// probe, and every file that differs carries it. That is the same rendering
// apply does, so it cannot name a stack apply would not hand the value to.
type pasted struct {
	path string
	// issuer is where a human replaces the secret, for the refusal.
	issuer func(cfg *config.Config) string
}

// DNSToken is the rotation of external.acme_dns_token, issued by the DNS
// provider: every site whose Caddy is the toolkit's, the gateway and each
// monitor serving its own apps, renders it into caddy.env, so each is a
// scoped apply of the site's infrastructure stack, in which caddy.env changes
// and so recreates Caddy behind the gateway's gates, as any apply that
// changes it does (internal/apply/apply.go, isEnvironment and isRouting).
func DNSToken() Rotator {
	return pasted{path: dnsTokenPath, issuer: func(cfg *config.Config) string { return cfg.ACME.Provider }}
}

// SMTPPassword is the rotation of a password the mail provider issued:
// external.smtp_password, the deployment's, or apps.<app>.smtp_password, an
// app's own. Every app stack that sends mail with it renders it.
func SMTPPassword(path string) Rotator {
	return pasted{path: path, issuer: func(*config.Config) string { return "the mail provider" }}
}

// pastedClient is a pasted OIDC client secret whose client's other secrets
// are retired at Pocket ID once every site presents the new one.
type pastedClient struct {
	pasted
	app    string
	retire func(app string) error
}

// PastedClient is the rotation of oidc_clients.<app>.client_secret for an
// app whose client `oidc client create` cannot build, so cannot rotate: the
// operator mints a new secret at Pocket ID and sets it with `paisans secrets
// set`, and the rotation hands it to every site running the app. retire,
// when not nil, deletes the client's other secrets at Pocket ID
// (oidcclient.Retire) after every site presents the new one, since an old
// secret that stays valid is a secret not rotated; it must read the file
// again for the secret to keep. Without a recorded client ID there is no
// client to retire by, and retire is nil.
func PastedClient(app string, retire func(app string) error) Rotator {
	p := pasted{path: "oidc_clients." + app + ".client_secret", issuer: func(*config.Config) string { return "Pocket ID" }}
	if retire == nil {
		return p
	}
	return pastedClient{pasted: p, app: app, retire: retire}
}

// Retire deletes the client's other secrets at Pocket ID.
func (r pastedClient) Retire(e Env) error { return r.retire(r.app) }

func (p pasted) Path() string { return p.path }
func (p pasted) Cost() Cost   { return Cost{} }

// Next is the value already in the file, which `secrets set` put there.
func (p pasted) Next(e Env, s *config.Secrets) (string, error) {
	v, _ := s.Get(p.path)
	if v == "" {
		return "", fmt.Errorf("the secrets file holds no %s. Set the one %s issued with `paisans secrets set %s`", p.path, p.issuer(e.Cfg), p.path)
	}
	return v, nil
}

// Checked has nothing to do: the issuer checks the secret, and it issued
// the new one before it was set.
func (pasted) Checked(e Env, next string) error         { return nil }
func (pasted) Accepts(e Env, next string) (bool, error) { return true, nil }

// Promote leaves the file as it is: the next value is the file's own, set
// before the rotation began.
func (pasted) Promote(s *config.Secrets, next string) error { return nil }

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

// Precheck refuses a secret that has not been replaced since the hosts were
// last applied: rotating it would restart every stack using it for nothing.
// Each file carrying it is rendered on the workstation from the file, and its
// hash compared with the one its site's manifest records for it, so only the
// manifests are read over ssh and nothing secret crosses it. One file
// rendered with another value is enough to rotate. A site with no record of
// such a file was never applied, and what it holds cannot be told, so it is
// refused too, whatever the other sites hold.
func (p pasted) Precheck(e Env) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	if _, err := p.Next(e, s); err != nil {
		return err
	}
	files, err := carriers(e.Cfg, s, p.path)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nothing the configuration renders carries %s, so no host uses it", p.path)
	}
	d := e.Cfg.Deployment()
	var sites []string
	changed := false
	recorded := map[string]map[string]string{}
	for _, f := range files {
		if recorded[f.site] == nil {
			t := e.Sites[f.site]
			if t == nil {
				return fmt.Errorf("%s renders %s, and there is no way to reach it", f.site, p.path)
			}
			content, found, err := t.ReadFile(d.Manifest())
			if err != nil {
				return fmt.Errorf("reading %s's manifest: %w", f.site, err)
			}
			var m render.Manifest
			if found {
				if err := json.Unmarshal([]byte(content), &m); err != nil {
					return fmt.Errorf("%s's %s is not readable as a manifest: %w", f.site, d.Manifest(), err)
				}
			}
			recorded[f.site] = map[string]string{}
			for _, mf := range m.Files {
				recorded[f.site][mf.Path] = mf.SHA256
			}
			sites = append(sites, f.site)
		}
		had := recorded[f.site][f.rel]
		if had == "" {
			return fmt.Errorf("%s renders %s into %s and has no record of an apply writing it, so which value it holds cannot be told. Run `paisans apply --site %s` first", f.site, p.path, f.rel, f.site)
		}
		if had != f.sha {
			changed = true
		}
	}
	if changed {
		return nil
	}
	return fmt.Errorf("%s is the value every host using it was last applied with (%s), so rotating it would restart them for nothing. Replace it at %s, set the new one with `paisans secrets set %s`, then rotate it", p.path, strings.Join(sites, ", "), p.issuer(e.Cfg), p.path)
}

// Carries reports whether anything cfg renders with s carries the secret at
// path: what `secrets rotate --all` reads to leave out a pasted secret that
// no stack uses, Eg: an SMTP password on a deployment that sends no mail.
func Carries(cfg *config.Config, s *config.Secrets, path string) (bool, error) {
	files, err := carriers(cfg, s, path)
	return len(files) > 0, err
}

// carrier is a rendered file that carries a secret: its site, its stack, its
// path relative to the host's / and the hash of its content as rendered.
type carrier struct{ site, stack, rel, sha string }

// probeValue stands in for a secret to find the files that carry it. It is
// long and plain so that no renderer refuses it.
const probeValue = "paisans-rotate-probe-0123456789abcdef0123456789abcdef"

// carriers renders cfg with s and with the secret at path replaced by a
// probe, and returns every file whose content differs, in render's order.
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
		digest := sha256.Sum256([]byte(f.Content))
		out = append(out, carrier{site: site, stack: stack, rel: rel, sha: hex.EncodeToString(digest[:])})
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
