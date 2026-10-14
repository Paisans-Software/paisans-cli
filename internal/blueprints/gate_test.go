package blueprints_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// Every kind says whether it can be gated, so a new kind fails here until
// somebody decides, rather than defaulting to a gate it might not survive.
func TestEveryBlueprintHasAGateRecord(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		if !blueprints.GateRecorded(blueprint) {
			t.Errorf("%s has no gate record", blueprint)
		}
	}
}

// Every path a gate record names is a Caddy path, and every auto-login target
// too: a target that did not start with / would be read by `redir` as a
// matcher, and the redirect would silently become an empty 200.
func TestGatePathsAreAbsolute(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		spec := blueprints.GateFor(blueprint)
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
				t.Errorf("%s: %q is not an absolute path", blueprint, p)
			}
		}
	}
}

// A kind that can be gated records nothing that would open it whole, and a
// kind that cannot be gated records nothing at all.
func TestNoGateRecordOpensTheFrontPage(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		spec := blueprints.GateFor(blueprint)
		open := blueprints.OpenPathsFor(blueprint)
		if slices.Contains(open, "/") || slices.Contains(open, "/*") || slices.Contains(spec.TokenPaths, "/*") {
			t.Errorf("%s opens its whole site past the gate: %v %v", blueprint, open, spec.TokenPaths)
		}
		if !spec.Gateable && (len(spec.OpenPaths) > 0 || len(spec.TokenPaths) > 0 || len(spec.AutoLogin) > 0 || spec.SignedFetch != nil) {
			t.Errorf("%s cannot be gated, yet records what gets past a gate", blueprint)
		}
	}
}

// A dedicated health route is open past the gate, so a gated app can be
// checked through the edge; `/` is the front page and never is.
func TestADedicatedHealthRouteIsOpen(t *testing.T) {
	for _, blueprint := range []config.Blueprint{config.Blueprint("mbin"), config.Blueprint("wikijs"), config.Blueprint("writefreely")} {
		if !slices.Contains(blueprints.OpenPathsFor(blueprint), "/healthz") {
			t.Errorf("%s's /healthz is not open past the gate", blueprint)
		}
	}
}

// A federating kind that can be gated records how it refuses an unsigned
// read, because the gate leaves ActivityPub to the app.
func TestAGateableFederatingBlueprintRecordsSignedFetch(t *testing.T) {
	for _, blueprint := range []config.Blueprint{config.Blueprint("mbin"), config.Blueprint("writefreely")} {
		if blueprints.GateFor(blueprint).SignedFetch == nil {
			t.Errorf("%s federates and can be gated, but records no signed fetch", blueprint)
		}
		if len(blueprints.GateFor(blueprint).InboxPaths) == 0 {
			t.Errorf("%s federates and can be gated, but records no inbox, so deliveries would meet the gate", blueprint)
		}
	}
}

// Signed fetch is the app's authorized fetch setting, on unless
// settings.authorized_fetch turns it off. WriteFreely reads only from its
// allowlist in private mode alone, so one with private turned off has no
// signed fetch either, and one with federation off does not federate at all.
func TestSignedFetchFollowsAuthorizedFetch(t *testing.T) {
	for _, blueprint := range []config.Blueprint{config.Blueprint("mbin"), config.Blueprint("writefreely")} {
		app := config.App{Blueprint: blueprint}
		if !blueprints.AuthorizedFetch(app) || blueprints.SignedFetchFor(app) == nil || !blueprints.Federates(app) {
			t.Fatalf("a default %s has no authorized fetch, no signed fetch, or does not federate", blueprint)
		}
		app.Settings = map[string]any{blueprints.AuthorizedFetchSetting: false}
		if blueprints.AuthorizedFetch(app) {
			t.Errorf("%s: authorized_fetch false still reads as on", blueprint)
		}
		if blueprints.SignedFetchFor(app) != nil {
			t.Errorf("%s: authorized_fetch false still claims signed fetch", blueprint)
		}
	}
	app := config.App{Blueprint: config.Blueprint("writefreely"), Settings: map[string]any{"private": false}}
	if blueprints.SignedFetchFor(app) != nil {
		t.Error("a writefreely with private off still claims signed fetch")
	}
	app.Settings = map[string]any{"federation": false}
	if blueprints.Federates(app) {
		t.Error("a writefreely with federation off still federates")
	}
	sf := blueprints.GateFor(config.Blueprint("writefreely")).SignedFetch
	if probe := sf.SignedFetchProbe("blog.example.org"); probe != "/api/collections/blog.example.org/outbox" {
		t.Errorf("writefreely probe is %q", probe)
	}
	if !slices.Contains(sf.Settings, "app.authorized_fetch") {
		t.Errorf("writefreely signed fetch settings %v do not name app.authorized_fetch", sf.Settings)
	}
	if probe := blueprints.GateFor(config.Blueprint("mbin")).SignedFetch.SignedFetchProbe("talk.example.org"); probe != "/" {
		t.Errorf("mbin probe is %q", probe)
	}
}

// A gated Mbin lets a native app through on its token, and opens before sign
// in only the three API routes the app calls before it holds one. The rest of
// the API, /api/instance/* included, needs the token. MBIN_PRIVATE_INSTANCE is
// what makes the token path safe; render's mbin_env_test.go holds that it is
// rendered for every gated Mbin.
func TestMbinOpensItsAPIToATokenOnly(t *testing.T) {
	spec := blueprints.GateFor(config.Blueprint("mbin"))
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

// A kind with auto-login names the sign-in page a public app sends through the
// gate's sign-in, and a kind without has none, since nothing would fire after.
func TestAutoLoginNamesTheSignInPage(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		spec := blueprints.GateFor(blueprint)
		if (len(spec.AutoLogin) > 0) != (spec.SignInPath != "") {
			t.Errorf("%s: %d auto-login rules, sign-in page %q", blueprint, len(spec.AutoLogin), spec.SignInPath)
		}
	}
}
