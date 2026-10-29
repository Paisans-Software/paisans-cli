package render

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// beszelValues is what the beszel blueprint's templates need beyond any
// app's: the systems list built from the whole deployment, the hub's key
// pair, and the break glass account's address.
type beszelValues struct {
	// ConfigYAML is the hub's data/config.yml (beszelConfigYAML).
	ConfigYAML string
	// HubKey is the hub's private key, rendered as data/id_ed25519, and
	// HubKeyPublic its authorized_keys line, every agent's KEY.
	HubKey       string
	HubKeyPublic string
	// AdminEmail is the break glass user's and superuser's address,
	// beszel-admin@ under community.domain. Nothing is sent to it: it is a
	// sign in name the hub requires to be an email address.
	AdminEmail string
}

// beszelSystem is one system in the hub's config.yml: a site, named for
// itself, at its mesh address, with the token its agent presents.
type beszelSystem struct {
	Name, Host, Token string
}

// beszelSystems is every site of the deployment as the hub lists it, in site
// name order. Every site runs an agent, so every site is a system.
//
// A site without a token is refused by name (beszelToken). The hub keeps a
// listed system's token when config.yml gives none, and makes one up for a
// new one (internal/hub/config/config.go:103-149 at Beszel v0.21.1), so the
// agent, presenting the token from the secrets file, would never be
// accepted.
func beszelSystems(cfg *config.Config, secrets *config.Secrets) ([]beszelSystem, error) {
	var out []beszelSystem
	p := &planner{cfg: cfg, secrets: secrets}
	for _, name := range cfg.SiteNames() {
		token, err := p.beszelToken(name)
		if err != nil {
			return nil, err
		}
		out = append(out, beszelSystem{Name: name, Host: cfg.Sites[name].Address, Token: token})
	}
	return out, nil
}

// beszelConfigYAML is the hub's config.yml, in the shape SyncSystems reads
// (internal/hub/config/config.go:19-29 at Beszel v0.21.1).
//
// The hub reads it at every start, creates each system it lists, updates the
// rest and deletes every system it does not list, history included, so the
// file is the whole truth and the toolkit owns it. host only names the
// system, since with DISABLE_SSH the hub never dials it; port is left out,
// as the hub fills in its default. An empty users gives a new system to the
// first user, the break glass admin, and SHARE_ALL_SYSTEMS shows it to
// everyone else.
func beszelConfigYAML(systems []beszelSystem) (string, error) {
	type entry struct {
		Name  string   `yaml:"name"`
		Host  string   `yaml:"host"`
		Token string   `yaml:"token"`
		Users []string `yaml:"users"`
	}
	doc := struct {
		Systems []entry `yaml:"systems"`
	}{}
	for _, s := range systems {
		doc.Systems = append(doc.Systems, entry{Name: s.Name, Host: s.Host, Token: s.Token, Users: []string{}})
	}
	var buf bytes.Buffer
	buf.WriteString("# Rendered by paisans. Do not edit: `paisans deployment apply` overwrites this file.\n")
	buf.WriteString("#\n# One system per site. The hub deletes every system this file does not list.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// beszelValues is a beszel app's values. The key pair is required of the
// app's secrets already (requireAppSecrets), so it is read here as it is.
func (p *planner) beszelValues(app string) (beszelValues, error) {
	systems, err := beszelSystems(p.cfg, p.secrets)
	if err != nil {
		return beszelValues{}, err
	}
	configYAML, err := beszelConfigYAML(systems)
	if err != nil {
		return beszelValues{}, fmt.Errorf("apps.%s: writing the hub's config.yml: %w", app, err)
	}
	return beszelValues{
		ConfigYAML:   configYAML,
		HubKey:       p.appSecret(app, "hub_key"),
		HubKeyPublic: p.appSecret(app, "hub_key_public"),
		AdminEmail:   "beszel-admin@" + p.cfg.Community.Domain,
	}, nil
}

// beszelAgent is one site's Beszel agent, in its infrastructure stack: the
// hub it dials, the token it presents and the hub's key it accepts, and the
// name it reports as.
type beszelAgent struct {
	HubURL, Token, Key, Name string
}

// BeszelAgentDataDir is where the agent keeps its state in its container,
// DATA_DIR, bind mounted from beszel-agent/data in the infrastructure stack's
// directory. The hub pins an agent's fingerprint on its first connection
// (internal/hub/agent_connect.go handleSingleRecord at Beszel v0.21.1), and
// the agent keeps the fingerprint it presents there (agent/fingerprint.go),
// so a recreated container reconnects as the same agent rather than as one
// whose fingerprint does not match.
const BeszelAgentDataDir = "/var/lib/beszel-agent"

// beszelTokenShape is the token init generates, 32 random bytes as hex. It
// goes unquoted into the agent's env file, where a space, a quote or a line
// break would be read as something other than the token.
var beszelTokenShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// beszelAgentOf is a site's agent as the configuration alone decides it, nil
// while the deployment declares no beszel app. Every site runs one, whatever
// its roles, since the hub lists every site (beszelSystems). The hub's URL,
// token and key are filled in by beszelAgentFor.
func beszelAgentOf(cfg *config.Config, site string) *beszelAgent {
	if cfg.BeszelApp() == "" {
		return nil
	}
	return &beszelAgent{Name: site}
}

// beszelHubURL is where every agent dials the hub: over plain HTTP at the mesh
// address of the site the hub is pinned to, on the port the beszel blueprint
// publishes it on there, as every service talks across sites (Rule 2).
//
// A hub pinned to no declared site is refused by name. `paisans` runs
// validate first, where cluster-placement-without-a-cluster and
// undeclared-site refuse it with a message written for an operator; this is
// the library level guard for a caller that reaches render.Build without
// validating, which would otherwise render HUB_URL=http://:8084 and an agent
// that dials nothing.
func beszelHubURL(cfg *config.Config, hub string) (string, error) {
	placement := cfg.Apps[hub].Placement
	site, ok := cfg.Sites[placement.Site]
	if placement.Mode != config.PlacementPinned || !ok || site.Address == "" {
		return "", fmt.Errorf("apps.%s.placement: a beszel app is pinned to a declared site with a mesh address, which every site's agent dials the hub at. Pin it to the monitor's site", hub)
	}
	return fmt.Sprintf("http://%s:%d", site.Address, blueprints.Port(config.BlueprintBeszel)), nil
}

// beszelToken is a site's token from the secrets, refused by name when it is
// missing or not in the shape init generates. The hub and the site's agent
// read the same one.
func (p *planner) beszelToken(site string) (string, error) {
	token := p.secrets.Sites[site].BeszelToken
	if token == "" {
		return "", fmt.Errorf("secrets sites.%s.beszel_token: required for a deployment with a beszel app, whose hub lists every site and accepts its agent only with this token. Run `%s init` to generate it", site, progname.Cmd())
	}
	if !beszelTokenShape.MatchString(token) {
		return "", fmt.Errorf("secrets sites.%s.beszel_token: is not 64 lowercase hex characters, the shape init generates. It goes unquoted into the agent's env file, where anything else may not reach the agent as written. Run `%s init` after removing it, which generates one in the right shape", site, progname.Cmd())
	}
	return token, nil
}

// beszelAgentFor is the site's agent with its token and the hub's public key
// from the secrets.
func (p *planner) beszelAgentFor(site *siteView) (beszelAgent, error) {
	agent := *site.Beszel
	token, err := p.beszelToken(site.Name)
	if err != nil {
		return beszelAgent{}, err
	}
	hub := p.cfg.BeszelApp()
	url, err := beszelHubURL(p.cfg, hub)
	if err != nil {
		return beszelAgent{}, err
	}
	key := p.appSecret(hub, "hub_key_public")
	if key == "" {
		return beszelAgent{}, fmt.Errorf("secrets apps.%s.hub_key_public: required for every site's Beszel agent, which accepts only the hub holding this key. Run `%s init` to generate it", hub, progname.Cmd())
	}
	agent.HubURL, agent.Token, agent.Key = url, token, key
	return agent, nil
}
