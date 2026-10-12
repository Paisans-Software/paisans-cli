package secretsrotate

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// A secret an app checks itself is checked by the stack that presents it: the
// app reads it from its own .env or configuration file when its containers
// start, and so does the part of the same stack that verifies it (valkey
// takes --requirepass from its command line at each recreate, and MAS and the
// homeserver are both in the synapse app's stack, which restarts together).
// So there is no checking side to change before the users: stage 2 has
// nothing to do, and stage 3's scoped apply is the whole of the rotation. It
// goes one site at a time, each healthy before the next, so with cluster
// placement the second site runs the old value while the first is checked.

// AppSelfKind is the kind whose secret key is, for each key AppSelf rotates,
// and false for any other key: what `secrets rotate` reads to find, in a
// deployment's apps, the ones whose secret it is. Each kind's record names
// its keys (kinds.Def.SelfRotated).
func AppSelfKind(key string) (config.Kind, bool) { return kinds.SelfRotatedKind(key) }

// mbinRestart is the downtime of a rotation that restarts the Mbin stack.
const mbinRestart = "the Mbin stack, for one restart on each site in turn"

// AppSelfCost is what rotating key costs the members, for each key AppSelf
// rotates; a key it does not rotate costs nothing here, and AppSelf refuses
// it at Precheck.
//
//   - Mbin app_secret signs members out. It is Symfony's framework.secret
//     (config/packages/framework.yaml, `secret: '%env(APP_SECRET)%'`), and the
//     remember me cookie is signed with it (config/packages/security.yaml,
//     `remember_me: secret: '%kernel.secret%'`, lifetime four months), read in
//     the fork's checkout after its v1.13.3+paisans tag. Sessions themselves
//     are in Postgres (PdoSessionHandler, framework.yaml) and are not signed
//     with it, so a member's open session lasts until it ends, and their four
//     month sign in does not.
//   - oauth2-proxy cookie_secret signs members out. The kind renders no
//     session store, so oauth2-proxy keeps the whole session in the cookie,
//     encrypted with the cookie secret (oauth2-proxy documentation,
//     Configuration, Session Storage, "Cookie Storage", taken from that
//     documentation and not fetched for this); a cookie sealed with the old
//     secret no longer opens.
//   - uptime session_secret signs members out. The fork passes it to
//     express-session as `secret` (src/auth.js, sessionMiddleware, and
//     src/config.js, sessionSecret, read in the local checkout at
//     1.1.0-oidc.3), which signs the uptime.sid cookie; a cookie signed with
//     the old secret is refused. It keeps sessions in memory, so the restart
//     ends them as well.
//   - MAS mas_signing_key and mas_matrix_secret sign members out, the
//     stricter reading. MAS's configuration reference says the keys under
//     secrets.keys sign the tokens it issues and matrix.secret authenticates
//     it to the homeserver; that reference does not say whether Matrix
//     sessions survive either changing, and it has not been verified against
//     a running MAS. Marking them only adds a confirmation.
//   - Mbin mercure_jwt_secret and valkey_password sign nobody out: the hub
//     and the cache hold no session (sessions are in Postgres, above). Their
//     cost is a restart of the Mbin stack on each site, as the spec's cost
//     table gives it.
//   - invite session_key signs members out of the invite service and nothing
//     else: it signs the service's sign-in cookie
//     (docs/specs/2026-10-12-invite-service.md, Secrets), so a cookie signed
//     with the old key is refused.
//   - Pocket ID static_api_key signs nobody out: its users are the toolkit,
//     the admin reconciler and every invite service whose identity_app names
//     the Pocket ID app, not members (secretsgen, appSecretKeys). Each of
//     those invite stacks is handed the next key with it (see Users).
func AppSelfCost(key string) Cost {
	switch key {
	case "app_secret", "cookie_secret", "session_secret", "session_key", "mas_signing_key", "mas_matrix_secret":
		return Cost{SignsOut: true}
	case "mercure_jwt_secret", "valkey_password":
		return Cost{Downtime: mbinRestart}
	}
	return Cost{}
}

// appSelf rotates one secret an app checks itself.
type appSelf struct {
	Generated
	app, key string
	cost     Cost
}

// AppSelf is the rotation of apps.<app>.<key>, a secret checked by the same
// stack that uses it: Mbin's app_secret, mercure_jwt_secret and
// valkey_password, oauth2-proxy's cookie_secret, uptime's session_secret,
// the invite service's session_key, MAS's mas_signing_key and
// mas_matrix_secret, and Pocket ID's static_api_key. cost is what the plan says it costs; AppSelfCost has each
// one, with its source.
func AppSelf(app, key string, cost Cost) Rotator {
	return appSelf{Generated: Generated{SecretPath: "apps." + app + "." + key}, app: app, key: key, cost: cost}
}

func (r appSelf) Cost() Cost { return r.cost }

// Precheck refuses a key that is not one of the app's own, and an app that
// runs on no site, whose rotation would hand the value to nobody.
func (r appSelf) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if _, ok := oauthKey[r.key]; ok {
		return fmt.Errorf("%s encrypts or opens Mbin's OAuth2 keypair, and is rotated with the rest of it as apps.%s.oauth", r.Path(), r.app)
	}
	kind, ok := AppSelfKind(r.key)
	if !ok {
		return fmt.Errorf("%s is not a secret its app checks itself", r.Path())
	}
	if app.Kind != kind {
		return fmt.Errorf("apps.%s is %s, and %s is a secret of %s", r.app, app.Kind, r.key, kind)
	}
	return runsSomewhere(e, r.app)
}

// Checked has nothing to do: the stack that checks the secret is the one
// stage 3 hands it to.
func (r appSelf) Checked(e Env, next string) error { return nil }

// Accepts is true: the app accepts whatever it is started with.
func (r appSelf) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is every site running the app, its own stack on each. Pocket ID's
// static_api_key is also presented by every invite app whose identity_app
// names it, from that app's .env, so each of those stacks is handed the next
// key too: on a site running both, in the same step with no order between the
// two stacks guaranteed; on a site running only the invite app, in a step of
// its own. The invite service calls Pocket ID only when it serves a request,
// so the window in which it holds a key Pocket ID does not yet accept is
// seconds. Without them the invite service would keep calling with a key
// Pocket ID no longer accepts.
func (r appSelf) Users(e Env) []Use {
	uses := appUses(e, r.app)
	if r.key != "static_api_key" {
		return uses
	}
	sites := render.AppSites(e.Cfg)
	bySite := map[string]int{}
	for i, u := range uses {
		bySite[u.Site] = i
	}
	for _, name := range e.Cfg.AppNames() {
		app := e.Cfg.Apps[name]
		if app.Kind != config.KindInvite {
			continue
		}
		if idp, _ := app.Settings["identity_app"].(string); idp != r.app {
			continue
		}
		for _, site := range sites[name] {
			if i, ok := bySite[site]; ok {
				uses[i].Stacks = append(uses[i].Stacks, name)
				continue
			}
			bySite[site] = len(uses)
			uses = append(uses, Use{Site: site, Stacks: []string{name}})
		}
	}
	return uses
}

// appUses is every site running app, as render places it (render.AppSites:
// its pinned site, or every apps site), its own stack on each, one at a time.
func appUses(e Env, app string) []Use {
	var out []Use
	for _, site := range render.AppSites(e.Cfg)[app] {
		out = append(out, Use{Site: site, Stacks: []string{app}})
	}
	return out
}

func runsSomewhere(e Env, app string) error {
	if len(render.AppSites(e.Cfg)[app]) == 0 {
		return fmt.Errorf("apps.%s runs on no site, so no stack would be handed the next value", app)
	}
	return nil
}

// oauthKeys are the four values of Mbin's OAuth2 unit, in the order its next
// value holds them.
var oauthKeys = []string{"oauth_passphrase", "oauth_encryption_key", "oauth_private_key", "oauth_public_key"}

var oauthKey = map[string]struct{}{
	"oauth_passphrase": {}, "oauth_encryption_key": {}, "oauth_private_key": {}, "oauth_public_key": {},
}

// oauthUnit is the next value of Mbin's OAuth2 unit.
type oauthUnit struct {
	Passphrase    string `yaml:"oauth_passphrase"`
	EncryptionKey string `yaml:"oauth_encryption_key"`
	PrivateKey    string `yaml:"oauth_private_key"`
	PublicKey     string `yaml:"oauth_public_key"`
}

func (u oauthUnit) values() map[string]string {
	return map[string]string{
		"oauth_passphrase":     u.Passphrase,
		"oauth_encryption_key": u.EncryptionKey,
		"oauth_private_key":    u.PrivateKey,
		"oauth_public_key":     u.PublicKey,
	}
}

// parseOAuthUnit reads a next value and registers each of its values as a
// secret, so that none of them is shown. Its errors name no value.
func parseOAuthUnit(next string) (oauthUnit, error) {
	var u oauthUnit
	dec := yaml.NewDecoder(bytes.NewReader([]byte(next)))
	dec.KnownFields(true)
	if err := dec.Decode(&u); err != nil {
		return oauthUnit{}, fmt.Errorf("the next value is not Mbin's four OAuth2 values")
	}
	values := u.values()
	for _, key := range oauthKeys {
		v := values[key]
		if v == "" {
			return oauthUnit{}, fmt.Errorf("the next value has no %s", key)
		}
		ui.Secret(v)
	}
	return u, nil
}

// check refuses a unit Mbin could not use: a private key the passphrase does
// not open, or a public key that is not its half.
func (u oauthUnit) check() error {
	key, err := secretsgen.OpenOAuthPrivateKey(u.PrivateKey, u.Passphrase)
	if err != nil {
		return fmt.Errorf("the next OAuth2 private key does not open with the next passphrase, and Mbin could not sign a token with it")
	}
	block, _ := pem.Decode([]byte(u.PublicKey))
	if block == nil {
		return fmt.Errorf("the next OAuth2 public key is not PEM")
	}
	public, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("the next OAuth2 public key does not parse: %w", err)
	}
	if pub, ok := public.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return fmt.Errorf("the next OAuth2 public key is not the next private key's half")
	}
	return nil
}

// mbinOAuth rotates Mbin's OAuth2 server unit: oauth_passphrase,
// oauth_encryption_key and the keypair, as one.
type mbinOAuth struct{ app string }

// MbinOAuth is the rotation of Mbin's OAuth2 server unit: the passphrase, the
// encryption key and the keypair the passphrase encrypts, replaced together,
// because a private key the passphrase beside it does not open is an Mbin
// that cannot sign a token. Its path, apps.<app>.oauth, names the unit, not a
// key in the secrets file: it is the key of the unit's next value under
// rotations, a small YAML document holding all four values, and Promote sets
// all four keys from it. Every site running the app is handed the unit in
// turn, and all of them end on the same pair, so a token one site issues
// verifies on another.
//
// It signs members out, every API client and app at once: league's
// oauth2-server bundle signs the access tokens with the private key and
// encrypts the authorization codes and refresh tokens with the encryption key
// (config/packages/league_oauth2_server.yaml, private_key,
// private_key_passphrase and encryption_key, read in the fork's checkout after
// its v1.13.3+paisans tag), so nothing issued under the old unit verifies or
// opens under the new one.
func MbinOAuth(app string) Rotator { return mbinOAuth{app: app} }

func (r mbinOAuth) Path() string { return "apps." + r.app + ".oauth" }

func (r mbinOAuth) Cost() Cost { return Cost{SignsOut: true} }

func (r mbinOAuth) key(k string) string { return "apps." + r.app + "." + k }

func (r mbinOAuth) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if app.Kind != config.KindMbin {
		return fmt.Errorf("apps.%s is %s, and only Mbin runs an OAuth2 server", r.app, app.Kind)
	}
	return runsSomewhere(e, r.app)
}

// Next is a new passphrase and encryption key from secretsgen.Replacement,
// and a new keypair encrypted with that passphrase.
func (r mbinOAuth) Next(e Env, s *config.Secrets) (string, error) {
	pass, err := secretsgen.Replacement(r.key("oauth_passphrase"), s)
	if err != nil {
		return "", err
	}
	ui.Secret(pass)
	enc, err := secretsgen.Replacement(r.key("oauth_encryption_key"), s)
	if err != nil {
		return "", err
	}
	ui.Secret(enc)
	priv, pub, err := secretsgen.ReplacementOAuthKeypair(pass)
	if err != nil {
		return "", err
	}
	ui.Secret(priv)
	ui.Secret(pub)
	out, err := yaml.Marshal(oauthUnit{Passphrase: pass, EncryptionKey: enc, PrivateKey: priv, PublicKey: pub})
	if err != nil {
		return "", fmt.Errorf("writing the next OAuth2 unit")
	}
	return string(out), nil
}

// Checked has nothing to do: Mbin checks its own tokens, with the unit stage
// 3 hands it.
func (r mbinOAuth) Checked(e Env, next string) error { return nil }

func (r mbinOAuth) Accepts(e Env, next string) (bool, error) { return true, nil }

func (r mbinOAuth) Users(e Env) []Use { return appUses(e, r.app) }

// Promote sets the four keys from the unit, after checking that the private
// key opens with the passphrase and the public key is its half. Setting a
// value twice changes nothing, so it is safe to run twice.
func (r mbinOAuth) Promote(s *config.Secrets, next string) error {
	u, err := parseOAuthUnit(next)
	if err != nil {
		return err
	}
	if err := u.check(); err != nil {
		return err
	}
	values := u.values()
	for _, k := range oauthKeys {
		if err := s.Set(r.key(k), values[k]); err != nil {
			return fmt.Errorf("%s: setting it", r.key(k))
		}
	}
	return nil
}
