package render

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/crypto/curve25519"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// The `all:` prefix matters: without it embed skips files beginning with a
// dot, and every blueprint configured by environment ships a `.env` template.
//
//go:embed all:templates
var templateFS embed.FS

// templates holds the infrastructure templates, which are shared by every
// deployment. An application's templates are not here: each blueprint owns a
// directory under templates/, rendered as a set, so that adding an application
// adds a directory rather than a branch in a file every other application
// shares.
var templates = template.Must(template.New("infra").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.tmpl"))

// templateFuncs is the whole function surface a template gets. It is small on
// purpose: logic that needs more than this belongs in Go, where it can be
// tested.
var templateFuncs = template.FuncMap{
	"quote":  quote,
	"indent": indent,
	// regexquote escapes a value for a regular expression, so a hostname's
	// dots match only dots.
	"regexquote": regexp.QuoteMeta,
	// gateSessionCookie is the gate's session cookie name, so the auto-login
	// matcher and the Go constant cannot disagree.
	"gateSessionCookie": func() string { return GateSessionCookie },
	// gateMarker is the body of the gate's own redirects, so the monitor can
	// tell the gate's 302 from an app's.
	"gateMarker": func() string { return GateMarker },
	// configFile is the deployment declaration's name, so a rendered file's
	// "change this instead" comment names the file the toolkit reads.
	"configFile": func() string { return config.FileName },
	// envquote quotes a value for a .env exactly as a passthrough key is
	// quoted, for a value an operator supplied rather than one generated.
	"envquote": envquote,
	// yamlquote writes a string as a double quoted YAML scalar, for a value
	// an operator supplied: a JSON string is one, with every quote, backslash
	// and line break escaped, so the value cannot end the scalar early.
	"yamlquote": yamlquote,
	// jsonquote writes a string as a JSON string, quotes included, for a
	// template that writes an operator's setting into JSON: a quote or a
	// backslash in it cannot end the string early.
	"jsonquote": jsonquote,
	// stableULID is a ULID derived from an app's name (blueprints.StableULID),
	// for software that must be told one and given the same one every run.
	"stableULID": blueprints.StableULID,
	"yesno": func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	},
}

// indent prefixes every line of a value with n spaces, so that a multi line
// secret can be placed into a YAML block scalar. A PEM encoded key is the case
// that needs it: unindented, its second line ends the block and the rest of the
// file becomes a parse error.
//
// A trailing newline is dropped, because the template supplies the line break
// after the value and two would render a blank line inside the block.
func indent(n int, value string) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}

// secretSuffix marks a template whose rendered file is written 0600.
//
// The mode travels with the template rather than in a table somewhere else,
// for the same reason the destination path does: a file and the facts about it
// should not be able to drift apart. Everything else is 0644, because a
// rendered file a container's own user cannot read is a stack that does not
// start.
const secretSuffix = ".secret"

// infraImages are the images the infrastructure stack runs, each the
// configuration's (infra) or else the core catalog foundation's: a track
// apply resolves while it plans, or a reference to one build. Spilo's is the
// deployment's Postgres major's, since Spilo publishes one repository per
// major.
type infraImages struct {
	etcd, spilo, haproxy, garage, heartbeat string
}

// infraOrigins is the image of each service the infrastructure template
// writes but the gateway's Caddy, keyed by compose service name: what infra
// declares, or else the core catalog foundation's. Spilo's is the
// deployment's Postgres major's, since Spilo publishes one repository per
// major, and infra.postgres declares it per major for the same reason.
func (p *planner) infraOrigins() (map[string]imageOrigin, error) {
	out := map[string]imageOrigin{}
	for service, name := range map[string]string{
		"etcd":      blueprints.FoundationEtcd,
		"haproxy":   blueprints.FoundationHAProxy,
		"garage":    blueprints.FoundationGarage,
		"heartbeat": blueprints.FoundationHeartbeat,
	} {
		o, err := declaredOr(p.cfg.Infra.Images[name], name)
		if err != nil {
			return nil, err
		}
		out[service] = o
	}
	spilo, err := blueprints.SpiloImage(p.postgresVersion())
	if err != nil {
		return nil, fmt.Errorf("cluster.postgres_version: %w", err)
	}
	out["patroni"] = imageOrigin{image: spilo, foundation: true}
	if declared := p.cfg.SpiloImage(); declared != "" {
		out["patroni"] = imageOrigin{image: declared, declared: true, replaces: spilo}
	}
	return out, nil
}

// infraSetting is the configuration key that decides an infrastructure
// service's image.
func (p *planner) infraSetting(service string) string {
	switch service {
	case "patroni":
		return "infra.postgres." + p.postgresVersion() + ".spilo"
	case "caddy":
		return "acme.image"
	}
	return "infra.images." + service
}

func (p *planner) infraImages() (infraImages, error) {
	o, err := p.infraOrigins()
	if err != nil {
		return infraImages{}, err
	}
	return infraImages{
		etcd: o["etcd"].image, spilo: o["patroni"].image, haproxy: o["haproxy"].image,
		garage: o["garage"].image, heartbeat: o["heartbeat"].image,
	}, nil
}

// caddyImage is the image the gateway runs, as the configuration names it.
//
// A DNS provider in Caddy is a module compiled into the binary, not a setting,
// so the provider decides the image. A declared image wins, because it is the
// only way a provider the toolkit publishes nothing for can reach a gateway now
// that nothing is built on a host.
//
// What this returns may be a tag that moves, and is rendered as it is: render
// reaches no network, so it cannot know which build a tag names today. apply
// does, and pins the digest into the rendered file before comparing it with
// the host's (see PinImages and Plan.Images).
func (p *planner) caddyImage() (string, error) {
	if declared := p.cfg.ACME.Image; declared != "" {
		return declared, nil
	}
	if !acme.Published(p.cfg.ACME.Provider) {
		// Kept rather than deleted, and unreachable through the CLI: `paisans`
		// runs validate first, and acme-provider-needs-an-image refuses this
		// configuration there with a message written for an operator. This is
		// the library level guard for a caller that reaches render.Build
		// without validating, which is a supported way to use this package.
		// Without it the template would write `image: ` and the operator would
		// meet the problem as a compose file the host rejects.
		return "", fmt.Errorf(
			"acme.provider %q has no image in this toolkit and acme.image declares none. A DNS provider is a module compiled into Caddy, so an image carrying it is the only way it reaches a gateway. Published providers: %s",
			p.cfg.ACME.Provider, strings.Join(acme.Providers(), ", "))
	}
	return blueprints.FoundationImage(blueprints.FoundationCaddy)
}

type clusterMember struct {
	Name    string
	Address string
}

type peerView struct {
	Name       string
	PublicKey  string
	AllowedIPs string
	Endpoint   string
}

type route struct {
	App string
	// Role is here so a later task can gate the host block on it: whether an
	// import belongs in this block is a fact about which hostname it is, not
	// about the app in general.
	Role      string
	Hostname  string
	Upstreams []string
	// Snippet is where this hostname's own routing lives on the gateway, as
	// the container sees it. The host block imports it rather than
	// containing it.
	Snippet string
	// Gated puts this hostname behind the gate. It is the app's gated key,
	// carried onto every one of its hostnames, with one exception: a media
	// hostname is never gated (see routes).
	Gated bool
	// AutoLogin is the app's auto_login key, carried the same way. On a
	// public hostname it is the only use the host block makes of the gate.
	AutoLogin bool
	// What a hostname using the gate lets past it, and its auto-login, from
	// the blueprint's GateSpec. Empty on a hostname that does not use the gate.
	// See docs/specs/2026-10-08-visibility-gate.md.
	Bypass gateBypass
}

// gateBypass is what reaches a gated app without a gate session, in the order
// the host block matches it: ActivityPub, auto-login, open paths, token paths.
type gateBypass struct {
	// ActivityPub is true for an app that federates. Its signed fetch is what
	// authenticates these requests, and validation refuses a gated
	// federating app with none.
	ActivityPub bool
	InboxPaths  []string
	AutoLogin   []blueprints.AutoLoginRule
	// LoopBreaker is the cookie an auto-login redirect sets, so a user the
	// app refuses after the gate admitted them is redirected once rather
	// than forever.
	LoopBreaker string
	// SignInPath is the app's sign-in page, which a public hostname with
	// auto-login sends through the gate's sign-in when there is no gate
	// session, and SignInBreaker the cookie that stops it doing so twice in
	// two minutes.
	SignInPath    string
	SignInBreaker string
	OpenPaths     []string
	TokenPaths    []string
}

// GateSessionCookie is the gate's session cookie: oauth2-proxy's default
// name, which the rendered .env does not change. Auto-login fires only when
// it is present, so an anonymous visitor always meets the gate first.
const GateSessionCookie = "_oauth2_proxy"

// GateMarker is the body of every redirect the gate snippets send. An app
// behind the gate can answer `/` with a 302 of its own (Eg: a private Mbin or
// WriteFreely, to its sign-in page), so the monitor's gate check asserts this
// body rather than the status alone, and fails when the gate is gone.
const GateMarker = "Sign in required by the visibility gate."

// bypassFor is what an app using the gate lets past it, from its blueprint's
// GateSpec. Auto-login is carried only when the app sets auto_login.
//
// An app keeping public files on its own disk also opens the paths they are
// served on (blueprints.LocalMediaPaths). In object storage they are on a media
// hostname, which is never gated; on the app's own hostname they would be,
// and a federating server fetching an image will not follow a redirect to a
// passkey prompt any more than it would there.
func bypassFor(cfg *config.Config, name string, app config.App) gateBypass {
	spec := blueprints.GateFor(app.Blueprint)
	b := gateBypass{
		ActivityPub: blueprints.Federates(app),
		InboxPaths:  spec.InboxPaths,
		OpenPaths:   append(blueprints.OpenPathsFor(app.Blueprint), blueprints.LocalMediaPaths(cfg, app)...),
		TokenPaths:  spec.TokenPaths,
	}
	if app.AutoLogin {
		b.AutoLogin = spec.AutoLogin
		b.LoopBreaker = name + "_autologin"
		b.SignInPath = spec.SignInPath
		b.SignInBreaker = name + "_gatesignin"
	}
	return b
}

func (p *planner) renderSite(site *siteView) ([]File, error) {
	var files []File
	base := site.Name + "/"

	wg, err := p.renderWireGuard(site)
	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: base + p.cfg.Deployment().WireGuardConf(), Content: wg, Mode: 0o600})

	images, err := p.infraImages()
	if err != nil {
		return nil, err
	}
	// Resolved only where Caddy runs, a gateway or a monitor serving its own
	// apps: any other site needs no Caddy image, and acme.provider is not
	// even required in a deployment where Caddy runs nowhere, so demanding one
	// here would refuse a legitimate configuration over an image nothing will
	// use. The template gates the caddy service on Site.RunsCaddy, so an
	// empty string here never reaches a compose file.
	var caddy string
	if site.RunsCaddy {
		caddy, err = p.caddyImage()
		if err != nil {
			return nil, err
		}
	}
	initial := p.etcdInitialFor(site.Name)
	infra, err := p.renderTemplate("infra-compose.yaml.tmpl", map[string]any{
		"Site":                    site,
		"Project":                 p.dep().Project("infra"),
		"DeploymentLabel":         deployment.Label,
		"DeploymentID":            p.dep().ID,
		"Dir":                     p.dep().Dir("infra"),
		"Scope":                   p.scope(),
		"EtcdInitialCluster":      initial.Cluster,
		"EtcdInitialState":        initial.State,
		"EtcdInitialToken":        initial.EtcdToken(),
		"HeartbeatMS":             p.heartbeatMS(),
		"ElectionTimeoutMS":       p.electionTimeoutMS(),
		"PostgresVersion":         p.postgresVersion(),
		"SpiloImage":              images.spilo,
		"EtcdImage":               images.etcd,
		"HAProxyImage":            images.haproxy,
		"GarageImage":             images.garage,
		"CaddyImage":              caddy,
		"HostSites":               HostSitesMount,
		"HostSitesSource":         HostSitesDir(p.dep()),
		"CaddyConfig":             CaddyConfigMount,
		"CaddyConfigSource":       "/" + CaddyConfigDir(p.dep()),
		"EtcdClientPort":          etcdClientPort,
		"EtcdPeerPort":            etcdPeerPort,
		"EtcdCompactionRetention": etcdCompactionRetention,
		"EtcdQuotaBackendBytes":   etcdQuotaBackendBytes,
		"HeartbeatImage":          images.heartbeat,
	})
	if err != nil {
		return nil, err
	}
	// A site with no roles exists only to host what is pinned to it and runs
	// no infrastructure, so it gets no infra stack: a compose project with an
	// empty services map is nothing apply could start or recreate.
	if site.runsInfra() {
		files = append(files, File{Path: base + p.dep().RelPath("infra", "compose.yaml"), Content: infra, Mode: 0o644})
	}

	if len(site.Heartbeats) > 0 {
		// The URLs carry the site's token, so they go in a 0600 env file the
		// service reads through env_file, and the script that pushes them is
		// a separate 0644 file with no secret in it; the compose file names
		// neither value. A changed env file recreates the service, which is
		// how a new monitor's URL reaches a host (apply, isEnvironment).
		urls, err := p.heartbeatURLs(site.Name, site.Heartbeats)
		if err != nil {
			return nil, err
		}
		env, err := p.renderTemplate("heartbeat.env.tmpl", map[string]any{"Site": site, "URLs": urls})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "heartbeat", "heartbeat.env"), Content: env, Mode: 0o600})
		push, err := p.renderTemplate("heartbeat-push.sh.tmpl", map[string]any{"Site": site})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "heartbeat", "push.sh"), Content: push, Mode: 0o644})
	}
	if site.IsEtcd {
		files = append(files, File{Path: base + EtcdInitialPath(p.dep()), Content: FormatEtcdInitial(initial), Mode: 0o644})
	}

	if site.IsData {
		env, err := p.renderTemplate("patroni.env.tmpl", map[string]any{
			"Site":              site,
			"Scope":             p.scope(),
			"EtcdClientHosts":   p.etcdClientHosts(),
			"PatroniAPIPort":    patroniAPIPort,
			"BgMonPort":         bgMonPort,
			"PostgresPort":      postgresPort,
			"SuperuserPassword": p.secrets.Cluster.SuperuserPassword,
			"StandbyPassword":   p.secrets.Cluster.StandbyPassword,
			"AdminPassword":     p.secrets.Cluster.AdminPassword,
			"Synchronous":       boolString(p.cfg.Cluster.Synchronous),
			"SynchronousStrict": boolString(p.cfg.Cluster.SynchronousStrict),
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "patroni.env"), Content: env, Mode: 0o600})
	}

	if site.NeedsProxy {
		cfg, err := p.renderTemplate("haproxy.cfg.tmpl", map[string]any{
			"Address":        site.Address,
			"ClusterPort":    p.clusterPort(),
			"ClusterMembers": p.clusterMembers(),
			"PostgresPort":   postgresPort,
			"PatroniAPIPort": patroniAPIPort,
			"StatsPort":      haproxyStatsPort,
			// Pocket ID's API on the mesh: README, "Pocket ID runs on every
			// apps site, and one of them is active".
			"PocketIDServers": p.pocketIDServers(),
			"PocketIDPort":    blueprints.Port(config.BlueprintPocketID),
			"PocketIDAPIPort": pocketIDAPIPort,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "haproxy", "haproxy.cfg"), Content: cfg, Mode: 0o644})
	}

	if site.IsGarage {
		// No fallback onto admin_token, which is what this used to do.
		// admin_token is a base64 password and rpc_secret is parsed as a hex
		// encoded 32 byte key, so the fallback could only ever render a file
		// Garage refuses to start against: "Invalid RPC secret key: expected
		// 32 bits of entropy". Failing by name here sends an operator to
		// `paisans init`, which generates one in the right shape.
		rpcSecret := p.secrets.Storage.Garage.RPCSecret
		if rpcSecret == "" {
			return nil, fmt.Errorf("secrets storage.garage.rpc_secret: required for a site with a Garage role, and there is no fallback. Garage parses it as 64 hex characters and will not start without one. Run `%s init` to generate it", progname.Cmd())
		}
		toml, err := p.renderTemplate("garage.toml.tmpl", map[string]any{
			"Site":        site,
			"Domain":      p.cfg.Community.Domain,
			"Replication": p.replication(),
			"Consistency": p.consistency(),
			"RPCSecret":   rpcSecret,
			"AdminToken":  p.secrets.Storage.Garage.AdminToken,
			"S3Port":      garageS3Port,
			"RPCPort":     garageRPCPort,
			"WebPort":     garageWebPort,
			"AdminPort":   garageAdminPort,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "garage", "garage.toml"), Content: toml, Mode: 0o600})
	}

	if site.RunsCaddy {
		// A gateway routes every app but another site's monitor's; a monitor
		// that is not a gateway serves only the apps pinned to it. Both are the same Caddyfile, image and
		// certificate story, so a monitor's edge is the gateway's minus the
		// other hostnames.
		routes := p.routesFor(site)
		gates := site.IsGateway || usesGate(routes)
		var mounts []string
		if gates {
			mounts = p.gateSnippetMounts()
		}
		caddyfile, err := p.renderTemplate("Caddyfile.tmpl", map[string]any{
			"Domain":          p.cfg.Community.Domain,
			"Routes":          routes,
			"GateSnippets":    mounts,
			"TrustedProxies":  p.mesh,
			"ACMEDirective":   acme.Directive(p.cfg.ACME.Provider),
			"HostSites":       hostSitesMount(site),
			"HostSitesSource": HostSitesDir(p.dep()),
			"Token":           p.dep().Token(),
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + Caddyfile(p.dep()), Content: caddyfile, Mode: 0o644})

		snippets, err := p.renderSnippets(base, site, routes)
		if err != nil {
			return nil, err
		}
		files = append(files, snippets...)

		if gates {
			gateSnippets, err := p.renderGateSnippets(base)
			if err != nil {
				return nil, err
			}
			files = append(files, gateSnippets...)
		}

		env, err := p.renderTemplate("caddy.env.tmpl", map[string]any{
			"ACMEDNSToken": p.secrets.External["acme_dns_token"],
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.dep().RelPath("infra", "caddy", "caddy.env"), Content: env, Mode: 0o600})
	}

	for _, app := range site.Apps {
		appFiles, err := p.renderTemplateSet(base, app)
		if err != nil {
			return nil, err
		}
		files = append(files, appFiles...)
		pusher, err := p.operatorsHeartbeatFiles(base, app)
		if err != nil {
			return nil, err
		}
		files = append(files, pusher...)
	}

	return files, nil
}

// runsInfra reports whether the site runs any infrastructure service, and so
// gets an infra stack. The heartbeat counts: a site watched by a monitor
// elsewhere pushes to it from here, whatever its roles.
func (site *siteView) runsInfra() bool {
	return site.IsEtcd || site.IsData || site.NeedsProxy || site.IsGarage || site.RunsCaddy || len(site.Heartbeats) > 0
}

// hostSitesMount is where the Caddyfile imports the host's own site blocks
// from: the gateway's mount of HostSitesDir, and nothing on a monitor, which
// serves only the apps pinned to it.
func hostSitesMount(site *siteView) string {
	if site.IsGateway {
		return HostSitesMount
	}
	return ""
}

// snippetTemplate is the file in a blueprint's set that describes how the
// gateway routes to it on its primary hostname. It is rendered onto the gateway
// rather than beside the app, because that is where it is read.
//
// snippetPrefix matches it and every extra hostname's own snippet, such as
// caddy.snippet.wellknown.tmpl, all of which belong to the gateway for the
// same reason.
const (
	snippetTemplate = "caddy.snippet.tmpl"
	snippetPrefix   = "caddy.snippet."
)

// snippetMount is the directory snippets land in, as the Caddy container sees
// it. The Caddyfile imports by it, because Caddy reads it from inside the
// container.
const snippetMount = CaddyConfigMount + "/snippets/"

// snippetDir is the same directory on the gateway, in the rendered tree.
func (p *planner) snippetDir() string { return SnippetsDir(p.dep()) + "/" }

// dep is the deployment every rendered path and name derives from.
func (p *planner) dep() deployment.Deployment { return p.cfg.Deployment() }

// renderSnippets renders every hostname's routing onto a site running Caddy.
//
// On a gateway the routes come from the whole configuration rather than the
// gateway's own apps: a gateway routes to applications that run elsewhere,
// which is the usual case. One route renders one snippet, from the template
// its role selects, because two hostnames on the same app can serve entirely
// different things.
func (p *planner) renderSnippets(base string, site *siteView, routes []route) ([]File, error) {
	var files []File
	for _, r := range routes {
		app := p.cfg.Apps[r.App]
		planned, err := p.plannedFor(r.App, app)
		if err != nil {
			return nil, err
		}
		values, err := p.values(planned, app)
		if err != nil {
			return nil, err
		}
		// The snippet names the hostname it serves, not the app's primary: the
		// apex snippet for a homeserver has to say the apex, never the API
		// name, or its own comments would lie about which name reaches it.
		values.Hostname = r.Hostname
		values.PublicURL = "https://" + r.Hostname
		// Whether an upstream is elsewhere depends on whose Caddy this is:
		// the gateway's, a monitor's, or a gateway being handed off, which
		// reaches the new gateway's apps over the mesh.
		values.UpstreamElsewhere = elsewhere(site, values.Upstreams)
		values.S3.GarageElsewhere = elsewhere(site, values.S3.GarageAddresses)

		path := string(app.Blueprint) + "/" + blueprints.SnippetFor(r.Role)
		content, err := p.renderFile(path, values)
		if err != nil {
			return nil, err
		}
		name := r.App
		if r.Role != blueprints.PrimaryRole {
			name = r.App + "-" + r.Role
		}
		files = append(files, File{Path: base + p.snippetDir() + name + ".caddy", Content: content, Mode: 0o644, App: r.App})
	}
	return files, nil
}

// gateSnippetTemplate defines the oauth2-proxy blueprint's named Caddy
// snippets, gate, gate_autologin and gate_signin. It is not a hostname's
// routing: nothing imports it by path, and no host block is rendered for it. A
// later blueprint's own snippet imports the names it defines, the same way the
// deployment this toolkit generalises imports its single `(pocketid_gate)`
// snippet by name.
const gateSnippetTemplate = "caddy.snippet.gates.tmpl"

// GatesSnippet is what an oauth2-proxy app's named gate snippets file is
// suffixed with on the gateway: <app>-gates.caddy, beside its routes.
const GatesSnippet = "gates"

// CaddyConfigDir is where a site's Caddyfile and snippets land, relative to
// the host's /: <root>/infra/caddy/etc, which the Caddy container mounts
// whole at CaddyConfigMount. A directory, never the Caddyfile on its own: a
// file is replaced by renaming a new one over it, and a single file bind
// mount keeps the file the container was created with, so a reload would
// read the Caddyfile of the apply before. Nothing else is rendered into it,
// because the container sees all of it: caddy.env, data and config stay
// beside it.
func CaddyConfigDir(d deployment.Deployment) string { return d.RelPath("infra", "caddy", "etc") }

// Caddyfile is the Caddyfile in CaddyConfigDir, relative to the host's /.
func Caddyfile(d deployment.Deployment) string { return CaddyConfigDir(d) + "/Caddyfile" }

// CaddyConfigMount is CaddyConfigDir as the Caddy container sees it.
const CaddyConfigMount = "/etc/caddy"

// SnippetsDir is where a site's Caddy snippets land, relative to the host's
// /: CaddyConfigDir's snippets.
func SnippetsDir(d deployment.Deployment) string { return CaddyConfigDir(d) + "/snippets" }

// renderGateSnippets renders every oauth2-proxy app's named gate snippets onto
// the gateway. It walks every app in the configuration, not just this site's
// own, for the same reason renderSnippets does: the gateway routes to
// applications wherever they run.
func (p *planner) renderGateSnippets(base string) ([]File, error) {
	var files []File
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		if app.Blueprint != config.BlueprintOAuth2Proxy {
			continue
		}
		planned, err := p.plannedFor(name, app)
		if err != nil {
			return nil, err
		}
		values, err := p.values(planned, app)
		if err != nil {
			return nil, err
		}
		path := string(app.Blueprint) + "/" + gateSnippetTemplate
		content, err := p.renderFile(path, values)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: base + p.snippetDir() + name + "-" + GatesSnippet + ".caddy", Content: content, Mode: 0o644, App: name})
	}
	return files, nil
}

// gateSnippetMounts is where each oauth2-proxy app's gate snippet file lands,
// as the Caddy container sees it. The Caddyfile imports each at the top
// level, before any site block, because a named Caddy snippet has to be
// defined before something else can import it by name.
func (p *planner) gateSnippetMounts() []string {
	var out []string
	for _, name := range p.cfg.AppNames() {
		if p.cfg.Apps[name].Blueprint == config.BlueprintOAuth2Proxy {
			out = append(out, snippetMount+name+"-gates.caddy")
		}
	}
	return out
}

// plannedFor rebuilds an app's planned form for the gateway, which needs its
// hostname and port without caring where it runs.
func (p *planner) plannedFor(name string, app config.App) (plannedApp, error) {
	images, err := p.appImages(app)
	if err != nil {
		return plannedApp{}, err
	}
	return plannedApp{
		Name:      name,
		Dir:       p.dep().Dir(name),
		Blueprint: app.Blueprint,
		Hostname:  app.Hostname,
		Pinned:    app.Placement.Mode == config.PlacementPinned,
		Site:      app.Placement.Site,
		Port:      blueprints.Port(app.Blueprint),
		DBName:    dbIdentifier(name),
		DBUser:    dbIdentifier(name),
		Images:    images,
	}, nil
}

// TemplateSet lists the templates a blueprint ships, as paths inside the
// embedded filesystem. It exists so a test can assert that a set is complete:
// embed sees what git checked out, so a template left uncommitted is absent
// here even though it is present on the machine that wrote it.
func TemplateSet(blueprint config.Blueprint) ([]string, error) {
	dir := string(blueprint)
	var out []string
	err := fs.WalkDir(blueprints.Templates, dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("blueprint %s ships no template set: %w", blueprint, err)
	}
	sort.Strings(out)
	return out, nil
}

// renderTemplateSet renders every file in a blueprint's template directory.
//
// A template's path is its destination: <blueprint>/config/packages/x.yaml in
// the blueprint's templates (internal/blueprints)
// lands at /srv/paisans/<token>/<stack>/config/packages/x.yaml, so nothing holds a separate
// mapping of template to location and a new file in a set needs no code.
func (p *planner) renderTemplateSet(base string, app plannedApp) ([]File, error) {
	// The stack's directory is one directory under the deployment's root,
	// named for the app. A name that is not one segment would put the
	// stack's files elsewhere, out of the root or out of the review that
	// reads every stack (config refuses it; this is the render's own check).
	if stack := p.dep().RelPath(app.Name); path.Dir(stack) != p.dep().RelPath() || path.Base(stack) != app.Name {
		return nil, fmt.Errorf("apps.%s: the name puts the stack's files outside the deployment's own directory for it, %s", app.Name, p.dep().RelPath(app.Name))
	}
	dir := string(app.Blueprint)
	entries, err := fs.ReadDir(blueprints.Templates, dir)
	if err != nil {
		return nil, fmt.Errorf("blueprint %s ships no template set: %w", app.Blueprint, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("blueprint %s ships an empty template set, so the stack would be rendered with no configuration at all", app.Blueprint)
	}

	values, err := p.values(app, p.cfg.Apps[app.Name])
	if err != nil {
		return nil, err
	}

	var files []File
	sources := map[string]string{}
	err = fs.WalkDir(blueprints.Templates, dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".tmpl") {
			return fmt.Errorf("%s is in a template set but is not a template. Every file in a set is rendered, so there is nowhere for a plain file to go", path)
		}
		if strings.HasPrefix(strings.TrimPrefix(path, dir+"/"), snippetPrefix) {
			// Routing belongs to the gateway, not to the app's own directory,
			// whichever hostname it is for.
			return nil
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(path, dir+"/"), ".tmpl")
		mode := uint32(0o644)
		if strings.HasSuffix(rel, secretSuffix) {
			rel = strings.TrimSuffix(rel, secretSuffix)
			mode = 0o600
		}

		content, err := p.renderFile(path, values)
		if err != nil {
			return err
		}
		if _, fromCatalog := blueprints.Source(app.Blueprint); fromCatalog && rel == "compose.yaml" {
			if err := checkComposePolicy(content, values.DataPath); err != nil {
				return fmt.Errorf("apps.%s: blueprint %s: %w", app.Name, app.Blueprint, err)
			}
		}
		dest := base + p.dep().RelPath(app.Name, rel)
		files = append(files, File{Path: dest, Content: content, Mode: mode})
		sources[dest] = path
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Only when the app declares keys: an app without them renders exactly
	// what its templates wrote, with no merge in the way.
	if keys := p.cfg.Apps[app.Name].Config; len(keys) > 0 {
		if err := mergeConfig(app, keys, base+p.dep().RelPath(app.Name)+"/", files, sources); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// renderFile renders one template from a blueprint's set. Each is parsed on its
// own rather than into the shared set, so that two blueprints may name a file
// the same thing, which they routinely do.
func (p *planner) renderFile(path string, values appValues) (string, error) {
	tmpl, err := template.New(filepath.Base(path)).Funcs(templateFuncs).ParseFS(blueprints.Templates, path)
	if err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("rendering %s: %w", path, err)
	}
	return buf.String(), nil
}

func (p *planner) renderTemplate(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("rendering %s: %w", name, err)
	}
	return buf.String(), nil
}

// renderWireGuard builds one site's interface and peer list.
//
// A site with a dial endpoint (config.Site.DialEndpoint: its public_address,
// or the endpoint it writes) is dialled by everyone else. Two sites that both
// lack one cannot dial each other, so their traffic relays through a site
// that has one. Relaying keeps this to static configuration with no daemon and no
// coordination service. The relay forwards with the host's
// net.ipv4.ip_forward, which site admit turns on for a site with an
// endpoint: the file carries no PostUp, because wg-quick runs one as root
// and the WireGuard helper refuses any.
func (p *planner) renderWireGuard(site *siteView) (string, error) {
	private := p.secrets.Sites[site.Name].WireGuardPrivateKey
	if _, err := publicKey(private); err != nil {
		return "", fmt.Errorf("secrets sites.%s.wireguard_private_key: %w", site.Name, err)
	}

	relays := p.relays()
	isRelay := site.Endpoint != ""

	var peers []peerView
	assignedMesh := false
	for _, name := range p.order {
		if name == site.Name {
			continue
		}
		other := p.sites[name]
		otherPublic, err := publicKey(p.secrets.Sites[name].WireGuardPrivateKey)
		if err != nil {
			return "", fmt.Errorf("secrets sites.%s.wireguard_private_key: %w", name, err)
		}
		if !p.cfg.Sites[site.Name].PeersDirectly(p.cfg.Sites[name]) {
			// Neither side is dialled. Nothing to configure: the traffic
			// goes through a relay.
			continue
		}
		switch {
		case other.Endpoint != "":
			// The other side has a stable address, so this side dials it.
			allowed := other.Address + "/32"
			if !isRelay && !assignedMesh && contains(relays, name) {
				// Route everything not directly reachable through the first
				// relay, in sorted order so the choice is deterministic.
				allowed = p.mesh
				assignedMesh = true
			}
			peers = append(peers, peerView{
				Name:       name,
				PublicKey:  otherPublic,
				AllowedIPs: allowed,
				Endpoint:   other.Endpoint,
			})
		default:
			// This side is dialled by the other, so no endpoint is known here.
			peers = append(peers, peerView{
				Name:       name,
				PublicKey:  otherPublic,
				AllowedIPs: other.Address + "/32",
			})
		}
	}

	return p.renderTemplate("wireguard.conf.tmpl", map[string]any{
		"Site":       site,
		"PrivateKey": private,
		"MeshPrefix": p.cfg.Mesh.Prefix(),
		"Peers":      peers,
		"ListenPort": p.cfg.Sites[site.Name].ListenPort(),
	})
}

// publicKey derives a WireGuard public key from a private key. Keys are
// generated on the workstation and kept in the encrypted file, so rebuilding a
// dead node restores the same identity and no peer is reconfigured.
func publicKey(private string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(private))
	if err != nil {
		return "", fmt.Errorf("is not base64. WireGuard keys are 32 bytes, base64 encoded: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("decodes to %d bytes, but a WireGuard key is 32", len(raw))
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("is not a usable curve25519 key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// sortedKeys returns a map's keys in sorted order, for a message that lists
// them.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *planner) relays() []string {
	var out []string
	for _, name := range p.order {
		if p.sites[name].Endpoint != "" {
			out = append(out, name)
		}
	}
	return out
}

// routesFor is a Caddy site's inventory: one host block per hostname, each
// importing the snippet for that hostname's role. A gateway routes every app
// that no other site's monitor serves; a monitor that is not a gateway routes
// only the apps pinned to it.
func (p *planner) routesFor(site *siteView) []route {
	var out []route
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		monitor, onMonitor := ServedBy(p.cfg, name)
		switch {
		case site.IsGateway && onMonitor && monitor != site.Name:
			continue
		case !site.IsGateway && (!onMonitor || monitor != site.Name):
			continue
		}
		var bypass gateBypass
		if app.UsesGate() {
			bypass = bypassFor(p.cfg, name, app)
		}
		out = append(out, route{
			App:       name,
			Role:      blueprints.PrimaryRole,
			Hostname:  app.Hostname,
			Upstreams: p.upstreams(name),
			Snippet:   snippetMount + name + ".caddy",
			Gated:     app.Gated,
			AutoLogin: app.AutoLogin,
			Bypass:    bypass,
		})
		// Every hostname beyond the primary that exists: a derived media
		// hostname as well as a declared one, an account app's join hostname,
		// and nothing for a role whose condition does not hold.
		for _, rh := range blueprints.RoleHostnames(p.cfg, app) {
			routeGated, routeAutoLogin, routeBypass := app.Gated, app.AutoLogin, bypass
			if rh.Role == blueprints.MediaRole || rh.Role == blueprints.JoinRole {
				// Never gated, whatever the app's gate is. A federating server
				// fetching an image is a machine, and it will not follow a
				// redirect to a passkey prompt; someone signing up has no
				// account yet.
				routeGated, routeAutoLogin, routeBypass = false, false, gateBypass{}
			}
			out = append(out, route{
				App:       name,
				Role:      rh.Role,
				Hostname:  rh.Hostname,
				Upstreams: p.upstreams(name),
				Snippet:   snippetMount + name + "-" + rh.Role + ".caddy",
				Gated:     routeGated,
				AutoLogin: routeAutoLogin,
				Bypass:    routeBypass,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}

// usesGate reports whether any route uses the gate, so a monitor's Caddy
// carries the gate's named snippets only when one of its own routes imports
// them.
func usesGate(routes []route) bool {
	for _, r := range routes {
		if r.Gated || r.AutoLogin {
			return true
		}
	}
	return false
}

// upstreams is where an app's traffic goes: its own location when pinned, and
// every site holding the apps role when clustered.
func (p *planner) upstreams(name string) []string {
	app, ok := p.cfg.Apps[name]
	if !ok {
		return nil
	}
	return p.upstreamsOnPort(name, blueprints.Port(app.Blueprint))
}

// elsewhere reports whether a route has exactly one upstream and it is on a
// machine other than site, whose Caddy renders the route. Addresses are host
// or host:port, compared by host. Several upstreams import upstream_failover
// instead, and a single upstream on site itself dies with this Caddy, so
// neither needs it.
func elsewhere(site *siteView, upstreams []string) bool {
	if len(upstreams) != 1 {
		return false
	}
	host := upstreams[0]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host != site.Address
}

// upstreamsOnPort is upstreams with the port supplied rather than looked up,
// for a service that answers on a second, explicitly declared port, such as
// the homeserver's authentication service. It exists so that finding that
// upstream never computes a port from another blueprint's port, only from the
// one the blueprint declares.
func (p *planner) upstreamsOnPort(name string, port int) []string {
	app, ok := p.cfg.Apps[name]
	if !ok {
		return nil
	}
	if app.Placement.Mode == config.PlacementPinned {
		site, ok := p.sites[app.Placement.Site]
		if !ok {
			return nil
		}
		return []string{fmt.Sprintf("%s:%d", site.Address, port)}
	}
	var out []string
	for _, hostName := range p.cfg.AppsSites() {
		out = append(out, fmt.Sprintf("%s:%d", p.sites[hostName].Address, port))
	}
	return out
}

func (p *planner) clusterMembers() []clusterMember {
	names := append([]string(nil), p.cfg.Cluster.Sites...)
	sort.Strings(names)
	var out []clusterMember
	for _, name := range names {
		site, ok := p.sites[name]
		if !ok {
			continue
		}
		out = append(out, clusterMember{Name: name, Address: site.Address})
	}
	return out
}

// pocketIDServers is every site that runs the deployment's one pocket-id app,
// sorted, at its mesh address: the servers of each HAProxy's `listen
// pocket-id`. Nil without exactly one pocket-id app, and then no site renders
// the listener.
func (p *planner) pocketIDServers() []clusterMember {
	idp, ok := p.cfg.PocketIDApp()
	if !ok {
		return nil
	}
	names := append([]string(nil), AppSites(p.cfg)[idp]...)
	sort.Strings(names)
	var out []clusterMember
	for _, name := range names {
		if site, ok := p.sites[name]; ok {
			out = append(out, clusterMember{Name: name, Address: site.Address})
		}
	}
	return out
}

func (p *planner) etcdInitialCluster() string {
	names := append([]string(nil), p.cfg.Etcd.Members...)
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		site, ok := p.sites[name]
		if !ok {
			continue
		}
		parts = append(parts, name+"="+EtcdPeerURL(site.Address))
	}
	return strings.Join(parts, ",")
}

func (p *planner) etcdClientHosts() string {
	names := append([]string(nil), p.cfg.Etcd.Members...)
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		site, ok := p.sites[name]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d", site.Address, etcdClientPort))
	}
	return strings.Join(parts, ",")
}

func (p *planner) scope() string {
	return PatroniScope
}

func (p *planner) postgresVersion() string {
	return p.cfg.PostgresMajor()
}

func (p *planner) replication() int {
	if p.cfg.Storage.Garage.Replication == 0 {
		return 1
	}
	return p.cfg.Storage.Garage.Replication
}

func (p *planner) consistency() string {
	if p.cfg.Storage.Garage.Consistency == "" {
		return config.GarageConsistent
	}
	return p.cfg.Storage.Garage.Consistency
}

func (p *planner) heartbeatMS() int {
	if p.cfg.Etcd.HeartbeatMS == 0 {
		return 100
	}
	return p.cfg.Etcd.HeartbeatMS
}

func (p *planner) electionTimeoutMS() int {
	if p.cfg.Etcd.ElectionTimeoutMS == 0 {
		return 1000
	}
	return p.cfg.Etcd.ElectionTimeoutMS
}

// PublicKey derives a site's WireGuard public key from its private key, for a
// command that reads a peer's key back from `wg show`.
func PublicKey(private string) (string, error) { return publicKey(private) }

// yamlquote is the template function of that name. A `$` is doubled as
// well, since docker compose interpolates `$` in every value of a compose
// file, quoted or not, and `$$` is its escape for a literal one.
func yamlquote(v string) (string, error) {
	raw, err := json.Marshal(v)
	return strings.ReplaceAll(string(raw), "$", "$$"), err
}

// jsonquote is the template function of that name.
func jsonquote(v string) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}
