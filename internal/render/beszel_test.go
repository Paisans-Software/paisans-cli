package render

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// beszelFixture is the fixture deployment and its secrets, with its stats
// app on the watch monitor.
func beszelFixture(t *testing.T) (*config.Config, *config.Secrets) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(filepath.Join("testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, secrets
}

// beszelFile is one rendered file of the stats app on watch.
func beszelFile(t *testing.T, rel string) File {
	t.Helper()
	cfg, secrets := beszelFixture(t)
	plan, err := Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	want := "watch/srv/paisans/f2a9/stats/" + rel
	for _, f := range plan.Files {
		if f.Path == want {
			return f
		}
	}
	t.Fatalf("no %s was rendered", want)
	return File{}
}

// One system per site, in site name order, named for the site, at its mesh
// address, with the site's own token.
func TestBeszelSystems(t *testing.T) {
	cfg := &config.Config{Sites: map[string]config.Site{
		"watch":  {Address: "10.44.0.4"},
		"home-b": {Address: "10.44.0.2"},
		"home-a": {Address: "10.44.0.1"},
	}}
	secrets := &config.Secrets{Sites: map[string]config.SiteSecrets{
		"home-a": {BeszelToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"home-b": {BeszelToken: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"watch":  {BeszelToken: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}}
	got, err := beszelSystems(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	want := []beszelSystem{
		{Name: "home-a", Host: "10.44.0.1", Token: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Name: "home-b", Host: "10.44.0.2", Token: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "watch", Host: "10.44.0.4", Token: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("system %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	// A site without a token is refused by name, since the hub would create
	// a token of its own the site's agent never presents.
	// So is one not in init's shape, which would reach the agent's env file
	// as something other than written.
	secrets.Sites["home-b"] = config.SiteSecrets{BeszelToken: "not hex # at all"}
	if _, err := beszelSystems(cfg, secrets); err == nil || !strings.Contains(err.Error(), "sites.home-b.beszel_token: is not 64 lowercase hex") {
		t.Errorf("a malformed beszel_token: %v", err)
	}
	delete(secrets.Sites, "home-b")
	if _, err := beszelSystems(cfg, secrets); err == nil {
		t.Error("a site without a beszel_token was accepted")
	}
}

// The hub's config.yml lists every site of the deployment and gives each to
// nobody in particular: an empty users is the first user, the break glass
// admin, and SHARE_ALL_SYSTEMS shows it to everyone else. It carries every
// site's token, so only its owner reads it.
func TestBeszelConfigYAML(t *testing.T) {
	f := beszelFile(t, "data/config.yml")
	if f.Mode != 0o600 {
		t.Errorf("config.yml carries every site's token and was rendered %o", f.Mode)
	}
	var parsed struct {
		Systems []struct {
			Name  string    `yaml:"name"`
			Host  string    `yaml:"host"`
			Token string    `yaml:"token"`
			Users *[]string `yaml:"users"`
		} `yaml:"systems"`
	}
	if err := yaml.Unmarshal([]byte(f.Content), &parsed); err != nil {
		t.Fatalf("config.yml is not YAML: %v\n%s", err, f.Content)
	}
	cfg, secrets := beszelFixture(t)
	names := cfg.SiteNames()
	if len(parsed.Systems) != len(names) {
		t.Fatalf("%d systems, want one per site %v:\n%s", len(parsed.Systems), names, f.Content)
	}
	for i, s := range parsed.Systems {
		site := names[i]
		if s.Name != site || s.Host != cfg.Sites[site].Address || s.Token != secrets.Sites[site].BeszelToken {
			t.Errorf("system %d is %+v, want %s at %s with its token", i, s, site, cfg.Sites[site].Address)
		}
		if s.Users == nil || len(*s.Users) != 0 {
			t.Errorf("system %s: users %v, want an empty list", s.Name, s.Users)
		}
	}
}

// The hub's private key is the secret exactly, in a 0600 file the hub
// parses as an SSH key.
func TestBeszelKeyFile(t *testing.T) {
	f := beszelFile(t, "data/id_ed25519")
	_, secrets := beszelFixture(t)
	want, _ := secrets.Apps["stats"]["hub_key"].(string)
	if want == "" {
		t.Fatal("the fixture has no apps.stats.hub_key")
	}
	if f.Content != want {
		t.Errorf("id_ed25519 is not apps.stats.hub_key:\n%s", f.Content)
	}
	if f.Mode != 0o600 {
		t.Errorf("the hub's private key was rendered %o", f.Mode)
	}
	if _, err := ssh.ParsePrivateKey([]byte(f.Content)); err != nil {
		t.Errorf("the hub could not parse id_ed25519: %v", err)
	}
}

// setup.sh writes the oidc provider in PocketBase v0.40.5's own field names
// (core/collection_model_auth_options.go:407-413 and :527-543), with this
// app's client and Pocket ID's endpoints, and carries them, so it is 0600.
func TestBeszelSetupWritesTheProvider(t *testing.T) {
	f := beszelFile(t, "setup.sh")
	if f.Mode != 0o600 {
		t.Errorf("setup.sh carries the client secret and was rendered %o", f.Mode)
	}
	var body string
	for _, line := range strings.Split(f.Content, "\n") {
		if strings.HasPrefix(line, `{"oauth2":`) {
			body = line
		}
	}
	var patch struct {
		OAuth2 struct {
			Enabled   bool `json:"enabled"`
			Providers []struct {
				Name         string `json:"name"`
				DisplayName  string `json:"displayName"`
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
				AuthURL      string `json:"authURL"`
				TokenURL     string `json:"tokenURL"`
				UserInfoURL  string `json:"userInfoURL"`
				PKCE         *bool  `json:"pkce"`
			} `json:"providers"`
		} `json:"oauth2"`
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		t.Fatalf("the users collection's PATCH body is not the JSON PocketBase reads: %v\n%s", err, body)
	}
	if !patch.OAuth2.Enabled || len(patch.OAuth2.Providers) != 1 {
		t.Fatalf("oauth2 %+v, want enabled with one provider", patch.OAuth2)
	}
	p := patch.OAuth2.Providers[0]
	want := map[string][2]string{
		"name":         {p.Name, "oidc"},
		"clientId":     {p.ClientID, "fixture-not-a-secret-stats-id"},
		"clientSecret": {p.ClientSecret, "fixture-not-a-secret-stats-secret"},
		"authURL":      {p.AuthURL, "https://id.example.org/authorize"},
		"tokenURL":     {p.TokenURL, "https://id.example.org/api/oidc/token"},
		"userInfoURL":  {p.UserInfoURL, "https://id.example.org/api/oidc/userinfo"},
	}
	for field, gw := range want {
		if gw[0] != gw[1] {
			t.Errorf("%s is %q, want %q", field, gw[0], gw[1])
		}
	}
	if p.PKCE == nil || !*p.PKCE {
		t.Error("the provider leaves PKCE off, which the client at Pocket ID requires")
	}
}

// setup.sh signs in as the superuser with the break glass address and
// admin_password (PocketBase apis/record_auth_with_password.go:102-105), and
// sets the break glass user's password to the same.
func TestBeszelSetupSignsInAndResetsThePassword(t *testing.T) {
	f := beszelFile(t, "setup.sh")
	_, secrets := beszelFixture(t)
	password, _ := secrets.Apps["stats"]["admin_password"].(string)
	line := func(prefix string) string {
		for _, l := range strings.Split(f.Content, "\n") {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		t.Fatalf("setup.sh has no body starting %s:\n%s", prefix, f.Content)
		return ""
	}
	var auth struct {
		Identity string `json:"identity"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(strings.NewReader(line(`{"identity":`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&auth); err != nil {
		t.Fatalf("the sign in body is not the JSON PocketBase reads: %v", err)
	}
	if auth.Identity != "beszel-admin@example.org" || auth.Password != password {
		t.Errorf("signs in as %q with %q, want beszel-admin@example.org with apps.stats.admin_password", auth.Identity, auth.Password)
	}
	var reset struct {
		Password        string `json:"password"`
		PasswordConfirm string `json:"passwordConfirm"`
	}
	dec = json.NewDecoder(strings.NewReader(line(`{"password":`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reset); err != nil {
		t.Fatalf("the password reset body is not the JSON PocketBase reads: %v", err)
	}
	if reset.Password != password || reset.PasswordConfirm != password {
		t.Errorf("resets the user's password to %q / %q, want apps.stats.admin_password twice", reset.Password, reset.PasswordConfirm)
	}
}

// setup takes the hub's .env, so a changed .env, a rotated admin_password
// among them, recreates it under `up -d` and setup.sh runs again.
func TestBeszelSetupTakesTheEnv(t *testing.T) {
	f := beszelFile(t, "compose.yaml")
	var compose struct {
		Services map[string]struct {
			EnvFile []string `yaml:"env_file"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(f.Content), &compose); err != nil {
		t.Fatalf("compose.yaml is not YAML: %v", err)
	}
	for _, service := range []string{"app", "setup"} {
		if got := compose.Services[service].EnvFile; len(got) != 1 || got[0] != ".env" {
			t.Errorf("%s's env_file is %v, want [.env]", service, got)
		}
	}
}

// PocketBase's superuser panel is refused at the edge, ahead of the proxy.
func TestBeszelSnippetRefusesTheSuperuserPanel(t *testing.T) {
	cfg, secrets := beszelFixture(t)
	plan, err := Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	var snippet string
	for _, f := range plan.Files {
		if f.Path == "watch/srv/paisans/f2a9/infra/caddy/etc/snippets/stats.caddy" {
			snippet = f.Content
		}
	}
	matcher := strings.Index(snippet, "\n@superuser_panel path /_ /_/*\n")
	refusal := strings.Index(snippet, "\nrespond @superuser_panel 404\n")
	proxy := strings.Index(snippet, "\nreverse_proxy ")
	if matcher < 0 || refusal < matcher || proxy < refusal {
		t.Errorf("the snippet does not refuse /_/ ahead of the proxy:\n%s", snippet)
	}
}

// beszelAgentEnv is the agent's env file's path on a site.
func beszelAgentEnv(site string) string {
	return site + "/srv/paisans/f2a9/infra/beszel-agent/beszel-agent.env"
}

// Without a beszel app no site runs an agent, and the deployment plans
// against a core catalog released before the Beszel tracks, which names
// neither image.
func TestNoBeszelRendersNothing(t *testing.T) {
	cfg, secrets := beszelFixture(t)
	delete(cfg.Apps, "stats")
	real := foundationImage
	t.Cleanup(func() { foundationImage = real })
	resolved := 0
	foundationImage = func(name string) (string, error) {
		if name == blueprints.FoundationBeszel || name == blueprints.FoundationBeszelAgent {
			return "", fmt.Errorf("the core catalog's foundation.yaml has no %s image; it needs a catalog release that publishes one", name)
		}
		resolved++
		return real(name)
	}
	plan, err := Build(cfg, secrets)
	if err != nil {
		t.Fatalf("a deployment without a beszel app does not plan against a foundation without the Beszel tracks: %v", err)
	}
	if resolved == 0 {
		t.Fatal("the test's foundation was never read")
	}
	for _, f := range plan.Files {
		if strings.Contains(f.Path, "/infra/beszel-agent/") {
			t.Errorf("%s rendered without a beszel app", f.Path)
		}
		if strings.HasSuffix(f.Path, "/infra/compose.yaml") && strings.Contains(f.Content, "beszel-agent:") {
			t.Errorf("%s runs a beszel agent without a beszel app", f.Path)
		}
	}
}

// With the stats app on watch, every site's infrastructure stack runs an
// agent on the host's network, with the deployment user's Docker socket
// read only and its data directory under the stack, and reads a 0600 env
// file naming the hub at watch's mesh address, the site's own token, the
// hub's key and the site's name.
func TestBeszelAgentEveryInfra(t *testing.T) {
	cfg, secrets := beszelFixture(t)
	plan, err := Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]File{}
	for _, f := range plan.Files {
		files[f.Path] = f
	}
	foundation, err := blueprints.FoundationImage(blueprints.FoundationBeszelAgent)
	if err != nil {
		t.Fatal(err)
	}
	key := secrets.Apps["stats"]["hub_key_public"].(string)
	for _, site := range cfg.SiteNames() {
		compose, ok := files[site+"/srv/paisans/f2a9/infra/compose.yaml"]
		if !ok {
			t.Errorf("%s has no infra stack", site)
			continue
		}
		var parsed struct {
			Services map[string]struct {
				Image       string   `yaml:"image"`
				NetworkMode string   `yaml:"network_mode"`
				EnvFile     []string `yaml:"env_file"`
				Volumes     []string `yaml:"volumes"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(compose.Content), &parsed); err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		agent, ok := parsed.Services["beszel-agent"]
		if !ok {
			t.Errorf("%s's infra stack runs no beszel-agent", site)
			continue
		}
		if agent.NetworkMode != "host" {
			t.Errorf("%s: network_mode %q, want host", site, agent.NetworkMode)
		}
		if agent.Image != foundation {
			t.Errorf("%s: image %q, want the foundation's %q", site, agent.Image, foundation)
		}
		wantVolumes := []string{
			"${PAISANS_DOCKER_SOCKET:?run by paisans deployment apply}:/var/run/docker.sock:ro",
			"/srv/paisans/f2a9/infra/beszel-agent/data:/var/lib/beszel-agent",
		}
		if strings.Join(agent.Volumes, "\n") != strings.Join(wantVolumes, "\n") {
			t.Errorf("%s: volumes %q, want %q", site, agent.Volumes, wantVolumes)
		}
		if len(agent.EnvFile) != 1 || agent.EnvFile[0] != "beszel-agent/beszel-agent.env" {
			t.Errorf("%s: env_file %v", site, agent.EnvFile)
		}

		env, ok := files[beszelAgentEnv(site)]
		if !ok {
			t.Errorf("%s has no beszel-agent.env", site)
			continue
		}
		if env.Mode != 0o600 {
			t.Errorf("%s: beszel-agent.env carries the site's token and was rendered %o", site, env.Mode)
		}
		for _, line := range []string{
			"HUB_URL=http://10.44.0.4:8084",
			"TOKEN=" + secrets.Sites[site].BeszelToken,
			"KEY=" + key,
			"SYSTEM_NAME=" + site,
			"DISABLE_SSH=true",
			"DOCKER_HOST=unix:///var/run/docker.sock",
			"DATA_DIR=/var/lib/beszel-agent",
		} {
			if !strings.Contains("\n"+env.Content+"\n", "\n"+line+"\n") {
				t.Errorf("%s: beszel-agent.env has no line %q:\n%s", site, line, env.Content)
			}
		}
		if strings.Contains(compose.Content, secrets.Sites[site].BeszelToken) {
			t.Errorf("%s: the compose file names the site's token", site)
		}
	}
	// The image is the foundation's in the plan, as every infra image is,
	// and infra.images.beszel-agent overrides it.
	if image := beszelAgentImage(t, plan, "home-a"); len(image.FoundationServices) != 1 || image.Setting() != "infra.images.beszel-agent" {
		t.Errorf("the agent's image is %+v", image)
	}
	const pinned = "ghcr.io/henrygd/beszel/beszel-agent:0.21.99"
	cfg.Infra.Images = map[string]string{blueprints.FoundationBeszelAgent: pinned}
	plan, err = Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if image := beszelAgentImage(t, plan, "home-a"); image.Image != pinned || image.Origin != "infra.images.beszel-agent" {
		t.Errorf("an overridden agent image is %+v", image)
	}
}

// beszelAgentImage is the plan's image of site's beszel-agent.
func beszelAgentImage(t *testing.T, plan *Plan, site string) ComposeImage {
	t.Helper()
	for _, image := range plan.Images[site] {
		if image.Stack == "infra" && slices.Contains(image.Services, "beszel-agent") {
			return image
		}
	}
	t.Fatalf("%s runs no beszel-agent image: %+v", site, plan.Images[site])
	return ComposeImage{}
}
