package render

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// appValues is everything a kind's template set may need about one app.
//
// It exists because the template set, not this package, decides what an
// application's configuration looks like. Go works out the values that require
// the whole deployment to know: which host holds the database, what the
// object storage endpoint is, which secret belongs to this app. The templates
// decide which of those go into which file, under what names, in what format.
//
// The alternative was a switch on kind that assembled environment variables
// here, which is what this replaces. It could only ever produce one file per
// app, and two of the five applications this toolkit renders are not
// configured by environment variables at all.
type appValues struct {
	App plannedApp

	// Hostname is the public name, and PublicURL the same with a scheme. Both
	// are here because roughly half the settings that want one want the other.
	Hostname  string
	PublicURL string

	// ServerName is the name that appears in a user identifier. It is the
	// hostname holding the wellknown role when the app declares one, and the
	// primary hostname otherwise.
	//
	// Two kinds use it, and they must agree: a homeserver writes it into
	// homeserver.yaml, and an Element client offers it to members as the name
	// they have an account on. Element's is therefore resolved from the
	// homeserver its `homeserver` setting names, not from its own hostname.
	//
	// The reason it is not simply the
	// primary hostname is that delegation exists: a deployment serves
	// /.well-known/matrix/server on the apex so that identifiers read
	// @someone:example.org while the API lives on a subdomain. If the two
	// disagree the delegation documents point at a server that does not answer
	// to the name they claim. Changing it after an account or a room exists is
	// a migration rather than a setting, which is why it is derived from the
	// declared hostnames rather than from anything that can drift.
	ServerName string

	// Domain is the community's own domain, community.domain in the
	// configuration, never an app's own hostname. The oauth2-proxy kind scopes
	// its cookie to it with a leading dot so one login covers every current
	// and future subdomain, the same shape as the live authgate stack's
	// COOKIE_DOMAINS.
	Domain string

	// Gated is the app's gated key. A gated app that federates turns on its
	// kind's signed fetch, because the gate leaves ActivityPub to the app.
	Gated bool

	// TrustedProxies is the mesh subnet, never a host address, so the gateway
	// role can move without rewriting every application's configuration.
	TrustedProxies string

	// TrustProxy is the proxy an app on a monitor site trusts for the
	// client's address: the site's own mesh address behind the monitor's own
	// Caddy, and where the operator's web server connects from with managed_by:
	// external. See trustProxy.
	TrustProxy string

	// WebServerListen is where an app on a monitor with managed_by: external is
	// published for the operator's web server, as host:port, instead of its
	// mesh address. Empty for every other app. WebServerNetwork and
	// WebServerGateway pin its compose network, so the address that web
	// server's connections arrive from is known (see trustProxy).
	WebServerListen  string
	WebServerNetwork string
	WebServerGateway string

	// MeshAddress is the mesh address of the site this instance runs on. A
	// published port binds it and nothing else, and a clustered app reaches
	// its local HAProxy at it. Empty when the gateway rebuilds the app for
	// routing, which never publishes a port or opens a connection.
	MeshAddress string

	// The database this app connects to. A clustered app is given the
	// HAProxy on its own site, at that site's mesh address; a pinned app is
	// given its own container. DSN is the same connection as a URL, for the
	// applications that take one.
	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string
	DSN        string

	// PostgresVersion is the cluster's major version, which every database in
	// the deployment runs. Mbin's Doctrine wants it in its DSN.
	PostgresVersion string

	// S3 is object storage, which is Garage over the mesh. Zero for an app
	// that keeps its files on its own disk (LocalStorage), so a template
	// cannot render half an S3 configuration for it.
	S3 s3Values

	// LocalStorage is true for an app of a kind that can use S3 and keeps its
	// files in its own stack directory instead: `storage: local`, or the
	// default in a deployment with no Garage (kinds.InObjectStorage).
	LocalStorage bool

	// OIDC is this app's client at the identity provider, empty when the app
	// has no client declared in the secrets.
	OIDC oidcValues

	// DataPath is the app's bind mounted directory on the host. Templates that
	// mount a rendered file need it to say where the file lives.
	DataPath string

	// Project is the app's compose project name, paisans-<token>-<app>, and
	// DeploymentLabel and DeploymentID are the label every service and network
	// in it carries. See internal/deployment.
	Project         string
	DeploymentLabel string
	DeploymentID    string

	// Upstreams is where the gateway sends this app's traffic: one address for
	// a pinned app, every apps site for a clustered one. A snippet never
	// hardcodes a location, which is what keeps a pinned app and a clustered
	// app the same shape.
	Upstreams []string
	// UpstreamElsewhere is true when Upstreams is a single address on a
	// machine other than the one whose Caddy renders the route, so the route
	// imports upstream_single: that site can die while this Caddy stays up,
	// and a request on a connection already open to it would otherwise wait
	// for TCP to give up. Set per Caddy site, by renderSnippets.
	UpstreamElsewhere bool

	// ServicePorts and ServiceUpstreams are, for each service other than app
	// that its blueprint gives a port of its own, that port and the addresses
	// the gateway reaches it at, keyed by the service's name. Databases is the
	// name of each extra database the blueprint keeps, keyed by its name in
	// the blueprint.
	ServicePorts     map[string]int
	ServiceUpstreams map[string][]string
	Databases        map[string]string

	// SMTP is this app's resolved mail settings, populated only for a kind
	// that sends mail (kinds.SendsMail) and zero when no host resolves.
	SMTP smtpValues

	// PocketID is the pocket-id kind's application configuration from its
	// settings and its recorded signup group IDs (pocketid.go). Populated
	// only for that kind.
	PocketID pocketIDConfig

	// UptimeSeed is the uptime kind's monitors.json, built from the whole
	// deployment (uptime.go). Populated only for that kind.
	UptimeSeed string

	// Invite is the invite kind's identity provider, settings and code of
	// conduct (invite.go). Populated only for that kind.
	Invite inviteValues

	// AdminsHeartbeat is true for a Pocket ID app in a deployment with a
	// monitor, whose stack then runs the admins heartbeat pusher on
	// HeartbeatImage (adminsheartbeat.go).
	AdminsHeartbeat bool
	HeartbeatImage  string

	// DNS is a Pocket ID app's DNS continuity check, its Enabled false when
	// the deployment renders none (dnscontinuity.go).
	DNS dnsValues

	secrets map[string]any
	// set is the app's settings as its blueprint sees them, with a catalog
	// blueprint's defaults filled in (kinds.SettingsSeen).
	set map[string]any
	// decl is the app as the configuration declares it.
	decl config.App
	// links is what each role the app's blueprint consumes offers, keyed by
	// role and then by value (Linked).
	links map[string]map[string]string
}

type s3Values struct {
	Endpoint string

	// MediaHostname is where this app's objects are served from: its own
	// hostname, a sibling of the app's, from kinds.MediaHostname. Empty for a
	// kind that stores nothing.
	MediaHostname string

	// PublicBase is what an app writes into a page, a feed or a federated
	// post: the media hostname, over https, never the mesh endpoint. It has
	// to resolve for a browser and for a server that will never join this
	// mesh, which the endpoint above does not. It carries no bucket segment,
	// because the media hostname is this app's alone. Empty when there is no
	// media hostname, so a kind that stores nothing does not carry a
	// half-built URL.
	PublicBase string

	// GarageAddresses is every Garage node, in storage.garage.sites order,
	// that the gateway proxies a media hostname to over the mesh. The first
	// is the node Endpoint names and serves while it is up; the rest stand
	// by, so losing one site does not take media down while another holds
	// the data.
	GarageAddresses []string
	// GarageElsewhere is UpstreamElsewhere for GarageAddresses: one Garage
	// node, on a machine other than the one whose Caddy renders the route.
	// Set per Caddy site, by renderSnippets.
	GarageElsewhere bool

	// WebHost is the Host Garage's web endpoint resolves this app's bucket
	// from: the bucket name under an internal suffix that resolves nowhere.
	// Only a media snippet for a kind that serves objects publicly sends it.
	WebHost string

	AccessKeyID    string
	SecretKey      string
	Bucket         string
	Region         string
	ForcePathStyle bool
}

// oidcValues is the app's own client, plus where the identity provider is.
//
// Endpoints are spelled out rather than left to discovery because two of the
// applications here want them individually: WriteFreely's `[oauth.generic]`
// takes an auth, token and inspect endpoint, and neither it nor Synapse can be
// given only an issuer in every version. The paths are Pocket ID's, read from
// a running instance's discovery document rather than recalled.
type oidcValues struct {
	Present      bool
	ClientID     string
	ClientSecret string
	RedirectURL  string

	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string

	// The same three as paths. WriteFreely stores a host and appends a path to
	// it (oauth.go builds AuthLocation as Host + AuthEndpoint), so giving it an
	// absolute URL there produces a doubled host and a sign in that fails at
	// the redirect.
	AuthorizationPath string
	TokenPath         string
	UserinfoPath      string
}

// Secret returns one of this app's secrets by key, empty when unset. A
// template asking for a secret that does not exist renders an empty value
// rather than failing, because the applications differ in which they need and
// an absent optional secret is not an error.
func (v appValues) Secret(key string) string {
	value, _ := v.secrets[key].(string)
	return value
}

// Setting returns a declared setting, or a catalog blueprint's default for
// it, or the fallback.
func (v appValues) Setting(key, fallback string) string {
	if s, ok := v.set[key].(string); ok && s != "" {
		return s
	}
	return fallback
}

// AuthorizedFetch is whether the app requires a valid HTTP signature on
// every ActivityPub read: settings.authorized_fetch, on when absent. Mbin and
// WriteFreely render it always, and lock it, so the configuration is its only
// source.
func (v appValues) AuthorizedFetch() bool {
	return kinds.AuthorizedFetch(config.App{Settings: v.set})
}

// SettingBool returns a declared boolean setting, or a catalog blueprint's
// default for it, or the fallback.
func (v appValues) SettingBool(key string, fallback bool) bool {
	if b, ok := v.set[key].(bool); ok {
		return b
	}
	return fallback
}

// SettingList returns a declared list setting, or a catalog blueprint's
// default for it, as strings; nil when there is neither.
func (v appValues) SettingList(key string) []string {
	items, ok := v.set[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// SettingInt returns a declared whole number setting, or a catalog
// blueprint's default for it, or the fallback.
func (v appValues) SettingInt(key string, fallback int) int {
	if n, ok := v.set[key].(int); ok {
		return n
	}
	return fallback
}

// OIDCAdminGroup and OIDCMemberGroup are the groups the app's client at
// Pocket ID admits, from the kind's client spec, so that the template renders
// the same names the client is restricted to and never repeats a default.
func (v appValues) OIDCAdminGroup() string  { admin, _ := v.oidcGroups(); return admin }
func (v appValues) OIDCMemberGroup() string { _, member := v.oidcGroups(); return member }

func (v appValues) oidcGroups() (admin, member string) {
	spec, ok := kinds.OIDCClient(v.App.Kind, v.App.Hostname, v.App.Name)
	if !ok {
		return "", ""
	}
	return spec.Groups(config.App{Kind: v.App.Kind, Hostname: v.App.Hostname, Settings: v.set})
}

// Image returns the reference a service runs, so a compose template never
// decides for itself.
func (v appValues) Image(service string) string { return v.App.Images[service] }

// StandbyMarker is the text a Pocket ID app's image logs when another
// instance already holds the database, for the standby wrapper. See
// kinds.PocketIDStandbyMarker.
func (v appValues) StandbyMarker() string {
	marker, _ := kinds.StandbyMarker(v.Image("app"))
	return marker
}

// PostgresDataMount is where a pinned app's Postgres container mounts its data
// directory, which moved in the official image's 18. See
// kinds.PostgresDataMount.
func (v appValues) PostgresDataMount() string {
	return kinds.PostgresDataMount(v.Image(kinds.PostgresService), v.PostgresVersion)
}

// s3For is an app's object storage settings, for an app that keeps its files
// in Garage (kinds.InObjectStorage).
func (p *planner) s3For(planned plannedApp, app config.App, v appValues) s3Values {
	var s3Endpoint string
	if garage := garageEndpointHost(p); garage != "" {
		s3Endpoint = fmt.Sprintf("http://%s:%d", garage, garageS3Port)
	}
	s3 := s3Values{
		// Mbin and the blog write to this address themselves, server side,
		// over the mesh. Outline never uses it: its uploads are a presigned
		// POST the browser submits directly, so for Outline every upload and
		// every read goes through PublicBase, not here.
		Endpoint:        s3Endpoint,
		GarageAddresses: garageAddresses(p),
		AccessKeyID:     v.Secret("s3_access_key_id"),
		SecretKey:       v.Secret("s3_secret_access_key"),
		Bucket:          kinds.BucketName(planned.Name, app),
		Region:          v.Setting("s3_region", "garage"),
		ForcePathStyle:  v.SettingBool("s3_force_path_style", true),
	}
	s3.WebHost = s3.Bucket + webEndpointSuffix
	// Set only for a kind that stores objects, so a kind that stores nothing
	// does not carry the literal string "https://" into a template that has
	// never been written to check.
	if media := kinds.MediaHostname(p.cfg, app); media != "" {
		s3.MediaHostname = media
		s3.PublicBase = "https://" + media
	}
	return s3
}

// values assembles what the template set is given.
func (p *planner) values(planned plannedApp, app config.App) (appValues, error) {
	var password string
	if kinds.UsesPostgres(planned.Kind) {
		var err error
		password, err = p.appDatabasePassword(planned)
		if err != nil {
			return appValues{}, err
		}
	}
	if err := p.requireAppSecrets(planned); err != nil {
		return appValues{}, err
	}

	dbHost, dbPort := p.clusteredDatabaseHost(planned.Site), p.clusterPort()
	if planned.Pinned {
		dbHost, dbPort = "postgres", postgresPort
	}

	v := appValues{
		App:             planned,
		Hostname:        planned.Hostname,
		PublicURL:       "https://" + planned.Hostname,
		Domain:          p.cfg.Community.Domain,
		Gated:           app.Gated,
		TrustedProxies:  p.mesh,
		DBHost:          dbHost,
		DBPort:          dbPort,
		DBName:          planned.DBName,
		DBUser:          planned.DBUser,
		DBPassword:      password,
		DSN:             dsn(planned, password, dbHost, dbPort),
		PostgresVersion: p.postgresVersion(),
		DataPath:        planned.Dir,
		Project:         p.dep().Project(planned.Name),
		DeploymentLabel: deployment.Label,
		DeploymentID:    p.dep().ID,
		MeshAddress:     p.meshAddress(planned.Site),
		secrets:         p.secrets.Apps[planned.Name],
		set:             kinds.SettingsSeen(app),
		decl:            app,
	}
	v.TrustProxy = trustProxy(p.cfg, planned.Name)
	v.WebServerListen = webServerListen(p.cfg, planned.Name)
	if v.WebServerListen != "" {
		v.WebServerNetwork, v.WebServerGateway = WebServerNetwork, WebServerGateway
	}
	v.ServerName = planned.Hostname
	if delegated := app.Hostnames[kinds.WellknownRole]; delegated != "" {
		v.ServerName = delegated
	}
	for _, c := range kinds.Consumes(planned.Kind) {
		if v.links == nil {
			v.links = map[string]map[string]string{}
		}
		links, err := p.linked(c.Role, v.Setting(c.Setting, planned.Hostname))
		if err != nil {
			return appValues{}, fmt.Errorf("%s: %w", planned.Name, err)
		}
		v.links[c.Role] = links
	}
	v.LocalStorage = kinds.KeepsFilesLocally(p.cfg, app)
	if kinds.InObjectStorage(p.cfg, app) {
		v.S3 = p.s3For(planned, app, v)
	}
	v.OIDC = p.oidcFor(planned)
	v.Upstreams = p.upstreams(planned.Name)
	if kinds.SendsMail(planned.Kind) {
		v.SMTP = p.smtpFor(planned.Name)
	}
	if planned.Kind == config.KindPocketID {
		pid, err := p.pocketIDConfigFor(planned.Name, app)
		if err != nil {
			return appValues{}, err
		}
		v.PocketID = pid
		v.AdminsHeartbeat = len(adminsHeartbeatTargets(p.cfg)) > 0
		if v.HeartbeatImage, err = kinds.FoundationImage(kinds.FoundationHeartbeat); err != nil {
			return appValues{}, err
		}
		dns, err := p.dnsValuesFor(planned.Name)
		if err != nil {
			return appValues{}, err
		}
		v.DNS = dns
	}
	if planned.Kind == config.KindUptime {
		seed, err := p.uptimeSeed(planned.Name)
		if err != nil {
			return appValues{}, err
		}
		v.UptimeSeed = seed
	}
	if planned.Kind == config.KindInvite {
		invite, err := p.inviteValuesFor(planned.Name)
		if err != nil {
			return appValues{}, err
		}
		v.Invite = invite
	}
	for _, svc := range kinds.ServicePorts(planned.Kind) {
		if v.ServicePorts == nil {
			v.ServicePorts, v.ServiceUpstreams = map[string]int{}, map[string][]string{}
		}
		v.ServicePorts[svc.Name] = svc.Port
		v.ServiceUpstreams[svc.Name] = p.upstreamsOnPort(planned.Name, svc.Port)
	}
	for _, extra := range kinds.ExtraDatabases(planned.Kind) {
		if v.Databases == nil {
			v.Databases = map[string]string{}
		}
		v.Databases[extra] = planned.DBName + "_" + extra
	}
	return v, nil
}

// Linked is a value the provider of a role the app's blueprint consumes
// offers, as the provider's blueprint declares it. A value the provider does
// not declare, or a role the blueprint does not consume, fails the render,
// naming it, rather than rendering an empty string.
func (v appValues) Linked(role, value string) (string, error) {
	values, ok := v.links[role]
	if !ok {
		return "", fmt.Errorf("the blueprint reads .Linked %q, a role it does not consume", role)
	}
	got, ok := values[value]
	if !ok {
		return "", fmt.Errorf("a provider of %s offers no value %q", role, value)
	}
	return got, nil
}

// linked is what the provider of role at hostname offers. A hostname no
// declared provider answers on is taken as the provider's own: a consumer
// pointed at a server outside the deployment still gets its address.
func (p *planner) linked(role, hostname string) (map[string]string, error) {
	for _, name := range p.cfg.AppNames() {
		app := p.cfg.Apps[name]
		if app.Hostname == hostname && slices.Contains(kinds.Provides(app.Kind), role) {
			return kinds.LinkValues(p.cfg, name, role)
		}
	}
	return kinds.LinkValuesAt(role, hostname)
}

// dsn is the app's connection URL, empty for a kind that uses no Postgres so
// that a template cannot render a connection string to a database nobody
// created.
func dsn(planned plannedApp, password, host string, port int) string {
	if !kinds.UsesPostgres(planned.Kind) {
		return ""
	}
	return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s", planned.DBUser, password, host, port, planned.DBName)
}

// oidcFor builds the app's identity provider client.
//
// The issuer is the Pocket ID app declared in this same configuration, because
// there is exactly one identity provider in this design and hardcoding its URL
// anywhere else would be a second place to change it.
func (p *planner) oidcFor(planned plannedApp) oidcValues {
	client, ok := p.secrets.OIDCClients[planned.Name]
	if !ok {
		return oidcValues{}
	}
	issuer := p.identityProviderURL()
	// The callback is the one the blueprint declares, which is also the one
	// apply registers at the identity provider.
	redirect := "https://" + planned.Hostname + "/oauth/callback"
	if spec, ok := kinds.OIDCClient(planned.Kind, planned.Hostname, planned.Name); ok {
		redirect = spec.CallbackURL
	}
	return oidcValues{
		Present:      true,
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURL:  redirect,

		Issuer:                issuer,
		AuthorizationEndpoint: issuer + authorizationPath,
		TokenEndpoint:         issuer + tokenPath,
		UserinfoEndpoint:      issuer + userinfoPath,

		AuthorizationPath: authorizationPath,
		TokenPath:         tokenPath,
		UserinfoPath:      userinfoPath,
	}
}

// The identity provider's OIDC endpoints, as paths. Read from a running Pocket
// ID v2.14.0's discovery document on 2026-09-08 rather than recalled.
const (
	authorizationPath = "/authorize"
	tokenPath         = "/api/oidc/token"
	userinfoPath      = "/api/oidc/userinfo"
)

// identityProviderURL is the Pocket ID app's public URL, empty when the
// deployment declares none.
func (p *planner) identityProviderURL() string {
	for _, name := range p.cfg.AppNames() {
		if p.cfg.Apps[name].Kind == config.KindPocketID {
			return "https://" + p.cfg.Apps[name].Hostname
		}
	}
	return ""
}

// webEndpointSuffix is the internal suffix Garage's s3_web endpoint resolves
// a bucket from, and that nothing on the mesh or off it otherwise resolves.
// It must match the root_domain rendered into garage.toml.
const webEndpointSuffix = ".web.garage.internal"

// quote renders a value into a double quoted YAML or INI string. Templates use
// it wherever a secret lands in a structured file, since a generated password
// can legitimately contain a character the format would otherwise read.
func quote(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}
