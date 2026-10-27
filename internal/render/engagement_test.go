package render_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

const digestEnvA = "home-a/srv/paisans/f2a9/digest/.env"

func digestAndTalkEnv(t *testing.T, cfg *config.Config) (digest, talk string) {
	t.Helper()
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	digest, ok := files[digestEnvA]
	if !ok {
		t.Fatalf("no %s in the plan", digestEnvA)
	}
	return digest, files[talkEnvA]
}

// The digest names the community exactly as Mbin does, so each case is
// asserted against Mbin's own .env as well as the string it should be.
func TestMbinTitles(t *testing.T) {
	for _, c := range []struct {
		display, community string
		settings           map[string]any
		title, meta        string
	}{
		{"Talk", "Paisans", nil, "Talk", "Talk · Paisans"},
		{"Talk", "", nil, "Talk", "Talk"},
		{"", "Fixture Community", nil, "talk", "talk · Fixture Community"},
		{"Talk", "Paisans", map[string]any{"title": "Forum", "meta_title": "The Forum"}, "Forum", "The Forum"},
	} {
		cfg := fixture(t)
		cfg.Community.Name = c.community
		app := cfg.Apps["talk"]
		app.DisplayName = c.display
		if c.settings != nil {
			app.Settings = c.settings
		}
		cfg.Apps["talk"] = app
		digest, talk := digestAndTalkEnv(t, cfg)
		for key, want := range map[string]struct{ mbin, value string }{
			"ENGAGEMENT_INSTANCE_TITLE": {"KBIN_TITLE", c.title},
			"ENGAGEMENT_META_TITLE":     {"KBIN_META_TITLE", c.meta},
		} {
			got := envValue(digest, key)
			if got != want.value {
				t.Errorf("display %q, community %q: %s=%s, want %s", c.display, c.community, key, got, want.value)
			}
			if mbin := envValue(talk, want.mbin); got != mbin {
				t.Errorf("display %q, community %q: %s=%s, but Mbin renders %s=%s", c.display, c.community, key, got, want.mbin, mbin)
			}
		}
	}
}

// engagementKeys is every variable the digest's .env carries whatever else
// the deployment runs.
var engagementKeys = []string{
	"ENGAGEMENT_LISTEN_ADDR", "ENGAGEMENT_PUBLIC_URL", "ENGAGEMENT_DATABASE_URL",
	"ENGAGEMENT_SIGNING_KEY", "ENGAGEMENT_MBIN_DATABASE_URL", "ENGAGEMENT_MBIN_URL",
	"ENGAGEMENT_INSTANCE_TITLE", "ENGAGEMENT_META_TITLE",
	"ENGAGEMENT_SMTP_HOST", "ENGAGEMENT_SMTP_PORT", "ENGAGEMENT_SMTP_FROM",
	"ENGAGEMENT_SMTP_FROM_NAME", "ENGAGEMENT_SMTP_TLS",
}

// The digest reads talk's database as its own read only role, through its
// site's HAProxy, and links to talk's public hostname.
func TestEngagementEnv(t *testing.T) {
	env := planFiles(build(t))[digestEnvA]
	for _, key := range engagementKeys {
		if envValue(env, key) == "" {
			t.Errorf("%s is empty or absent in the digest's .env:\n%s", key, env)
		}
	}
	for key, want := range map[string]string{
		"ENGAGEMENT_MBIN_DATABASE_URL": "postgresql://digest_reader:fixture-not-a-secret-digest-reader@10.44.0.1:5000/talk",
		"ENGAGEMENT_MBIN_URL":          "https://talk.example.org",
		"ENGAGEMENT_PUBLIC_URL":        "https://digest.example.org",
		"ENGAGEMENT_SMTP_FROM_NAME":    "Example Community",
		"ENGAGEMENT_SMTP_TLS":          "starttls",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
}

func TestReaderRole(t *testing.T) {
	if got := render.ReaderRole("weekly-digest"); got != "weekly_digest_reader" {
		t.Errorf("ReaderRole(weekly-digest) = %s, want weekly_digest_reader", got)
	}
}

// Only the unsubscribe links, the web digests and the health probe the
// monitor's public check reads reach the service; the gateway answers every
// other path itself.
func TestEngagementSnippetProxiesItsThreePaths(t *testing.T) {
	var snippet string
	for path, body := range planFiles(build(t)) {
		if strings.HasSuffix(path, "/digest.caddy") {
			snippet = body
		}
	}
	if snippet == "" {
		t.Fatal("no digest.caddy in the plan")
	}
	for _, want := range []string{
		"@public expression `path('/u/*') || path('/d/*') || (method('GET') && path('/healthz'))`",
		"handle @public {",
		"respond 404",
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the digest's snippet lacks %q:\n%s", want, snippet)
		}
	}
	if n := strings.Count(snippet, "reverse_proxy"); n != 1 {
		t.Errorf("the digest's snippet has %d reverse_proxy, want 1:\n%s", n, snippet)
	}
}

// Validate refuses an engagement app without its Mbin, but a render reached
// without validate still renders, with nothing to connect to.
func TestEngagementWithoutMbinRendersEmpty(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Apps, "talk")
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	env, ok := planFiles(plan)[digestEnvA]
	if !ok {
		t.Fatalf("no %s in the plan", digestEnvA)
	}
	for _, key := range []string{"ENGAGEMENT_MBIN_DATABASE_URL", "ENGAGEMENT_MBIN_URL", "ENGAGEMENT_INSTANCE_TITLE", "ENGAGEMENT_META_TITLE"} {
		if got := envValue(env, key); got != "" {
			t.Errorf("%s=%s with no Mbin app, want empty", key, got)
		}
	}
}

// The fixture's digest links to its one homeserver, chat on vm, whose
// wellknown hostname is example.org.
func TestEngagementMatrixEnv(t *testing.T) {
	env := planFiles(build(t))[digestEnvA]
	for key, want := range map[string]string{
		"ENGAGEMENT_MATRIX_URL":            "https://chat.example.org",
		"ENGAGEMENT_MATRIX_SERVER_NAME":    "example.org",
		"ENGAGEMENT_MAS_ADMIN_URL":         "http://10.44.0.3:18010",
		"ENGAGEMENT_MAS_CLIENT_ID":         blueprints.StableULID("digest-mas-admin"),
		"ENGAGEMENT_MAS_CLIENT_SECRET":     "fixture-not-a-secret-digest-mas-admin",
		"ENGAGEMENT_MAS_UPSTREAM_PROVIDER": blueprints.StableULID("chat"),
		"ENGAGEMENT_MATRIX_BOT_LOCALPART":  "digest-bot",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
}

func TestEngagementBotLocalpartSetting(t *testing.T) {
	cfg := fixture(t)
	app := cfg.Apps["digest"]
	app.Settings = map[string]any{"bot_localpart": "weekly"}
	cfg.Apps["digest"] = app
	digest, _ := digestAndTalkEnv(t, cfg)
	if got := envValue(digest, "ENGAGEMENT_MATRIX_BOT_LOCALPART"); got != "weekly" {
		t.Errorf("ENGAGEMENT_MATRIX_BOT_LOCALPART=%s, want weekly", got)
	}
}

// The service finds a member's Matrix account by the upstream provider MAS
// signs them in with, so the two ids are one.
func TestEngagementUpstreamProviderMatchesMAS(t *testing.T) {
	files := planFiles(build(t))
	mas, ok := files["vm/srv/paisans/f2a9/chat/mas.yaml"]
	if !ok {
		t.Fatal("no chat mas.yaml in the plan")
	}
	_, providers, found := strings.Cut(mas, "upstream_oauth2:")
	if !found {
		t.Fatalf("chat's mas.yaml has no upstream_oauth2:\n%s", mas)
	}
	m := regexp.MustCompile(`(?m)^\s*- id: (\S+)$`).FindStringSubmatch(providers)
	if m == nil {
		t.Fatalf("chat's mas.yaml has no upstream provider id:\n%s", providers)
	}
	if got := envValue(files[digestEnvA], "ENGAGEMENT_MAS_UPSTREAM_PROVIDER"); got != m[1] {
		t.Errorf("ENGAGEMENT_MAS_UPSTREAM_PROVIDER=%s, and chat's mas.yaml names %s", got, m[1])
	}
}

// With no homeserver the bot is off: none of the Matrix variables is
// rendered, and the rest of the .env is what it always is.
func TestEngagementWithoutHomeserverRendersNoMatrix(t *testing.T) {
	with := planFiles(build(t))[digestEnvA]
	cfg := fixture(t)
	delete(cfg.Apps, "chat")
	delete(cfg.Apps, "web")
	env, _ := digestAndTalkEnv(t, cfg)
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "ENGAGEMENT_MATRIX_") || strings.HasPrefix(line, "ENGAGEMENT_MAS_") {
			t.Errorf("the digest's .env has %q with no homeserver", line)
		}
	}
	for _, key := range engagementKeys {
		if got, want := envValue(env, key), envValue(with, key); got != want {
			t.Errorf("%s=%s with no homeserver, want %s", key, got, want)
		}
	}
}
