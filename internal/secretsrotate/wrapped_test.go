package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const (
	oldDNSToken = "fixture-not-a-secret-desec"
	newDNSToken = "fixture-not-a-secret-desec-reissued"
)

// manifestHost is a site that holds the manifest its last apply wrote, and
// nothing else this package reads. reads is every path read; anything other
// than a read is a failure, since a precheck changes nothing.
type manifestHost struct {
	t        *testing.T
	site     string
	manifest string
	found    bool
	reads    []string
}

func (h *manifestHost) Describe() string { return h.site }
func (h *manifestHost) ReadFile(path string) (string, bool, error) {
	h.reads = append(h.reads, path)
	return h.manifest, h.found, nil
}
func (h *manifestHost) Run(command string) (string, error) {
	h.t.Errorf("%s ran %q", h.site, command)
	return "", errors.New("not run here")
}
func (h *manifestHost) RunInput(command, stdin string) (string, error) {
	h.t.Errorf("%s ran %q with stdin", h.site, command)
	return "", errors.New("not run here")
}
func (h *manifestHost) WriteFile(path, content string, mode uint32) error {
	h.t.Errorf("%s wrote %s", h.site, path)
	return errors.New("not written here")
}

// wrapWorld is the fixture deployment, whose Caddy runs on vm, the gateway,
// and on watch, a monitor serving its own apps, each last applied with the
// secrets given to applied.
type wrapWorld struct {
	t     *testing.T
	cfg   *config.Config
	store *memStore
	hosts map[string]*manifestHost
	log   []string
	// presented is the DNS token each Caddy site's caddy.env was rendered
	// with by stage 3's applies.
	presented map[string]string
	unhealthy map[string]error
}

func newWrapWorld(t *testing.T) *wrapWorld {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	w := &wrapWorld{t: t, cfg: cfg, store: &memStore{s: s}, hosts: map[string]*manifestHost{}, presented: map[string]string{}, unhealthy: map[string]error{}}
	for _, site := range cfg.SiteNames() {
		w.hosts[site] = &manifestHost{t: t, site: site}
	}
	w.applied(s, cfg.SiteNames()...)
	return w
}

// applied records in each site's manifest the files rendered with s, as an
// apply leaves it.
func (w *wrapWorld) applied(s *config.Secrets, sites ...string) {
	w.t.Helper()
	plan, err := render.Build(w.cfg, s)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, site := range sites {
		var m render.Manifest
		m.Version = 1
		for _, f := range plan.Files {
			rel, ok := strings.CutPrefix(f.Path, site+"/")
			if !ok {
				continue
			}
			digest := sha256.Sum256([]byte(f.Content))
			m.Files = append(m.Files, render.ManifestFile{Path: rel, SHA256: hex.EncodeToString(digest[:]), Mode: fmt.Sprintf("%04o", f.Mode)})
		}
		data, _ := json.Marshal(m)
		w.hosts[site].manifest, w.hosts[site].found = string(data), true
	}
}

func (w *wrapWorld) env() Env {
	sites := map[string]apply.Transport{}
	for name, h := range w.hosts {
		sites[name] = h
	}
	return Env{
		Cfg: w.cfg, Store: w.store, Sites: sites,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			w.log = append(w.log, fmt.Sprintf("apply %s %s", site, strings.Join(stacks, ",")))
			if slices.Contains(stacks, "infra") {
				w.presented[site] = s.External["acme_dns_token"]
			}
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			w.log = append(w.log, fmt.Sprintf("healthy %s %s", site, strings.Join(stacks, ",")))
			return w.unhealthy[site]
		},
	}
}

func (w *wrapWorld) setToken(token string) {
	s, _ := w.store.Load()
	s.External["acme_dns_token"] = token
	_ = w.store.Save(s)
}

// A token the hosts were last applied with has not been replaced at its
// issuer: rotating it would recreate Caddy for nothing, so the run is
// refused before anything changes, naming the command that sets a new one.
func TestDNSTokenUnchangedSinceTheLastApplyIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	_, err := Plan(w.env(), []Rotator{DNSToken()})
	if err == nil {
		t.Fatal("an unchanged token was planned")
	}
	for _, want := range []string{"external.acme_dns_token", "paisans secrets set external.acme_dns_token", "Nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), oldDNSToken) {
		t.Errorf("the refusal carries the token: %v", err)
	}
	if len(w.log) != 0 {
		t.Errorf("a refused plan changed %v", w.log)
	}
}

// The precheck reads only each Caddy site's manifest, and so sends nothing
// secret over ssh: the hash it compares is computed on the workstation.
func TestDNSTokenPrecheckReadsOnlyTheManifests(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	if _, err := Plan(w.env(), []Rotator{DNSToken()}); err != nil {
		t.Fatal(err)
	}
	manifest := w.cfg.Deployment().Manifest()
	for name, h := range w.hosts {
		want := []string(nil)
		if w.cfg.Sites[name].RunsCaddy() {
			want = []string{manifest}
		}
		if !slices.Equal(h.reads, want) {
			t.Errorf("%s read %v, want %v", name, h.reads, want)
		}
	}
}

// A token replaced with `secrets set` is handed to every site whose Caddy is
// the toolkit's, the gateway and the monitor serving its own apps, one at a
// time and each healthy before the next, by a scoped apply of the
// infrastructure stack, which recreates Caddy behind its gates.
func TestDNSTokenSetAnewReachesEveryCaddy(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	e := w.env()
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(run.Items[0].Uses); got != fmt.Sprint([]Use{{Site: "vm", Stacks: []string{"infra"}}, {Site: "watch", Stacks: []string{"infra"}}}) {
		t.Errorf("uses %s", got)
	}
	if run.Items[0].Cost != (Cost{}) {
		t.Errorf("cost %+v", run.Items[0].Cost)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"apply vm infra", "healthy vm infra", "apply watch infra", "healthy watch infra"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v, want %v", w.log, want)
	}
	if w.presented["vm"] != newDNSToken || w.presented["watch"] != newDNSToken {
		t.Errorf("presented %v", w.presented)
	}
	s, _ := w.store.Load()
	if s.External["acme_dns_token"] != newDNSToken || len(s.Rotations) != 0 {
		t.Errorf("after: token %q, rotations %v", s.External["acme_dns_token"], s.Rotations)
	}
}

// One Caddy site still rendered with an older token is enough: the rotation
// brings it up to the file's value.
func TestDNSTokenOneStaleCaddyIsEnough(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	s, _ := w.store.Load()
	w.applied(s, "vm")
	if _, err := Plan(w.env(), []Rotator{DNSToken()}); err != nil {
		t.Fatalf("a stale monitor's Caddy was refused: %v", err)
	}
}

// A Caddy site with no manifest, or none recording caddy.env, was never
// applied, and what it runs cannot be told: refused, naming apply.
func TestDNSTokenACaddyNeverAppliedIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	w.hosts["watch"].found = false
	_, err := Plan(w.env(), []Rotator{DNSToken()})
	if err == nil || !strings.Contains(err.Error(), "watch") || !strings.Contains(err.Error(), "paisans apply") {
		t.Fatalf("got %v", err)
	}
}

// An empty token is no token to hand anybody.
func TestDNSTokenEmptyIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken("")
	_, err := Plan(w.env(), []Rotator{DNSToken()})
	if err == nil || !strings.Contains(err.Error(), "paisans secrets set external.acme_dns_token") {
		t.Fatalf("got %v", err)
	}
}

// A stopped rotation resumes with the value its first run wrote, past the
// precheck, which the hosts it already reached would now fail.
func TestDNSTokenResumesPastItsPrecheck(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	w.unhealthy["watch"] = errors.New("caddy restarting")
	e := w.env()
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err == nil {
		t.Fatal("an unhealthy Caddy passed the gate")
	}
	s, _ := w.store.Load()
	w.applied(s, "vm", "watch")
	delete(w.unhealthy, "watch")
	w.log = nil
	run, err = Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatalf("the resumed rotation was refused: %v", err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = w.store.Load(); len(s.Rotations) != 0 || s.External["acme_dns_token"] != newDNSToken {
		t.Errorf("after: %v, %q", s.Rotations, s.External["acme_dns_token"])
	}
}

// fakeFlow is an existing rotation command: it records the app it ran for and
// writes what that command writes into the secrets file.
type fakeFlow struct {
	w    *wrapWorld
	name string
	fail error
	// write is what the command records in the secrets file.
	write func(s *config.Secrets)
}

func (f *fakeFlow) run(app string) error {
	f.w.log = append(f.w.log, f.name+" "+app)
	if f.fail != nil {
		return f.fail
	}
	if f.write != nil {
		s, _ := f.w.store.Load()
		f.write(s)
		_ = f.w.store.Save(s)
	}
	return nil
}

// An OIDC client secret is rotated by the existing command, then every site
// running the app is applied in turn with what that command recorded, each
// healthy before the next, and only then is the old secret retired at
// Pocket ID. An --all run takes the path off what it has left.
func TestOIDCClientRotatesAppliesThenRetires(t *testing.T) {
	w := newWrapWorld(t)
	var rendered []string
	flow := &fakeFlow{w: w, name: "oidc client create --rotate-secret", write: func(s *config.Secrets) {
		_ = s.Set("oidc_clients.talk.client_secret", "fixture-not-a-secret-talk-secret-2")
	}}
	retire := &fakeFlow{w: w, name: "retire"}
	e := w.env()
	inner := e.Apply
	e.Apply = func(site string, s *config.Secrets, stacks []string) error {
		got, _ := s.Get("oidc_clients.talk.client_secret")
		rendered = append(rendered, got)
		return inner(site, s, stacks)
	}
	r := OIDCClient("talk", flow.run, retire.run)
	if r.Path() != "oidc_clients.talk.client_secret" {
		t.Errorf("path %s", r.Path())
	}
	run, err := PlanAll(e, []Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Items[0].Whole {
		t.Fatal("not a whole rotation")
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"oidc client create --rotate-secret talk",
		"apply home-a talk", "healthy home-a talk",
		"apply home-b talk", "healthy home-b talk",
		"retire talk",
	}
	if !slices.Equal(w.log, want) {
		t.Errorf("log %v\nwant %v", w.log, want)
	}
	for _, got := range rendered {
		if got != "fixture-not-a-secret-talk-secret-2" {
			t.Errorf("a site was rendered with %q", got)
		}
	}
	s, _ := w.store.Load()
	if len(s.RotateRemaining) != 0 || len(s.Rotations) != 0 {
		t.Errorf("after: remaining %v, rotations %v", s.RotateRemaining, s.Rotations)
	}
}

// A site that fails its health gate stops the rotation before the old secret
// is retired, so the sites not yet switched keep signing in with it.
func TestOIDCClientUnhealthySiteKeepsTheOldSecret(t *testing.T) {
	w := newWrapWorld(t)
	w.unhealthy["home-a"] = errors.New("talk unhealthy")
	flow := &fakeFlow{w: w, name: "rotate"}
	retire := &fakeFlow{w: w, name: "retire"}
	e := w.env()
	run, err := PlanAll(e, []Rotator{OIDCClient("talk", flow.run, retire.run)})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err == nil || !strings.Contains(err.Error(), "home-a") {
		t.Fatalf("got %v", err)
	}
	if want := []string{"rotate talk", "apply home-a talk", "healthy home-a talk"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v, want %v", w.log, want)
	}
	s, _ := w.store.Load()
	if !slices.Equal(s.RotateRemaining, []string{"oidc_clients.talk.client_secret"}) {
		t.Errorf("remaining %v", s.RotateRemaining)
	}
}

// The command failing applies nothing and retires nothing.
func TestOIDCClientFailedCommandChangesNothingMore(t *testing.T) {
	w := newWrapWorld(t)
	flow := &fakeFlow{w: w, name: "rotate", fail: errors.New("Pocket ID unreachable")}
	retire := &fakeFlow{w: w, name: "retire"}
	e := w.env()
	run, err := Plan(e, []Rotator{OIDCClient("talk", flow.run, retire.run)})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err == nil || !strings.Contains(err.Error(), "Pocket ID unreachable") {
		t.Fatalf("got %v", err)
	}
	if want := []string{"rotate talk"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v", w.log)
	}
}

// An app with no client recorded has no secret to rotate: creating one is
// `oidc client create`'s, and the plan refuses before anything runs.
func TestOIDCClientWithoutARecordedClientIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	flow := &fakeFlow{w: w, name: "rotate"}
	for _, app := range []string{"web", "nobody"} {
		_, err := Plan(w.env(), []Rotator{OIDCClient(app, flow.run, flow.run)})
		if err == nil {
			t.Errorf("%s was planned", app)
		}
	}
	if len(w.log) != 0 {
		t.Errorf("ran %v", w.log)
	}
}

// An app's S3 key is rotated by `storage rotate-key --app`, which switches
// every site running the app itself and deletes the old key from Garage, so
// no site is applied a second time.
func TestAppS3DelegatesTheWholeRotation(t *testing.T) {
	w := newWrapWorld(t)
	flow := &fakeFlow{w: w, name: "storage rotate-key --app"}
	e := w.env()
	e.Apply = func(site string, s *config.Secrets, stacks []string) error {
		t.Errorf("applied %s on %s", strings.Join(stacks, ","), site)
		return nil
	}
	r := AppS3("talk", flow.run)
	if r.Path() != "apps.talk.s3_secret_access_key" {
		t.Errorf("path %s", r.Path())
	}
	run, err := PlanAll(e, []Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"storage rotate-key --app talk"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v, want %v", w.log, want)
	}
	if s, _ := w.store.Load(); len(s.RotateRemaining) != 0 {
		t.Errorf("remaining %v", s.RotateRemaining)
	}
}

// An app holding no S3 key has none to rotate.
func TestAppS3WithoutAKeyIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	flow := &fakeFlow{w: w, name: "rotate"}
	for _, app := range []string{"gate", "nobody"} {
		if _, err := Plan(w.env(), []Rotator{AppS3(app, flow.run)}); err == nil {
			t.Errorf("%s was planned", app)
		}
	}
}

// In --all the three keep the order they are given in: the DNS token, the
// client secrets, then the S3 keys, each in its place.
func TestWrappedRotationsKeepTheirPlaceInTheOrder(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	flow := &fakeFlow{w: w, name: "run"}
	run, err := PlanAll(w.env(), []Rotator{DNSToken(), OIDCClient("talk", flow.run, flow.run), AppS3("talk", flow.run)})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range run.Items {
		paths = append(paths, it.Rotator.Path())
	}
	if want := []string{"external.acme_dns_token", "oidc_clients.talk.client_secret", "apps.talk.s3_secret_access_key"}; !slices.Equal(paths, want) {
		t.Errorf("order %v", paths)
	}
}

// An SMTP password the hosts were last applied with has not been replaced at
// the mail provider, and is refused like the DNS token, naming the command
// that sets the new one.
func TestSMTPPasswordUnchangedIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	_, err := Plan(w.env(), []Rotator{SMTPPassword("external.smtp_password")})
	if err == nil || !strings.Contains(err.Error(), "paisans secrets set external.smtp_password") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("got %v", err)
	}
}

// A replaced SMTP password reaches every stack that renders it, found by
// rendering, one site at a time and each healthy before the next; the file
// keeps the value it was set to.
func TestSMTPPasswordSetAnewReachesEveryUser(t *testing.T) {
	w := newWrapWorld(t)
	s, _ := w.store.Load()
	s.External["smtp_password"] = "fixture-not-a-secret-smtp-reissued"
	_ = w.store.Save(s)
	e := w.env()
	run, err := Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatal(err)
	}
	uses := run.Items[0].Uses
	var stacks []string
	for _, u := range uses {
		stacks = append(stacks, u.Stacks...)
	}
	for _, want := range []string{"talk", "status"} {
		if !slices.Contains(stacks, want) {
			t.Errorf("%s is not handed the password: %v", want, uses)
		}
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.log) != 2*len(uses) {
		t.Errorf("log %v for uses %v", w.log, uses)
	}
	for i, u := range uses {
		if w.log[2*i] != fmt.Sprintf("apply %s %s", u.Site, strings.Join(u.Stacks, ",")) || !strings.HasPrefix(w.log[2*i+1], "healthy "+u.Site) {
			t.Errorf("log %v", w.log)
		}
	}
	if s, _ = w.store.Load(); s.External["smtp_password"] != "fixture-not-a-secret-smtp-reissued" || len(s.Rotations) != 0 {
		t.Errorf("after: %q, %v", s.External["smtp_password"], s.Rotations)
	}
}

// The client secret of a kind `oidc client create` cannot build is pasted:
// minted at Pocket ID and set in the file, it is refused while unchanged,
// and once set anew every site running the app is applied, and only then
// are the client's other secrets retired.
func TestPastedClientAppliesThenRetires(t *testing.T) {
	w := newWrapWorld(t)
	retire := &fakeFlow{w: w, name: "retire"}
	if _, err := Plan(w.env(), []Rotator{PastedClient("gate", retire.run)}); err == nil || !strings.Contains(err.Error(), "paisans secrets set oidc_clients.gate.client_secret") {
		t.Fatalf("an unchanged client secret: %v", err)
	}
	s, _ := w.store.Load()
	_ = s.Set("oidc_clients.gate.client_secret", "fixture-not-a-secret-gate-secret-2")
	_ = w.store.Save(s)
	e := w.env()
	run, err := Plan(e, []Rotator{PastedClient("gate", retire.run)})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	run.Show(rec)
	if rec.Index("pending", "retire") < 0 {
		t.Errorf("the plan does not show the retirement:\n%s", rec.Lines())
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"apply home-a gate", "healthy home-a gate", "retire gate"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v, want %v", w.log, want)
	}
}

// Without a recorded client ID there is nothing to retire by: no step.
func TestPastedClientWithoutRetireOnlyApplies(t *testing.T) {
	w := newWrapWorld(t)
	s, _ := w.store.Load()
	_ = s.Set("oidc_clients.gate.client_secret", "fixture-not-a-secret-gate-secret-2")
	_ = w.store.Save(s)
	e := w.env()
	run, err := Plan(e, []Rotator{PastedClient("gate", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"apply home-a gate", "healthy home-a gate"}; !slices.Equal(w.log, want) {
		t.Errorf("log %v, want %v", w.log, want)
	}
}
