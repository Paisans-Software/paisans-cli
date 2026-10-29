package render_test

import (
	"regexp"
	"slices"
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

// The digest names the community by its name, and falls back to Mbin's meta
// title without one. Its instance title is Mbin's, so each case asserts the
// instance title, and a meta title from the fallback, against Mbin's own .env.
func TestMbinTitles(t *testing.T) {
	for _, c := range []struct {
		display, community string
		settings           map[string]any
		title, meta        string
	}{
		{"Talk", "Paisans", nil, "Talk", "Paisans"},
		{"Talk", "", nil, "Talk", "Talk"},
		{"", "Fixture Community", nil, "talk", "Fixture Community"},
		{"Talk", "Paisans", map[string]any{"title": "Forum", "meta_title": "The Forum"}, "Forum", "Paisans"},
		{"Talk", "", map[string]any{"meta_title": "The Forum"}, "Talk", "The Forum"},
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
		if got := envValue(digest, "ENGAGEMENT_META_TITLE"); got != c.meta {
			t.Errorf("display %q, community %q: ENGAGEMENT_META_TITLE=%s, want %s", c.display, c.community, got, c.meta)
		}
		if mbin := envValue(talk, "KBIN_META_TITLE"); c.community == "" && c.meta != mbin {
			t.Errorf("display %q: ENGAGEMENT_META_TITLE=%s, but Mbin renders KBIN_META_TITLE=%s", c.display, c.meta, mbin)
		}
		got := envValue(digest, "ENGAGEMENT_INSTANCE_TITLE")
		if mbin := envValue(talk, "KBIN_TITLE"); got != c.title || got != mbin {
			t.Errorf("display %q: ENGAGEMENT_INSTANCE_TITLE=%s, want %s, and Mbin renders KBIN_TITLE=%s", c.display, got, c.title, mbin)
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
	// No forum, so no agent client and the Mbin welcomes stay off.
	for _, key := range []string{"ENGAGEMENT_MBIN_API_URL=", "ENGAGEMENT_MBIN_AGENT_CLIENT_ID=", "ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET="} {
		if strings.Contains(env, key) {
			t.Errorf("the .env with no Mbin app renders %s", key)
		}
	}
}

// The fixture's digest links to its one homeserver, chat on vm, whose
// wellknown hostname is example.org.
func TestEngagementMatrixEnv(t *testing.T) {
	env := planFiles(build(t))[digestEnvA]
	for key, want := range map[string]string{
		"ENGAGEMENT_MATRIX_URL":              "https://chat.example.org",
		"ENGAGEMENT_MATRIX_SERVER_NAME":      "example.org",
		"ENGAGEMENT_MAS_ADMIN_URL":           "http://10.44.0.3:18010",
		"ENGAGEMENT_MAS_CLIENT_ID":           blueprints.StableULID("digest-mas-admin"),
		"ENGAGEMENT_MAS_CLIENT_SECRET":       "fixture-not-a-secret-digest-mas-admin",
		"ENGAGEMENT_MAS_UPSTREAM_PROVIDER":   blueprints.StableULID("chat"),
		"ENGAGEMENT_MATRIX_BOT_LOCALPART":    "community-bot",
		"ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME": "Community Bot",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
}

func TestEngagementBotLocalpartSetting(t *testing.T) {
	cfg := fixture(t)
	app := cfg.Apps["digest"]
	app.Settings = map[string]any{"bot_localpart": "weekly", "bot_display_name": "Weekly Bot"}
	cfg.Apps["digest"] = app
	digest, _ := digestAndTalkEnv(t, cfg)
	if got := envValue(digest, "ENGAGEMENT_MATRIX_BOT_LOCALPART"); got != "weekly" {
		t.Errorf("ENGAGEMENT_MATRIX_BOT_LOCALPART=%s, want weekly", got)
	}
	if got := envValue(digest, "ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME"); got != "Weekly Bot" {
		t.Errorf("ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME=%s, want Weekly Bot", got)
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
	// The linked render with its Matrix block taken out, the block's comments
	// and every ENGAGEMENT_MATRIX_, ENGAGEMENT_MAS_ and
	// ENGAGEMENT_COMMUNITY_ROOM_ line, is the unlinked
	// render byte for byte.
	lines := strings.Split(with, "\n")
	start := slices.Index(lines, "# The homeserver the bot runs on, and the MAS admin client it looks")
	if start < 0 {
		t.Fatalf("the linked .env has no Matrix block:\n%s", with)
	}
	end := start
	for end < len(lines) && (strings.HasPrefix(lines[end], "#") || strings.HasPrefix(lines[end], "ENGAGEMENT_MATRIX_") || strings.HasPrefix(lines[end], "ENGAGEMENT_MAS_") || strings.HasPrefix(lines[end], "ENGAGEMENT_COMMUNITY_ROOM_")) {
		end++
	}
	want := strings.Join(slices.Delete(lines, start, end), "\n")
	if env != want {
		t.Errorf("the digest's .env with no homeserver:\n%s\nwant the linked one without its Matrix block:\n%s", env, want)
	}
}

// The digest's hostname has no page of its own: the front page and /d/ with
// no date redirect to the account app, or to the community's domain in a
// deployment without one.
func TestEngagementRedirectsHome(t *testing.T) {
	want := "@home path / /d /d/ /d/preview /d/preview/\nhandle @home {\n\tredir https://account.example.org 302\n}"
	snippet := planFiles(build(t))["vm/srv/paisans/f2a9/infra/caddy/etc/snippets/digest.caddy"]
	if !strings.Contains(snippet, want) {
		t.Errorf("digest.caddy does not redirect home to the account app:\n%s", snippet)
	}
	cfg := fixture(t)
	delete(cfg.Apps, "account")
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatal(err)
	}
	snippet = planFiles(plan)["vm/srv/paisans/f2a9/infra/caddy/etc/snippets/digest.caddy"]
	if !strings.Contains(snippet, "redir https://example.org 302") {
		t.Errorf("with no account app, digest.caddy does not redirect to the community's domain:\n%s", snippet)
	}
}

// The welcome loop's variables: the forum's API on the mesh and the agent
// client, the community room, the daily limit and the operators.
func TestEngagementWelcomeEnv(t *testing.T) {
	env := planFiles(build(t))[digestEnvA]
	for key, want := range map[string]string{
		"ENGAGEMENT_MBIN_AGENT_CLIENT_ID":     "engagement-agent",
		"ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET": "fixture-not-a-secret-digest-mbin-agent",
		"ENGAGEMENT_COMMUNITY_ROOM_NAME":      "Fixture Community",
		"ENGAGEMENT_COMMUNITY_ROOM_ALIAS":     "fixture-community",
		"ENGAGEMENT_WELCOME_DAILY_LIMIT":      "50",
		"ENGAGEMENT_OPERATOR_EMAILS":          "alice@example.org,bob@example.org",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
	// home-a's own mesh address: Mbin is clustered like the digest, so it
	// runs on the same site.
	if got, want := envValue(env, "ENGAGEMENT_MBIN_API_URL"), "http://10.44.0.1:8080"; got != want {
		t.Errorf("ENGAGEMENT_MBIN_API_URL=%s, want %s", got, want)
	}
}

func TestEngagementWelcomeSettings(t *testing.T) {
	cfg := fixture(t)
	app := cfg.Apps["digest"]
	app.Settings = map[string]any{"community_room_name": "Riverside Hall", "welcome_email_daily_limit": 12}
	cfg.Apps["digest"] = app
	digest, _ := digestAndTalkEnv(t, cfg)
	for key, want := range map[string]string{
		"ENGAGEMENT_COMMUNITY_ROOM_NAME":  "Riverside Hall",
		"ENGAGEMENT_COMMUNITY_ROOM_ALIAS": "riverside-hall",
		"ENGAGEMENT_WELCOME_DAILY_LIMIT":  "12",
	} {
		if got := envValue(digest, key); got != want {
			t.Errorf("%s=%s, want %s", key, got, want)
		}
	}
}

// Without a community name the room is named for what the digest calls the
// community: Mbin's meta title.
func TestEngagementRoomWithoutCommunityName(t *testing.T) {
	cfg := fixture(t)
	cfg.Community.Name = ""
	digest, _ := digestAndTalkEnv(t, cfg)
	meta := envValue(digest, "ENGAGEMENT_META_TITLE")
	if got, want := envValue(digest, "ENGAGEMENT_COMMUNITY_ROOM_NAME"), render.CommunityRoomName(meta); got != want {
		t.Errorf("ENGAGEMENT_COMMUNITY_ROOM_NAME=%s, want %s", got, want)
	}
}
