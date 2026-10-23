// Package secretsgen fills in the secrets a deployment needs and never invents
// the ones it cannot.
//
// Three kinds of secret live in one file, and only one of them belongs here.
// Generated secrets are made by this package, never typed and never seen: a
// WireGuard private key per site, the cluster's three role passwords, a
// database password per app, and Garage's keys. Pasted secrets are issued by
// somebody else, a DNS token being the example, and captured secrets are minted
// by a running service, an OIDC client secret being the example. This package
// leaves both alone and reports them as missing so an operator knows what is
// still owed.
//
// The rule that matters most here is that nothing already set is ever
// replaced. Regenerating a WireGuard key silently breaks every peer that
// trusted the old one; regenerating a database password leaves an application
// unable to reach a database whose role still has the old one. `init` is
// therefore safe to re-run, and re-running it is the intended way to fill in a
// site or an app added to the configuration later.
package secretsgen

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"regexp"
	"sort"

	"golang.org/x/crypto/curve25519"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// garageKeyIDPattern and garageSecretPattern are the only shapes Garage
// accepts for an S3 access key ID and secret key, established by running
// dxflrs/garage:v1.0.1.
var (
	garageKeyIDPattern  = regexp.MustCompile(`^GK[0-9a-f]{24}$`)
	garageSecretPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Result records what a generation pass did, by name and never by value. A
// secret that reaches a terminal has been written to a scrollback buffer,
// a screen recording, or a terminal multiplexer's log.
type Result struct {
	// Generated names the secrets this pass created, in sorted order.
	Generated []string
	// Kept names the secrets that already had a value and were left alone.
	Kept []string
	// Owed names secrets only the operator can provide, because somebody
	// outside the deployment issues them: a DNS token, a mail password, a
	// backup provider's keys. Each is something to do.
	Owed []Owed
	// Pending names secrets a running service of the deployment issues, which
	// apply records before the app that needs one starts. None is the
	// operator's to do, so none is in Owed. A pending secret is a key absent
	// from the file, never a placeholder, so render still reads the file as
	// it is.
	Pending []Pending
}

// Owed is a secret only the operator can provide, with the reason it cannot
// be generated here and who issues it.
type Owed struct {
	Name string
	Why  string
}

// Pending is a secret apply obtains from a running service and records
// itself. What is the kind of thing it is, the same for every pending secret
// one service issues (Eg: "sign in client"), so a report can name them
// together; App is the app that needs it; Why says what apply makes, for an
// operator checking an existing one.
type Pending struct {
	Name string
	What string
	App  string
	Why  string
}

// Changed reports whether anything was generated, so a caller can say "nothing
// to do" rather than claiming to have written a file it did not change.
func (r Result) Changed() bool { return len(r.Generated) > 0 }

// Fill generates every missing generated secret for a configuration, in place.
//
// It is deliberately driven by the configuration rather than by what the file
// already contains: a site or an app added to paisans-stack.yaml is a secret owed,
// and the whole point of re-running init is to notice that.
func Fill(cfg *config.Config, secrets *config.Secrets) (Result, error) {
	var result Result
	note := func(created bool, name string) {
		if created {
			result.Generated = append(result.Generated, name)
			return
		}
		result.Kept = append(result.Kept, name)
	}

	if secrets.Version == 0 {
		secrets.Version = config.SecretsVersion
	}

	// The cluster's three roles. Spilo ships published defaults for all three
	// (zalando, cola, standby), so leaving any unset is worse than it looks.
	created, err := fillString(&secrets.Cluster.SuperuserPassword)
	if err != nil {
		return result, err
	}
	note(created, "cluster.superuser_password")

	created, err = fillString(&secrets.Cluster.AdminPassword)
	if err != nil {
		return result, err
	}
	note(created, "cluster.admin_password")

	created, err = fillString(&secrets.Cluster.StandbyPassword)
	if err != nil {
		return result, err
	}
	note(created, "cluster.standby_password")

	// Garage's own two credentials, and only one of them is an opaque string.
	// admin_token is unconstrained: Garage takes it as given, and a base64
	// password starts fine. rpc_secret is not. Garage parses it as a hex
	// encoded 32 byte key and refuses anything else at startup with "Invalid
	// RPC secret key: expected 32 bits of entropy", which is a node that never
	// comes up rather than a node that comes up wrong. Established by booting
	// dxflrs/garage:v1.0.1 against this toolkit's own rendered garage.toml;
	// internal/garage's integration suite boots the rendered file so that a
	// future change here cannot quietly go back to base64.
	created, err = fillString(&secrets.Storage.Garage.AdminToken)
	if err != nil {
		return result, err
	}
	note(created, "storage.garage.admin_token")

	created, err = fillHex(&secrets.Storage.Garage.RPCSecret)
	if err != nil {
		return result, err
	}
	note(created, "storage.garage.rpc_secret")

	if cfg.Backup.On() {
		// The restic repository's password and WAL-G's libsodium key, which
		// encrypt every backup in every destination. Generated, never seen:
		// a restore reads them from this file.
		created, err = fillString(&secrets.Backup.ResticPassword)
		if err != nil {
			return result, err
		}
		note(created, "backup.restic_password")
		// 32 bytes as hex, which WAL-G reads with
		// WALG_LIBSODIUM_KEY_TRANSFORM=hex (wal-g docs/README.md,
		// "Encryption", at v3.0.9).
		created, err = fillHex(&secrets.Backup.WALGLibsodiumKey)
		if err != nil {
			return result, err
		}
		note(created, "backup.walg_libsodium_key")
	}

	// One WireGuard identity per site, kept in the file rather than on the
	// host, so rebuilding a dead machine restores the same identity and no
	// peer is reconfigured.
	if secrets.Sites == nil {
		secrets.Sites = map[string]config.SiteSecrets{}
	}
	for _, name := range cfg.SiteNames() {
		site := secrets.Sites[name]
		if site.WireGuardPrivateKey != "" {
			note(false, "sites."+name+".wireguard_private_key")
		} else {
			key, err := wireGuardPrivateKey()
			if err != nil {
				return result, err
			}
			site.WireGuardPrivateKey = key
			note(true, "sites."+name+".wireguard_private_key")
		}
		// The site's heartbeat token: what its host pushes to every monitor
		// with, at https://<monitor>/ping/<token>, and what each monitor's
		// seed names for the site. 16 random bytes, hex, which is the shape
		// the uptime fork's seed takes a token in (src/lib/sitePayload.js
		// insertSite at 1.1.0-oidc.4). Kept once set, like every generated
		// secret: a replaced token is a host pushing at a URL the monitor no
		// longer knows, which it reports as the site being down.
		if site.HeartbeatToken != "" {
			note(false, "sites."+name+".heartbeat_token")
		} else {
			token, err := hexSecret(16)
			if err != nil {
				return result, err
			}
			site.HeartbeatToken = token
			note(true, "sites."+name+".heartbeat_token")
		}
		secrets.Sites[name] = site
	}

	// Every app connects as its own role with its own password. There is no
	// shared fallback, so a missing one is a hard failure at render rather
	// than a quiet reuse of somebody else's credential.
	if secrets.Apps == nil {
		secrets.Apps = map[string]map[string]any{}
	}
	if err := CheckGarageKeys(cfg, secrets); err != nil {
		return result, err
	}
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		entries := secrets.Apps[name]
		if entries == nil {
			entries = map[string]any{}
		}
		pairs := keypairs(app.Blueprint)
		for _, key := range appSecretKeys(cfg, app) {
			if pairs.has(key) {
				continue // a pair, derived from its passphrase; see below
			}
			if existing, ok := entries[key].(string); ok && existing != "" {
				note(false, "apps."+name+"."+key)
				continue
			}
			value, err := appSecret(app.Blueprint, key)
			if err != nil {
				return result, err
			}
			entries[key] = value
			note(true, "apps."+name+"."+key)
		}
		for _, pair := range pairs {
			if err := fillKeypair(name, entries, pair, note); err != nil {
				return result, err
			}
		}
		secrets.Apps[name] = entries
	}

	result.Owed = owed(cfg, secrets)
	result.Pending = pending(cfg, secrets)
	sort.Strings(result.Generated)
	sort.Strings(result.Kept)
	return result, nil
}

// appSecretKeys is what one app's stanza needs: a database role for a
// Postgres backed blueprint, the blueprint's own secrets
// (blueprints.Def.AppSecrets, which says why each is there), and storage keys
// for an app in object storage.
func appSecretKeys(cfg *config.Config, app config.App) []string {
	var keys []string
	if blueprints.UsesPostgres(app.Blueprint) {
		keys = append(keys, "database_password")
	}
	keys = append(keys, blueprints.AppSecrets(app.Blueprint)...)
	if blueprints.InObjectStorage(cfg, app) {
		// Only for an app keeping its files in Garage: one keeping them on
		// its own disk has no bucket for a key to open.
		//
		// Per app rather than shared: one key for every app would mean each
		// bucket granted to it with --owner, so any one app's .env would be
		// enough to read, rewrite and delete every other app's objects.
		keys = append(keys, "s3_access_key_id", "s3_secret_access_key")
	}
	sort.Strings(keys)
	return keys
}

// appSecret produces the value for one app secret key: in the format its
// blueprint declares, a password by default. A database password is always
// a password, and Garage refuses a base64 key ID or secret outright, so the
// two S3 credential keys get their own generators.
func appSecret(blueprint config.Blueprint, key string) (string, error) {
	switch key {
	case "s3_access_key_id":
		return garageKeyID()
	case "s3_secret_access_key":
		return garageSecretKey()
	}
	if spec, ok := blueprints.SecretOf(blueprint, key); ok {
		return generate(spec.Format)
	}
	return password()
}

// hexSecret is n bytes from the system source, hex encoded, for a consumer
// that parses its secret as hex rather than taking it as an opaque string.
func hexSecret(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// rsaPrivateKeyPEM is a 2048 bit RSA key in PKCS#8 PEM, which is one of the
// formats MAS documents accepting.
//
// 2048 rather than 4096 because it is what an OpenID Connect signing key is
// expected to be, and the key signs short lived tokens rather than protecting
// anything at rest.
func rsaPrivateKeyPEM() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("generating an RSA signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("encoding the RSA signing key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// generate is a new value of a format a blueprint names. The keypair
// formats are made with their passphrase, by keypairOf.
func generate(format string) (string, error) {
	switch format {
	case blueprints.FormatHex16:
		return hexSecret(16)
	case blueprints.FormatHex32:
		return hexSecret(32)
	case blueprints.FormatRSAPEM:
		return rsaPrivateKeyPEM()
	case blueprints.FormatPassword, "":
		return password()
	}
	return "", fmt.Errorf("a %s secret is made with the secret it depends on", format)
}

// keypair names the three secrets of an encrypted RSA keypair: the private
// key, its public half and the passphrase it is encrypted under.
type keypair struct{ private, public, passphrase string }

type keypairList []keypair

func (l keypairList) has(key string) bool {
	for _, p := range l {
		if p.private == key || p.public == key {
			return true
		}
	}
	return false
}

// keypairs is the encrypted keypairs an app's blueprint declares. Keeping
// one in the secrets file means every apps site under cluster placement
// renders the same pair, so a token one site signs verifies on another.
func keypairs(blueprint config.Blueprint) keypairList {
	var out keypairList
	for _, s := range blueprints.AppSecrets(blueprint) {
		spec, _ := blueprints.SecretOf(blueprint, s)
		if spec.Format != blueprints.FormatRSAKeypair {
			continue
		}
		p := keypair{private: spec.Name, passphrase: spec.Passphrase}
		for _, t := range blueprints.AppSecrets(blueprint) {
			if half, _ := blueprints.SecretOf(blueprint, t); half.Format == blueprints.FormatRSAPublic && half.Of == spec.Name {
				p.public = half.Name
			}
		}
		out = append(out, p)
	}
	return out
}

// fillKeypair generates an encrypted keypair a blueprint declares when its
// private key is unset.
//
// The private key is never replaced: every token Mbin has issued is signed by
// it, so a new one logs out every API client and app at once. The public key
// is derived, so a missing one is filled from the private key rather than
// counted as a reason to make a new pair. Deriving it means opening the
// private key, and one the passphrase beside it cannot open is refused rather
// than regenerated, because Mbin would refuse it too, on the host.
func fillKeypair(app string, entries map[string]any, pair keypair, note func(bool, string)) error {
	privateKey, publicKey := pair.private, pair.public
	passphrase, _ := entries[pair.passphrase].(string)
	if passphrase == "" {
		return fmt.Errorf("apps.%s.%s is empty, and the private key %s is encrypted with it", app, pair.passphrase, pair.private)
	}
	privateName := "apps." + app + "." + privateKey
	publicName := "apps." + app + "." + publicKey

	existing, _ := entries[privateKey].(string)
	if existing == "" {
		private, public, err := oauthKeypairPEM(passphrase)
		if err != nil {
			return err
		}
		entries[privateKey] = private
		note(true, privateName)
		if publicKey != "" {
			entries[publicKey] = public
			note(true, publicName)
		}
		return nil
	}
	note(false, privateName)
	if publicKey == "" {
		return nil
	}
	if current, _ := entries[publicKey].(string); current != "" {
		note(false, publicName)
		return nil
	}

	key, err := OpenPrivateKey(existing, passphrase)
	if err != nil {
		return fmt.Errorf("oauth-key-does-not-open: %s cannot be opened with apps.%s.%s, so its public half cannot be derived and the app could not sign a token with it either: %w", privateName, app, pair.passphrase, err)
	}
	public, err := publicKeyPEM(&key.PublicKey)
	if err != nil {
		return err
	}
	entries[publicKey] = public
	note(true, publicName)
	return nil
}

// oauthKeypairPEM is a 4096 bit RSA key, encrypted with the passphrase, and
// its public half. Both choices are upstream's: its documented command is
// `openssl genrsa -des3 -out ./storage/oauth/private.pem 4096`, and its
// docker/setup.sh runs the same with -passout set to OAUTH_PASSPHRASE.
//
// The encryption is the traditional PEM form (Proc-Type and DEK-Info headers)
// with AES-256-CBC, which OpenSSL, and so PHP's openssl_pkey_get_private that
// league/oauth2-server calls with OAUTH_PASSPHRASE, reads. Go deprecates
// writing it because decrypting it can be a padding oracle when an attacker
// can submit ciphertexts to it; nothing here does. Encrypted PKCS#8 would need
// a dependency outside the standard library for no difference to Mbin.
//
// It is encrypted, rather than left plain with an unused passphrase, because
// it is rendered 0644: README, "Three kinds of secret", says why.
func oauthKeypairPEM(passphrase string) (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, oauthKeyBits)
	if err != nil {
		return "", "", fmt.Errorf("generating the OAuth2 key: %w", err)
	}
	//nolint:staticcheck // see above for why the deprecated form is the right one
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY",
		x509.MarshalPKCS1PrivateKey(key), []byte(passphrase), x509.PEMCipherAES256)
	if err != nil {
		return "", "", fmt.Errorf("encrypting the OAuth2 key: %w", err)
	}
	public, err := publicKeyPEM(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), public, nil
}

// oauthKeyBits is the size of the key oauthKeypairPEM makes.
var oauthKeyBits = 4096

// SetOAuthKeyBits makes oauthKeypairPEM's keys bits long, and returns a
// function restoring the size before. It is for tests in other packages,
// which fill secrets often and test what is filled rather than the key's
// size: a 4096 bit key costs most of a second to make, a 1024 bit one a few
// milliseconds. This package's own tests keep 4096.
func SetOAuthKeyBits(bits int) (restore func()) {
	old := oauthKeyBits
	oauthKeyBits = bits
	return func() { oauthKeyBits = old }
}

// OpenPrivateKey decrypts and parses an OAuth2 private key the way Mbin
// will, so a key that would fail there fails here first. An unencrypted key
// is accepted, as OpenSSL accepts one with a passphrase supplied.
func OpenPrivateKey(privatePEM, passphrase string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		return nil, fmt.Errorf("not PEM")
	}
	der := block.Bytes
	//nolint:staticcheck // reading the form oauthKeypairPEM writes
	if x509.IsEncryptedPEMBlock(block) {
		var err error
		//nolint:staticcheck // reading the form oauthKeypairPEM writes
		der, err = x509.DecryptPEMBlock(block, []byte(passphrase))
		if err != nil {
			return nil, err
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA key")
	}
	return key, nil
}

// publicKeyPEM is the SubjectPublicKeyInfo form `openssl rsa -pubout` writes.
func publicKeyPEM(key *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", fmt.Errorf("encoding the OAuth2 public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// owed lists what only the operator can provide, with the reason, so that an
// operator finishing an install knows exactly what is left and who issues it.
func owed(cfg *config.Config, secrets *config.Secrets) []Owed {
	var out []Owed
	if secrets.External["acme_dns_token"] == "" && len(cfg.CaddySites()) > 0 {
		out = append(out, Owed{
			Name: "external.acme_dns_token",
			Why: fmt.Sprintf(
				"issued by the DNS provider, %s in this deployment, scoped to this zone only. Certificates use DNS-01, so the gateway, and a monitor serving its own hostname, cannot obtain one without it",
				cfg.ACME.Provider),
		})
	}
	if o, ok := owedSMTPPassword(cfg, secrets); ok {
		out = append(out, o)
	}
	if cfg.Backup.On() {
		for _, d := range cfg.Backup.Destinations {
			have := secrets.Backup.Destinations[d.Name]
			for _, leaf := range []struct{ name, value string }{{"access_key_id", have.AccessKeyID}, {"secret_access_key", have.SecretAccessKey}} {
				if leaf.value != "" {
					continue
				}
				out = append(out, Owed{
					Name: "backup.destinations." + d.Name + "." + leaf.name,
					Why:  fmt.Sprintf("issued by the provider behind %s for the bucket %s, with read, write, list and delete on it and nothing else. Backups cannot reach %s without it", d.Endpoint, d.Bucket, d.Name),
				})
			}
		}
	}
	return out
}

// signInClient is what a pending OIDC client is, in a report.
const signInClient = "sign in client"

// pending lists what apply obtains from a running service and records
// itself: today, the sign in client of every app that has one, which apply
// creates at the deployment's Pocket ID. Declaring the app is the approval
// for its client (founder decision, 2026-10-08), so nothing here is the
// operator's to do.
func pending(cfg *config.Config, secrets *config.Secrets) []Pending {
	var out []Pending
	for _, name := range cfg.AppNames() {
		if _, ok := secrets.OIDCClients[name]; ok {
			continue
		}
		app := cfg.Apps[name]
		// A blueprint with no client spec has no client: Pocket ID has none at
		// itself, and a Matrix client authenticates at the homeserver, whose
		// authentication service is the relying party.
		spec, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name)
		if !ok {
			continue
		}
		// The redirect URI and PKCE are named, because they are what apply
		// creates and what an operator checking an existing client needs to
		// see; for a homeserver, the rendered configuration that also names
		// them is 0600 and does not exist until apply.
		pkce := "PKCE off: the app sends no code challenge, and a Pocket ID client with PKCE on refuses a request without one"
		if spec.PKCE {
			pkce = "PKCE enabled: the app always sends a code challenge"
		}
		why := "created at the deployment's Pocket ID by `" + progname.Cmd() + " deployment apply --execute` on a site that runs " + name +
			", which records the client ID and secret here itself before " + name + " starts; declaring the app is the approval. `" + progname.Cmd() + " oidc client create --app " + name +
			"` runs the same step alone. The client's redirect URI is " + spec.CallbackURL + ", with " + pkce
		admin, member := spec.Groups(app)
		switch {
		case app.Blueprint == config.BlueprintOAuth2Proxy:
			why += ". It is restricted to the groups " + member + " and " + admin + ", the ones the gate admits"
		case app.Blueprint == config.BlueprintUptime:
			why += ". The redirect URI is the fork's own (src/lib/oidc.js:26 in the uptime fork). It is restricted to the group named in apps." +
				name + ".settings.admin_group, so the identity provider refuses everyone else before the monitor does"
		case spec.ReadsGroups:
			why += ". It is restricted to the groups " + member + " and " + admin +
				", which the app reads from the groups claim as its member and admin groups"
		default:
			// An app that reads no groups claim has the client's restriction
			// at Pocket ID as the whole control, and the reason says so where
			// an operator checking an existing client will read it.
			why += ". It is restricted to the groups " + member + " and " + admin +
				", and that restriction is the whole control: the app reads no groups claim, so a person outside them is refused at Pocket ID before the app sees them"
		}
		out = append(out, Pending{Name: "oidc_clients." + name, What: signInClient, App: name, Why: why})
	}
	return out
}

// owedSMTPPassword is external.smtp_password, owed once any app that sends
// mail logs in to its SMTP host and neither that secret nor the app's own
// apps.<app>.smtp_password covers it. Said at init rather than discovered when
// the first alert fails to send.
func owedSMTPPassword(cfg *config.Config, secrets *config.Secrets) (Owed, bool) {
	if secrets.External["smtp_password"] != "" {
		return Owed{}, false
	}
	for _, name := range smtpLogins(cfg) {
		if ownSMTPPassword(secrets, name) {
			continue
		}
		return Owed{
			Name: "external.smtp_password",
			Why:  "for " + cfg.SMTPFor(name).Username + " on " + cfg.SMTPFor(name).Host,
		}, true
	}
	return Owed{}, false
}

// SMTPPasswordUsed reports whether an app logs in to its SMTP host with the
// password at path: apps.<app>.smtp_password for that app, or
// external.smtp_password for any app without its own. A relay with no
// username is sent to unauthenticated, so a password nothing logs in with is
// neither owed nor rotated.
func SMTPPasswordUsed(cfg *config.Config, secrets *config.Secrets, path string) bool {
	for _, name := range smtpLogins(cfg) {
		if path == "apps."+name+".smtp_password" {
			return true
		}
		if path == "external.smtp_password" && !ownSMTPPassword(secrets, name) {
			return true
		}
	}
	return false
}

// smtpLogins is every app that sends mail and logs in to do it: its
// resolved SMTP settings name both a host and a username.
func smtpLogins(cfg *config.Config) []string {
	var out []string
	for _, name := range cfg.AppNames() {
		if !blueprints.SendsMail(cfg.Apps[name].Blueprint) {
			continue
		}
		if s := cfg.SMTPFor(name); s.Host != "" && s.Username != "" {
			out = append(out, name)
		}
	}
	return out
}

// ownSMTPPassword reports whether app has a password of its own under
// apps.<app>.smtp_password.
func ownSMTPPassword(secrets *config.Secrets, app string) bool {
	own, _ := secrets.Apps[app]["smtp_password"].(string)
	return own != ""
}

// fillString generates into a pointer when it is empty, reporting whether it
// did.
func fillString(into *string) (bool, error) {
	return fillWith(into, password)
}

// fillHex is fillString for a consumer that parses its secret as hex rather
// than taking it as an opaque string. It reuses garageSecretKey rather than
// growing a third generator of the same shape.
func fillHex(into *string) (bool, error) {
	return fillWith(into, garageSecretKey)
}

// fillWith is the rule that matters in this package, in one place: nothing
// already set is ever replaced.
func fillWith(into *string, gen func() (string, error)) (bool, error) {
	if *into != "" {
		return false, nil
	}
	value, err := gen()
	if err != nil {
		return false, err
	}
	*into = value
	return true, nil
}

// garageKeyID returns an S3 access key ID in the only shape Garage accepts:
// the literal "GK" followed by 12 hex encoded bytes. `password()` is not
// reused here, and base64 is exactly why: Garage rejects it outright with
// "The specified key ID is not a valid Garage key ID".
func garageKeyID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return "GK" + hex.EncodeToString(raw), nil
}

// garageSecretKey returns 32 hex encoded bytes, which is the only shape Garage
// accepts for an S3 secret key and also the only shape it accepts for
// rpc_secret in garage.toml. One generator serves both because it is one
// requirement, stated by the same piece of software, and a second copy is a
// second thing to get wrong.
func garageSecretKey() (string, error) {
	return hexSecret(32)
}

// PreviousKeyID and PreviousSecretKey hold an app's S3 key while `storage
// rotate-key` replaces it: the key being retired, kept beside the new one in
// s3_access_key_id and s3_secret_access_key until the new one is proven and
// the old one deleted from Garage. Their presence is what says a rotation is
// in progress, so they live in the encrypted file a resumed run reads rather
// than anywhere on a host.
const (
	PreviousKeyID     = "s3_previous_access_key_id"
	PreviousSecretKey = "s3_previous_secret_access_key"
)

// GarageKeyPair generates one S3 key ID and secret in the shapes Garage
// accepts, the generators `init` uses. Exported for storage rotate-key.
func GarageKeyPair() (keyID, secret string, err error) {
	if keyID, err = garageKeyID(); err != nil {
		return "", "", err
	}
	if secret, err = garageSecretKey(); err != nil {
		return "", "", err
	}
	return keyID, secret, nil
}

// garageKeyIsMalformed refuses an S3 credential Garage will not accept.
//
// Generated credentials cannot trip this: garageKeyID and garageSecretKey
// only ever produce the accepted shape. A hand edited secrets file can, and
// the failure it prevents is a provisioning run that dies halfway through
// with a message about hex encoding, after it has already imported some
// keys.
//
// This lives here rather than as a validate.go rule because validate.Check
// takes only a *config.Config and never sees secrets. Fill is the one place
// that reads a hand written key rather than generating one, so it is the one
// place that can see it to check it.
func garageKeyIsMalformed(appName, key, value string) error {
	switch key {
	case "s3_access_key_id", PreviousKeyID:
		if !garageKeyIDPattern.MatchString(value) {
			return fmt.Errorf("garage-key-is-malformed: apps.%s.%s is %q, which Garage will not accept. A Garage access key ID is the literal \"GK\" followed by exactly 24 lowercase hex characters.", appName, key, value)
		}
	case "s3_secret_access_key", PreviousSecretKey:
		if !garageSecretPattern.MatchString(value) {
			return fmt.Errorf("garage-key-is-malformed: apps.%s.%s is not in the shape Garage accepts. A Garage secret key is exactly 64 lowercase hex characters.", appName, key)
		}
	}
	return nil
}

// CheckGarageKeys refuses a hand edited S3 credential Garage would reject, for
// every app that keeps its files in Garage and already has one set. An app
// keeping them on its own disk uses no key, so one left in its stanza is not
// checked: nothing reaches a host with it.
//
// Fill calls this itself, so `init` is covered. It is exported because `init`
// is not the only path that reads a secrets file: `render` and `deployment apply` both
// call config.LoadSecrets directly and never call Fill, so a key hand edited
// into the file after the last `init` would otherwise reach a rendered
// artifact, and from there a host, with nothing ever having looked at it.
// Call this once after loading secrets and before using them for anything
// that reaches a host.
func CheckGarageKeys(cfg *config.Config, secrets *config.Secrets) error {
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		if !blueprints.InObjectStorage(cfg, app) {
			continue
		}
		entries := secrets.Apps[name]
		for _, key := range []string{"s3_access_key_id", "s3_secret_access_key", PreviousKeyID, PreviousSecretKey} {
			value, _ := entries[key].(string)
			if value == "" {
				continue
			}
			if err := garageKeyIsMalformed(name, key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// password is 32 bytes from the system source, base64 encoded without padding.
//
// Encoding matters more than it looks: these land in an .env, an INI file, a
// YAML file and a Postgres connection URL, and a raw byte string would need
// different escaping in each. The alphabet here survives all four unescaped
// except for the two characters removed below.
func password() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return encoded, nil
}

// WireGuardPrivateKey is a new WireGuard private key, generated here on the
// workstation as init generates every site's: `paisans mesh rekey` uses it
// for a site's next key.
func WireGuardPrivateKey() (string, error) { return wireGuardPrivateKey() }

// wireGuardPrivateKey returns a curve25519 private key, base64 encoded, in the
// form WireGuard expects. Clamping is what `wg genkey` does, and an unclamped
// key is not wrong so much as not canonical: the same key would be derived
// either way, but tooling that compares keys would disagree with itself.
func wireGuardPrivateKey() (string, error) {
	raw := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ClientSecret is a new OIDC client secret, in the same shape as every other
// generated password: 43 characters of unpadded base64url, which is printable
// ASCII and well over the 16 characters Pocket ID requires of a supplied
// secret (dto/oidc_dto.go:80 at tag v2.14.0). It is generated here rather
// than by the identity provider so that it can be recorded before the
// provider is sent it.
func ClientSecret() (string, error) { return password() }

// OwedSecrets is what Fill would report as owed, each with why, without
// generating anything. `secrets set` with no key asks for these in turn.
func OwedSecrets(cfg *config.Config, secrets *config.Secrets) []Owed {
	return owed(cfg, secrets)
}

// OwedNames is the names Fill would report as owed, without generating
// anything.
func OwedNames(cfg *config.Config, secrets *config.Secrets) []string {
	var out []string
	for _, o := range owed(cfg, secrets) {
		out = append(out, o.Name)
	}
	return out
}

// ExpectedNames is the names Fill would report as owed or pending: every
// secret that comes from outside this package and is not in the file yet.
// `secrets set` accepts exactly these, besides keys already set, so an
// operator can supply an owed secret or record a client made by hand.
func ExpectedNames(cfg *config.Config, secrets *config.Secrets) []string {
	out := OwedNames(cfg, secrets)
	for _, p := range pending(cfg, secrets) {
		out = append(out, p.Name)
	}
	return out
}

// hasKey reports whether keys holds key.
func hasKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
