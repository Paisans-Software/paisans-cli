package render

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

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
		"home-a": {BeszelToken: "token-a"},
		"home-b": {BeszelToken: "token-b"},
		"watch":  {BeszelToken: "token-w"},
	}}
	got, err := beszelSystems(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	want := []beszelSystem{
		{Name: "home-a", Host: "10.44.0.1", Token: "token-a"},
		{Name: "home-b", Host: "10.44.0.2", Token: "token-b"},
		{Name: "watch", Host: "10.44.0.4", Token: "token-w"},
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
