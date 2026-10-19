package secretsrotate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck/smtptest"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const (
	oldDNSToken = "fixture-not-a-secret-desec"
	newDNSToken = "fixture-not-a-secret-desec-reissued"
)

// fileHost is a site that holds the files its last apply wrote, and answers
// only the command that prints the hash of each line of one of them. runs is
// every command run; anything else is a failure, since a precheck changes
// nothing and reads nothing but those hashes.
type fileHost struct {
	t     *testing.T
	site  string
	files map[string]string
	runs  []string
}

func (h *fileHost) Describe() string { return h.site }
func (h *fileHost) ReadFile(path string) (string, bool, error) {
	h.t.Errorf("%s read %s", h.site, path)
	return "", false, errors.New("not read here")
}
func (h *fileHost) Run(command string) (string, error) {
	h.runs = append(h.runs, command)
	out, ok := answerLineHashes(command, h.files)
	if !ok {
		h.t.Errorf("%s ran %q", h.site, command)
		return "", errors.New("not run here")
	}
	return out, nil
}
func (h *fileHost) RunInput(command, stdin string) (string, error) {
	h.t.Errorf("%s ran %q with stdin", h.site, command)
	return "", errors.New("not run here")
}
func (h *fileHost) WriteFile(path, content string, mode uint32) error {
	h.t.Errorf("%s wrote %s", h.site, path)
	return errors.New("not written here")
}

// answerLineHashes answers lineHashCommand from files, as the shell would:
// the SHA-256 of each line of the named file, or lineHashesAbsent.
func answerLineHashes(command string, files map[string]string) (string, bool) {
	rest, ok := strings.CutPrefix(command, "f='")
	if !ok || !strings.Contains(command, "sha256sum") {
		return "", false
	}
	path, _, _ := strings.Cut(rest, "'")
	content, ok := files[path]
	if !ok {
		return lineHashesAbsent + "\n", true
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		digest := sha256.Sum256([]byte(line))
		out.WriteString(hex.EncodeToString(digest[:]) + "  -\n")
	}
	return out.String(), true
}

// wrapWorld is the fixture deployment, whose Caddy runs on vm, the gateway,
// and on watch, a monitor serving its own apps, each last applied with the
// secrets given to applied.
type wrapWorld struct {
	t     *testing.T
	cfg   *config.Config
	store *memStore
	hosts map[string]*fileHost
	log   []string
	// presented is the DNS token each Caddy site's caddy.env was rendered
	// with by stage 3's applies.
	presented map[string]string
	unhealthy map[string]error
	// mail answers the SMTP checks: any login unless its Users say.
	mail *smtptest.Server
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
	w := &wrapWorld{t: t, cfg: cfg, store: &memStore{s: s}, hosts: map[string]*fileHost{}, presented: map[string]string{}, unhealthy: map[string]error{}, mail: smtptest.New()}
	for _, site := range cfg.SiteNames() {
		w.hosts[site] = &fileHost{t: t, site: site}
	}
	w.applied(s, cfg.SiteNames()...)
	return w
}

// applied writes to each site the files rendered with s, as an apply leaves
// them.
func (w *wrapWorld) applied(s *config.Secrets, sites ...string) {
	w.t.Helper()
	plan, err := render.Build(w.cfg, s)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, site := range sites {
		files := map[string]string{}
		for _, f := range plan.Files {
			if rel, ok := strings.CutPrefix(f.Path, site+"/"); ok {
				files["/"+rel] = f.Content
			}
		}
		w.hosts[site].files = files
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
		Mail: w.mail.Env(),
	}
}

func (w *wrapWorld) setToken(token string) {
	s, _ := w.store.Load()
	s.External["acme_dns_token"] = token
	_ = w.store.Save(s)
}

// A token the hosts were last applied with has nothing new in the file, so
// the plan asks for its next value at --execute rather than refusing, and
// says where it is replaced. Unanswered, Execute changes nothing.
func TestDNSTokenUnchangedSinceTheLastApplyIsAskedFor(t *testing.T) {
	w := newWrapWorld(t)
	e := w.env()
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Items[0].Asks {
		t.Fatal("an unchanged token is not asked for")
	}
	if got := run.Asked(); len(got) != 1 || got[0] != (Asking{Path: "external.acme_dns_token", Issuer: "desec"}) {
		t.Errorf("asked %+v", got)
	}
	rec := &ui.Recorder{}
	run.Show(rec)
	if !rec.Has("pending", "stage 1, next value") || !endedWith(rec, "replace it at desec when asked") || !rec.Has("note", "this run will ask for external.acme_dns_token") {
		t.Errorf("the plan does not say it asks:\n%s", rec.Lines())
	}
	err = run.Execute(e, nil)
	if err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("an unanswered run went on: %v", err)
	}
	if s, _ := w.store.Load(); len(s.Rotations) != 0 || len(w.log) != 0 {
		t.Errorf("an unanswered run changed %v, %v", s.Rotations, w.log)
	}
}

// The token asked for goes through all four stages: written under rotations
// first, accepted, handed to every Caddy in turn, then promoted to the
// file's own value.
func TestDNSTokenAskedForIsRotated(t *testing.T) {
	w := newWrapWorld(t)
	e := w.env()
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Answer(e, "external.acme_dns_token", newDNSToken); err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	run.Report = rec
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"stage 1, next value", "stage 2, where it is checked", "hand infra the next value on vm", "hand infra the next value on watch", "stage 4, promote"} {
		if !rec.Has("done", step) {
			t.Errorf("%s did not run:\n%s", step, rec.Lines())
		}
	}
	if w.presented["vm"] != newDNSToken || w.presented["watch"] != newDNSToken {
		t.Errorf("presented %v", w.presented)
	}
	s, _ := w.store.Load()
	if s.External["acme_dns_token"] != newDNSToken || len(s.Rotations) != 0 {
		t.Errorf("after: token %q, rotations %v", s.External["acme_dns_token"], s.Rotations)
	}
	if strings.Contains(rec.Lines(), newDNSToken) || strings.Contains(ui.Redact("x "+newDNSToken), newDNSToken) {
		t.Error("the answer is shown or not registered as a secret")
	}
}

// An answer that is empty, or the value every host runs, is refused with
// nothing changed; the empty one names the other ways out.
func TestDNSTokenAnswerRefusesEmptyAndUnchanged(t *testing.T) {
	w := newWrapWorld(t)
	e := w.env()
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	err = run.Answer(e, "external.acme_dns_token", "")
	for _, want := range []string{"nothing was pasted", "--replaced external.acme_dns_token", "paisans secrets set external.acme_dns_token", "Nothing was changed"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("empty: %v lacks %q", err, want)
		}
	}
	err = run.Answer(e, "external.acme_dns_token", oldDNSToken)
	if err == nil || !strings.Contains(err.Error(), "unchanged") || strings.Contains(err.Error(), oldDNSToken) {
		t.Errorf("unchanged: %v", err)
	}
	if err := run.Execute(e, nil); err == nil {
		t.Error("a refused answer let the run go on")
	}
	if s, _ := w.store.Load(); len(s.Rotations) != 0 || len(w.log) != 0 {
		t.Errorf("a refused answer changed %v, %v", s.Rotations, w.log)
	}
}

// cloudflareWorld is the fixture with a DNS provider whose record management
// exists, so stage 2 tries the token, against zones, a provider held in
// memory: example.org is visible when zones holds it.
func cloudflareWorld(t *testing.T, zones map[string]string) (*wrapWorld, Env) {
	t.Helper()
	w := newWrapWorld(t)
	w.cfg.ACME.Provider = "cloudflare"
	s, _ := w.store.Load()
	w.applied(s, w.cfg.SiteNames()...)
	e := w.env()
	e.DNSProvider = func(name, token string) (dns.Provider, error) {
		if name != "cloudflare" {
			return nil, fmt.Errorf("asked for %s", name)
		}
		if token == "" {
			return nil, errors.New("no token")
		}
		return zoneLookup(zones), nil
	}
	return w, e
}

// zoneLookup is a provider that only looks zones up.
type zoneLookup map[string]string

func (z zoneLookup) Name() string { return "cloudflare" }
func (z zoneLookup) LookupZone(_ context.Context, name string) (string, bool, error) {
	id, ok := z[name]
	return id, ok, nil
}
func (zoneLookup) Records(context.Context, string, string) ([]dns.Record, error) {
	return nil, errors.New("not read here")
}
func (zoneLookup) Create(context.Context, string, dns.Record) error { return errors.New("not here") }
func (zoneLookup) AllRecords(context.Context, string) ([]dns.Record, error) {
	return nil, errors.New("not read here")
}
func (zoneLookup) Delete(context.Context, string, string) error { return errors.New("not here") }

// A pasted token that cannot see the zone is refused when it is answered,
// before anything is written; one that can is rotated, and the plan says
// what stage 2 checks.
func TestDNSTokenAnswerIsTriedAgainstTheZone(t *testing.T) {
	_, e := cloudflareWorld(t, nil)
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	err = run.Answer(e, "external.acme_dns_token", newDNSToken)
	if err == nil || !strings.Contains(err.Error(), "does not work against cloudflare") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("a token that cannot see the zone was taken: %v", err)
	}

	w, e := cloudflareWorld(t, map[string]string{"example.org": "zone-1"})
	run, err = Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	run.Show(rec)
	if !endedWith(rec, "looks up example.org's zone at cloudflare") {
		t.Errorf("the plan does not say what stage 2 checks:\n%s", rec.Lines())
	}
	if err := run.Answer(e, "external.acme_dns_token", newDNSToken); err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.store.Load(); s.External["acme_dns_token"] != newDNSToken {
		t.Errorf("after: %q", s.External["acme_dns_token"])
	}
}

// A next token that stops working before stage 2, Eg: revoked at the provider
// after a run stopped, stops the rotation at stage 2: no Caddy is handed it,
// and it stays under rotations for the next run.
func TestDNSTokenStage2FailureStopsBeforeStage3(t *testing.T) {
	w, e := cloudflareWorld(t, map[string]string{})
	s, _ := w.store.Load()
	s.Rotations = map[string]string{"external.acme_dns_token": newDNSToken}
	_ = w.store.Save(s)
	run, err := Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if run.Items[0].Asks || run.Items[0].From != StageChecked {
		t.Errorf("a pending token: asks %t, from %d", run.Items[0].Asks, run.Items[0].From)
	}
	err = run.Execute(e, nil)
	if err == nil || !strings.Contains(err.Error(), "does not work against cloudflare") {
		t.Fatalf("got %v", err)
	}
	if len(w.log) != 0 || len(w.presented) != 0 {
		t.Errorf("stage 3 ran: %v", w.log)
	}
	if s, _ := w.store.Load(); s.Rotations["external.acme_dns_token"] != newDNSToken || s.External["acme_dns_token"] != oldDNSToken {
		t.Errorf("after: %v, %q", s.Rotations, s.External["acme_dns_token"])
	}
}

// A token set with `secrets set` that cannot see the zone is refused when
// the run is planned, before it is written under rotations.
func TestDNSTokenSetThatDoesNotWorkIsRefused(t *testing.T) {
	w, e := cloudflareWorld(t, map[string]string{})
	w.setToken(newDNSToken)
	_, err := Plan(e, []Rotator{DNSToken()})
	if err == nil || !strings.Contains(err.Error(), "does not work against cloudflare") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("got %v", err)
	}
}

// The precheck runs only the command that prints each line's hash of
// caddy.env, on each Caddy site, so nothing secret crosses ssh: the hashes it
// compares are computed on the workstation, and the token is on no command
// line.
func TestDNSTokenPrecheckReadsOnlyLineHashes(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	if _, err := Plan(w.env(), []Rotator{DNSToken()}); err != nil {
		t.Fatal(err)
	}
	for name, h := range w.hosts {
		if !w.cfg.Sites[name].RunsCaddy() {
			if len(h.runs) != 0 {
				t.Errorf("%s ran %v", name, h.runs)
			}
			continue
		}
		if len(h.runs) != 1 || !strings.Contains(h.runs[0], "/caddy.env'") {
			t.Errorf("%s ran %v, want the line hashes of its caddy.env", name, h.runs)
		}
		for _, c := range h.runs {
			if strings.Contains(c, newDNSToken) || strings.Contains(c, oldDNSToken) {
				t.Errorf("%s: a token is on the command line: %s", name, c)
			}
		}
	}
}

// A carrying file that differs from what the hosts hold only elsewhere, Eg:
// a comment an upgrade changed, is not a replaced secret: the check is made
// on the lines that carry the token, so an unreplaced token is still asked
// for.
func TestDNSTokenUnrelatedChangeInItsFileIsStillAskedFor(t *testing.T) {
	w := newWrapWorld(t)
	for _, h := range w.hosts {
		for path, content := range h.files {
			if strings.HasSuffix(path, "/caddy.env") {
				h.files[path] = "# written by an older toolkit\n" + content
			}
		}
	}
	run, err := Plan(w.env(), []Rotator{DNSToken()})
	if err != nil || !run.Items[0].Asks {
		t.Fatalf("an unreplaced token passed for replaced: %v", err)
	}
	ok, err := HostsRun(w.env(), DNSToken())
	if err != nil || !ok {
		t.Fatalf("HostsRun %v, %v: the hosts run the file's token", ok, err)
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
	if run.Items[0].Asks {
		t.Error("a token set anew is asked for")
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

// A Caddy site with no caddy.env was never applied, and what it runs cannot
// be told: refused, naming apply.
func TestDNSTokenACaddyNeverAppliedIsRefused(t *testing.T) {
	w := newWrapWorld(t)
	w.setToken(newDNSToken)
	w.hosts["watch"].files = nil
	_, err := Plan(w.env(), []Rotator{DNSToken()})
	if err == nil || !strings.Contains(err.Error(), "watch") || !strings.Contains(err.Error(), "paisans deployment apply") {
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

// An SMTP password the hosts were last applied with is asked for like the DNS
// token, replaced at the mail provider. The pasted one logs in and sends the
// operators one test message when pasted; stage 2 logs in again and sends
// nothing more.
func TestSMTPPasswordUnchangedIsAskedFor(t *testing.T) {
	w := newWrapWorld(t)
	e := w.env()
	run, err := Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Asked(); len(got) != 1 || got[0].Issuer != "the mail provider" {
		t.Fatalf("asked %+v", got)
	}
	rec := &ui.Recorder{}
	run.Show(rec)
	if !endedWith(rec, "the next password logs in to the mail server") {
		t.Errorf("the plan does not say how it is checked:\n%s", rec.Lines())
	}
	if len(w.mail.Dials()) != 0 {
		t.Error("the plan of a value to be asked for reached the mail server")
	}
	const next = "fixture-not-a-secret-smtp-asked"
	if err := run.Answer(e, "external.smtp_password", next); err != nil {
		t.Fatal(err)
	}
	msgs := w.mail.Messages()
	if len(msgs) != 1 || msgs[0].From != "hello@example.org" || !slices.Equal(msgs[0].To, []string{"alice@example.org", "bob@example.org"}) {
		t.Fatalf("messages after the answer: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Data, "`paisans secrets rotate` is changing external.smtp_password") {
		t.Errorf("message:\n%s", msgs[0].Data)
	}
	logins := len(w.mail.Logins())
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.mail.Logins()) <= logins || len(w.mail.Messages()) != 1 {
		t.Errorf("stage 2: %d logins after %d, %d messages", len(w.mail.Logins()), logins, len(w.mail.Messages()))
	}
	if s, _ := w.store.Load(); s.External["smtp_password"] != next || len(s.Rotations) != 0 {
		t.Errorf("after: %v", s.Rotations)
	}
}

// A pasted SMTP password the server refuses is refused when pasted, naming
// the server's reply and not the password, and nothing is written.
func TestSMTPPasswordPastedIsRefusedWhenItDoesNotLogIn(t *testing.T) {
	w := newWrapWorld(t)
	w.mail.Users = map[string]string{"robot@example.org": "the-right-one"}
	e := w.env()
	run, err := Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatal(err)
	}
	const wrong = "fixture-not-a-secret-smtp-wrong"
	err = run.Answer(e, "external.smtp_password", wrong)
	if err == nil || !strings.Contains(err.Error(), "535 5.7.8") || !strings.Contains(err.Error(), "Nothing was changed") || strings.Contains(err.Error(), wrong) {
		t.Fatalf("err = %v", err)
	}
	if len(w.mail.Messages()) != 0 {
		t.Error("a message was sent")
	}
	if s, _ := w.store.Load(); len(s.Rotations) != 0 {
		t.Errorf("rotations: %v", s.Rotations)
	}
}

// One set with secrets set, which sent its own test message, logs in when
// the run is planned and sends nothing; one the server refuses is refused
// then.
func TestSMTPPasswordSetAnewIsLoggedInWithAtThePlan(t *testing.T) {
	w := newWrapWorld(t)
	w.mail.Users = map[string]string{"robot@example.org": "fixture-not-a-secret-smtp-reissued"}
	s, _ := w.store.Load()
	s.External["smtp_password"] = "fixture-not-a-secret-smtp-reissued"
	_ = w.store.Save(s)
	if _, err := Plan(w.env(), []Rotator{SMTPPassword("external.smtp_password")}); err != nil {
		t.Fatal(err)
	}
	if len(w.mail.Logins()) == 0 || len(w.mail.Messages()) != 0 {
		t.Errorf("the plan: %d logins, %d messages", len(w.mail.Logins()), len(w.mail.Messages()))
	}
	w.mail.Users = map[string]string{"robot@example.org": "the-right-one"}
	_, err := Plan(w.env(), []Rotator{SMTPPassword("external.smtp_password")})
	if err == nil || !strings.Contains(err.Error(), "does not pass where it is checked") || !strings.Contains(err.Error(), "535") {
		t.Errorf("err = %v", err)
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
	for _, want := range []string{"talk", "health"} {
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

// A pasted secret's value is registered as a secret as soon as it is read,
// so nothing shown afterwards carries it.
func TestPastedNextRegistersTheValue(t *testing.T) {
	w := newWrapWorld(t)
	const token = "fixture-not-a-secret-desec-registered"
	w.setToken(token)
	s, _ := w.store.Load()
	if _, err := DNSToken().Next(w.env(), s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.Redact("the host said "+token), token) {
		t.Fatal("the token is not registered as a secret")
	}
}

// endedWith reports whether a step the recorder saw ended pending with a
// result holding text.
func endedWith(rec *ui.Recorder, text string) bool {
	for _, e := range rec.Events {
		if e.Kind == "pending" && strings.Contains(e.Extra, text) {
			return true
		}
	}
	return false
}

// A mail server this machine cannot reach leaves the password untried, as
// `secrets set` leaves it: the run is told so, naming this machine and not a
// blank site, and goes on, since the scoped apply at each site tries it from
// there before handing it out.
func TestSMTPPasswordNotReachedFromThisMachineIsWarnedAndHandedOut(t *testing.T) {
	w := newWrapWorld(t)
	w.mail.Unreachable = map[string]error{"": errors.New("dial tcp: connection refused")}
	s, _ := w.store.Load()
	s.External["smtp_password"] = "fixture-not-a-secret-smtp-reissued"
	_ = w.store.Save(s)
	e := w.env()
	var warned []string
	e.Warn = func(hint, detail string) { warned = append(warned, hint+"\n"+detail) }
	run, err := Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(warned) == 0 {
		t.Fatal("nothing was warned")
	}
	for _, text := range warned {
		if !strings.Contains(text, "from this machine") || strings.HasPrefix(text, " ") || strings.Contains(text, "reach the site") {
			t.Errorf("warning %q", text)
		}
	}
	if s, _ := w.store.Load(); s.External["smtp_password"] != "fixture-not-a-secret-smtp-reissued" || len(s.Rotations) != 0 {
		t.Errorf("after: %v", s.Rotations)
	}
}

// Stage 3 tries an SMTP password from each site before handing it to the
// stacks there, whether it was pasted or set beforehand, and a site that
// refuses it is not handed it. The DNS token is handed over with no such
// try.
func TestSMTPPasswordIsTriedFromEachSiteBeforeItIsHandedOut(t *testing.T) {
	w := newWrapWorld(t)
	s, _ := w.store.Load()
	s.External["smtp_password"] = "fixture-not-a-secret-smtp-reissued"
	_ = w.store.Save(s)
	e := w.env()
	var tried []string
	e.MailFrom = func(site, path string, s *config.Secrets, stacks []string) error {
		v, _ := s.Get(path)
		tried = append(tried, fmt.Sprintf("%s %s %s", site, path, v))
		w.log = append(w.log, "mail "+site)
		return nil
	}
	run, err := Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	uses := run.Items[0].Uses
	if len(tried) != len(uses) {
		t.Fatalf("tried %v for uses %v", tried, uses)
	}
	for i, u := range uses {
		if tried[i] != u.Site+" external.smtp_password fixture-not-a-secret-smtp-reissued" {
			t.Errorf("tried %q", tried[i])
		}
	}
	if len(w.log) == 0 || !strings.HasPrefix(w.log[0], "mail ") {
		t.Errorf("handed out before it was tried: %v", w.log)
	}

	// A site that refuses it stops the rotation before that site applies.
	w = newWrapWorld(t)
	s, _ = w.store.Load()
	s.External["smtp_password"] = "fixture-not-a-secret-smtp-reissued"
	_ = w.store.Save(s)
	e = w.env()
	e.MailFrom = func(string, string, *config.Secrets, []string) error { return errors.New("535 5.7.8 refused") }
	run, err = Plan(e, []Rotator{SMTPPassword("external.smtp_password")})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err == nil || !strings.Contains(err.Error(), "535") {
		t.Errorf("err = %v", err)
	}
	for _, line := range w.log {
		if strings.HasPrefix(line, "apply ") {
			t.Errorf("applied after a refusal: %v", w.log)
		}
	}

	// The DNS token is not tried at the sites.
	w = newWrapWorld(t)
	e = w.env()
	e.MailFrom = func(string, string, *config.Secrets, []string) error {
		t.Error("the DNS token was tried as mail")
		return nil
	}
	run, err = Plan(e, []Rotator{DNSToken()})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Answer(e, "external.acme_dns_token", newDNSToken); err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
}

// Every site is tried before any is handed the password: with two or more
// sites, a refusal at a later one stops the rotation before the first is
// handed it, so no site runs a password another site's server refuses.
func TestSMTPPasswordRefusedAtALaterSiteIsHandedToNone(t *testing.T) {
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
	if len(uses) < 2 {
		t.Fatalf("the fixture hands the password to %d sites, want two or more", len(uses))
	}
	last := uses[len(uses)-1].Site
	var tried []string
	e.MailFrom = func(site, _ string, _ *config.Secrets, _ []string) error {
		tried = append(tried, site)
		if site == last {
			return errors.New("535 5.7.8 refused")
		}
		return nil
	}
	rec := &ui.Recorder{}
	run.Report = rec
	if err := run.Execute(e, nil); err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v", err)
	}
	if len(tried) != len(uses) {
		t.Errorf("tried %v, want every site %v", tried, uses)
	}
	for _, line := range w.log {
		if strings.HasPrefix(line, "apply ") {
			t.Errorf("a site was handed the password: %v", w.log)
		}
	}
	if !rec.Has("fail", "stage 2, where it is checked") {
		t.Errorf("the refusal is not stage 2's:\n%s", rec.Lines())
	}
}
