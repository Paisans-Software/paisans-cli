package secretsrotate

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// selfWorld is a deployment with two apps sites and an apps site that is not
// one, an Mbin with cluster placement, and one pinned app of each other kind
// that checks a secret of its own. presented is the value each site's stack
// presents for a key, keyed site/app/key, as the last apply rendered it.
type selfWorld struct {
	t         *testing.T
	cfg       *config.Config
	store     *memStore
	presented map[string]string
	// unhealthy fails the health gate on that site.
	unhealthy map[string]error
	// seenAt records, at each site's health gate, what every site presents.
	seenAt map[string]map[string]string
	log    []string
}

var selfKeysFor = map[string][]string{
	"forum":  {"app_secret", "mercure_jwt_secret", "valkey_password", "oauth_passphrase", "oauth_encryption_key", "oauth_private_key", "oauth_public_key"},
	"gate":   {"cookie_secret"},
	"status": {"session_secret"},
	"chat":   {"mas_signing_key", "mas_matrix_secret"},
	"id":     {"static_api_key"},
}

func newSelfWorld(t *testing.T) *selfWorld {
	t.Helper()
	cfg := &config.Config{
		ID: "3f2a6c1e-8b4d-4e2a-9c1f-7d5e6b8a9c0d",
		Sites: map[string]config.Site{
			"home-a": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.1"},
			"home-b": {Roles: []config.Role{config.RoleData, config.RoleApps}, Address: "10.0.0.2"},
			"home-c": {Roles: []config.Role{config.RoleData}, Address: "10.0.0.3"},
		},
		Cluster: config.Cluster{Sites: []string{"home-a", "home-b", "home-c"}},
		Apps: map[string]config.App{
			"forum":  {Kind: config.KindMbin, Placement: config.Placement{Mode: config.PlacementCluster}},
			"gate":   {Kind: config.KindOAuth2Proxy, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
			"status": {Kind: config.KindUptime, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-c"}},
			"chat":   {Kind: config.KindSynapse, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-b"}},
			"id":     {Kind: config.KindPocketID, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
		},
	}
	s := &config.Secrets{Apps: map[string]map[string]any{}}
	w := &selfWorld{t: t, cfg: cfg, store: &memStore{s: s}, presented: map[string]string{}, unhealthy: map[string]error{}, seenAt: map[string]map[string]string{}}
	for app, keys := range selfKeysFor {
		s.Apps[app] = map[string]any{}
		for _, key := range keys {
			s.Apps[app][key] = app + "-" + key + "-old"
		}
	}
	// The OAuth unit is a real pair, so that a promotion is checked against
	// one that opens.
	priv, pub, err := secretsgen.ReplacementOAuthKeypair("forum-oauth_passphrase-old")
	if err != nil {
		t.Fatal(err)
	}
	s.Apps["forum"]["oauth_private_key"], s.Apps["forum"]["oauth_public_key"] = priv, pub
	// Every stack presents what the file holds, as the last apply left it.
	for app, sites := range map[string][]string{"forum": {"home-a", "home-b"}, "gate": {"home-a"}, "status": {"home-c"}, "chat": {"home-b"}, "id": {"home-a"}} {
		for _, site := range sites {
			for _, key := range selfKeysFor[app] {
				v, _ := s.Get("apps." + app + "." + key)
				w.presented[site+"/"+app+"/"+key] = v
			}
		}
	}
	return w
}

// env applies by recording the value each rotated key renders to, logging
// only a change, as compose up -d changes only what differs.
func (w *selfWorld) env() Env {
	return Env{
		Cfg:   w.cfg,
		Store: w.store,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			for _, app := range stacks {
				for _, key := range selfKeysFor[app] {
					v, _ := s.Get("apps." + app + "." + key)
					at := site + "/" + app + "/" + key
					if w.presented[at] != v {
						w.presented[at] = v
						w.log = append(w.log, fmt.Sprintf("apply %s %s %s", site, app, key))
					}
				}
			}
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			w.seenAt[site] = maps(w.presented)
			if err := w.unhealthy[site]; err != nil {
				w.log = append(w.log, "unhealthy "+site)
				return err
			}
			w.log = append(w.log, "healthy "+site)
			return nil
		},
	}
}

func signOut(word, what string) error {
	if word != "sign out" {
		return fmt.Errorf("asked for %q", word)
	}
	return nil
}

func (w *selfWorld) rotate(r Rotator) error {
	w.t.Helper()
	e := w.env()
	run, err := Plan(e, []Rotator{r})
	if err != nil {
		return err
	}
	return run.Execute(e, signOut)
}

func usesOf(e Env, r Rotator) []string {
	var out []string
	for _, u := range r.Users(e) {
		out = append(out, u.Site+":"+strings.Join(u.Stacks, ","))
	}
	return out
}

// Review Focus 2: an app with cluster placement on two apps sites is handed
// the next value one site at a time, each healthy before the next, and the
// second site runs the old value while the first is checked.
func TestAppSelfClusterAppliesSiteBySite(t *testing.T) {
	w := newSelfWorld(t)
	r := AppSelf("forum", "app_secret", AppSelfCost("app_secret"))
	if r.Path() != "apps.forum.app_secret" {
		t.Fatalf("path %q", r.Path())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-a:forum", "home-b:forum"}) {
		t.Fatalf("users %v, want every apps site's forum and no other site", got)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	want := []string{"apply home-a forum app_secret", "healthy home-a", "apply home-b forum app_secret", "healthy home-b"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
	next, _ := w.store.s.Get("apps.forum.app_secret")
	if next == "forum-app_secret-old" || next == "" {
		t.Fatalf("the file holds %q", next)
	}
	if w.seenAt["home-a"]["home-a/forum/app_secret"] != next || w.seenAt["home-a"]["home-b/forum/app_secret"] != "forum-app_secret-old" {
		t.Fatalf("at home-a's gate, home-a presents %q and home-b %q: want home-b still on the old value", w.seenAt["home-a"]["home-a/forum/app_secret"], w.seenAt["home-a"]["home-b/forum/app_secret"])
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}

func TestAppSelfStopsAtAnUnhealthySite(t *testing.T) {
	w := newSelfWorld(t)
	w.unhealthy["home-a"] = errors.New("app is restarting")
	err := w.rotate(AppSelf("forum", "app_secret", AppSelfCost("app_secret")))
	if err == nil {
		t.Fatal("an unhealthy first site did not stop the rotation")
	}
	if slices.Contains(w.log, "apply home-b forum app_secret") {
		t.Fatalf("home-b was handed the value after home-a failed its gate: %v", w.log)
	}
	if v, _ := w.store.s.Get("apps.forum.app_secret"); v != "forum-app_secret-old" {
		t.Fatalf("promoted %q past a failed gate", v)
	}
	if _, pending := w.store.s.Rotations["apps.forum.app_secret"]; !pending {
		t.Fatal("the next value was not kept for the resumed run")
	}

	// The resumed run hands home-a the same value again, which changes
	// nothing there, and finishes on home-b.
	delete(w.unhealthy, "home-a")
	w.log = nil
	if err := w.rotate(AppSelf("forum", "app_secret", AppSelfCost("app_secret"))); err != nil {
		t.Fatal(err)
	}
	want := []string{"healthy home-a", "apply home-b forum app_secret", "healthy home-b"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("resumed log %v, want %v", w.log, want)
	}
}

func TestAppSelfCosts(t *testing.T) {
	for key, signsOut := range map[string]bool{
		"app_secret": true, "cookie_secret": true, "session_secret": true,
		"mas_signing_key": true, "mas_matrix_secret": true,
		"mercure_jwt_secret": false, "valkey_password": false, "static_api_key": false,
	} {
		if got := AppSelfCost(key).SignsOut; got != signsOut {
			t.Errorf("%s: SignsOut %v, want %v", key, got, signsOut)
		}
	}
	if c := AppSelf("forum", "app_secret", Cost{Downtime: "x"}).Cost(); c.Downtime != "x" {
		t.Errorf("AppSelf does not keep the cost it is given: %+v", c)
	}
	if !MbinOAuth("forum").Cost().SignsOut {
		t.Error("the Mbin OAuth unit does not sign members out")
	}
}

func TestAppSelfEachKindOnItsOwnSite(t *testing.T) {
	w := newSelfWorld(t)
	for _, tc := range []struct{ app, key, use string }{
		{"forum", "mercure_jwt_secret", ""},
		{"forum", "valkey_password", ""},
		{"gate", "cookie_secret", "home-a:gate"},
		{"status", "session_secret", "home-c:status"},
		{"chat", "mas_signing_key", "home-b:chat"},
		{"chat", "mas_matrix_secret", "home-b:chat"},
		{"id", "static_api_key", "home-a:id"},
	} {
		r := AppSelf(tc.app, tc.key, AppSelfCost(tc.key))
		e := w.env()
		if err := r.(Prechecker).Precheck(e); err != nil {
			t.Errorf("%s.%s: %v", tc.app, tc.key, err)
		}
		if tc.use != "" {
			if got := usesOf(e, r); !slices.Equal(got, []string{tc.use}) {
				t.Errorf("%s.%s users %v, want %s", tc.app, tc.key, got, tc.use)
			}
		}
		if ok, err := r.Accepts(e, "anything"); !ok || err != nil {
			t.Errorf("%s.%s Accepts %v %v: the app checks it itself", tc.app, tc.key, ok, err)
		}
		if err := r.Checked(e, "anything"); err != nil {
			t.Errorf("%s.%s Checked: %v", tc.app, tc.key, err)
		}
	}
	if err := w.rotate(AppSelf("chat", "mas_signing_key", AppSelfCost("mas_signing_key"))); err != nil {
		t.Fatal(err)
	}
	next, _ := w.store.s.Get("apps.chat.mas_signing_key")
	if !strings.Contains(next, "PRIVATE KEY") {
		t.Fatalf("MAS's signing key is not a PEM key: the file holds a %d byte value", len(next))
	}
	if !slices.Equal(w.log, []string{"apply home-b chat mas_signing_key", "healthy home-b"}) {
		t.Fatalf("log %v", w.log)
	}
}

func TestAppSelfPrecheckRefuses(t *testing.T) {
	w := newSelfWorld(t)
	for _, tc := range []struct{ app, key, want string }{
		{"nowhere", "app_secret", "declares no app"},
		{"forum", "cookie_secret", "oauth2-proxy"},
		{"gate", "app_secret", "mbin"},
		{"forum", "database_password", "not a secret"},
		{"forum", "oauth_passphrase", "apps.forum.oauth"},
	} {
		err := AppSelf(tc.app, tc.key, Cost{}).(Prechecker).Precheck(w.env())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s.%s: %v, want a refusal naming %q", tc.app, tc.key, err, tc.want)
		}
	}
}

// The OAuth unit's four values go to the users together and are promoted
// together: a passphrase that does not open the private key beside it is an
// Mbin that cannot sign a token.
func TestMbinOAuthPromotesFourKeysTogether(t *testing.T) {
	w := newSelfWorld(t)
	r := MbinOAuth("forum")
	if r.Path() != "apps.forum.oauth" {
		t.Fatalf("path %q", r.Path())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-a:forum", "home-b:forum"}) {
		t.Fatalf("users %v", got)
	}
	if err := r.(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
	old := map[string]string{}
	for _, key := range oauthKeys {
		old[key], _ = w.store.s.Get("apps.forum." + key)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	now := map[string]string{}
	for _, key := range oauthKeys {
		now[key], _ = w.store.s.Get("apps.forum." + key)
		if now[key] == "" || now[key] == old[key] {
			t.Errorf("%s was not replaced", key)
		}
		if ui.Redact(now[key]) == now[key] {
			t.Errorf("%s is not registered as a secret, so output would show it", key)
		}
	}
	if _, err := secretsgen.OpenOAuthPrivateKey(now["oauth_private_key"], now["oauth_passphrase"]); err != nil {
		t.Fatalf("the new private key does not open with the new passphrase: %v", err)
	}
	// Each site took all four in one apply, so no site ever ran a key with
	// a passphrase that does not open it.
	for _, site := range []string{"home-a", "home-b"} {
		var changed []string
		for _, l := range w.log {
			if strings.HasPrefix(l, "apply "+site+" ") {
				changed = append(changed, l)
			}
		}
		if len(changed) != 4 {
			t.Errorf("%s changed %v, want the four OAuth values", site, changed)
		}
	}
	want := []string{"healthy home-a", "healthy home-b"}
	var gates []string
	for _, l := range w.log {
		if strings.HasPrefix(l, "healthy") {
			gates = append(gates, l)
		}
	}
	if !slices.Equal(gates, want) || slices.Index(w.log, "healthy home-a") > slices.Index(w.log, "apply home-b forum oauth_passphrase") {
		t.Fatalf("log %v: want home-a healthy before home-b is handed anything", w.log)
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %d", len(w.store.s.Rotations))
	}
}

func TestMbinOAuthPromoteRefusesAPairThatDoesNotOpen(t *testing.T) {
	w := newSelfWorld(t)
	r := MbinOAuth("forum")
	s, _ := w.store.Load()
	next, err := r.Next(w.env(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Promote(roundTrip(s), next); err != nil {
		t.Fatalf("its own next value: %v", err)
	}
	other, err := r.Next(w.env(), s)
	if err != nil {
		t.Fatal(err)
	}
	// The passphrase of one next value with the keys of another.
	mixed := strings.Replace(next, oauthValue(t, next, "oauth_passphrase"), oauthValue(t, other, "oauth_passphrase"), 1)
	err = r.Promote(roundTrip(s), mixed)
	if err == nil {
		t.Fatal("a private key that the passphrase does not open was promoted")
	}
	for _, key := range oauthKeys {
		if strings.Contains(err.Error(), oauthValue(t, mixed, key)) {
			t.Fatalf("the refusal shows %s", key)
		}
	}
	if err := r.Promote(roundTrip(s), "not: [the, unit]"); err == nil {
		t.Fatal("a next value that is not the four values was promoted")
	}
}

func oauthValue(t *testing.T, next, key string) string {
	t.Helper()
	u, err := parseOAuthUnit(next)
	if err != nil {
		t.Fatal(err)
	}
	return u.values()[key]
}
