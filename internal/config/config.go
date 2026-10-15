// Package config loads and structurally checks a paisans-stack.yaml deployment
// declaration. It performs no policy checks: those live in internal/validate,
// so that every problem in a file can be reported at once rather than one per
// run.
package config

import (
	"fmt"
	"net"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// FileName is the name of the deployment declaration: the default of every
// command's --config, the name a configuration store version holds it by, and
// the name every message gives it. It is defined once so that the name an
// operator reads is always the name the toolkit looks for.
const FileName = "paisans-stack.yaml"

// Role is a capability a site provides. Sites declare roles; apps declare
// placement. Keeping those separate is what stops every new app from becoming
// a new role.
type Role string

const (
	RoleData    Role = "data"
	RoleApps    Role = "apps"
	RoleGateway Role = "gateway"
	RoleWitness Role = "witness"
	// RoleStorage is a host that runs Garage and nothing else. A data or apps
	// site that also runs Garage needs no such role: storage.garage.sites is
	// what places Garage, and this role only names a host that exists for it,
	// so its purpose reads in its own entry. Validation refuses it on a site
	// that list does not name. Founder decision, over accepting an empty
	// role list for such a site.
	RoleStorage Role = "storage"
	// RoleMonitor is a host that runs the uptime monitor. It serves its own
	// apps rather than routing them through the gateway, which would take it
	// dark at exactly the moment it is needed. It is best on a machine of its
	// own, since reporting the others' failures is its job; beside the gateway
	// or the witness it is allowed with a warning, and the gateway's Caddy
	// serves it there. See docs/specs/2026-10-08-monitor-role-and-host-check.md
	// and docs/specs/2026-10-12-monitor-on-gateway.md. Founder decisions.
	RoleMonitor Role = "monitor"
)

var knownRoles = map[Role]bool{RoleData: true, RoleApps: true, RoleGateway: true, RoleWitness: true, RoleStorage: true, RoleMonitor: true}

// KnownRole reports whether r is a site role.
func KnownRole(r Role) bool { return knownRoles[r] }

// Blueprint names an application the toolkit knows how to render, qualified
// by its catalog unless it is the core catalog's.
type Blueprint string

// The reserved blueprints, the only ones Go names: no catalog may provide
// them, so their names never change with a catalog commit.
const (
	BlueprintOAuth2Proxy Blueprint = "oauth2-proxy"
	BlueprintPocketID    Blueprint = "pocket-id"
	BlueprintUptime      Blueprint = "uptime"
	BlueprintAccount     Blueprint = "account"
)

// Config is a whole deployment, declared. It records intent and never status:
// which node is primary lives in etcd, not here.
type Config struct {
	Version int `yaml:"version"`
	// ID is the deployment's identity, a version 4 UUID that `paisans init`
	// writes once and nothing changes afterwards. Every name and path on a host
	// is derived from it (see internal/deployment), and the host registry is
	// keyed by it, so changing it would orphan everything already deployed.
	ID        string    `yaml:"id"`
	Community Community `yaml:"community"`
	Mesh      Mesh      `yaml:"mesh"`
	// ACME is how certificates are obtained. It is deployment wide rather than
	// a property of the gateway site, because the gateway role moves between
	// machines by design and certificates do not move with it.
	ACME ACME `yaml:"acme"`
	// Infra overrides the images the infrastructure stack runs, each of
	// which is otherwise the core catalog foundation's.
	Infra Infra `yaml:"infra"`
	// Operators is the people who may log in to the sites, under the name a
	// site's ssh.keys lists them by.
	Operators map[string]Operator `yaml:"operators"`
	Sites     map[string]Site     `yaml:"sites"`
	Cluster   Cluster             `yaml:"cluster"`
	Etcd      Etcd                `yaml:"etcd"`
	Storage   Storage             `yaml:"storage"`
	// SMTP is how apps send mail, the default for every app that sends any.
	// An app's own smtp block overrides it field by field: see SMTPFor.
	SMTP SMTP `yaml:"smtp"`
	// Backup is nil when the declaration has no backup section, which means
	// no backups, and validate warns about it on every run.
	Backup *Backup `yaml:"backup"`
	// Catalogs are the git repositories of blueprints the apps may name, by
	// <catalog>/<blueprint>. The core catalog is always there.
	Catalogs map[string]Catalog `yaml:"catalogs"`
	Apps     map[string]App     `yaml:"apps"`

	// Path is where this configuration was read from. Error messages use it.
	Path string `yaml:"-"`
}

type Community struct {
	Name string `yaml:"name"`
	// Domain is a one way door. Federation identity is the hostname and remote
	// instances have recorded it, so changing it after federating is a rebuild.
	Domain string `yaml:"domain"`
	// MailDomains are names under Domain whose mail somebody other than this
	// toolkit runs (Eg: the domain itself on a hosted mail service). dns init
	// publishes no mail records at them, while every other name it publishes
	// says it sends and receives no mail. A sender in an smtp block makes its
	// own domain one of these without being listed here.
	MailDomains []string `yaml:"mail_domains"`
}

// Mesh is the private network every site sits on.
//
// The subnet is declared rather than derived. Applications are rendered to
// trust it for forwarded client addresses, so it has to be a value that never
// changes: deriving the tightest network containing today's site addresses
// would widen it the moment a site was added, silently rewriting
// TRUSTED_PROXIES in every app. Declaring it also lets an operator pick a
// range that does not collide with something their hosts already route.
type Mesh struct {
	Subnet string `yaml:"subnet"`
}

// Prefix returns the subnet's prefix length, for rendering an interface
// address. It assumes the subnet has already been checked.
func (m Mesh) Prefix() int {
	_, network, err := net.ParseCIDR(m.Subnet)
	if err != nil {
		return 0
	}
	ones, _ := network.Mask.Size()
	return ones
}

// Contains reports whether an address sits inside the mesh.
func (m Mesh) Contains(address string) bool {
	_, network, err := net.ParseCIDR(m.Subnet)
	if err != nil {
		return false
	}
	ip := net.ParseIP(address)
	return ip != nil && network.Contains(ip)
}

// Infra overrides the infrastructure stack's images. Each key is optional,
// and an unset one takes the core catalog foundation's image. A value is one
// full reference, exact or a track, for the reason App.Images gives.
type Infra struct {
	// Images is keyed by InfraImageKeys, the names foundation.yaml gives
	// the images they replace. The heartbeat key replaces the image both the
	// infrastructure stack's heartbeat and every Pocket ID stack's operators
	// heartbeat run.
	Images map[string]string `yaml:"images"`
	// Postgres is keyed by Postgres major, then by InfraPostgresKeys. An
	// entry applies only while cluster.postgres_version is its major, so a
	// build of one major never follows the cluster onto another.
	Postgres map[string]map[string]string `yaml:"postgres"`
}

// InfraImageKeys are the keys infra.images takes.
var InfraImageKeys = []string{"etcd", "garage", "haproxy", "heartbeat"}

// InfraPostgresKeys are the keys infra.postgres.<major> takes.
var InfraPostgresKeys = []string{"spilo"}

// DefaultPostgresVersion is the major a deployment runs when
// cluster.postgres_version is left out.
const DefaultPostgresVersion = "18"

// PostgresMajor is the Postgres major the deployment runs.
func (c *Config) PostgresMajor() string {
	if c.Cluster.PostgresVersion == "" {
		return DefaultPostgresVersion
	}
	return c.Cluster.PostgresVersion
}

// SpiloImage is the Spilo image infra.postgres declares for the major the
// deployment runs, empty when it declares none.
func (c *Config) SpiloImage() string {
	return c.Infra.Postgres[c.PostgresMajor()]["spilo"]
}

// problems are infra's keys and references that cannot be rendered: a key
// the toolkit does not run, or a value that is not one image reference.
func (i Infra) problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	for _, key := range sortedKeys(i.Images) {
		if !slices.Contains(InfraImageKeys, key) {
			add("infra.images.%s: not an image the infrastructure stack runs. infra.images takes %s; the gateway's Caddy is acme.image.", keyPath(key), strings.Join(InfraImageKeys, ", "))
			continue
		}
		imageProblem(add, "infra.images."+keyPath(key), i.Images[key])
	}
	for _, major := range sortedKeys(i.Postgres) {
		for _, key := range sortedKeys(i.Postgres[major]) {
			path := fmt.Sprintf("infra.postgres.%s.%s", keyPath(major), keyPath(key))
			if !slices.Contains(InfraPostgresKeys, key) {
				add("%s: infra.postgres.<major> takes %s. A pinned app's own database is apps.<name>.images.", path, strings.Join(InfraPostgresKeys, ", "))
				continue
			}
			imageProblem(add, path, i.Postgres[major][key])
		}
	}
	return out
}

// imageProblem adds what is wrong with ref as the image at path: a control
// character, nothing, or something no image reference holds.
func imageProblem(add func(string, ...any), path, ref string) {
	switch r, ok := ControlRune(ref); {
	case ok:
		add("%s %s", path, controlProblem(r))
	case strings.TrimSpace(ref) == "":
		add("%s: empty. Give a full image reference, or remove the key to take the core catalog's.", path)
	case !imageRef.MatchString(ref):
		add("%s: %s", path, imageRefProblem(ref))
	}
}

// ACME is the certificate story: which DNS provider answers the challenge, and
// optionally which Caddy image carries that provider's module.
//
// DNS-01 is not configurable. A gateway has to be able to hold valid
// certificates before DNS points at it, which is what makes moving a gateway an
// overlap rather than a cutover, and a hostname served behind a VPN has nothing
// on the internet that can answer an HTTP-01 challenge.
type ACME struct {
	// Provider is the Caddy DNS provider name, as it appears in `acme_dns`.
	Provider string `yaml:"provider"`
	// Image overrides the Caddy image. It is only meaningful for a provider the
	// toolkit publishes no image for: an image is the only way that provider's
	// module reaches the gateway, since nothing is built on a host.
	Image string `yaml:"image"`
}

type Site struct {
	Roles   []Role `yaml:"roles"`
	Address string `yaml:"address"`
	// Endpoint is where the other sites' WireGuard reaches this one, and its
	// port is the one this site listens on: host:port, or ":port" beside a
	// public_address. Optional. Read it through DialEndpoint, which says what
	// is actually dialled: public_address, where one is declared, unless the
	// endpoint is itself an address.
	Endpoint string `yaml:"endpoint"`
	// SSH is how the toolkit reaches the site and who may log in to it. See
	// the SSH type.
	SSH SSH `yaml:"ssh"`

	// PublicAddress is the IPv4 address the internet reaches this site on. It
	// is optional. It is the content of the A records `paisans dns` creates,
	// the address the other sites' WireGuard dials (DialEndpoint), and the
	// default ssh.host. It is declared rather than discovered
	// because the address a host sees on its own interface is often not the
	// one the internet sees, behind NAT or a cloud provider's 1:1 mapping, and
	// a guessed address published in DNS sends every visitor somewhere wrong.
	PublicAddress string `yaml:"public_address"`
	// PublicAddress6 is the IPv6 counterpart, for AAAA records. Optional even
	// where PublicAddress is set.
	PublicAddress6 string `yaml:"public_address6"`
	// Watchdog is how `host prepare` gives Patroni a /dev/watchdog on this
	// site. It only matters where the site holds the data role. Empty means
	// auto; read it through WatchdogMode rather than directly.
	Watchdog WatchdogMode `yaml:"watchdog"`
	// WebServer is what sits in front of a monitor site's apps. Only a site
	// holding the monitor role may declare it: a gateway always runs the
	// toolkit's Caddy, and every other site is reached through the gateway.
	// Absent means managed_by paisans; read it through WebServerManagedBy.
	WebServer *WebServer `yaml:"web_server"`
	// Arch is the host's architecture, amd64 or arm64, as `dpkg
	// --print-architecture` reported it the first time the toolkit reached
	// the site. The toolkit writes it (SetArch), so validate can check which
	// sites must share one without reaching a host, and a host replaced by one
	// of another architecture is refused rather than joined. Deleting it is
	// how an operator accepts new hardware.
	Arch string `yaml:"arch"`
}

// ManagedBy is what serves a monitor site's apps to the internet.
type ManagedBy string

const (
	// ManagedByPaisans runs the toolkit's own Caddy on the monitor, the same
	// image as the gateway's, with certificates over DNS-01.
	ManagedByPaisans ManagedBy = "paisans"
	// ManagedByExternal runs nothing in front of the app: a web server the
	// operator already runs terminates TLS and proxies to Listen. Its
	// certificates are the operator's, because the toolkit cannot know how
	// an unfamiliar proxy obtains them and must not edit a configuration it
	// does not own.
	ManagedByExternal ManagedBy = "external"
)

var knownManagedBy = map[ManagedBy]bool{ManagedByPaisans: true, ManagedByExternal: true}

// WebServer is how the internet reaches a monitor site's apps.
type WebServer struct {
	ManagedBy ManagedBy `yaml:"managed_by"`
	// Listen is where the app is published for the operator's web server, as
	// an IPv4 address and a port. Required with managed_by external, refused with
	// managed_by paisans.
	Listen string `yaml:"listen"`
	// IncludeDir is the directory on the host the toolkit writes its own
	// files in for the operator's web server, which includes them with one
	// wildcard line. Unset, apply derives it beside the web server's
	// configuration and writes it here; false writes nothing there.
	IncludeDir IncludeDir `yaml:"include_dir"`
}

// IncludeDir is sites.<site>.web_server.include_dir: a path, false, or unset.
type IncludeDir struct {
	Path string
	// Off is include_dir: false.
	Off bool
}

// Unset reports whether include_dir is not written at all.
func (d IncludeDir) Unset() bool { return d.Path == "" && !d.Off }

// UnmarshalYAML reads a path or false. true means nothing, so it is refused
// rather than read as either.
func (d *IncludeDir) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!bool" {
		var on bool
		if err := node.Decode(&on); err != nil {
			return err
		}
		if on {
			return fmt.Errorf("line %d: include_dir is true, which names no directory. Write the directory, or false to have the toolkit write nothing for your web server", node.Line)
		}
		*d = IncludeDir{Off: true}
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: include_dir is %s, not a directory or false", node.Line, describeNode(node))
	}
	*d = IncludeDir{Path: node.Value}
	return nil
}

// IncludeDirProblem says what is wrong with an include_dir path, empty when
// nothing is. It is written into the web server's configuration and into
// shell commands, so it is held to plain absolute paths.
func IncludeDirProblem(dir string) string {
	switch {
	case !strings.HasPrefix(dir, "/"):
		return "is not an absolute path"
	case dir == "/":
		return "is the root directory"
	case path.Clean(dir) != dir:
		return "is not written in its clean form, " + path.Clean(dir)
	case strings.ContainsAny(dir, " \t\n'\"\\$`;#*?[]{}"):
		return "holds a space, a quote or a character a shell or a web server's glob reads specially"
	}
	return ""
}

// ListenHostPort splits Listen into an IPv4 address and a port, false when it
// is not one. The port is 1 to 65535 written without leading zeros, the one
// spelling a request carries, so the file and the request cannot disagree on
// whether 08480 is 8480.
func (i WebServer) ListenHostPort() (string, int, bool) {
	host, port, err := net.SplitHostPort(i.Listen)
	if err != nil || !isIPv4(host) {
		return "", 0, false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", 0, false
	}
	return host, n, true
}

// ProxiesToThisHost reports whether listen is an address the owner's web
// server may be handed for a site whose mesh address is address: an IPv4
// loopback address or the site's own mesh address, in plain dotted form, with
// a port ListenHostPort accepts. A monitor behind the owner's web server is
// handed to that server on the same host, so nothing beyond the host is
// needed, and Docker publishes a port with rules of its own in front of the
// firewall, so any other address would expose the app around the web
// server. validate and host request both hold web_server.listen to it.
func ProxiesToThisHost(listen, address string) bool {
	host, _, ok := WebServer{Listen: listen}.ListenHostPort()
	if !ok {
		return false
	}
	ip := net.ParseIP(host)
	return ip.String() == host && (ip.IsLoopback() || host == address)
}

// WebServerManagedBy returns the site's declared web_server.managed_by, paisans when none is
// declared. It is meaningful only on a monitor site.
func (s Site) WebServerManagedBy() ManagedBy {
	if s.WebServer == nil || s.WebServer.ManagedBy == "" {
		return ManagedByPaisans
	}
	return s.WebServer.ManagedBy
}

// RunsCaddy reports whether the toolkit's Caddy runs on the site: on a
// gateway, and on a monitor that serves its own apps.
func (s Site) RunsCaddy() bool {
	return s.Has(RoleGateway) || (s.Has(RoleMonitor) && s.WebServerManagedBy() == ManagedByPaisans)
}

// WatchdogMode is how a data site's watchdog device is provided.
//
// It is declared per site because the answer is a property of the machine: a
// board with an iTCO timer, a VM with an emulated i6300esb, and a small cloud
// instance with nothing at all each want a different one, and the toolkit
// cannot tell which from the configuration alone.
type WatchdogMode string

const (
	// WatchdogAuto uses whatever device the host already has, whatever its
	// driver, and falls back to loading softdog with a warning.
	WatchdogAuto WatchdogMode = "auto"
	// WatchdogRequired refuses a host whose only watchdog is softdog, or
	// which has none.
	WatchdogRequired WatchdogMode = "required"
	// WatchdogSoftdog loads and persists softdog because the operator chose
	// it, so no warning is printed.
	WatchdogSoftdog WatchdogMode = "softdog"
	// WatchdogOff leaves the host alone and renders Patroni without a
	// watchdog at all.
	WatchdogOff WatchdogMode = "off"
)

var knownWatchdogModes = map[WatchdogMode]bool{WatchdogAuto: true, WatchdogRequired: true, WatchdogSoftdog: true, WatchdogOff: true}

// WatchdogMode returns the site's declared mode, auto when none is declared.
func (s Site) WatchdogMode() WatchdogMode {
	if s.Watchdog == "" {
		return WatchdogAuto
	}
	return s.Watchdog
}

// Has reports whether the site declares a role.
func (s Site) Has(r Role) bool {
	for _, got := range s.Roles {
		if got == r {
			return true
		}
	}
	return false
}

type Cluster struct {
	// Sites is the cluster's data sites: as written, or, when the file
	// leaves the key out, every site with the data role, in name order.
	// Load fills it either way, so every reader reads one list.
	Sites []string `yaml:"sites"`
	// SitesDerived records that the file left cluster.sites out and Sites
	// came from the roles. validate's comparisons between the list and the
	// data role hold by construction then, and the yaml writers leave a key
	// the file does not have alone.
	SitesDerived      bool   `yaml:"-"`
	Port              int    `yaml:"port"`
	PostgresVersion   string `yaml:"postgres_version"`
	Synchronous       bool   `yaml:"synchronous"`
	SynchronousStrict bool   `yaml:"synchronous_strict"`
}

type Etcd struct {
	// Members is etcd's voters: as written, or, when the file leaves the
	// key out, every site with the witness role, then every site with the
	// data role, each in name order. Witnesses come first because that is
	// the order a new deployment is founded in. Load fills it either way.
	Members []string `yaml:"members"`
	// MembersDerived records that the file left etcd.members out, as
	// Cluster.SitesDerived does for cluster.sites.
	MembersDerived    bool `yaml:"-"`
	HeartbeatMS       int  `yaml:"heartbeat_ms"`
	ElectionTimeoutMS int  `yaml:"election_timeout_ms"`
}

type Storage struct {
	Garage Garage `yaml:"garage"`
	// There is no deployment wide media hostname. Each app that stores objects
	// serves them on a hostname of its own, derived from its own hostname and
	// overridable as `hostnames.media` on that app: see
	// blueprints.MediaHostname. A single hostname with a bucket as a path under
	// it was the earlier shape, and it was replaced because a path cannot be
	// pointed at a CDN or another provider one app at a time, while a hostname
	// can.
}

type Garage struct {
	Sites       []string `yaml:"sites"`
	Replication int      `yaml:"replication"`
	// Capacity is what this node advertises to Garage's layout. Garage
	// requires a unit suffix, as in 100G. It is not derived from the disk
	// because the toolkit cannot know how much of that disk is meant for
	// objects. Defaulted in Load when left unset.
	Capacity string `yaml:"capacity"`
	// Capacities overrides Capacity for the sites it names, so nodes of
	// different sizes can share a layout: Garage spreads partitions in
	// proportion to capacity. Founder decision, over a field on each site,
	// which would spread Garage's configuration across site entries.
	Capacities map[string]string `yaml:"capacities"`
	// Consistency is Garage's consistency_mode: consistent, degraded or
	// dangerous. At replication 2 on two sites, dangerous is the only mode in
	// which uploads continue while a site is down, at the price of an upload
	// being confirmed once one copy exists (Garage v1.0.1,
	// doc/book/reference-manual/configuration.md, "consistency_mode").
	// Garage reads it at start and does not record it in the layout, so
	// changing it is an ordinary apply. Defaulted in Load when left unset.
	Consistency string `yaml:"consistency"`
}

// Garage consistency modes, as Garage v1.0.1 spells them
// (src/rpc/replication_mode.rs).
const (
	GarageConsistent = "consistent"
	GarageDegraded   = "degraded"
	GarageDangerous  = "dangerous"
)

type App struct {
	Blueprint Blueprint `yaml:"blueprint"`
	Hostname  string    `yaml:"hostname"`
	// DisplayName is the name of the app's OIDC client at the identity
	// provider, which is what a person signing in is shown. Empty names the
	// client after the app (see ClientName).
	DisplayName string    `yaml:"display_name"`
	Placement   Placement `yaml:"placement"`
	// Images overrides the container image a service runs, keyed by service
	// name in the blueprint's compose template. Every key is optional and an
	// unset one takes the default that blueprint ships.
	//
	// It is a map rather than a single reference because a stack is several
	// containers, and it is one full reference per key rather than a repository
	// and a tag because switching to a fork changes registry, repository and
	// tag together: split into two fields, a half finished switch parses
	// cleanly and pulls something nobody intended.
	//
	// Changing this is not changing Blueprint. A fork that renames a variable
	// or moves a config path needs a different template set, which is a
	// different blueprint; this is for a different build of the same software.
	Images map[string]string `yaml:"images"`
	// Backup is nil unless written. `backup: false` excludes everything the
	// app owns from backups, and validate warns about it on every run.
	Backup   *bool          `yaml:"backup"`
	Settings map[string]any `yaml:"settings"`

	// Config is passed through to the file this app's blueprint reads, in that
	// file's own syntax, without the toolkit interpreting the key.
	//
	// It is not `settings`. A setting is an input the toolkit reasons about: it
	// reads it, sometimes validates it, and decides things with it. A config key
	// is one the toolkit has no opinion about and only places.
	//
	// A key the blueprint's template already writes is refused rather than
	// overridden, because two sources of truth for one value is how a
	// deployment ends up with a setting nobody can locate.
	Config map[string]any `yaml:"config"`

	// Hostnames are the additional names this app answers on, keyed by a role
	// the blueprint understands. The primary hostname stays in Hostname; a role
	// here selects a different Caddy snippet, because a second hostname usually
	// exists to serve something different rather than the same thing twice.
	//
	// `media` is the one role that exists without being declared: every
	// blueprint that stores objects serves them on <label>-media.<domain>, and
	// declaring it here only chooses another name. See
	// blueprints.MediaHostname.
	Hostnames map[string]string `yaml:"hostnames"`

	// Gated puts the app behind the gate: every request on its hostnames
	// meets the community's sign-in, and only members get through, except
	// the paths that carry their own authentication, which the blueprint
	// names.
	//
	// It is declared rather than inferred because it is access policy. It is
	// also the setting most likely to be wrong in a way nothing notices: a
	// gated Matrix hostname authenticates a browser and breaks every client,
	// because a client will not follow a redirect to a passkey prompt.
	Gated bool `yaml:"gated"`

	// AutoLogin skips the app's own "Log in with" button for a member the
	// gate has already signed in, on a gated app or a public one. Only
	// blueprints with an auto-login rule take it.
	AutoLogin bool `yaml:"auto_login"`

	// Derived marks the gate app, which no file declares: Load adds it when
	// any app sets gated or auto_login. See deriveGate.
	Derived bool `yaml:"-"`

	// SMTP overrides the deployment's smtp block for this app, field by
	// field: a field left out is inherited. Only blueprints that send mail
	// read it, and validate refuses it on any other
	// (smtp-on-a-blueprint-without-mail).
	SMTP *SMTP `yaml:"smtp"`

	// Storage is where an app that stores files keeps them: s3, a bucket in
	// the deployment's Garage, or local, a directory in the app's own stack
	// on the site it is pinned to. Empty means the default, which depends on
	// the deployment rather than on this file: s3 when storage.garage.sites
	// names a site, local when it names none. Read it through
	// blueprints.InObjectStorage, never by comparing this field, so that the
	// default is decided in one place.
	//
	// local is valid only with pinned placement, because a clustered app runs
	// on every apps site and each would keep its own copy of the files.
	// validate refuses the rest of the incoherent combinations by
	// blueprint.
	Storage string `yaml:"storage"`
}

// The values of an app's storage key.
const (
	StorageLocal = "local"
	StorageS3    = "s3"
)

// UsesGate reports whether the app needs the gate at all.
func (a App) UsesGate() bool { return a.Gated || a.AutoLogin }

// GateApp is the name of the gate app Load derives. It is the toolkit's, so a
// file declaring an app of that name while the gate is needed is refused.
const GateApp = "gate"

// keyPath is a mapping key as an error message prints it in a key's path:
// as written when it is a plain word, and quoted otherwise, so a key holding
// a line break or a terminal control cannot change what the message shows.
func keyPath(k string) string {
	if plainKey.MatchString(k) {
		return k
	}
	return fmt.Sprintf("%q", k)
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// settingControlRune is the first control character (ControlRune) in a
// setting's value: a string, or any string in a list or a mapping. A
// multi-line text setting, written into a file of its own, may hold line
// breaks and tabs.
func settingControlRune(v any, multiline bool) (rune, bool) {
	switch x := v.(type) {
	case string:
		for _, r := range x {
			if multiline && (r == '\n' || r == '\t') {
				continue
			}
			if c, ok := ControlRune(string(r)); ok {
				return c, true
			}
		}
	case []any:
		for _, item := range x {
			if r, ok := settingControlRune(item, multiline); ok {
				return r, true
			}
		}
	case map[string]any:
		for _, item := range x {
			if r, ok := settingControlRune(item, multiline); ok {
				return r, true
			}
		}
	}
	return 0, false
}

// acmeProvider is a DNS provider's name as caddy-dns modules take it
// (dns.providers.<name>).
var acmeProvider = regexp.MustCompile(`^[a-z0-9_]+$`)

// imageRef is the characters an image reference is written with: a
// registry, a repository, a tag (a track's `*` included) and a digest. A
// reference is written into a compose file as it is, so anything else (a
// space, a `#`, a quote, a line break in any encoding) could end the value
// and add a key.
var imageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+*-]*$`)

func imageRefProblem(ref string) string {
	return fmt.Sprintf("%q is not an image reference: letters, digits and . _ / : @ + * -, as in ghcr.io/example/app:1.2 or a track like ghcr.io/example/app:1.*. It is written into a compose file as it is, so any other character could add a key to the service.", ref)
}

// nameCharsRE is what nameChars accepts.
var nameCharsRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// nameChars reports whether s holds only characters a DNS name can, and an
// underscore, which validate refuses with its own reason. It is not a full
// check of a name's shape; it is what keeps a value the toolkit's templates
// write as it is (a Caddyfile, an env file, a WireGuard file) from adding a
// line, a brace or a shell expansion to them.
func nameChars(s string) bool { return len(s) <= 253 && nameCharsRE.MatchString(s) }

// InfraStack is the toolkit's infrastructure stack: the directory and the
// compose project every site's infrastructure services render into, beside
// one per app. An app takes its stack's name from its own, so an app of this
// name is refused: its files would land on the infrastructure stack's.
const InfraStack = "infra"

// GateHostname is the gate's hostname: the one name the identity provider's
// callback is registered against, under the community's domain so the
// session cookie the gate writes covers every app.
func (c *Config) GateHostname() string { return "gate." + c.Community.Domain }

// ClientName is the name of app's OIDC client at the identity provider,
// which is how the toolkit finds the client there and what the provider
// shows a person signing in. An app's display_name names its client when it
// sets one. Otherwise the client is named after the app, except the gate's:
// the gate is the community's one sign in rather than an app a member chose,
// so its client carries the community's name, and falls back to the app's
// only when the configuration gives the community none. The gate is decided
// by name rather than by blueprint so that `app remove gate`, run after the
// gate has left the configuration, still finds the client.
func (c *Config) ClientName(app string) string {
	if name := c.Apps[app].DisplayName; name != "" {
		return name
	}
	if app == GateApp && c.Community.Name != "" {
		return c.Community.Name
	}
	return app
}

// NeedsGate reports whether any app sets gated or auto_login, which is when
// the deployment runs the gate.
func (c *Config) NeedsGate() bool {
	for _, app := range c.Apps {
		if !app.Derived && app.UsesGate() {
			return true
		}
	}
	return false
}

// deriveGate adds the gate app when an app needs it: one oauth2-proxy at
// GateHostname, pinned to the first gateway site. The gate holds no data, so
// it sits with the edge it serves: it is down only when the gateway is, and
// that takes every app with it anyway. It does nothing when the name is
// already taken or no site is a gateway; structural refuses both.
func (c *Config) deriveGate() {
	if !c.NeedsGate() {
		return
	}
	if _, taken := c.Apps[GateApp]; taken {
		return
	}
	gateways := c.GatewaySites()
	if len(gateways) == 0 {
		return
	}
	c.Apps[GateApp] = App{
		Blueprint: BlueprintOAuth2Proxy,
		Hostname:  c.GateHostname(),
		Placement: Placement{Mode: PlacementPinned, Site: gateways[0]},
		Derived:   true,
	}
}

// SMTP is how an app sends mail. The deployment's block is the default for
// every app that sends any; an app's own block overrides it field by field, so
// one app can use a different sender or a different account without
// repeating the rest. The password is not here: it is a secret, under
// apps.<app>.smtp_password or else external.smtp_password.
type SMTP struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Security    string `yaml:"security"`
	Username    string `yaml:"username"`
	FromAddress string `yaml:"from_address"`
	FromName    string `yaml:"from_name"`
}

// SMTP security modes. none is plaintext, which validate accepts only for a
// host on a private network (smtp-plaintext-public-host); see "Mail is
// declared once and overridden per app" in README.md.
const (
	SMTPStartTLS = "starttls"
	SMTPTLS      = "tls"
	SMTPNone     = "none"
)

// PortOrDefault is the declared port, or the conventional one for the
// security mode: 465 for implicit TLS, 25 for plaintext, 587 for STARTTLS.
func (s SMTP) PortOrDefault() int {
	if s.Port != 0 {
		return s.Port
	}
	switch s.Security {
	case SMTPTLS:
		return 465
	case SMTPNone:
		return 25
	}
	return 587
}

// CanMail reports whether an app's effective SMTP settings name both a host
// and a sender, the two a mail needs before anything else.
func (c *Config) CanMail(app string) bool {
	s := c.SMTPFor(app)
	return s.Host != "" && s.FromAddress != ""
}

// SMTPFor is an app's effective SMTP settings: the deployment's block with
// every field the app declares replacing the deployment's.
func (c *Config) SMTPFor(app string) SMTP {
	out := c.SMTP
	o := c.Apps[app].SMTP
	if o == nil {
		return out
	}
	if o.Host != "" {
		out.Host = o.Host
	}
	if o.Port != 0 {
		out.Port = o.Port
	}
	if o.Security != "" {
		out.Security = o.Security
	}
	if o.Username != "" {
		out.Username = o.Username
	}
	if o.FromAddress != "" {
		out.FromAddress = o.FromAddress
	}
	if o.FromName != "" {
		out.FromName = o.FromName
	}
	return out
}

// smtpProblems checks one smtp block's shape, wherever it is declared.
func smtpProblems(key string, s SMTP) []string {
	var out []string
	if s.Security != "" && s.Security != SMTPStartTLS && s.Security != SMTPTLS && s.Security != SMTPNone {
		out = append(out, fmt.Sprintf("%s.security: unknown value %q. Valid values are starttls, tls and none.", key, s.Security))
	}
	if s.Port < 0 || s.Port > 65535 {
		out = append(out, fmt.Sprintf("%s.port: %d is not a port. Give one from 1 to 65535, or leave it out for 587 with starttls, 465 with tls and 25 with none.", key, s.Port))
	}
	return out
}

// PlacementMode is where an app runs. There are exactly two, and pinned is the
// plain case: an app exactly as upstream ships it, self contained at one site.
type PlacementMode string

const (
	PlacementCluster PlacementMode = "cluster"
	PlacementPinned  PlacementMode = "pinned"
	// PlacementInvalid records a placement the file declared that is neither of
	// the above. It is carried rather than returned as a decode error so that
	// validate can report it alongside every other problem in the file.
	PlacementInvalid PlacementMode = "invalid"
)

type Placement struct {
	Mode PlacementMode
	Site string
	// Literal is what the file actually said, quoted back in the error when the
	// placement is invalid.
	Literal string
}

// UnmarshalYAML accepts the two forms the design allows, `cluster` and
// `{ pinned: <site> }`, and records anything else as invalid instead of
// failing the decode.
func (p *Placement) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err == nil {
		if s == string(PlacementCluster) {
			p.Mode = PlacementCluster
			p.Literal = s
			return nil
		}
		p.Mode = PlacementInvalid
		p.Literal = s
		return nil
	}
	var m map[string]string
	if err := node.Decode(&m); err == nil && len(m) == 1 {
		if site, ok := m["pinned"]; ok && site != "" {
			p.Mode = PlacementPinned
			p.Site = site
			p.Literal = fmt.Sprintf("{ pinned: %s }", site)
			return nil
		}
	}
	p.Mode = PlacementInvalid
	p.Literal = describeNode(node)
	return nil
}

func describeNode(node *yaml.Node) string {
	var v any
	if err := node.Decode(&v); err != nil {
		return "unreadable value"
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return "unreadable value"
	}
	return trimTrailingNewline(string(out))
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// Load reads and structurally checks a configuration file. Unknown keys are an
// error: `trusted_proxies` is deliberately absent from the schema, and a typo
// that silently does nothing is worse than a refusal.
func Load(path string) (*Config, error) { return load(path, false) }

// LoadForInit is Load for `paisans init`, which is what gives a declaration
// its mesh.subnet: a file without one is accepted, and every other problem
// is refused as Load refuses it. Nothing else may run on such a file, since
// every address a host binds is in that subnet.
func LoadForInit(path string) (*Config, error) { return load(path, true) }

// LoadBytes is Load for a configuration held in memory, as a version in the
// configuration store holds it: the same strict decode and structural check,
// with no file behind it, so its Path is empty.
func LoadBytes(data []byte) (*Config, error) {
	cfg, err := parse(data, FileName, false)
	if err != nil {
		return nil, err
	}
	cfg.Path = ""
	cfg.resolveUsers()
	return cfg, nil
}

func load(path string, noSubnet bool) (*Config, error) {
	cfg, _, err := loadBytes(path, noSubnet)
	return cfg, err
}

// LoadWithBytes is Load, and the bytes it read: what apply publishes as the
// version it applies is then exactly what it loaded.
func LoadWithBytes(path string) (*Config, []byte, error) { return loadBytes(path, false) }

func loadBytes(path string, noSubnet bool) (*Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, readProblem(path, "Run paisans from the directory that holds "+FileName+", or name the file with --config.", err)
	}
	cfg, err := parse(data, path, noSubnet)
	return cfg, data, err
}

// parse decodes and checks data, naming it path in every problem.
func parse(data []byte, path string, noSubnet bool) (*Config, error) {
	dec := yaml.NewDecoder(newReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, parseProblem(path, "a configuration", err)
	}
	cfg.Path = path
	written, err := writtenLists(data)
	if err != nil {
		return nil, parseProblem(path, "a configuration", err)
	}
	cfg.deriveLists(written)
	// `backup:` with no value decodes to nil, which would read as no section
	// at all; a section written empty is a section, and backups are on.
	if written["backup"] && cfg.Backup == nil {
		cfg.Backup = &Backup{}
	}
	cfg.deriveGate()
	cfg.resolveKeys()
	cfg.resolveUsers()
	if cfg.Storage.Garage.Capacity == "" {
		cfg.Storage.Garage.Capacity = "100G"
	}
	if cfg.Storage.Garage.Consistency == "" {
		cfg.Storage.Garage.Consistency = GarageConsistent
	}
	if err := cfg.structural(noSubnet); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Lists a file may leave out, to be derived from the roles.
var (
	membersKey      = []string{"etcd", "members"}
	clusterSitesKey = []string{"cluster", "sites"}
	backupKey       = []string{"backup"}
)

// writtenLists reports which of etcd.members, cluster.sites and backup the
// file has, with any value, so an empty list or a key with no value counts as
// written.
// The decoded struct cannot tell a key left out from one written empty.
func writtenLists(data []byte) (map[string]bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return out, nil
	}
	for _, keys := range [][]string{membersKey, clusterSitesKey, backupKey} {
		out[strings.Join(keys, ".")] = walk(doc.Content[0], keys...) != nil
	}
	return out, nil
}

// deriveLists fills each of etcd.members and cluster.sites the file left out
// from the roles, and records that it did.
func (c *Config) deriveLists(written map[string]bool) {
	// A list behind a merge key decodes, and the node walk cannot see it.
	if !written["etcd.members"] && len(c.Etcd.Members) == 0 {
		c.Etcd.Members = c.DerivedEtcdMembers()
		c.Etcd.MembersDerived = true
	}
	if !written["cluster.sites"] && len(c.Cluster.Sites) == 0 {
		c.Cluster.Sites = c.DataSites()
		c.Cluster.SitesDerived = true
	}
}

// EtcdMembersKey names etcd.members in a message, saying when the list was
// derived, so an operator who never wrote the key is not sent looking for it.
func (c *Config) EtcdMembersKey() string {
	if c.Etcd.MembersDerived {
		return "etcd.members (derived from the roles)"
	}
	return "etcd.members"
}

// ClusterSitesKey names cluster.sites in a message, as EtcdMembersKey does.
func (c *Config) ClusterSitesKey() string {
	if c.Cluster.SitesDerived {
		return "cluster.sites (derived from the roles)"
	}
	return "cluster.sites"
}

// DerivedEtcdMembers is etcd.members as the roles give it: every witness, in
// name order, then every data site, in name order. A site with both, which
// validate refuses, is listed once, as a data site, so no quorum sum counts
// it twice.
func (c *Config) DerivedEtcdMembers() []string {
	var witnesses []string
	for _, name := range c.SiteNames() {
		if s := c.Sites[name]; s.Has(RoleWitness) && !s.Has(RoleData) {
			witnesses = append(witnesses, name)
		}
	}
	return append(witnesses, c.DataSites()...)
}

// Deployment is this configuration's identity and every name and path
// derived from it.
func (c *Config) Deployment() deployment.Deployment {
	return deployment.Deployment{ID: c.ID}
}

// SiteNames returns declared site names in sorted order. Rendering and
// reporting both iterate sites, and both must be deterministic.
func (c *Config) SiteNames() []string {
	names := make([]string, 0, len(c.Sites))
	for name := range c.Sites {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AppNames returns declared app names in sorted order.
func (c *Config) AppNames() []string {
	names := make([]string, 0, len(c.Apps))
	for name := range c.Apps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DataSites returns the sites holding the data role, sorted.
func (c *Config) DataSites() []string {
	var out []string
	for _, name := range c.SiteNames() {
		if c.Sites[name].Has(RoleData) {
			out = append(out, name)
		}
	}
	return out
}

// PocketIDApp is the deployment's one pocket-id app, and false when it has
// none or more than one. The account app signs in and creates accounts
// there, and validate refuses an account app without exactly one.
func (c *Config) PocketIDApp() (string, bool) {
	var found []string
	for _, name := range c.AppNames() {
		if c.Apps[name].Blueprint == BlueprintPocketID {
			found = append(found, name)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// AppsSites returns the sites holding the apps role, sorted.
func (c *Config) AppsSites() []string {
	var out []string
	for _, name := range c.SiteNames() {
		if c.Sites[name].Has(RoleApps) {
			out = append(out, name)
		}
	}
	return out
}

// PinnedTo returns the apps pinned to a site, sorted.
func (c *Config) PinnedTo(site string) []string {
	var out []string
	for _, name := range c.AppNames() {
		if p := c.Apps[name].Placement; p.Mode == PlacementPinned && p.Site == site {
			out = append(out, name)
		}
	}
	return out
}

// CaddySites returns the sites that run the toolkit's Caddy, sorted: every
// gateway, and every monitor serving its own apps.
func (c *Config) CaddySites() []string {
	var out []string
	for _, name := range c.SiteNames() {
		if c.Sites[name].RunsCaddy() {
			out = append(out, name)
		}
	}
	return out
}

// MonitorSites returns the sites holding the monitor role, sorted.
func (c *Config) MonitorSites() []string {
	var out []string
	for _, name := range c.SiteNames() {
		if c.Sites[name].Has(RoleMonitor) {
			out = append(out, name)
		}
	}
	return out
}

// GatewaySites returns the sites holding the gateway role, sorted.
func (c *Config) GatewaySites() []string {
	var out []string
	for _, name := range c.SiteNames() {
		if c.Sites[name].Has(RoleGateway) {
			out = append(out, name)
		}
	}
	return out
}

// structural checks the things that are wrong regardless of policy: missing
// required values, unknown enum members, malformed addresses. Every message
// names the key and says what to do about it.
func (c *Config) structural(noSubnet bool) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Version != 1 {
		add("version: expected 1, got %d. This toolkit reads version 1 files.", c.Version)
	}
	switch {
	case c.ID == "":
		add("id: required. It is this deployment's identity, a random UUID every name and path on a host is derived from. Run `paisans init`, which adds one and never changes it afterwards.")
	case !deployment.ValidID(c.ID):
		add("id: %q is not a lowercase version 4 UUID in canonical form (8-4-4-4-12 hex digits). It is written once by `paisans init`; remove the line and run `paisans init` to generate one, unless this deployment already runs somewhere, in which case restore the id it was deployed with.", c.ID)
	}
	if c.Community.Domain == "" {
		add("community.domain: required. It is the community's federation identity and cannot be changed later.")
	} else if !nameChars(c.Community.Domain) {
		add("community.domain: %q holds characters no DNS name has: a name is letters, digits, hyphens and dots. It is written into the gateway's Caddyfile and the apps' env files as it is, so anything else could add to them.", c.Community.Domain)
	}
	switch {
	case c.Mesh.Subnet == "" && noSubnet:
	case c.Mesh.Subnet == "":
		add("mesh.subnet: required. Run `paisans init`, which reaches every site, picks a /24 that overlaps nothing on any of them, and writes it here with each site's address moved into it. It is what applications trust for forwarded client addresses, so once a site is deployed it never changes.")
	default:
		network, problems := CheckMeshSubnet(c.Mesh.Subnet)
		for _, p := range problems {
			advice := "Write it as an IPv4 network address and a prefix length, for example 10.44.0.0/24."
			switch {
			case network != nil && strings.HasPrefix(p, "overlaps "):
				advice = "A host's firewall lets the deployment reach the mesh before it stops it reaching loopback, link-local and other reserved ranges, so the mesh must lie outside them; `paisans init` picks a /24 in private space."
			case network != nil:
				advice = "The mesh is what the hosts' firewalls trust, so a larger one could cover a LAN; `paisans init` picks a /24."
			}
			add("mesh.subnet: %q %s. %s", c.Mesh.Subnet, p, advice)
		}
	}

	if len(c.Sites) == 0 {
		add("sites: required. Declare at least one site.")
	}
	for _, name := range c.SiteNames() {
		site := c.Sites[name]
		if !blueprintName.MatchString(name) {
			add("sites.%q: %q is not a site name: lowercase letters, digits and hyphens, starting with a letter or digit, at most 63. The name is written into compose files, shell scripts and HAProxy and Patroni configuration, and names a directory of the render, so anything else could add to those files, run as a command, or write outside the directory.", name, name)
			continue
		}
		if len(site.Roles) == 0 && len(c.PinnedTo(name)) == 0 {
			add("sites.%s.roles: required. Give the site at least one of data, apps, gateway, witness, storage, monitor. A site may have none only when an app is pinned to it, because then it exists to host that app.", name)
		}
		for _, role := range site.Roles {
			if !knownRoles[role] {
				add("sites.%s.roles: unknown role %q. Valid roles are data, apps, gateway, witness, storage, monitor.", name, role)
			}
		}
		if in := site.WebServer; in != nil {
			if in.ManagedBy != "" && !knownManagedBy[in.ManagedBy] {
				add("sites.%s.web_server.managed_by: unknown value %q. Valid values are paisans, where the toolkit's Caddy serves the site's apps, and external, where your own web server does.", name, in.ManagedBy)
			}
			if in.Listen != "" {
				if _, _, ok := in.ListenHostPort(); !ok {
					add("sites.%s.web_server.listen: %q is not an IPv4 address and a port from 1 to 65535 written without leading zeros. Write it as the address your web server proxies to, for example 127.0.0.1:8480.", name, in.Listen)
				}
			}
		}
		if site.Address == "" {
			add("sites.%s.address: required. Every site needs a mesh address, and it never changes.", name)
		} else if !isIPv4(site.Address) {
			add("sites.%s.address: %q is not an IPv4 address. Use the site's WireGuard address, for example 10.44.0.1.", name, site.Address)
		} else if a := net.ParseIP(site.Address).To4().String(); a != site.Address {
			add("sites.%s.address: %q is not written as plain dotted IPv4, %s. Write it as four dotted numbers, for example 10.44.0.1.", name, site.Address, a)
		} else if _, mesh, err := net.ParseCIDR(c.Mesh.Subnet); err == nil && IsNetworkOrBroadcast(net.ParseIP(site.Address), mesh) {
			add("sites.%s.address: %s is mesh.subnet %s's network or broadcast address, which no host may hold. Give the site another address inside the mesh.", name, site.Address, c.Mesh.Subnet)
		}
		if site.Endpoint != "" {
			if _, err := ParseEndpointPort(site.Endpoint); err != nil {
				add("sites.%s.endpoint: %q %v. Write it as a host and a port, for example vm.example.org:51820, or as a port alone, for example \":51820\", beside a public_address: the port is also the one this site's WireGuard listens on.", name, site.Endpoint, err)
			} else if host, _, _ := net.SplitHostPort(site.Endpoint); host == "" && site.PublicAddress == "" {
				add("sites.%s.endpoint: %q is a port alone, which means public_address on that port, and the site declares no public_address. Declare it, for example public_address: 203.0.113.10, or write the endpoint as a host and a port.", name, site.Endpoint)
			} else if host != "" && net.ParseIP(host) == nil && !nameChars(host) {
				add("sites.%s.endpoint: %q names a host with characters no DNS name has: a name is letters, digits, hyphens and dots. It is written into every other site's WireGuard file as it is, and a line added there runs as root when the interface comes up.", name, site.Endpoint)
			}
		}
		// Each is written into WireGuard files as it is, for the same reason
		// as the endpoint; validate says which kind of address each must be.
		for _, a := range []struct{ key, value string }{{"public_address", site.PublicAddress}, {"public_address6", site.PublicAddress6}} {
			if a.value != "" && net.ParseIP(a.value) == nil {
				add("sites.%s.%s: %q is not an IP address. It is written into every other site's WireGuard file as it is, and a line added there runs as root when the interface comes up.", name, a.key, a.value)
			}
		}
		if site.Watchdog != "" && !knownWatchdogModes[site.Watchdog] {
			add("sites.%s.watchdog: unknown value %q. Valid values are auto, required, softdog and off.", name, site.Watchdog)
		}
		if site.Arch != "" && !hostarch.Supported(site.Arch) {
			add("sites.%s.arch: unknown value %q. The toolkit supports amd64 and arm64, and writes this line itself: delete it, and the next apply records what the host reports.", name, site.Arch)
		}
		problems = append(problems, sshProblems(name, site, c.ID, c.Operators)...)
	}
	problems = append(problems, c.operatorProblems()...)
	for _, name := range c.AppNames() {
		app := c.Apps[name]
		if !blueprintName.MatchString(name) {
			add("apps.%q: %q is not an app name: lowercase letters, digits and hyphens, starting with a letter or digit, at most 63. The name is the app's stack directory and compose project, so a slash, a dot or a capital would put its files somewhere else, outside the deployment or out of the review that reads every stack.", name, name)
			continue
		}
		if name == InfraStack {
			add("apps.%s: the name is the toolkit's infrastructure stack's. An app renders into a stack directory and a compose project named for it, beside the infrastructure stack, so this app's files would land on that stack's. Rename this app.", name)
		}
		if app.Blueprint == "" {
			add("apps.%s.blueprint: required. Say which application this is, for example mbin or wikijs: the blueprint it runs.", name)
		}
		if app.Hostname == "" {
			add("apps.%s.hostname: required. It is the public name the gateway routes to.", name)
		} else if p := HostnameProblem(app.Hostname); p != "" {
			add("apps.%s.hostname: %q is not a hostname: %s. It is written into the gateway's Caddyfile and the app's env file as it is, so anything else could add to them.", name, app.Hostname, p)
		}
		for _, service := range sortedKeys(app.Images) {
			if r, ok := ControlRune(app.Images[service]); ok {
				add("apps.%s.images.%s %s", name, keyPath(service), controlProblem(r))
			} else if strings.TrimSpace(app.Images[service]) == "" {
				add("apps.%s.images.%s: empty. Give a full image reference, or remove the key to take the default this blueprint ships.", name, keyPath(service))
			} else if !imageRef.MatchString(app.Images[service]) {
				add("apps.%s.images.%s: %s", name, keyPath(service), imageRefProblem(app.Images[service]))
			}
		}
		for _, role := range sortedKeys(app.Hostnames) {
			if role == "primary" {
				add("apps.%s.hostnames.primary: use the `hostname` key for the primary name. A role here names an additional hostname, and two places to write the same name is one place to get it wrong.", name)
			}
			if strings.TrimSpace(app.Hostnames[role]) == "" {
				add("apps.%s.hostnames.%s: empty. Give a hostname, or remove the role.", name, keyPath(role))
			} else if p := HostnameProblem(app.Hostnames[role]); p != "" {
				add("apps.%s.hostnames.%s: %q is not a hostname: %s. It is written into the gateway's Caddyfile as it is, so anything else could add to it.", name, keyPath(role), app.Hostnames[role], p)
			}
		}
		for _, key := range sortedKeys(app.Settings) {
			multiline := app.Blueprint == BlueprintAccount && key == "code_of_conduct"
			if r, ok := settingControlRune(app.Settings[key], multiline); ok {
				add("apps.%s.settings.%s %s A setting is written into the application's own configuration (JSON, INI, env or YAML), where a line break or another control character could add to it.", name, keyPath(key), controlProblem(r))
			}
		}
		if app.SMTP != nil {
			problems = append(problems, smtpProblems("apps."+name+".smtp", *app.SMTP)...)
		}
		if app.Blueprint == BlueprintOAuth2Proxy && !app.Derived {
			add("apps.%s.blueprint: oauth2-proxy is the gate, which the toolkit runs itself whenever an app sets gated or auto_login. Remove this app, and set gated: true or auto_login: true on the apps it was for.", name)
		}
		switch app.Storage {
		case "", StorageLocal, StorageS3:
		default:
			add("apps.%s.storage: unknown value %q. Valid values are local and s3, or leave it out for s3 when the deployment runs Garage and local when it does not.", name, app.Storage)
		}
	}
	if c.NeedsGate() {
		if app, ok := c.Apps[GateApp]; ok && !app.Derived {
			add("apps.%s: the name is the gate's. An app sets gated or auto_login, so the toolkit runs the gate as an app of that name. Rename this app.", GateApp)
		}
		if len(c.GatewaySites()) == 0 {
			add("apps: an app sets gated or auto_login, and no site holds the gateway role. The gate runs on the gateway, beside the Caddy that sends every request to it.")
		}
	}
	problems = append(problems, smtpProblems("smtp", c.SMTP)...)
	problems = append(problems, c.Infra.problems()...)
	if !c.Etcd.MembersDerived && len(c.Etcd.Members) == 0 {
		add("etcd.members: empty. No site would run etcd, so Patroni on every data site would have nowhere to keep its leader. Remove the key, and the voters are derived from the roles (every witness, then every data site), or list at least one site.")
	}
	if !c.Cluster.SitesDerived && len(c.Cluster.Sites) == 0 {
		add("cluster.sites: empty. Remove the key, and the cluster is derived from the roles (every data site), or list the data sites.")
	}
	if r, ok := ControlRune(c.ACME.Image); ok {
		add("acme.image %s", controlProblem(r))
	} else if c.ACME.Image != "" && !imageRef.MatchString(c.ACME.Image) {
		add("acme.image: %s", imageRefProblem(c.ACME.Image))
	}
	if c.ACME.Provider != "" && !acmeProvider.MatchString(c.ACME.Provider) {
		add("acme.provider: %q is not a provider's name: lowercase letters, digits and underscores, as caddy-dns names its modules (Eg: desec, cloudflare). It is written into the gateway's Caddyfile as it is, so anything else could add to it.", c.ACME.Provider)
	}
	if c.ACME.Provider == "" && len(c.CaddySites()) > 0 {
		// The providers are deliberately not listed here. This package does not
		// import internal/acme, by design, so any list written out would be a
		// second copy in prose that nothing keeps in step with the catalogue.
		// `validate` names them, built from acme.Providers.
		add("acme.provider: required, because %s runs the toolkit's Caddy (a gateway, or a monitor with managed_by: paisans). Certificates are issued over DNS-01, so the provider that answers the challenge has to be named. It must be one this toolkit publishes an image for, which `paisans validate` will list, or any other provider together with an acme.image carrying its module.", strings.Join(c.CaddySites(), ", "))
	}
	problems = append(problems, c.catalogProblems()...)
	// Resolving reads the catalogs, which may mean fetching them, so it runs
	// only on a file that is otherwise sound.
	if len(problems) == 0 && ResolveBlueprints != nil {
		problems = append(problems, ResolveBlueprints(c)...)
	}
	if len(problems) == 0 {
		return nil
	}
	return &LoadError{Path: c.Path, Problems: problems}
}

// sortedKeys returns a map's keys in sorted order, so that a file with several
// problems reports them in the same order every run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LoadError carries every structural problem found in one file, because
// fixing them one run at a time is miserable.
type LoadError struct {
	Path     string
	Problems []string
}

// Unwrap is the error as an operator reads it: one item per problem.
func (e *LoadError) Unwrap() error {
	items := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		items[i] = "- " + p
	}
	return &ui.Problem{Hint: ui.ShortPath(e.Path) + " is not a usable configuration", Explain: strings.Join(items, "\n")}
}

func (e *LoadError) Error() string {
	out := fmt.Sprintf("%s is not a usable configuration:", e.Path)
	for _, p := range e.Problems {
		out += "\n  " + p
	}
	return out
}

// CapacityFor is the capacity a Garage site advertises: its entry in
// Capacities, or the deployment's Capacity.
func (g Garage) CapacityFor(site string) string {
	if c := strings.TrimSpace(g.Capacities[site]); c != "" {
		return c
	}
	return g.Capacity
}

// sizeShape is a size as Garage's -c flag takes it: a number, then a unit.
var sizeShape = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([KMGTP]?)(I?)(B?)$`)

// ParseSize reads a size such as 100G, 3GB, 3GiB or 1.5T into bytes. K, M, G,
// T and P are decimal and Ki, Mi, Gi, Ti and Pi binary, with or without a
// trailing B, matching what dxflrs/garage:v1.0.1 accepts for a capacity and
// how it displays one (observed: 3G and 3GB show as 3.0 GB, 3GiB as 3.2 GB).
// A bare number is refused: Garage requires the unit, and so does this.
func ParseSize(s string) (int64, error) {
	m := sizeShape.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil || m[2] == "" {
		return 0, fmt.Errorf("%q is not a size with a unit, Eg: 100G, 3GiB or 1.5T", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive size", s)
	}
	base := 1000.0
	if m[3] == "I" {
		base = 1024
	}
	power := strings.Index("KMGTP", m[2]) + 1
	for i := 0; i < power; i++ {
		n *= base
	}
	return int64(n), nil
}
