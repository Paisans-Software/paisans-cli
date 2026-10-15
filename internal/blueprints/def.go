package blueprints

import (
	"io/fs"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Def is everything the toolkit records about one blueprint, in one place, read
// from its blueprint.yaml (docs/specs/2026-10-12-blueprints-and-
// catalogs.md). Parse documents the format.
//
// A field holds what is true of the software, never of a deployment. What an
// app's declaration decides is read through the functions that take a
// config.App, which take their answers from here.
type Def struct {
	// Services is every container the blueprint's compose template defines, in
	// compose order: `app` first. The lists are short because the compose
	// templates are short. They grow when a template gains a container, and
	// that is the point of keying them on service names: an `images` key
	// that names no service here is a typo, and the toolkit says so rather
	// than ignoring it.
	Services []Service

	// Port is the port the application listens on inside its container,
	// which is also the port it publishes on its site's mesh address. The
	// gateway needs it to route, and it is not configurable.
	Port int

	// ConfigFormat and ConfigFile are the syntax and the rendered path,
	// relative to the stack, of the file the blueprint reads its passthrough
	// configuration from. The renderer drops `.tmpl` and then `.secret`, so
	// writefreely/config.ini.secret.tmpl is `config.ini` on the host.
	ConfigFormat Format
	ConfigFile   string

	// SettingKeys are the passthrough keys a `settings` key is the sanctioned
	// input for. When a passthrough key collides with one, the refusal names
	// the setting rather than the template, because that is the way to change
	// the value that somebody will review.
	SettingKeys []SettingKey

	// OmittedKeys are keys the blueprint's template leaves out of its config
	// file on purpose, because putting one back would undo a decision the
	// toolkit made. A passthrough key that names one is refused.
	OmittedKeys []OmittedKey

	// Health is the route a monitor checks. Every built in blueprint has one; a
	// catalog blueprint may leave it out and is watched by a fallback.
	Health Health

	// Engine is the database engine the blueprint keeps its data in: postgres,
	// mysql, sqlite or none.
	Engine string

	// Backup is how to dump and restore a database the backups do not reach
	// as files, nil for a blueprint with none.
	Backup *BackupSpec

	// Checks are the blueprint's own validate rules, each a CEL condition every
	// app built from the blueprint must meet.
	Checks []Check

	// Settings are the `settings` keys the blueprint reads, with their types,
	// for a blueprint that declares them; a built in blueprint's are checked by
	// validate's own code instead.
	Settings map[string]SettingSpec

	// RequiresToolkit is the oldest toolkit version the blueprint needs, as
	// written: ">= MAJOR.MINOR[.PATCH]", empty for none.
	RequiresToolkit string

	// Gate is what the blueprint needs from the gateway's Caddy to sit behind
	// the gate, and GateSettings and LiveSettings what it must enforce itself
	// while it does, and how to read that back from a running app.
	Gate         GateSpec
	GateSettings []GateSetting
	LiveSettings LiveSettings

	// Federates is whether the blueprint answers ActivityPub, and
	// FederationSetting the boolean setting that turns it off, empty for a
	// blueprint that cannot turn it off.
	Federates         bool
	FederationSetting string

	// HostnameRoles are the hostname roles the blueprint understands beyond the
	// primary. The set is closed: a role selects which Caddy snippet that
	// hostname gets, so a role the blueprint ships no snippet for would render
	// a host block importing a file that does not exist.
	HostnameRoles []string

	// ObjectStorage is whether the blueprint can keep its uploads in S3 rather
	// than on local disk; PublicObjects whether its objects must be readable
	// without a credential; LocalFilesOnly whether it stores files that can
	// live only on its own site's disk, with no object storage form at all.
	ObjectStorage  bool
	PublicObjects  bool
	LocalFilesOnly bool

	// LocalOnlyReason is why a blueprint with LocalFilesOnly has no object
	// storage form, for the refusal of `storage: s3` on it.
	LocalOnlyReason string

	// ExtraDatabases are databases the blueprint keeps beside its own in the
	// same Postgres, each named <database>_<name>, which its templates create.
	ExtraDatabases []string

	// LockedSettings are the settings the configuration is the only source
	// of, which the app's own admin pages refuse to change.
	LockedSettings []LockedSetting

	// Provides and Consumes are the roles linking the blueprint to other apps.
	Provides []provided
	Consumes []Consume

	// LocalMediaPaths are the paths on its own hostname a blueprint serves its
	// stored files from when it keeps them locally, for a blueprint whose files
	// must be readable with no credential.
	LocalMediaPaths []string

	// SendsMail is whether the blueprint reads the deployment's smtp settings.
	SendsMail bool

	// OIDC is how the blueprint's client at the identity provider must look,
	// nil for a blueprint that signs nobody in.
	OIDC *OIDCDef

	// DashboardPath is where the identity provider's dashboard tile sends a
	// member, on the app's own hostname. Empty means `/`.
	DashboardPath string

	// AppSecrets are the generated secrets an app built from the blueprint has
	// beyond database_password, which every Postgres backed blueprint has, and
	// its storage keys, which every app in object storage has.
	AppSecrets []string

	// Secrets is how each of AppSecrets is generated, and what rotating it
	// costs and needs.
	Secrets []SecretSpec

	// RequiredSecrets are the app secrets without which the application will
	// not start, and why. Render refuses an app missing one.
	RequiredSecrets []RequiredSecret

	// Reserved is whether the blueprint is one no catalog may provide: the
	// identity provider, the gate, the account service and the monitor.
	Reserved bool

	// SelfRotated are the app secrets `secrets rotate` replaces by
	// re-rendering the app's own stack, because the stack that presents
	// each is also the one that checks it.
	SelfRotated []string
}

// SettingKey is one passthrough key a setting owns. Key is in the blueprint's
// `config` syntax; File is the stack file it is rendered into.
type SettingKey struct {
	Key     string
	File    string
	Setting string
}

// OmittedKey is one key a template leaves out on purpose. Key is in the
// blueprint's `config` syntax and matches itself and any path beneath it, so
// oidc_config.enabled is caught by oidc_config. Why completes the refusal's
// "leaves <key> out of <file> on purpose: ".
type OmittedKey struct {
	Key string
	Why string
}

// RequiredSecret is one app secret a blueprint cannot start without.
type RequiredSecret struct {
	Key string
	Why string
}

// defs is every blueprint's record: the reserved ones, read at init from the
// blueprints built into the binary (internal/blueprints), and the core and
// catalog ones as they are read.
//
// It is filled in init because a record holds functions that read records (a
// gate setting's condition asks Federates), which Go's package initialisation
// would refuse as a cycle. A built in blueprint that does not parse is a bug
// in this binary, found by every test in this package, so it panics.
var defs map[config.Blueprint]*Def

func init() {
	loaded, err := loadBuiltins()
	if err != nil {
		panic(err)
	}
	defs = loaded
}

// loadBuiltins reads every blueprint built into the binary.
func loadBuiltins() (map[config.Blueprint]*Def, error) {
	builtins, err := embeddedBlueprints()
	if err != nil {
		return nil, err
	}
	out := map[config.Blueprint]*Def{}
	for _, b := range builtins {
		hasTemplate := func(file string) bool {
			_, err := fs.Stat(embeddedTemplates, b.Name+"/"+file)
			return err == nil
		}
		d, err := Parse(b.Name, b.Spec, hasTemplate)
		if err != nil {
			return nil, err
		}
		blueprint := config.Blueprint(b.Name)
		if extra, ok := reservedExtras[blueprint]; ok {
			extra(d)
		}
		d.Reserved = true
		out[blueprint] = d
	}
	return out, nil
}

// For is a blueprint's record, and false for a blueprint this toolkit does not
// know. A core blueprint is read from the catalog cache the first time any is
// asked for.
func For(blueprint config.Blueprint) (*Def, bool) {
	mu.RLock()
	d, ok := defs[blueprint]
	mu.RUnlock()
	if ok || strings.Contains(string(blueprint), "/") {
		return d, ok
	}
	if LoadCore("") != nil {
		return nil, false
	}
	mu.RLock()
	defer mu.RUnlock()
	d, ok = defs[blueprint]
	return d, ok
}

// of is a blueprint's record, or an empty one for a blueprint this toolkit does
// not know, so that every question asked of an unknown blueprint has the zero
// answer. validate refuses the unknown blueprint itself.
func of(blueprint config.Blueprint) *Def {
	if d, ok := For(blueprint); ok {
		return d
	}
	return &Def{}
}

// Builtin is every reserved and core blueprint, sorted, and none a
// configuration's catalogs added.
func Builtin() []config.Blueprint {
	var out []config.Blueprint
	for _, k := range known() {
		if !strings.Contains(string(k), "/") {
			out = append(out, k)
		}
	}
	return out
}

// known is every blueprint with a record, sorted. The core blueprints are read
// first, so it holds them.
func known() []config.Blueprint {
	LoadCore("")
	mu.RLock()
	defer mu.RUnlock()
	out := make([]config.Blueprint, 0, len(defs))
	for k := range defs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Port is the port a blueprint listens on, which is also the port it publishes
// on its site's mesh address.
func Port(blueprint config.Blueprint) int { return of(blueprint).Port }

// SettingKeys is every passthrough key a setting owns, for a blueprint.
func SettingKeys(blueprint config.Blueprint) []SettingKey {
	return append([]SettingKey(nil), of(blueprint).SettingKeys...)
}

// OmittedKeys is every key a blueprint's template leaves out on purpose.
func OmittedKeys(blueprint config.Blueprint) []OmittedKey {
	return append([]OmittedKey(nil), of(blueprint).OmittedKeys...)
}

// AppSecrets is a blueprint's own generated secrets, in the order they are
// generated.
func AppSecrets(blueprint config.Blueprint) []string {
	return append([]string(nil), of(blueprint).AppSecrets...)
}

// RequiredSecrets is the app secrets a blueprint cannot start without.
func RequiredSecrets(blueprint config.Blueprint) []RequiredSecret {
	return append([]RequiredSecret(nil), of(blueprint).RequiredSecrets...)
}

// SelfRotated is the app secrets of a blueprint that `secrets rotate` replaces
// by re-rendering the app's own stack.
func SelfRotated(blueprint config.Blueprint) []string {
	return append([]string(nil), of(blueprint).SelfRotated...)
}

// SelfRotatedBlueprint is the blueprint whose secret key is, for each key
// `secrets rotate` replaces by re-rendering the app's own stack, and false for
// any other key. No two blueprints share such a key
// (TestSelfRotatedSecretsAreUnique), so the answer is one blueprint.
func SelfRotatedBlueprint(key string) (config.Blueprint, bool) {
	for _, blueprint := range known() {
		for _, k := range of(blueprint).SelfRotated {
			if k == key {
				return blueprint, true
			}
		}
	}
	return "", false
}
