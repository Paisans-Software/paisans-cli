package validate_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

func load(t *testing.T, name string) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", name+".yaml"))
	if err != nil {
		t.Fatalf("loading %s: %v", name, err)
	}
	return cfg
}

// Each fixture is the valid one with exactly one thing broken, so a rule that
// fires on the wrong fixture is a test failure rather than a puzzle.
func TestRulesFire(t *testing.T) {
	cases := []struct {
		fixture string
		rule    string
		level   validate.Level
	}{
		{"witness-shares-failure-domain", "witness-shares-failure-domain", validate.Refuse},
		{"account-must-be-clustered", "account-must-be-clustered", validate.Refuse},
		{"account-without-pocket-id", "account-without-pocket-id", validate.Refuse},
		{"account-login-link-disabled", "account-login-link-disabled", validate.Refuse},
		{"account-under-pocket-id-management", "account-under-pocket-id-management", validate.Refuse},
		{"operators-fewer-than-two", "operators-fewer-than-two", validate.Refuse},
		{"pocket-id-management-unset", "pocket-id-management-unset", validate.Refuse},
		{"pocket-id-setting-managed", "pocket-id-setting-managed", validate.Refuse},
		{"pocket-id-without-account", "pocket-id-without-account", validate.Refuse},
		{"pocket-id-paisans-without-smtp", "pocket-id-paisans-without-smtp", validate.Refuse},
		{"account-without-smtp", "account-without-smtp", validate.Refuse},
		{"account-code-of-conduct-missing", "account-code-of-conduct-missing", validate.Refuse},
		{"account-setting-invalid", "account-setting-invalid", validate.Refuse},
		{"account-one-identity-app", "account-one-identity-app", validate.Refuse},
		{"account-setting-unknown", "account-setting-unknown", validate.Warn},
		{"account-invites-unset", "account-invites-unset", validate.Refuse},
		{"account-invite-settings-without-invites", "account-invite-settings-without-invites", validate.Refuse},
		{"account-invite-hostname-without-invites", "account-invite-hostname-without-invites", validate.Refuse},
		{"account-serves-nothing", "account-serves-nothing", validate.Warn},
		{"two-etcd-voters", "two-etcd-voters", validate.Refuse},
		{"undeclared-site", "undeclared-site", validate.Refuse},
		{"invalid-placement", "invalid-placement", validate.Refuse},
		{"sso-consent-not-a-boolean", "sso-consent-not-a-boolean", validate.Refuse},
		{"sso-dashboard-link-not-a-path", "sso-dashboard-link-not-a-path", validate.Refuse},
		{"cluster-site-without-data-role", "cluster-site-without-data-role", validate.Refuse},
		{"cluster-app-without-apps-site", "cluster-app-without-apps-site", validate.Refuse},
		{"garage-replication-exceeds-sites", "garage-replication-exceeds-sites", validate.Refuse},
		{"garage-consistency-unknown", "garage-consistency-unknown", validate.Refuse},
		{"storage-role-without-garage", "storage-role-without-garage", validate.Refuse},
		{"garage-capacity-for-no-garage-site", "garage-capacity-for-no-garage-site", validate.Refuse},
		{"garage-capacity-not-a-size", "garage-capacity-not-a-size", validate.Refuse},
		{"garage-consistency-dangerous", "garage-consistency-dangerous", validate.Warn},
		{"garage-consistency-degraded-is-consistent", "garage-consistency-degraded-is-consistent", validate.Warn},
		{"garage-single-copy", "garage-single-copy", validate.Warn},
		{"garage-two-sites-stop-uploads", "garage-two-sites-stop-uploads", validate.Warn},
		{"garage-partial-uploads", "garage-partial-uploads", validate.Warn},
		{"site-address-outside-mesh", "site-address-outside-mesh", validate.Refuse},
		{"duplicate-site-address", "duplicate-site-address", validate.Refuse},
		{"sites-share-a-machine", "sites-share-a-machine", validate.Refuse},
		{"sites-share-a-machine-endpoint", "sites-share-a-machine", validate.Refuse},
		{"unknown-image-service", "unknown-image-service", validate.Refuse},
		{"floating-image-tag", "floating-image-tag", validate.Refuse},
		{"cluster-placement-without-a-cluster", "cluster-placement-without-a-cluster", validate.Refuse},
		{"even-etcd-voters", "even-etcd-voters", validate.Warn},
		{"mesh-subnet-is-not-private", "mesh-subnet-is-not-private", validate.Warn},
		{"pinned-app-on-witness", "pinned-app-on-witness", validate.Warn},
		{"gateway-on-data-site", "gateway-on-data-site", validate.Warn},
		{"pocket-id-file-backend", "pocket-id-file-backend", validate.Warn},
		{"admin-group-not-admins", "admin-group-not-admins", validate.Warn},
		{"mbin-queue-unknown", "mbin-queue-unknown", validate.Refuse},
		{"mbin-rabbitmq-across-sites", "mbin-rabbitmq-across-sites", validate.Warn},
		{"pocket-id-standby-marker-unknown", "pocket-id-standby-marker-unknown", validate.Warn},
		{"image-for-absent-postgres", "image-for-absent-postgres", validate.Warn},
		{"watchdog-off-on-data-site", "watchdog-off-on-data-site", validate.Warn},
		{"acme-provider-needs-an-image", "acme-provider-needs-an-image", validate.Refuse},
		{"acme-image-is-stock-caddy", "acme-image-is-stock-caddy", validate.Refuse},
		{"acme-image-is-latest", "acme-image-is-latest", validate.Refuse},
		{"unknown-hostname-role", "unknown-hostname-role", validate.Refuse},
		{"duplicate-hostname", "duplicate-hostname", validate.Refuse},
		{"gated-on-ungateable-blueprint", "gated-on-ungateable-blueprint", validate.Refuse},
		{"gated-without-signed-fetch", "gated-without-signed-fetch", validate.Refuse},
		{"gated-without-authorized-fetch", "gated-without-signed-fetch", validate.Refuse},
		{"auto-login-without-a-rule", "auto-login-without-a-rule", validate.Refuse},
		{"config-key-already-rendered", "config-key-already-rendered", validate.Refuse},
		{"uses-gate-outside-the-domain", "uses-gate-outside-the-domain", validate.Refuse},
		{"authorized-fetch-not-a-boolean", "authorized-fetch-not-a-boolean", validate.Refuse},
		{"writefreely-setting-invalid", "writefreely-setting-invalid", validate.Refuse},
		{"gated-on-pocket-id", "gated-on-ungateable-blueprint", validate.Refuse},
		{"gated-on-the-monitor", "gated-on-ungateable-blueprint", validate.Refuse},
		{"homeserver-must-be-pinned", "homeserver-must-be-pinned", validate.Refuse},
		{"storage-local-on-cluster", "storage-local-on-cluster", validate.Refuse},
		{"storage-s3-on-a-local-only-blueprint", "storage-s3-on-a-local-only-blueprint", validate.Refuse},
		{"storage-on-pocket-id", "storage-on-pocket-id", validate.Refuse},
		{"media-hostname-is-not-a-hostname", "media-hostname-is-not-a-hostname", validate.Refuse},
		{"media-hostname-outside-the-domain", "media-hostname-outside-the-domain", validate.Refuse},
		{"mail-domain-is-not-a-hostname", "mail-domain-is-not-a-hostname", validate.Refuse},
		{"mail-domain-outside-the-domain", "mail-domain-outside-the-domain", validate.Refuse},
		{"media-hostname-under-an-app-hostname", "media-hostname-under-an-app-hostname", validate.Refuse},
		{"duplicate-derived-media-hostname", "duplicate-hostname", validate.Refuse},
		{"config-key-is-nested-in-an-env-file", "config-key-is-nested-in-an-env-file", validate.Refuse},
		{"config-key-looks-like-a-secret", "config-key-looks-like-a-secret", validate.Refuse},
		{"config-key-steers-compose", "config-key-steers-compose", validate.Refuse},
		{"oidc-member-group-disagrees-with-gate", "oidc-member-group-disagrees-with-gate", validate.Refuse},
		{"oidc-member-group-not-a-name", "oidc-member-group-not-a-name", validate.Refuse},
		{"oidc-extra-groups-not-names", "oidc-group-not-a-name", validate.Refuse},
		{"wikijs-synced-groups-lack-admin-group", "wikijs-synced-groups-lack-admin-group", validate.Refuse},
		{"smtp-on-a-blueprint-without-mail", "smtp-on-a-blueprint-without-mail", validate.Refuse},
		{"uptime-without-smtp", "uptime-without-smtp", validate.Warn},
		{"mbin-without-smtp", "mbin-without-smtp", validate.Warn},
		{"mbin-smtp-without-sender", "mbin-smtp-without-sender", validate.Warn},
		{"mbin-smtp-from-not-an-email", "mbin-smtp-from-not-an-email", validate.Refuse},
		{"mbin-contact-email-not-an-email", "mbin-contact-email-not-an-email", validate.Refuse},
		{"no-uptime-monitor", "no-uptime-monitor", validate.Warn},
		{"monitor-on-gateway", "monitor-shares-a-site", validate.Warn},
		{"monitor-on-witness", "monitor-shares-a-site", validate.Warn},
		{"monitor-shares-a-site", "monitor-shares-a-site", validate.Warn},
		{"web-server-on-gateway", "web-server-on-gateway", validate.Refuse},
		{"monitor-without-uptime", "monitor-without-uptime", validate.Refuse},
		{"uptime-needs-a-monitor-site", "uptime-needs-a-monitor-site", validate.Refuse},
		{"monitor-without-public-address", "monitor-without-public-address", validate.Refuse},
		{"web-server-outside-monitor", "web-server-outside-monitor", validate.Refuse},
		{"web-server-listen-managed-by", "web-server-listen-managed-by", validate.Refuse},
		{"web-server-listen-public", "web-server-listen-public", validate.Refuse},
		{"web-server-listen-bypasses-firewall", "web-server-listen-bypasses-firewall", validate.Warn},
		{"web-server-external-serves-one-app", "web-server-external-serves-one-app", validate.Refuse},
		{"voters-share-a-relay", "voters-share-a-relay", validate.Refuse},
		{"data-site-not-in-cluster", "data-site-not-in-cluster", validate.Refuse},
		{"async-automatic-failover", "async-automatic-failover", validate.Warn},
		{"one-voter-no-failover", "one-voter-no-failover", validate.Warn},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			result := validate.Check(load(t, tc.fixture))
			if !result.Has(tc.rule) {
				t.Fatalf("rule %s did not fire. findings: %v", tc.rule, result.Findings)
			}
			for _, f := range result.Findings {
				if f.Rule != tc.rule {
					continue
				}
				if f.Level != tc.level {
					t.Fatalf("rule %s fired at %s, want %s", tc.rule, f.Level, tc.level)
				}
				if f.Key == "" {
					t.Fatalf("rule %s fired without naming a key", tc.rule)
				}
				if len(f.Message) < 40 {
					t.Fatalf("rule %s message is too short to act on: %q", tc.rule, f.Message)
				}
			}
			// A refusal blocks rendering; a warning must not.
			if tc.level == validate.Refuse && !result.Refused() {
				t.Fatal("a refusal did not block rendering")
			}
			if tc.level == validate.Warn && result.Refused() {
				t.Fatalf("a warning blocked rendering: %v", result.Refusals())
			}
		})
	}
}

// withConfig is the valid fixture plus the element app of the secret fixture,
// so that env (wiki, auth) and json (web) blueprints are all present, with
// every passthrough map emptied and then one app's replaced by keys.
func withConfig(t *testing.T, app string, keys map[string]any) *config.Config {
	t.Helper()
	cfg := load(t, "config-key-looks-like-a-secret")
	// The valid fixture's backups, so the base is quiet without each case
	// tolerating the warning that a deployment without them draws.
	cfg.Backup = load(t, "valid").Backup
	for name, a := range cfg.Apps {
		a.Config = nil
		cfg.Apps[name] = a
	}
	if result := validate.Check(cfg); len(result.Findings) != 0 {
		t.Fatalf("the base for these cases is not quiet: %v", result.Findings)
	}
	a, ok := cfg.Apps[app]
	if !ok {
		t.Fatalf("the fixture declares no app %q", app)
	}
	a.Config = keys
	cfg.Apps[app] = a
	return cfg
}

// refusedFor reports whether rule refused exactly the given key.
func refusedFor(result validate.Result, rule, key string) bool {
	for _, f := range result.Refusals() {
		if f.Rule == rule && f.Key == key {
			return true
		}
	}
	return false
}

// The secret check reads a name the way people write one, so the separator a
// convention puts between the words does not hide the word. The env spelling
// API_KEY is the likeliest real case, and the camel case privateKey the json
// one.
func TestTheSecretCheckIgnoresSeparatorsAndCase(t *testing.T) {
	cases := []struct {
		app, key string
		refused  bool
	}{
		{"wiki", "SENDGRID_API_KEY", true},
		{"web", "privateKey", true},
		{"web", "signing.private-key", true},
		{"wiki", "SMTP_PASSWORD", true},
		{"web", "app.api_token", true},
		// A control: an ordinary name in the same places is not refused, so
		// the rows above are not passing because everything is.
		{"wiki", "DEFAULT_LANGUAGE", false},
		{"web", "default_theme", false},
	}
	for _, tc := range cases {
		result := validate.Check(withConfig(t, tc.app, map[string]any{tc.key: "x"}))
		got := refusedFor(result, "config-key-looks-like-a-secret", "apps."+tc.app+".config."+tc.key)
		if got != tc.refused {
			t.Errorf("%s on %s: refused=%v, want %v. findings: %v", tc.key, tc.app, got, tc.refused, result.Findings)
		}
	}
}

// Both prefixes are refused on an env blueprint, whose .env compose also
// reads, and on no other blueprint, whose config file compose never sees.
func TestComposeSteeringKeysAreRefusedOnEnvBlueprintsOnly(t *testing.T) {
	cases := []struct {
		app, key string
		refused  bool
	}{
		{"wiki", "COMPOSE_PROJECT_NAME", true},
		{"wiki", "COMPOSE_PROFILES", true},
		{"auth", "DOCKER_HOST", true},
		{"web", "COMPOSE_PROJECT_NAME", false},
		{"wiki", "MY_COMPOSE_NOTE", false},
	}
	for _, tc := range cases {
		result := validate.Check(withConfig(t, tc.app, map[string]any{tc.key: "x"}))
		got := refusedFor(result, "config-key-steers-compose", "apps."+tc.app+".config."+tc.key)
		if got != tc.refused {
			t.Errorf("%s on %s: refused=%v, want %v. findings: %v", tc.key, tc.app, got, tc.refused, result.Findings)
		}
	}
}

// The valid fixture must be quiet. A rule that fires on a coherent
// configuration trains an operator to ignore the output.
func TestValidFixtureIsQuiet(t *testing.T) {
	result := validate.Check(load(t, "valid"))
	if len(result.Findings) != 0 {
		t.Fatalf("valid fixture produced findings: %v", result.Findings)
	}
}

// Pocket ID with cluster placement on two apps sites is allowed: one
// instance is active and the others stand by (README, "Pocket ID runs on
// every apps site, and one of them is active"). The valid fixture is that
// shape, so this names the fact rather than leaving it implied.
func TestPocketIDOnTwoAppsSitesIsAllowed(t *testing.T) {
	cfg := load(t, "valid")
	if n := len(cfg.AppsSites()); n < 2 {
		t.Fatalf("the valid fixture has %d apps site(s), so this proves nothing", n)
	}
	if app := cfg.Apps["auth"]; app.Blueprint != config.BlueprintPocketID || app.Placement.Mode != config.PlacementCluster {
		t.Fatalf("the valid fixture's auth is not a clustered pocket-id: %+v", app)
	}
	if result := validate.Check(cfg); len(result.Findings) != 0 {
		t.Fatalf("findings: %v", result.Findings)
	}
}

// The shipped example must validate. It deliberately demonstrates three
// warnings: its Synapse stack is pinned to the site that also holds the
// witness role, its two Garage sites at replication 2 stop uploads while
// either is down, and its home backup destination is never pruned. The
// monitor has a site of its own and adds none.
func TestExampleValidates(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "paisans-stack.example.yaml"))
	if err != nil {
		t.Fatalf("loading the example: %v", err)
	}
	result := validate.Check(cfg)
	if result.Refused() {
		t.Fatalf("the example was refused: %v", result.Refusals())
	}
	warnings := result.Warnings()
	var rules []string
	for _, w := range warnings {
		rules = append(rules, w.Rule)
	}
	sort.Strings(rules)
	if strings.Join(rules, ",") != "backup-prune-off,garage-two-sites-stop-uploads,pinned-app-on-witness" {
		t.Fatalf("unexpected warnings on the example: %v", warnings)
	}
}

// The home sites example must validate with only the Garage warning its two
// sites at replication 2 carry, and taking its second dialled voter away must
// be refused: that witness is the reason the layout exists.
func TestHomeSitesExampleValidates(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "home-sites.yaml"))
	if err != nil {
		t.Fatalf("loading the example: %v", err)
	}
	result := validate.Check(cfg)
	if result.Refused() {
		t.Fatalf("the example was refused: %v", result.Refusals())
	}
	var rules []string
	for _, w := range result.Warnings() {
		rules = append(rules, w.Rule)
	}
	if strings.Join(rules, ",") != "garage-two-sites-stop-uploads" {
		t.Fatalf("unexpected warnings on the example: %v", result.Warnings())
	}

	vm2 := cfg.Sites["vm2"]
	vm2.PublicAddress = ""
	vm2.SSH.Host = "vm2.local"
	cfg.Sites["vm2"] = vm2
	refused := false
	for _, f := range validate.Check(cfg).Refusals() {
		refused = refused || f.Rule == "voters-share-a-relay"
	}
	if !refused {
		t.Fatal("with vm2 not dialled, the example should be refused as voters-share-a-relay")
	}
}

// One fixture per refusal must name its rule in the failure, because the
// message is the only thing an operator sees.
func TestRefusalMessagesAreActionable(t *testing.T) {
	result := validate.Check(load(t, "two-etcd-voters"))
	var found bool
	for _, f := range result.Refusals() {
		if f.Rule != "two-etcd-voters" {
			continue
		}
		found = true
		if !strings.Contains(f.Message, "Add a witness") || !strings.Contains(f.Message, "declare one member") {
			t.Fatalf("the refusal does not say what to do instead: %q", f.Message)
		}
	}
	if !found {
		t.Fatal("two-etcd-voters did not fire")
	}
}

// The voters that survive any one site must still dial each other. Each case
// is the valid fixture reshaped: what matters is how many voters have an
// endpoint, not which site holds the witness.
func TestVotersShareARelay(t *testing.T) {
	const rule = "voters-share-a-relay"
	addSite := func(cfg *config.Config, name, address string) {
		site := cfg.Sites["home-b"]
		site.Address = address
		site.Endpoint = ""
		cfg.Sites[name] = site
	}
	cases := []struct {
		name    string
		change  func(*config.Config)
		refused bool
		mention []string
	}{
		{
			name:   "one home with an endpoint is accepted",
			change: func(*config.Config) {},
		},
		{
			name: "no home with an endpoint is refused, naming the relay and the fix",
			change: func(cfg *config.Config) {
				site := cfg.Sites["home-a"]
				site.Endpoint = ""
				cfg.Sites["home-a"] = site
			},
			refused: true,
			mention: []string{"vm", "home-a", "home-b", "endpoint"},
		},
		{
			// Three data voters and no witness: the one site with an
			// endpoint is the relay for the other two, so its loss strands
			// them exactly as the witness's does above.
			name: "three data voters with one endpoint is refused",
			change: func(cfg *config.Config) {
				addSite(cfg, "home-c", "10.44.0.5")
				cfg.Cluster.Sites = []string{"home-a", "home-b", "home-c"}
				cfg.Etcd.Members = []string{"home-a", "home-b", "home-c"}
				vm := cfg.Sites["vm"]
				vm.Roles = []config.Role{config.RoleGateway}
				cfg.Sites["vm"] = vm
			},
			refused: true,
			mention: []string{"home-a", "home-b", "home-c"},
		},
		{
			name: "three data voters with two endpoints is accepted",
			change: func(cfg *config.Config) {
				addSite(cfg, "home-c", "10.44.0.5")
				cfg.Cluster.Sites = []string{"home-a", "home-b", "home-c"}
				cfg.Etcd.Members = []string{"home-a", "home-b", "home-c"}
				vm := cfg.Sites["vm"]
				vm.Roles = []config.Role{config.RoleGateway}
				cfg.Sites["vm"] = vm
				b := cfg.Sites["home-b"]
				b.Endpoint = "home-b.example.org:51820"
				cfg.Sites["home-b"] = b
			},
		},
		{
			// A public address is dialled without an endpoint written.
			name: "three data voters, two with only a public address, is accepted",
			change: func(cfg *config.Config) {
				addSite(cfg, "home-c", "10.44.0.5")
				cfg.Cluster.Sites = []string{"home-a", "home-b", "home-c"}
				cfg.Etcd.Members = []string{"home-a", "home-b", "home-c"}
				vm := cfg.Sites["vm"]
				vm.Roles = []config.Role{config.RoleGateway}
				cfg.Sites["vm"] = vm
				for name, address := range map[string]string{"home-a": "203.0.113.31", "home-b": "203.0.113.32"} {
					site := cfg.Sites[name]
					site.Endpoint, site.PublicAddress = "", address
					cfg.Sites[name] = site
				}
			},
		},
		{
			// A declined witness leaves one voter, and one voter has
			// nobody to be partitioned from.
			name: "one voter is not this rule's concern",
			change: func(cfg *config.Config) {
				site := cfg.Sites["home-a"]
				site.Endpoint = ""
				cfg.Sites["home-a"] = site
				cfg.Etcd.Members = []string{"home-a"}
				vm := cfg.Sites["vm"]
				vm.Roles = []config.Role{config.RoleGateway}
				cfg.Sites["vm"] = vm
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, "valid")
			tc.change(cfg)
			result := validate.Check(cfg)
			if result.Has(rule) != tc.refused {
				t.Fatalf("%s fired: %v, want %v. findings: %v", rule, result.Has(rule), tc.refused, result.Findings)
			}
			for _, f := range result.Findings {
				if f.Rule != rule {
					continue
				}
				for _, m := range tc.mention {
					if !strings.Contains(f.Message, m) {
						t.Errorf("the refusal does not mention %q: %s", m, f.Message)
					}
				}
			}
		})
	}
}

// A witness-less pair with one voter warns, and the warning says what the
// voter's loss does; a single site with one voter is the plain case and must
// not.
func TestOneVoterWarnsOnlyWithReplicas(t *testing.T) {
	cfg := load(t, "one-voter-no-failover")
	cfg.Cluster.Sites = []string{"home-a"}
	b := cfg.Sites["home-b"]
	b.Roles = []config.Role{config.RoleApps}
	cfg.Sites["home-b"] = b
	if result := validate.Check(cfg); result.Has("one-voter-no-failover") {
		t.Fatalf("one data site with one voter warned: %v", result.Findings)
	}
}

// Asynchronous replication is only a failover risk where a failover can
// happen on its own: with one voter nothing promotes anyone.
func TestAsyncWarnsOnlyWithAutomaticFailover(t *testing.T) {
	cfg := load(t, "async-automatic-failover")
	cfg.Etcd.Members = []string{"home-a"}
	vm := cfg.Sites["vm"]
	vm.Roles = []config.Role{config.RoleGateway}
	cfg.Sites["vm"] = vm
	if result := validate.Check(cfg); result.Has("async-automatic-failover") {
		t.Fatalf("one voter warned about automatic failover: %v", result.Findings)
	}
}

// Findings are sorted, so that two runs over one file print the same thing and
// a diff between runs means something.
func TestFindingsAreDeterministic(t *testing.T) {
	cfg := load(t, "witness-shares-failure-domain")
	first := validate.Check(cfg)
	second := validate.Check(cfg)
	if len(first.Findings) != len(second.Findings) {
		t.Fatal("two runs produced different numbers of findings")
	}
	for i := range first.Findings {
		if first.Findings[i] != second.Findings[i] {
			t.Fatalf("finding %d differs between runs", i)
		}
	}
}

// withMedia is the valid fixture with blog's media hostname declared, or left
// to the derivation when media is empty, and one more app's hostname set.
func withMedia(t *testing.T, media string) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	blog := cfg.Apps["blog"]
	blog.Hostnames = nil
	if media != "" {
		blog.Hostnames = map[string]string{"media": media}
	}
	cfg.Apps["blog"] = blog
	return cfg
}

// The derived name and a declared one are checked alike, because both become
// a site address. A derived name can be wrong without anything in the file
// being wrong: a label of 60 characters is legal, and is not once -media is
// appended to it.
func TestADerivedMediaHostnameIsCheckedLikeADeclaredOne(t *testing.T) {
	cfg := withMedia(t, "")
	if result := validate.Check(cfg); len(result.Findings) != 0 {
		t.Fatalf("the derived blog-media.example.org should be quiet, got %v", result.Findings)
	}

	long := strings.Repeat("d", 60)
	blog := cfg.Apps["blog"]
	blog.Hostname = long + ".example.org"
	cfg.Apps["blog"] = blog
	result := validate.Check(cfg)
	if !refusedFor(result, "media-hostname-is-not-a-hostname", "apps.blog.hostnames.media") {
		t.Fatalf("%s-media is a 66 character label and must be refused, got %v", long, result.Findings)
	}
	for _, f := range result.Refusals() {
		if f.Rule == "media-hostname-is-not-a-hostname" && !strings.Contains(f.Message, "derived") {
			t.Errorf("a refusal of a name nobody wrote must say it was derived, got %q", f.Message)
		}
	}
}

// Each declared shape that cannot work, against the key an operator edits.
func TestADeclaredMediaHostnameIsAHostnameUnderTheDomain(t *testing.T) {
	for _, tc := range []struct {
		media, rule string
	}{
		{"Blog-Media.example.org", "media-hostname-is-not-a-hostname"},
		{"https://blog-media.example.org", "media-hostname-is-not-a-hostname"},
		{"blog-media.example.org:8443", "media-hostname-is-not-a-hostname"},
		{"-blog.example.org", "media-hostname-is-not-a-hostname"},
		{"blog..example.org", "media-hostname-is-not-a-hostname"},
		{"localhost", "media-hostname-is-not-a-hostname"},
		{"blog-media.example.net", "media-hostname-outside-the-domain"},
		// A suffix match on the bare domain would accept this one.
		{"blog-media.notexample.org", "media-hostname-outside-the-domain"},
		{"media.blog.example.org", "media-hostname-under-an-app-hostname"},
		// A child of another app's hostname is refused too.
		{"media.id.example.org", "media-hostname-under-an-app-hostname"},
	} {
		result := validate.Check(withMedia(t, tc.media))
		if !refusedFor(result, tc.rule, "apps.blog.hostnames.media") {
			t.Errorf("hostnames.media %q: want %s, got %v", tc.media, tc.rule, result.Findings)
		}
	}
	for _, ok := range []string{"blog-media.example.org", "attachments.example.org", "a.b.example.org"} {
		if result := validate.Check(withMedia(t, ok)); len(result.Findings) != 0 {
			t.Errorf("hostnames.media %q should be accepted, got %v", ok, result.Findings)
		}
	}
}

// A declared media hostname that another app already uses as its own, and a
// derived one that collides with a declared one, are the same refusal from two
// directions.
func TestAMediaHostnameCannotBeAnotherAppsHostname(t *testing.T) {
	result := validate.Check(withMedia(t, "id.example.org"))
	if !refusedFor(result, "duplicate-hostname", "apps.blog.hostnames.media") && !refusedFor(result, "duplicate-hostname", "apps.auth.hostname") {
		t.Errorf("blog's media hostname is auth's own hostname and must be refused, got %v", result.Findings)
	}

	cfg := load(t, "duplicate-derived-media-hostname")
	result = validate.Check(cfg)
	var found bool
	for _, f := range result.Refusals() {
		if f.Rule == "duplicate-hostname" && strings.Contains(f.Message, "derived") {
			found = true
		}
	}
	if !found {
		t.Errorf("a clash with a derived media hostname must say the name was derived, got %v", result.Findings)
	}
}

// A site holding nothing but the monitor role, with the monitor pinned to it,
// draws no finding of its own: it needs no other role. The fixture declares
// no backups, which is warned about and is not what this test is about.
func TestAMonitorOnlySiteIsQuiet(t *testing.T) {
	result := validate.Check(load(t, "uptime-without-smtp"))
	for _, f := range result.Findings {
		if f.Rule != "uptime-without-smtp" && f.Rule != "backups-off" {
			t.Errorf("unexpected finding: %s", f)
		}
	}
}

// A role-less site stays legal for any blueprint but uptime, which needs a site
// holding the monitor role.
func TestARolelessSiteStillHostsOtherBlueprints(t *testing.T) {
	cfg := load(t, "uptime-without-smtp")
	watch := cfg.Sites["watch"]
	watch.Roles = nil
	cfg.Sites["watch"] = watch
	web := config.App{Blueprint: config.Blueprint("element"), Hostname: "web.example.org", Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"}}
	cfg.Apps["web"] = web
	result := validate.Check(cfg)
	if !refusedFor(result, "uptime-needs-a-monitor-site", "apps.health.placement") {
		t.Fatalf("uptime on a role-less site: %v", result.Findings)
	}
	delete(cfg.Apps, "health")
	for _, f := range validate.Check(cfg).Refusals() {
		t.Errorf("a role-less site hosting element was refused: %s", f)
	}
}

// A listen address is where Docker publishes the app for the operator's web
// server. Loopback is quiet; a LAN address or the site's own mesh address is
// allowed with the firewall warning; anything the internet or another host
// could be reached on is refused. 10.44.0.1 is RFC 1918 as well as inside the
// mesh, but it is another site's address, which nothing on this host binds.
func TestWebServerListenAddresses(t *testing.T) {
	for _, tc := range []struct {
		listen         string
		refused, warns bool
	}{
		{"127.0.0.1:8480", false, false},
		{"192.168.1.20:8480", false, true},
		{"10.44.0.4:8480", false, true},
		{"10.44.0.1:8480", true, false},
		{"203.0.113.20:8480", true, false},
		{"0.0.0.0:8480", true, false},
	} {
		cfg := load(t, "uptime-without-smtp")
		site := cfg.Sites["watch"]
		site.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: tc.listen}
		cfg.Sites["watch"] = site
		result := validate.Check(cfg)
		if result.Has("web-server-listen-public") != tc.refused || result.Has("web-server-listen-bypasses-firewall") != tc.warns {
			t.Errorf("%s: %v", tc.listen, result.Findings)
		}
	}
}

// mode external without listen has nowhere to publish the app.
func TestExternalWebServerNeedsListen(t *testing.T) {
	cfg := load(t, "uptime-without-smtp")
	site := cfg.Sites["watch"]
	site.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal}
	cfg.Sites["watch"] = site
	if !refusedFor(validate.Check(cfg), "web-server-listen-managed-by", "sites.watch.web_server.listen") {
		t.Fatal("external with no listen was not refused")
	}
}

// include_dir is where apply writes for the operator's web server: it fits
// mode external alone, and must be a plain absolute path. false fits either.
func TestWebServerIncludeDir(t *testing.T) {
	for _, tc := range []struct {
		mode config.ManagedBy
		dir  config.IncludeDir
		rule string
	}{
		{config.ManagedByExternal, config.IncludeDir{Path: "/opt/caddy/paisans.d"}, ""},
		{config.ManagedByExternal, config.IncludeDir{Off: true}, ""},
		{config.ManagedByExternal, config.IncludeDir{Path: "paisans.d"}, "web-server-include-dir-path"},
		{config.ManagedByExternal, config.IncludeDir{Path: "/etc/nginx/*"}, "web-server-include-dir-path"},
		{config.ManagedByPaisans, config.IncludeDir{Path: "/opt/caddy/paisans.d"}, "web-server-include-dir-managed-by"},
	} {
		cfg := load(t, "uptime-without-smtp")
		site := cfg.Sites["watch"]
		site.WebServer = &config.WebServer{ManagedBy: tc.mode, IncludeDir: tc.dir}
		if tc.mode == config.ManagedByExternal {
			site.WebServer.Listen = "127.0.0.1:8480"
		}
		cfg.Sites["watch"] = site
		result := validate.Check(cfg)
		for _, rule := range []string{"web-server-include-dir-path", "web-server-include-dir-managed-by"} {
			if want := rule == tc.rule; refusedFor(result, rule, "sites.watch.web_server.include_dir") != want {
				t.Errorf("%s %+v: %s refused %v, want %v: %v", tc.mode, tc.dir, rule, !want, want, result.Findings)
			}
		}
	}
}

// acme.image's module check covers every site running the toolkit's Caddy,
// a monitor serving its own hostname included, not only a gateway.
func TestACMEProviderNeedsAnImageForAMonitorsCaddy(t *testing.T) {
	cfg := load(t, "uptime-without-smtp")
	vm := cfg.Sites["vm"]
	vm.Roles = []config.Role{config.RoleWitness}
	cfg.Sites["vm"] = vm
	cfg.ACME = config.ACME{Provider: "route53"}
	if !validate.Check(cfg).Has("acme-provider-needs-an-image") {
		t.Fatal("a monitor's Caddy with an unpublished provider and no image was not refused")
	}
}

// Carrier grade NAT space, which Tailscale uses, is as private to the site as
// RFC 1918: allowed as a listen address, with the firewall warning.
func TestACGNATListenIsWarnedNotRefused(t *testing.T) {
	cfg := load(t, "uptime-without-smtp")
	site := cfg.Sites["watch"]
	site.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "100.101.102.103:8480"}
	cfg.Sites["watch"] = site
	result := validate.Check(cfg)
	if result.Has("web-server-listen-public") || !result.Has("web-server-listen-bypasses-firewall") {
		t.Fatalf("%v", result.Findings)
	}
}

// An external monitor's compose network is pinned at 10.255.255.0/29, so a
// mesh over it would route the mesh into a Docker bridge.
func TestTheWebServerNetworkMustNotOverlapTheMesh(t *testing.T) {
	cfg := load(t, "uptime-without-smtp")
	site := cfg.Sites["watch"]
	site.WebServer = &config.WebServer{ManagedBy: config.ManagedByExternal, Listen: "127.0.0.1:8480"}
	cfg.Sites["watch"] = site
	if validate.Check(cfg).Has("web-server-network-overlaps-mesh") {
		t.Fatal("refused with the usual mesh")
	}
	cfg.Mesh.Subnet = "10.255.0.0/16"
	for name, s := range cfg.Sites {
		s.Address = strings.Replace(s.Address, "10.44.", "10.255.", 1)
		cfg.Sites[name] = s
	}
	if !refusedFor(validate.Check(cfg), "web-server-network-overlaps-mesh", "sites.watch.web_server") {
		t.Fatalf("%v", validate.Check(cfg).Findings)
	}
}

// A static check: every warn and refuse call passes a hint literal, or a
// fmt.Sprintf whose format is one.
func TestEveryCallPassesAHint(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	// rule literal, key expression (which may hold one level of call
	// parentheses with commas, Eg: fmt.Sprintf("sites.%s", name)), then the
	// hint literal.
	re := regexp.MustCompile(`c\.(warn|refuse)\(\s*"[^"]+",\s*(?:[^,()]|\([^()]*\))+,\s*(?:fmt\.Sprintf\()?"([^"]*)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		calls := regexp.MustCompile(`c\.(warn|refuse)\(`).FindAllIndex(src, -1)
		hinted := re.FindAllSubmatch(src, -1)
		if len(calls) != len(hinted) {
			t.Errorf("%s: %d warn/refuse calls, %d with a hint literal", f, len(calls), len(hinted))
		}
		for _, m := range hinted {
			if h := string(m[2]); h == "" || len(h) > 100 {
				t.Errorf("%s: bad hint %q", f, h)
			}
		}
	}
}

// The dangerous-consistency warning says, in its one line, how many copies
// must be written before an upload is confirmed, out of the replication
// factor.
func TestGarageDangerousNamesItsQuorums(t *testing.T) {
	for _, f := range validate.Check(load(t, "garage-consistency-dangerous")).Findings {
		if f.Rule != "garage-consistency-dangerous" {
			continue
		}
		if want := "garage consistency is dangerous: an upload is confirmed even if only 1 of 2 copies is written"; f.Hint != want {
			t.Errorf("hint %q, want %q", f.Hint, want)
		}
		return
	}
	t.Fatal("no garage-consistency-dangerous finding")
}

// acme.image may follow a moving tag, because apply resolves it to the build
// it names while it plans. `latest`, and no tag at all, are refused: they move
// to the next Caddy major version, whose Caddyfile may not be the one the
// gateway is rendered with.
func TestACMEImageMayMoveWithinAMajorVersion(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/example-org/caddy-route53:2",
		"ghcr.io/example-org/caddy-route53:2.11",
		"ghcr.io/example-org/caddy-route53:2.11.4",
		"ghcr.io/example-org/caddy-route53@sha256:b401d1cb18074026a535a1facc5f6d9d35bc62fdff480f3b1fd91042c8e48186",
		"ghcr.io/paisans-software/caddy:2",
	} {
		cfg := load(t, "acme-image-is-latest")
		cfg.ACME.Image = image
		if validate.Check(cfg).Has("acme-image-is-latest") {
			t.Errorf("%s was refused, but a tag inside one major version is resolved at apply", image)
		}
	}
	for _, image := range []string{
		"ghcr.io/example-org/caddy-route53:latest",
		"ghcr.io/example-org/caddy-route53",
	} {
		cfg := load(t, "acme-image-is-latest")
		cfg.ACME.Image = image
		if !refusedFor(validate.Check(cfg), "acme-image-is-latest", "acme.image") {
			t.Errorf("%s was not refused, and it crosses Caddy major versions", image)
		}
	}
}

// The watchdog warning names the mechanism that is actually rendered. Spilo
// strips every PATRONI_* variable before it starts Patroni, so the mode is set
// in SPILO_CONFIGURATION, and a warning that sent the operator looking for
// PATRONI_WATCHDOG_MODE in patroni.env would send them looking for nothing.
func TestWatchdogWarningNamesSpiloConfiguration(t *testing.T) {
	result := validate.Check(load(t, "watchdog-off-on-data-site"))
	found := false
	for _, f := range result.Findings {
		if f.Rule != "watchdog-off-on-data-site" {
			continue
		}
		found = true
		if !strings.Contains(f.Message, "SPILO_CONFIGURATION") {
			t.Errorf("the warning does not name SPILO_CONFIGURATION: %s", f.Message)
		}
		if strings.Contains(f.Message, "PATRONI_WATCHDOG_MODE") {
			t.Errorf("the warning names PATRONI_WATCHDOG_MODE, which Spilo never passes to Patroni: %s", f.Message)
		}
	}
	if !found {
		t.Fatalf("the watchdog warning did not fire: %v", result.Findings)
	}
}

// Two sites behind one router share its public address, each with a port of
// its own forwarded to it for ssh and for WireGuard. That is two machines,
// and is not refused; the same address and port for either is one machine.
func TestSitesBehindOneRouterAreTwoMachines(t *testing.T) {
	cfg := load(t, "valid")
	share := func(cfg *config.Config, sshPort int, endpoint string) {
		for name, port := range map[string]int{"home-a": 2201, "home-b": sshPort} {
			site := cfg.Sites[name]
			site.SSH.Host = ""
			site.PublicAddress = "203.0.113.30"
			site.SSH.Port = port
			cfg.Sites[name] = site
		}
		a := cfg.Sites["home-a"]
		a.Endpoint = ":51820"
		cfg.Sites["home-a"] = a
		b := cfg.Sites["home-b"]
		b.Endpoint = endpoint
		cfg.Sites["home-b"] = b
	}
	share(cfg, 2202, ":51821")
	if result := validate.Check(cfg); result.Has("sites-share-a-machine") {
		t.Fatalf("two sites behind one router were refused: %v", result.Refusals())
	}

	cfg = load(t, "valid")
	share(cfg, 2201, ":51821")
	if !validate.Check(cfg).Has("sites-share-a-machine") {
		t.Error("two sites at one ssh address and port were not refused")
	}
	cfg = load(t, "valid")
	share(cfg, 2202, ":51820")
	if !validate.Check(cfg).Has("sites-share-a-machine") {
		t.Error("two sites dialled at one address and port were not refused")
	}
}

// An app on the gate's hostname collides with the gate, which no file
// declares, so the message says where the gate's name came from rather than
// quoting a line the file does not have. The app is blog because its key
// sorts before gate, and the refusal falls on the later of the two.
func TestAnAppOnTheGatesHostnameIsRefused(t *testing.T) {
	cfg := load(t, "valid")
	blog := cfg.Apps["blog"]
	blog.Gated = true
	blog.Hostname = cfg.GateHostname()
	cfg.Apps["blog"] = blog
	cfg.Apps[config.GateApp] = config.App{Blueprint: config.BlueprintOAuth2Proxy, Hostname: cfg.GateHostname(), Derived: true}
	var found bool
	for _, f := range validate.Check(cfg).Refusals() {
		if f.Rule == "duplicate-hostname" {
			found = true
			if !strings.Contains(f.Message, "is not declared, so it is derived as") {
				t.Errorf("the refusal does not say the gate's name is derived: %s", f.Message)
			}
		}
	}
	if !found {
		t.Error("an app on the gate's hostname was not refused")
	}
}

// Whether a template writes a key can depend on the rest of the file: a gated
// Mbin's template writes MBIN_USE_FEDERATION_ALLOW_LIST and an ungated one
// does not. validate renders to find out, so the same key is refused on the
// gated app and accepted on the public one, and every colliding key is
// reported, not only the first.
func TestAKeyTheTemplateWritesIsRefusedBeforeRender(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	talk := cfg.Apps["talk"]
	talk.Config = map[string]any{"MBIN_USE_FEDERATION_ALLOW_LIST": true, "MBIN_PRIVATE_INSTANCE": true, "KBIN_META_TITLE": "A place to talk"}
	cfg.Apps["talk"] = talk
	var keys []string
	for _, f := range validate.Check(cfg).Refusals() {
		if f.Rule == "config-key-already-rendered" {
			keys = append(keys, f.Key)
		}
	}
	want := []string{"apps.talk.config.MBIN_PRIVATE_INSTANCE", "apps.talk.config.MBIN_USE_FEDERATION_ALLOW_LIST"}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("gated talk: refused %v, want %v", keys, want)
	}

	talk.Gated, talk.AutoLogin = false, false
	cfg.Apps["talk"] = talk
	for _, f := range validate.Check(cfg).Refusals() {
		if f.Rule == "config-key-already-rendered" {
			t.Errorf("public talk: %s refused, but its template does not write it", f.Key)
		}
	}
}
