// Package render turns a validated configuration and its secrets into a tree
// of per site artifacts on local disk. It writes files and nothing else: no
// SSH, no Docker, no network. Pushing is a separate concern and a later slice.
package render

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// File is one rendered artifact. Content is complete: rendering never appends
// to a file that already exists.
type File struct {
	// Path is relative to the output directory, always slash separated.
	Path    string
	Content string
	Mode    uint32
	// App is the app a file belongs to when it lies outside that app's own
	// stack directory, as a route snippet on a site running Caddy does, and
	// empty otherwise. apply holds such a file back with its app (see
	// apply.Except), which its path alone does not say: it sits in the
	// infrastructure stack's directory.
	App string
}

// Plan is every file a configuration renders to, sorted by path so that two
// runs over the same input produce the same tree in the same order.
type Plan struct {
	Files []File
	// Deployment is the identity every path and name in Files derives from.
	Deployment deployment.Deployment
	// HostSites names the sites whose Caddy mounts the host's own
	// HostSitesDir, which apply creates there when it is missing.
	HostSites map[string]bool
	// Images is, for each site, every image reference its compose files name
	// that the configuration decides: each app stack's images, and the
	// gateway's Caddy image. A tag here is not what a host runs. apply
	// resolves it to the build it names while it plans, and pins that digest
	// into each line naming it with PinImages. See listImages.
	Images map[string][]ComposeImage
}

// AppPort is the port a blueprint's app listens on, which is also the port it
// publishes on its site's mesh address (blueprints.Def.Port). A command that
// calls an application's own API from its host needs it.
func AppPort(blueprint config.Blueprint) int { return blueprints.Port(blueprint) }

// reconcilerPort is the pocket-id blueprint's operator reconciler, which publishes
// /healthz on the mesh address for the uptime monitor. A literal:
// the reconciler's own binary and the blueprint's compose template name the
// same number.
const reconcilerPort = 1412

// patroniAPIPort is where Patroni answers the health check HAProxy uses to
// find the primary. It is fixed by Spilo.
const patroniAPIPort = 8008

// bgMonPort is where Spilo's bg_mon extension serves its monitoring page.
// bg_mon defaults to 8080, the most common application port there is, and on
// the first real host it took 8080 on a data site before Mbin could publish
// there. It is moved beside the Patroni API instead of moving every app.
const bgMonPort = 8009

// haproxyStatsPort is where HAProxy serves its statistics, on loopback only.
// Nothing else the toolkit renders listens on 8404.
const haproxyStatsPort = 8404

// HAProxyStatsPort is haproxyStatsPort, for the command that reads it.
const HAProxyStatsPort = haproxyStatsPort

// postgresPort is the cluster's real Postgres port. Applications never use it:
// they connect to the local HAProxy instead, from the first install, so that
// adding a site changes a backend list rather than application configuration.
const postgresPort = 5432

type plannedApp struct {
	Name string
	// Dir is the app's stack directory on a host, under the deployment's root.
	Dir       string
	Blueprint config.Blueprint
	Hostname  string
	Pinned    bool
	Site      string
	Port      int
	OwnsDB    bool
	DBName    string
	DBUser    string
	MediaDirs []string
	// Images is the reference each of the blueprint's services runs, resolved
	// from the configuration where it declared one and from the blueprint's
	// default otherwise. The template reads it rather than deciding, so what
	// runs is decided in one place.
	Images map[string]string
}

type siteView struct {
	Name    string
	Address string
	// Endpoint is what the other sites dial, config.Site.DialEndpoint: not
	// the endpoint as written, which may be a name beside a public_address.
	Endpoint  string
	Roles     []config.Role
	IsData    bool
	IsApps    bool
	IsGateway bool
	IsWitness bool
	IsEtcd    bool
	IsGarage  bool
	// RunsCaddy is set where the toolkit's Caddy runs: a gateway, and a
	// monitor with managed_by: paisans, which serves its own apps.
	RunsCaddy bool
	// Heartbeats is every monitor off this site, which its host pushes a
	// heartbeat to from its infrastructure stack. See heartbeat.go.
	Heartbeats []heartbeatTarget
	Apps       []plannedApp
	NeedsProxy bool
	// WatchdogOff renders Patroni without fencing and drops the device
	// mapping, which compose would otherwise refuse to start without.
	WatchdogOff bool
}

type planner struct {
	cfg     *config.Config
	secrets *config.Secrets
	mesh    string
	sites   map[string]*siteView
	order   []string
	// etcdInitial is how each etcd member was first started, by site, as
	// read from its host. See EtcdInitialPath.
	etcdInitial map[string]EtcdInitial
}

// Build produces the plan for a configuration. It assumes validation has
// already refused anything incoherent: a planner that re-checks policy is a
// second place for the rules to drift.
//
// opts carry facts a caller read from the live deployment, such as the flags
// an etcd member was born with. Without them every site renders as it would
// on a fresh deployment.
func Build(cfg *config.Config, secrets *config.Secrets, opts ...Option) (*Plan, error) {
	p := newPlanner(cfg, secrets)
	for _, opt := range opts {
		opt(p)
	}
	if err := p.placeApps(); err != nil {
		return nil, err
	}

	var files []File
	hostSites := map[string]bool{}
	for _, name := range p.order {
		siteFiles, err := p.renderSite(p.sites[name])
		if err != nil {
			return nil, err
		}
		files = append(files, siteFiles...)
		if hostSitesMount(p.sites[name]) != "" {
			hostSites[name] = true
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	// Two files at one path would leave one of them unwritten and the other
	// reviewed and applied as if it were both (Eg: an app named for the
	// infrastructure stack), so the render stops rather than choose.
	// Nothing leaves the deployment's own directory but the WireGuard file
	// wg-quick reads from /etc/wireguard: apply writes each file as root at
	// its path, so a path that walks out would be written anywhere.
	root, wg := cfg.Deployment().RelPath()+"/", cfg.Deployment().WireGuardConf()
	for _, f := range files {
		_, rel := splitSite(f.Path)
		if rel != path.Clean(rel) || path.IsAbs(rel) || (!strings.HasPrefix(rel, root) && rel != wg) {
			return nil, fmt.Errorf("the configuration renders %s, outside the deployment's own directory", f.Path)
		}
	}
	for i := 1; i < len(files); i++ {
		if files[i].Path == files[i-1].Path {
			return nil, fmt.Errorf("the configuration renders two files at %s. Each stack is a directory of its own; rename whatever shares a name with another stack", files[i].Path)
		}
	}
	images, err := p.listImages(files)
	if err != nil {
		return nil, err
	}
	return &Plan{Files: files, Deployment: cfg.Deployment(), HostSites: hostSites, Images: images}, nil
}

// newPlanner is a planner with every site laid out and no app placed yet.
func newPlanner(cfg *config.Config, secrets *config.Secrets) *planner {
	p := &planner{cfg: cfg, secrets: secrets, mesh: cfg.Mesh.Subnet, sites: map[string]*siteView{}}
	for _, name := range cfg.SiteNames() {
		site := cfg.Sites[name]
		p.order = append(p.order, name)
		p.sites[name] = &siteView{
			Name:       name,
			Address:    site.Address,
			Endpoint:   site.DialEndpoint(),
			Roles:      site.Roles,
			IsData:     site.Has(config.RoleData),
			IsApps:     site.Has(config.RoleApps),
			IsGateway:  site.Has(config.RoleGateway),
			IsWitness:  site.Has(config.RoleWitness),
			IsEtcd:     contains(cfg.Etcd.Members, name),
			IsGarage:   contains(cfg.Storage.Garage.Sites, name),
			RunsCaddy:  site.RunsCaddy(),
			Heartbeats: heartbeatTargets(cfg, name),

			WatchdogOff: site.WatchdogMode() == config.WatchdogOff,
		}
	}

	return p
}

// SiteStacks is every stack a site renders, by directory name and sorted:
// infra when the site runs any infrastructure service, and each app placed
// on it. It is what Build renders for the site, decided without secrets, for
// a command that reads a host and has none.
func SiteStacks(cfg *config.Config, site string) ([]string, error) {
	p := newPlanner(cfg, nil)
	if err := p.placeApps(); err != nil {
		return nil, err
	}
	view, ok := p.sites[site]
	if !ok {
		return nil, fmt.Errorf("no site %q is declared", site)
	}
	var out []string
	if view.runsInfra() {
		out = append(out, "infra")
	}
	for _, app := range view.Apps {
		out = append(out, app.Name)
	}
	sort.Strings(out)
	return out, nil
}

// placeApps decides where each app runs.
//
// A pinned app runs at its named site and is self contained there: its own
// compose file, its own Postgres, its own volumes. An app with cluster
// placement runs on every site holding the apps role and connects to the local
// HAProxy.
func (p *planner) placeApps() error {
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		switch app.Placement.Mode {
		case config.PlacementPinned:
			site, ok := p.sites[app.Placement.Site]
			if !ok {
				return fmt.Errorf("apps.%s.placement: site %q is not declared", name, app.Placement.Site)
			}
			planned, err := p.planApp(name, app, site, true)
			if err != nil {
				return err
			}
			site.Apps = append(site.Apps, planned)
		case config.PlacementCluster:
			hosts := p.cfg.AppsSites()
			if len(hosts) == 0 {
				return fmt.Errorf("apps.%s.placement: is `cluster`, but no site holds the apps role. Give a site the apps role, or pin the app", name)
			}
			for _, hostName := range hosts {
				site := p.sites[hostName]
				planned, err := p.planApp(name, app, site, false)
				if err != nil {
					return err
				}
				site.Apps = append(site.Apps, planned)
				site.NeedsProxy = true
			}
		default:
			return fmt.Errorf("apps.%s.placement: %q is not a placement", name, app.Placement.Literal)
		}
	}
	for _, name := range p.order {
		site := p.sites[name]
		sort.Slice(site.Apps, func(i, j int) bool { return site.Apps[i].Name < site.Apps[j].Name })
	}
	return nil
}

func (p *planner) planApp(name string, app config.App, site *siteView, pinned bool) (plannedApp, error) {
	planned := plannedApp{
		Name:      name,
		Dir:       p.dep().Dir(name),
		Blueprint: app.Blueprint,
		Hostname:  app.Hostname,
		Pinned:    pinned,
		Site:      site.Name,
		Port:      blueprints.Port(app.Blueprint),
		OwnsDB:    pinned,
		DBName:    dbIdentifier(name),
		DBUser:    dbIdentifier(name),
	}
	images, err := p.appImages(app)
	if err != nil {
		return plannedApp{}, err
	}
	planned.Images = images
	return planned, nil
}

// appImages resolves what each of the blueprint's services runs.
//
// A declared reference wins, and an absent one takes the default the
// blueprint names, usually a track apply resolves to its newest build. The
// database is the one service with no fixed default: every database in the
// deployment is the same major version, declared once in cluster, so it is
// derived rather than repeated per app, from the foundation's image for that
// major. That image's tag names the Debian release (Eg: 17.*-trixie): the C
// library decides how Postgres sorts text, so an index is only valid under
// the C library that built it, and naming the release keeps a pull from
// changing it under an existing data directory.
func (p *planner) appImages(app config.App) (map[string]string, error) {
	out, _, err := p.appImageOrigins(app)
	return out, err
}

// appImageOrigins is appImages, and the services whose image it took from
// the core catalog's foundation for the toolkit: one a reserved or core
// blueprint names with foundation:, and the database's, which the toolkit
// chooses for every blueprint, when the configuration declares neither. A
// foundation: in a blueprint from any other catalog is that catalog's
// choice, which the team that reviews foundation.yaml did not review, so it
// is not counted.
func (p *planner) appImageOrigins(app config.App) (map[string]string, map[string]bool, error) {
	out, foundation := map[string]string{}, map[string]bool{}
	toolkits := blueprints.ToolkitBlueprint(app.Blueprint)
	for _, service := range blueprints.Services(app.Blueprint) {
		image, err := blueprints.ServiceImage(service)
		switch {
		case app.Images[service.Name] != "":
			out[service.Name] = app.Images[service.Name]
		case err != nil:
			return nil, nil, fmt.Errorf("the image of %s's %s: %w", app.Blueprint, service.Name, err)
		case image != "":
			out[service.Name] = image
			foundation[service.Name] = service.Foundation != "" && toolkits
		case service.Name == blueprints.PostgresService:
			pg, err := blueprints.PostgresImage(p.postgresVersion())
			if err != nil {
				return nil, nil, err
			}
			out[service.Name] = pg
			foundation[service.Name] = true
		}
	}
	return out, foundation, nil
}

// dbIdentifier keeps app names usable as Postgres identifiers without
// quoting.
func dbIdentifier(name string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// DBIdentifier is the role and database name an app's rendered connection
// uses. apply creates them, and must name them exactly as the render did.
func DBIdentifier(name string) string { return dbIdentifier(name) }

// PatroniAPIPort is where the rendered Patroni serves its REST API, on the
// site's mesh address.
const PatroniAPIPort = patroniAPIPort

// PatroniScope is the Patroni cluster name every site renders as SCOPE.
// Patroni keeps its state in etcd under /service/<scope>/ (Patroni v4.1.0,
// patroni/dcs/__init__.py: the namespace defaults to "service"), and Spilo
// passes Patroni a namespace only when NAMESPACE is set to something other
// than "default" (zalando/spilo 4.1-p2, postgres-appliance/scripts/
// configure_spilo.py), which the rendered patroni.env never sets. A command
// that reads a key such as /sync names it with this.
const PatroniScope = "paisans"
