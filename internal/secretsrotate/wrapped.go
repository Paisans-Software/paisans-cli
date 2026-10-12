package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// dnsTokenPath is the pasted DNS token's path in the secrets file.
const dnsTokenPath = "external.acme_dns_token"

// dnsToken rotates the DNS token every toolkit Caddy obtains certificates
// with.
type dnsToken struct{}

// DNSToken is the rotation of external.acme_dns_token. The token is issued by
// the DNS provider, not generated, so no next value is made here: the
// operator replaces it at the provider and sets it with `paisans secrets set
// external.acme_dns_token`, and the rotation hands the value the file then
// holds to every site whose Caddy is the toolkit's, the gateway and each
// monitor serving its own apps. Each is a scoped apply of the site's
// infrastructure stack, in which caddy.env changes and so recreates Caddy
// behind the gateway's gates, as any apply that changes it does
// (internal/apply/apply.go, isEnvironment and isRouting). The provider checks
// the token, and it already accepts the new one.
func DNSToken() Rotator { return dnsToken{} }

func (dnsToken) Path() string { return dnsTokenPath }
func (dnsToken) Cost() Cost   { return Cost{} }

// Next is the value already in the file, which `secrets set` put there.
func (dnsToken) Next(e Env, s *config.Secrets) (string, error) {
	token := s.External["acme_dns_token"]
	if token == "" {
		return "", fmt.Errorf("the secrets file holds no DNS token. Set the one the provider issued with `paisans secrets set %s`", dnsTokenPath)
	}
	return token, nil
}

// Checked has nothing to do: the provider checks the token, and it issued
// the new one before it was set.
func (dnsToken) Checked(e Env, next string) error         { return nil }
func (dnsToken) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is every site running the toolkit's Caddy, in site order.
func (dnsToken) Users(e Env) []Use {
	var out []Use
	for _, site := range e.Cfg.CaddySites() {
		out = append(out, Use{Site: site, Stacks: []string{"infra"}})
	}
	return out
}

// Promote leaves the file as it is: the next value is the file's own, set
// before the rotation began.
func (dnsToken) Promote(s *config.Secrets, next string) error { return nil }

// Precheck refuses a token that has not been replaced since the hosts were
// last applied: rotating it would recreate every Caddy for nothing. Each
// Caddy site's caddy.env is rendered on the workstation from the file, and
// its hash compared with the one the site's manifest records for it, so only
// the manifest is read over ssh and nothing secret crosses it. One site
// rendered with another value is enough to rotate. A Caddy site with no
// record of caddy.env was never applied, and what its Caddy holds cannot be
// told, so it is refused too, whatever the other sites hold.
func (r dnsToken) Precheck(e Env) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	if _, err := r.Next(e, s); err != nil {
		return err
	}
	sites := e.Cfg.CaddySites()
	if len(sites) == 0 {
		return fmt.Errorf("no site runs the toolkit's Caddy, so nothing uses the DNS token")
	}
	plan, err := render.Build(e.Cfg, s)
	if err != nil {
		return fmt.Errorf("rendering caddy.env to compare: %w", err)
	}
	d := e.Cfg.Deployment()
	rel := d.RelPath("infra", "caddy", "caddy.env")
	rendered := map[string]string{}
	for _, f := range plan.Files {
		for _, site := range sites {
			if f.Path == site+"/"+rel {
				digest := sha256.Sum256([]byte(f.Content))
				rendered[site] = hex.EncodeToString(digest[:])
			}
		}
	}
	changed := false
	for _, site := range sites {
		want, ok := rendered[site]
		if !ok {
			return fmt.Errorf("%s runs the toolkit's Caddy and renders no %s", site, rel)
		}
		t := e.Sites[site]
		if t == nil {
			return fmt.Errorf("%s runs the toolkit's Caddy, and there is no way to reach it", site)
		}
		content, found, err := t.ReadFile(d.Manifest())
		if err != nil {
			return fmt.Errorf("reading %s's manifest: %w", site, err)
		}
		var m render.Manifest
		if found {
			if err := json.Unmarshal([]byte(content), &m); err != nil {
				return fmt.Errorf("%s's %s is not readable as a manifest: %w", site, d.Manifest(), err)
			}
		}
		recorded := ""
		for _, f := range m.Files {
			if f.Path == rel {
				recorded = f.SHA256
			}
		}
		if recorded == "" {
			return fmt.Errorf("%s runs the toolkit's Caddy and has no record of an apply writing its caddy.env, so which token it holds cannot be told. Run `paisans apply --site %s` first", site, site)
		}
		if recorded != want {
			changed = true
		}
	}
	if changed {
		return nil
	}
	return fmt.Errorf("%s is the value every Caddy was last applied with (%s), so rotating it would recreate them for nothing. Replace the token at %s, set the new one with `paisans secrets set %s`, then rotate it", dnsTokenPath, strings.Join(sites, ", "), e.Cfg.ACME.Provider, dnsTokenPath)
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
