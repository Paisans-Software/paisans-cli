package kinds_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every kind says whether it can be gated, so a new kind fails here until
// somebody decides, rather than defaulting to a gate it might not survive.
func TestEveryKindHasAGateRecord(t *testing.T) {
	for _, kind := range config.Kinds() {
		if !kinds.GateRecorded(kind) {
			t.Errorf("%s has no gate record", kind)
		}
	}
}

// Every path a gate record names is a Caddy path, and every auto-login target
// too: a target that did not start with / would be read by `redir` as a
// matcher, and the redirect would silently become an empty 200.
func TestGatePathsAreAbsolute(t *testing.T) {
	for _, kind := range config.Kinds() {
		spec := kinds.GateFor(kind)
		paths := slices.Concat(spec.OpenPaths, spec.InboxPaths, spec.TokenPaths)
		for _, rule := range spec.AutoLogin {
			paths = append(paths, rule.Target)
			paths = append(paths, rule.Paths...)
		}
		if spec.SignedFetch != nil {
			paths = append(paths, spec.SignedFetch.Probe)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, "/") {
				t.Errorf("%s: %q is not an absolute path", kind, p)
			}
		}
	}
}

// A kind that can be gated records nothing that would open it whole, and a
// kind that cannot be gated records nothing at all.
func TestNoGateRecordOpensTheFrontPage(t *testing.T) {
	for _, kind := range config.Kinds() {
		spec := kinds.GateFor(kind)
		open := kinds.OpenPathsFor(kind)
		if slices.Contains(open, "/") || slices.Contains(open, "/*") || slices.Contains(spec.TokenPaths, "/*") {
			t.Errorf("%s opens its whole site past the gate: %v %v", kind, open, spec.TokenPaths)
		}
		if !spec.Gateable && (len(spec.OpenPaths) > 0 || len(spec.TokenPaths) > 0 || len(spec.AutoLogin) > 0 || spec.SignedFetch != nil) {
			t.Errorf("%s cannot be gated, yet records what gets past a gate", kind)
		}
	}
}

// A dedicated health route is open past the gate, so a gated app can be
// checked through the edge; `/` is the front page and never is.
func TestADedicatedHealthRouteIsOpen(t *testing.T) {
	if !slices.Contains(kinds.OpenPathsFor(config.KindOutline), "/_health") {
		t.Error("outline's /_health is not open past the gate")
	}
	for _, kind := range []config.Kind{config.KindMbin, config.KindWriteFreely} {
		if !slices.Contains(kinds.OpenPathsFor(kind), "/healthz") {
			t.Errorf("%s's /healthz is not open past the gate", kind)
		}
	}
}

// A federating kind that can be gated records how it refuses an unsigned
// read, because the gate leaves ActivityPub to the app.
func TestAGateableFederatingKindRecordsSignedFetch(t *testing.T) {
	for _, kind := range []config.Kind{config.KindMbin, config.KindWriteFreely} {
		if kinds.GateFor(kind).SignedFetch == nil {
			t.Errorf("%s federates and can be gated, but records no signed fetch", kind)
		}
		if len(kinds.GateFor(kind).InboxPaths) == 0 {
			t.Errorf("%s federates and can be gated, but records no inbox, so deliveries would meet the gate", kind)
		}
	}
}

// Signed fetch is the app's authorized fetch setting, on unless
// settings.authorized_fetch turns it off. WriteFreely reads only from its
// allowlist in private mode alone, so one with private turned off has no
// signed fetch either, and one with federation off does not federate at all.
func TestSignedFetchFollowsAuthorizedFetch(t *testing.T) {
	for _, kind := range []config.Kind{config.KindMbin, config.KindWriteFreely} {
		app := config.App{Kind: kind}
		if !kinds.AuthorizedFetch(app) || kinds.SignedFetchFor(app) == nil || !kinds.Federates(app) {
			t.Fatalf("a default %s has no authorized fetch, no signed fetch, or does not federate", kind)
		}
		app.Settings = map[string]any{kinds.AuthorizedFetchSetting: false}
		if kinds.AuthorizedFetch(app) {
			t.Errorf("%s: authorized_fetch false still reads as on", kind)
		}
		if kinds.SignedFetchFor(app) != nil {
			t.Errorf("%s: authorized_fetch false still claims signed fetch", kind)
		}
	}
	app := config.App{Kind: config.KindWriteFreely, Settings: map[string]any{"private": false}}
	if kinds.SignedFetchFor(app) != nil {
		t.Error("a writefreely with private off still claims signed fetch")
	}
	app.Settings = map[string]any{"federation": false}
	if kinds.Federates(app) {
		t.Error("a writefreely with federation off still federates")
	}
	sf := kinds.GateFor(config.KindWriteFreely).SignedFetch
	if probe := sf.SignedFetchProbe("blog.example.org"); probe != "/api/collections/blog.example.org/outbox" {
		t.Errorf("writefreely probe is %q", probe)
	}
	if !slices.Contains(sf.Settings, "app.authorized_fetch") {
		t.Errorf("writefreely signed fetch settings %v do not name app.authorized_fetch", sf.Settings)
	}
	if probe := kinds.GateFor(config.KindMbin).SignedFetch.SignedFetchProbe("talk.example.org"); probe != "/" {
		t.Errorf("mbin probe is %q", probe)
	}
}

// A gated Mbin lets a native app through on its token, and opens before sign
// in only the three API routes the app calls before it holds one. The rest of
// the API, /api/instance/* included, needs the token. MBIN_PRIVATE_INSTANCE is
// what makes the token path safe; render's mbin_env_test.go holds that it is
// rendered for every gated Mbin.
func TestMbinOpensItsAPIToATokenOnly(t *testing.T) {
	spec := kinds.GateFor(config.KindMbin)
	if !slices.Contains(spec.TokenPaths, "/api/*") {
		t.Errorf("mbin token paths %v lack /api/*, so a native app meets the gate", spec.TokenPaths)
	}
	var api []string
	for _, p := range spec.OpenPaths {
		if strings.HasPrefix(p, "/api") {
			api = append(api, p)
		}
	}
	if want := []string{"/api/client", "/api/info", "/api/instance"}; !slices.Equal(api, want) {
		t.Errorf("mbin opens %v of its API, want exactly %v", api, want)
	}
}
