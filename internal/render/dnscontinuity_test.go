package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const (
	membersToken = "fixture-not-a-secret-members"
	dnsHashKey   = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	superToken   = "5555555555555555555555555555eeee"
	twoFAToken   = "6666666666666666666666666666ffff"
	bobAtWork    = "bob.work@example.org"
)

// withDNS is the fixture with a monitor, on Cloudflare, with the members
// token pasted, the check's generated secrets, and a second admin whose
// address at the provider differs.
func withDNS(t *testing.T, twoMonitors bool) (*config.Config, *config.Secrets) {
	t.Helper()
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	if twoMonitors {
		cfg, secrets = withTwoMonitors(t)
	}
	cfg.ACME.Provider = "cloudflare"
	cfg.Admins["bob"] = config.Admin{Name: "Bob Example", Email: "bob@example.org", Keys: cfg.Admins["alice"].Keys}
	if secrets.External == nil {
		secrets.External = map[string]string{}
	}
	secrets.External["dns_members_token"] = membersToken
	secrets.Apps["auth"]["dns_email_hash_key"] = dnsHashKey
	secrets.Apps["auth"]["dns_super_admins_heartbeat_token"] = superToken
	secrets.Apps["auth"]["dns_2fa_heartbeat_token"] = twoFAToken
	secrets.Admins = map[string]config.AdminSecrets{"bob": {DNSProviderEmail: bobAtWork}}
	return cfg, secrets
}

func buildFiles(t *testing.T, cfg *config.Config, secrets *config.Secrets) map[string]string {
	t.Helper()
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return planFiles(plan)
}

const authEnv = "home-a/srv/paisans/f2a9/auth/.env"

// Without the members token, or on another provider, nothing of the check is
// rendered, whatever else the deployment has.
func TestNoMembersTokenMeansNoDNSCheck(t *testing.T) {
	for name, change := range map[string]func(*config.Config, *config.Secrets){
		"no token":         func(_ *config.Config, s *config.Secrets) { delete(s.External, "dns_members_token") },
		"another provider": func(c *config.Config, _ *config.Secrets) { c.ACME.Provider = "desec" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, secrets := withDNS(t, false)
			change(cfg, secrets)
			files := buildFiles(t, cfg, secrets)
			for path, content := range files {
				if strings.Contains(content, "PAISANS_DNS_") || strings.Contains(content, "DNS_SUPER_ADMINS") || strings.Contains(content, "dns super admins") {
					t.Errorf("%s renders the DNS check: %s", path, content)
				}
			}
		})
	}
}

// With it, the Pocket ID .env carries the token, the domain, the key and the
// hashed map, and no address at the provider.
func TestTheDNSCheckIsRenderedIntoTheReconcilersEnv(t *testing.T) {
	cfg, secrets := withDNS(t, false)
	files := buildFiles(t, cfg, secrets)
	env := files[authEnv]
	for _, want := range []string{
		"\nPAISANS_DNS_MEMBERS_TOKEN=" + membersToken + "\n",
		"\nPAISANS_DNS_DOMAIN=" + cfg.Community.Domain + "\n",
		"\nPAISANS_DNS_EMAIL_HASH_KEY=" + dnsHashKey + "\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf(".env lacks %q", strings.TrimSpace(want))
		}
	}
	i := strings.Index(env, "PAISANS_DNS_EMAIL_MAP=\"")
	if i < 0 {
		t.Fatalf(".env has no map: %s", env)
	}
	line := env[i+len("PAISANS_DNS_EMAIL_MAP=\""):]
	line = line[:strings.Index(line, "\"")]
	from, to, ok := strings.Cut(line, "=")
	if !ok || len(from) != 64 || len(to) != 64 || strings.Contains(line, " ") {
		t.Errorf("the map is %q, want one pair of hashes", line)
	}
	for path, content := range files {
		if strings.Contains(content, bobAtWork) {
			t.Errorf("%s carries the mapped address", path)
		}
		if strings.Contains(content, membersToken) && !strings.HasSuffix(path, "/auth/.env") {
			t.Errorf("%s carries the members token", path)
		}
	}
}

// The seed holds the admins heartbeat and the check's two, each under its own
// token, named for the community.
func TestTheSeedExpectsTheDNSHeartbeats(t *testing.T) {
	cfg, secrets := withDNS(t, false)
	monitors := byName(renderSeed(t, cfg, secrets))
	for name, token := range map[string]string{
		"admin continuity":                   adminsToken(t, secrets),
		"admin continuity: dns super admins": superToken,
		"admin continuity: dns 2fa":          twoFAToken,
	} {
		m := monitors[name]
		if m == nil {
			t.Errorf("no monitor %q", name)
			continue
		}
		if m["monitor_type"] != "heartbeat" || m["heartbeat_token"] != token || m["interval_seconds"] != float64(60) || m["heartbeat_grace_seconds"] != float64(120) || m["failure_threshold"] != float64(2) {
			t.Errorf("%s: %v", name, m)
		}
	}
}

// The pusher's env holds each monitor's URLs under its own token.
func TestThePusherCarriesTheDNSURLs(t *testing.T) {
	cfg, secrets := withDNS(t, false)
	env := buildFiles(t, cfg, secrets)["home-a/srv/paisans/f2a9/auth/admins-heartbeat/heartbeat.env"]
	for _, want := range []string{
		"\nDNS_SUPER_ADMINS_HEARTBEAT_URLS=https://health.example.org/ping/" + superToken + "\n",
		"\nDNS_2FA_HEARTBEAT_URLS=https://health.example.org/ping/" + twoFAToken + "\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("heartbeat.env lacks %q: %s", strings.TrimSpace(want), env)
		}
	}
}

// One cycle relays each of the reconciler's three answers to its own
// monitors.
func TestThePusherRelaysTheDNSAnswers(t *testing.T) {
	cfg, secrets := withDNS(t, true)
	pushes := runPusher(t, cfg, secrets, []string{
		"ADMINS_HEARTBEAT_URLS=https://one.example.org/ping/t1",
		"DNS_SUPER_ADMINS_HEARTBEAT_URLS=https://one.example.org/ping/t2",
		"DNS_2FA_HEARTBEAT_URLS=https://one.example.org/ping/t3",
	}, "503", "1 of 2 admins have 2fa off")
	for _, url := range []string{"https://one.example.org/ping/t1/fail", "https://one.example.org/ping/t2/fail", "https://one.example.org/ping/t3/fail"} {
		if pushedTo(pushes, url) == "" {
			t.Errorf("no failure pushed to %s: %v", url, pushes)
		}
	}
	if len(pushes) != 3 {
		t.Errorf("pushes %v, want three", pushes)
	}
}

// A mapping that would let one membership count for two admins, or that
// names nobody, is refused by name, never by address.
func TestABadMappingIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		admins map[string]config.AdminSecrets
		want   string
	}{
		"unknown admin":         {map[string]config.AdminSecrets{"carol": {DNSProviderEmail: "carol@example.org"}}, "admins.carol"},
		"two at one address":    {map[string]config.AdminSecrets{"alice": {DNSProviderEmail: bobAtWork}, "bob": {DNSProviderEmail: bobAtWork}}, "alice and bob"},
		"another admin's email": {map[string]config.AdminSecrets{"bob": {DNSProviderEmail: " Alice@example.org"}}, "alice and bob"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, secrets := withDNS(t, false)
			secrets.Admins = tc.admins
			_, err := render.Build(cfg, secrets)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "@") {
				t.Errorf("the refusal names an address: %v", err)
			}
		})
	}
}

// With more than one Pocket ID app each continuity monitor carries its app's
// key, since the seed tells monitors apart by name.
func TestTwoPocketIDAppsPrefixTheirContinuityMonitors(t *testing.T) {
	cfg, secrets := withDNS(t, false)
	second := cfg.Apps["auth"]
	second.Hostname = "auth2.example.org"
	cfg.Apps["auth2"] = second
	copied := map[string]any{}
	for k, v := range secrets.Apps["auth"] {
		copied[k] = v
	}
	copied["admins_heartbeat_token"] = "7777777777777777777777777777aaaa"
	copied["dns_super_admins_heartbeat_token"] = "8888888888888888888888888888bbbb"
	copied["dns_2fa_heartbeat_token"] = "9999999999999999999999999999cccc"
	secrets.Apps["auth2"] = copied
	monitors := byName(renderSeed(t, cfg, secrets))
	for _, app := range []string{"auth", "auth2"} {
		for _, name := range []string{app + ": admin continuity", app + ": admin continuity: dns super admins", app + ": admin continuity: dns 2fa"} {
			if monitors[name] == nil {
				t.Errorf("no monitor %q", name)
			}
		}
	}
	if monitors["admin continuity"] != nil {
		t.Error("an unprefixed continuity monitor with two Pocket ID apps")
	}
}
