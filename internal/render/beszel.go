package render

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

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
// A site without a token is refused by name. The hub keeps a listed
// system's token when config.yml gives none, and makes one up for a new one
// (internal/hub/config/config.go:103-149 at Beszel v0.21.1), so the agent,
// presenting the token from the secrets file, would never be accepted.
func beszelSystems(cfg *config.Config, secrets *config.Secrets) ([]beszelSystem, error) {
	var out []beszelSystem
	for _, name := range cfg.SiteNames() {
		token := secrets.Sites[name].BeszelToken
		if token == "" {
			return nil, fmt.Errorf("secrets sites.%s.beszel_token: required for a deployment with a beszel app, whose hub lists every site and accepts its agent only with this token. Run `%s init` to generate it", name, progname.Cmd())
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
