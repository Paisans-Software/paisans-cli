package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/oidcclient"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The recorder is the one way a client's credentials reach the secrets file,
// for `oidc client create` and for apply. Written with no recipient, an
// encrypted file would come back as plaintext.
func TestSecretsRecorderRefusesAnEncryptedFileWithoutARecipient(t *testing.T) {
	path := tempSecrets(t)
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets.Encrypted = true
	rec := &secretsRecorder{app: "talk", path: path, secrets: secrets}
	err = rec.Record("c-1", "not-a-real-secret-0001")
	if err == nil || !strings.Contains(err.Error(), "names a recipient") {
		t.Fatalf("got %v", err)
	}
	if rec.wrote {
		t.Error("the recorder says it wrote")
	}
}

// passLog is a sitePass that renders the site as apply would, records what
// each pass held back and whether app rendered with its recorded client ID
// at that moment, and executes nothing.
type passLog struct {
	events []string
	// done is what each plan call was told the earlier passes did.
	done [][]*apply.Plan
	// plans is every plan returned, in order.
	plans []*apply.Plan
	// heldOverwrites is put on every plan, as Build reports an --overwrite
	// in a held stack.
	heldOverwrites []string
	// notes is put on every plan, as Build finds the same thing on every
	// pass.
	notes []apply.Note
}

func (l *passLog) pass(t *testing.T, c *clientStep, app string) sitePass {
	t.Helper()
	return sitePass{
		plan: func(hold []string, done []*apply.Plan) (*apply.Plan, error) {
			l.done = append(l.done, done)
			rendered, err := render.Build(c.cfg, c.secrets)
			if err != nil {
				t.Fatal(err)
			}
			id := c.secrets.OIDCClients[app].ClientID
			has := false
			for _, f := range rendered.Files {
				if f.Path == c.site+"/srv/paisans/f2a9/"+app+"/.env" && id != "" && strings.Contains(f.Content, id) {
					has = true
				}
			}
			l.events = append(l.events, fmt.Sprintf("plan hold=%s client=%t", strings.Join(hold, ","), has))
			p := &apply.Plan{Site: c.site, HeldOverwrites: l.heldOverwrites, Notes: l.notes}
			l.plans = append(l.plans, p)
			return p, nil
		},
		execute: func(*apply.Plan) error {
			l.events = append(l.events, "execute")
			return nil
		},
		notes: func(p *apply.Plan) {
			for _, n := range p.Notes {
				l.events = append(l.events, "note "+n.Hint)
			}
		},
	}
}

// secretsWithout is a writable copy of the fixture secrets with the named
// apps' clients removed, as before anything created them.
func secretsWithout(t *testing.T, apps ...string) string {
	t.Helper()
	path := tempSecrets(t)
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		delete(secrets.OIDCClients, app)
	}
	if err := config.WriteSecrets(path, secrets, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// stepFor is apply's identity step for site, over the fixture configuration
// and the secrets at path.
func stepFor(t *testing.T, site, path string, only ...string) *clientStep {
	t.Helper()
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newClientStep(stdoutReporter(), cfg, site, "", path, secrets, only)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatalf("%s has no identity step", site)
	}
	return c
}

// liveStdout writes to whatever os.Stdout is when it is written to, so a
// reporter made before captureOutput swaps it still lands in the capture.
type liveStdout struct{}

func (liveStdout) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// stdoutReporter is a verbose plain reporter on the live stdout, so a test
// that reads captured output sees every detail as text.
func stdoutReporter() ui.Reporter { return ui.NewPlain(liveStdout{}, true) }

// captureOutput runs fn and returns what it wrote to stdout and to stderr.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stderr
		os.Stderr = w
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			done <- buf.String()
		}()
		fn()
		w.Close()
		os.Stderr = saved
		stderr = <-done
	})
	return stdout, stderr
}

// Every pass is a plan of its own, and Build may decide something on it the
// operator has to act on, so each pass's notes are reported before it runs.
func TestEveryPassReportsItsNotes(t *testing.T) {
	withIDPFake(t)
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	log := passLog{notes: []apply.Note{{Hint: "run infra down", Text: "the network differs"}}}
	var err error
	captureOutput(t, func() { _, err = executeWithClients(c, log.pass(t, c, "talk")) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plan hold=account,blog,talk,wiki client=false", "note run infra down", "execute", "plan hold= client=true", "note run infra down", "execute"}
	if strings.Join(log.events, "\n") != strings.Join(want, "\n") {
		t.Errorf("passes:\n%s\nwant:\n%s", strings.Join(log.events, "\n"), strings.Join(want, "\n"))
	}
}

// On a site running Pocket ID, the first pass holds back every other app,
// Pocket ID answers, the client is created and recorded, and only then is
// the site planned again, with the app rendering the recorded client.
func TestApplyCreatesAndRecordsTheClientBeforeTheAppRenders(t *testing.T) {
	fake := withIDPFake(t)
	path := secretsWithout(t, "talk")
	c := stepFor(t, "home-a", path)
	var log passLog
	var err error
	stdout, stderr := captureOutput(t, func() {
		_, err = executeWithClients(c, log.pass(t, c, "talk"))
	})
	if err != nil {
		t.Fatalf("%v\n%s%s", err, stdout, stderr)
	}
	want := []string{"plan hold=account,blog,talk,wiki client=false", "execute", "plan hold= client=true", "execute"}
	if strings.Join(log.events, "\n") != strings.Join(want, "\n") {
		t.Errorf("passes:\n%s\nwant:\n%s", strings.Join(log.events, "\n"), strings.Join(want, "\n"))
	}
	if !fake.mutated {
		t.Error("Pocket ID was sent nothing")
	}
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	got := secrets.OIDCClients["talk"]
	if got.ClientID != "c-1" || len(got.ClientSecret) < 16 {
		t.Fatalf("recorded id %q and a %d character secret", got.ClientID, len(got.ClientSecret))
	}
	if len(fake.prefixes) != 1 || !strings.HasPrefix(got.ClientSecret, fake.prefixes[0]) {
		t.Error("Pocket ID was not sent the recorded secret")
	}
	if strings.Contains(stdout, got.ClientSecret) || strings.Contains(stderr, got.ClientSecret) {
		t.Error("the secret was printed")
	}
	if !strings.Contains(stdout, "recorded oidc_clients.talk.client_id") {
		t.Errorf("output:\n%s", stdout)
	}
	if err := c.result(); err != nil {
		t.Error(err)
	}
}

// A dry run probes, prints the client plan, and sends Pocket ID nothing.
func TestApplyDryRunSendsNoMutation(t *testing.T) {
	fake := withIDPFake(t)
	path := secretsWithout(t, "talk")
	before, _ := os.ReadFile(path)
	c := stepFor(t, "home-a", path)
	rec := &ui.Recorder{Verbose_: true}
	c.report = rec
	captureOutput(t, func() { c.ensure(false, c.pocketIDHere()) })
	if !rec.Has("pending", "create OIDC client talk") || !rec.Has("detail", "POST /api/oidc/clients") {
		t.Errorf("the dry run does not show the client:\n%s", rec.Lines())
	}
	if fake.mutated {
		t.Error("a dry run mutated Pocket ID")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("a dry run wrote the secrets file")
	}
	if c.steps == 0 || len(c.heldApps()) != 0 {
		t.Errorf("planned %d step(s), held %v", c.steps, c.heldApps())
	}
}

// A Pocket ID that cannot be reached holds back an app with no client, and
// lets one with a recorded client go ahead on it. Neither fails the apply.
func TestApplyHoldsBackOnlyAppsWithoutAClientWhenPocketIDIsUnreachable(t *testing.T) {
	var asked []string
	lookWith(t, map[string]string{"home-a.local": "down", "home-b.local": "down"}, &asked)
	for _, tc := range []struct {
		name     string
		recorded bool
		hold     string
		says     string
	}{
		{"no client", false, "plan hold=health ", "health held back: no client yet and Pocket ID not asked"},
		{"recorded client", true, "plan hold= ", "health: Pocket ID not asked, recorded client used"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := withIDPFake(t)
			path := secretsWithout(t, "health")
			if tc.recorded {
				secrets, _ := config.LoadSecrets(path)
				_ = secrets.Set("oidc_clients.health.client_id", "fixture-not-a-secret-health-id")
				_ = secrets.Set("oidc_clients.health.client_secret", "fixture-not-a-secret-health-secret")
				if err := config.WriteSecrets(path, secrets, nil); err != nil {
					t.Fatal(err)
				}
			}
			c := stepFor(t, "watch", path)
			var log passLog
			var err error
			stdout, _ := captureOutput(t, func() {
				_, err = executeWithClients(c, log.pass(t, c, "health"))
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(log.events) != 2 || !strings.HasPrefix(log.events[0], tc.hold) {
				t.Errorf("passes %v, want one that starts %q", log.events, tc.hold)
			}
			if !strings.Contains(stdout, tc.says) {
				t.Errorf("output lacks %q:\n%s", tc.says, stdout)
			}
			if len(fake.commands) != 0 {
				t.Error("an unreachable Pocket ID was called")
			}
			var result error
			after, _ := captureOutput(t, func() { result = c.result() })
			if result != nil {
				t.Errorf("an unreachable Pocket ID failed the apply: %v", result)
			}
			if !tc.recorded && !strings.Contains(after, "held back from this apply: health\n") {
				t.Errorf("the end of the apply does not say what was held back:\n%s", after)
			}
		})
	}
}

// An existing client that differs from what the app needs refuses that app:
// the rest of the site is applied, and the apply fails at the end.
func TestApplyRefusesOnlyTheAppWhoseClientDiffers(t *testing.T) {
	fake := withIDPFake(t)
	fake.client = &pocketid.OIDCClient{ID: "fixture-not-a-secret-talk-id", Name: "talk", CallbackURLs: []string{"https://elsewhere.example.org/callback"}, PkceEnabled: true}
	c := stepFor(t, "home-a", tempSecrets(t))
	var log passLog
	var err error
	stdout, _ := captureOutput(t, func() {
		_, err = executeWithClients(c, log.pass(t, c, "talk"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(log.events) != 4 || !strings.HasPrefix(log.events[2], "plan hold=talk ") || log.events[3] != "execute" {
		t.Errorf("passes %v, want the rest of the site applied with talk held back", log.events)
	}
	if fake.mutated {
		t.Error("a refused client was changed")
	}
	if !strings.Contains(stdout, "talk refused; the rest of the site is applied") {
		t.Errorf("output:\n%s", stdout)
	}
	if err := c.result(); err == nil || !strings.Contains(err.Error(), "talk") || !strings.Contains(err.Error(), "differs") {
		t.Errorf("result %v, want talk's refusal", err)
	}
}

// The secret is written before Pocket ID is sent it, so a file that cannot
// be written back encrypted is refused before Pocket ID is asked anything.
func TestApplyRefusesAnUnwritableSecretsFileBeforeContactingPocketID(t *testing.T) {
	fake := withIDPFake(t)
	path := secretsWithout(t, "talk")
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	secrets.Encrypted = true
	_, err = newClientStep(stdoutReporter(), cfg, "home-a", "", path, secrets, nil)
	if err == nil || !strings.Contains(err.Error(), "names a recipient") {
		t.Fatalf("got %v", err)
	}
	if len(fake.commands) != 0 {
		t.Error("Pocket ID was contacted")
	}
}

// --only naming no app that signs in through Pocket ID has no identity step.
func TestApplyHasNoIdentityStepWithoutAnAppThatSignsIn(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	c, err := newClientStep(stdoutReporter(), cfg, "home-a", "", fixtureSecretsPath(), secrets, []string{"gate"})
	if err != nil || c != nil {
		t.Errorf("got %+v, %v", c, err)
	}
}

// The second pass is told what the first did, so it does not repeat an
// --overwrite or a --recreate the first carried out (apply.After).
func TestApplyTellsTheSecondPassWhatTheFirstDid(t *testing.T) {
	withIDPFake(t)
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	var log passLog
	var err error
	captureOutput(t, func() { _, err = executeWithClients(c, log.pass(t, c, "talk")) })
	if err != nil {
		t.Fatal(err)
	}
	if len(log.done) != 2 || len(log.done[0]) != 0 || len(log.done[1]) != 1 || log.done[1][0] != log.plans[0] {
		t.Errorf("the passes were told %v, want nothing and then the first pass", log.done)
	}
}

// A Pocket ID on this site that never answers after the first pass holds
// back the apps without a client, and the rest of the site still applies.
func TestApplyHoldsBackWhenItsOwnPocketIDNeverAnswers(t *testing.T) {
	fake := withIDPFake(t)
	saved := checkOneActive
	checkOneActive = func(*config.Config, string, map[string]apply.Transport, io.Writer) error {
		return fmt.Errorf("pocket-id auth: no site has an active instance within 3m0s")
	}
	t.Cleanup(func() { checkOneActive = saved })
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	var log passLog
	var err error
	stdout, _ := captureOutput(t, func() { _, err = executeWithClients(c, log.pass(t, c, "talk")) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plan hold=account,blog,talk,wiki client=false", "execute", "plan hold=talk client=false", "execute"}
	if strings.Join(log.events, "\n") != strings.Join(want, "\n") {
		t.Errorf("passes:\n%s", strings.Join(log.events, "\n"))
	}
	if !strings.Contains(stdout, "talk held back: no client yet and Pocket ID not asked") || len(fake.commands) != 0 {
		t.Errorf("Pocket ID was called %d time(s), output:\n%s", len(fake.commands), stdout)
	}
	captureOutput(t, func() { err = c.result() })
	if err != nil {
		t.Error(err)
	}
}

// Credentials recorded for a client Pocket ID does not have are replaced:
// nothing can be signing in with a client that does not exist. apply creates
// the client, records the new credentials over the stale ones, and says so.
func TestApplyReplacesRecordedCredentialsForAMissingClient(t *testing.T) {
	fake := withIDPFake(t)
	path := tempSecrets(t)
	c := stepFor(t, "home-a", path, "talk")
	var err error
	stdout, stderr := captureOutput(t, func() { err = c.ensure(true, false) })
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	got := secrets.OIDCClients["talk"]
	if got.ClientID != "c-1" || got.ClientSecret == "fixture-not-a-secret-talk-secret" {
		t.Errorf("recorded id %q, and the secret replaced: %t", got.ClientID, got.ClientSecret != "fixture-not-a-secret-talk-secret")
	}
	if len(fake.prefixes) != 1 || !strings.HasPrefix(got.ClientSecret, fake.prefixes[0]) {
		t.Error("Pocket ID was not sent the recorded secret")
	}
	if !strings.Contains(stdout, "talk: recorded client fixture- is not at Pocket ID; created a new one") {
		t.Errorf("output:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, got.ClientSecret) || strings.Contains(stdout+stderr, "fixture-not-a-secret-talk-secret") {
		t.Error("a secret was printed")
	}
	if err := c.result(); err != nil {
		t.Error(err)
	}
	if len(c.heldApps()) != 0 {
		t.Errorf("held %v", c.heldApps())
	}
}

// A dry run lists the replacement as planned work, not as a refusal.
func TestApplyDryRunListsTheReplacementOfAMissingClient(t *testing.T) {
	fake := withIDPFake(t)
	path := tempSecrets(t)
	before, _ := os.ReadFile(path)
	c := stepFor(t, "home-a", path, "talk")
	rec := &ui.Recorder{Verbose_: true}
	c.report = rec
	captureOutput(t, func() { c.ensure(false, c.pocketIDHere()) })
	if !rec.Has("pending", "create OIDC client talk") || !rec.Has("pending", "create client secret for talk") {
		t.Errorf("the dry run does not plan the client:\n%s", rec.Lines())
	}
	if !rec.Has("warn", "talk: recorded client fixture- is not at Pocket ID; a new one replaces it") {
		t.Errorf("the dry run does not say the client is replaced:\n%s", rec.Lines())
	}
	if rec.Has("refuse", "talk") || len(c.refused) != 0 {
		t.Errorf("the dry run refused talk:\n%s", rec.Lines())
	}
	if fake.mutated {
		t.Error("a dry run mutated Pocket ID")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("a dry run wrote the secrets file")
	}
}

// Recorded credentials apply cannot safely replace are refused, and the
// operator is pointed at the command: a client named after the app under
// another ID may be what a running app signs in with, and half a record says
// something went wrong that a person should look at.
func TestApplyRefusesRecordedCredentialsItCannotReplace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		client  *pocketid.OIDCClient
		partial bool
	}{
		{"another client ID", &pocketid.OIDCClient{ID: "c-other", Name: "talk", CallbackURLs: []string{"https://talk.example.org/oauth/oidc/verify"}, PkceEnabled: true}, false},
		{"partly recorded, no client at Pocket ID", nil, true},
		{"partly recorded, another client ID", &pocketid.OIDCClient{ID: "c-other", Name: "talk", CallbackURLs: []string{"https://talk.example.org/oauth/oidc/verify"}, PkceEnabled: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := withIDPFake(t)
			fake.client = tc.client
			path := tempSecrets(t)
			if tc.partial {
				secrets, _ := config.LoadSecrets(path)
				_ = secrets.Set("oidc_clients.talk.client_secret", "")
				if err := config.WriteSecrets(path, secrets, nil); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			c := stepFor(t, "home-a", path)
			var err error
			captureOutput(t, func() { err = c.ensure(true, false) })
			if err != nil {
				t.Fatal(err)
			}
			if fake.mutated {
				t.Error("Pocket ID was changed")
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Error("the recorded credentials were replaced")
			}
			result := c.result()
			if result == nil || !strings.Contains(result.Error(), "paisans oidc client create --app talk") {
				t.Errorf("result %v, want a refusal naming the command", result)
			}
			if got := strings.Join(c.heldApps(), ","); got != "talk" {
				t.Errorf("held %s", got)
			}
		})
	}
}

// A plan that would record into a file that cannot be written back
// encrypted is refused before Pocket ID is sent anything.
func TestApplyRefusesARecordingStepBeforeAnyMutation(t *testing.T) {
	fake := withIDPFake(t)
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	c.secrets.Encrypted = true
	var err error
	captureOutput(t, func() { err = c.ensure(true, false) })
	if err == nil || !strings.Contains(err.Error(), "names a recipient") {
		t.Fatalf("got %v", err)
	}
	if fake.mutated {
		t.Error("Pocket ID was changed")
	}
}

// --ssh replaces the applying site's ssh section for every look at it,
// including the one that finds the active Pocket ID.
func TestActiveInstanceReachesTheApplyingSiteThroughSSH(t *testing.T) {
	var reached []string
	saved := standbyLook
	standbyLook = func(tr apply.SSHTransport) apply.Transport {
		reached = append(reached, tr.Describe())
		var asked []string
		return stateHost{"active", &asked, tr.Describe()}
	}
	t.Cleanup(func() { standbyLook = saved })
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = activeInstance(cfg, "auth", "home-a", "operator@jump.example.org")
	if !slices.Contains(reached, "operator@jump.example.org") || slices.Contains(reached, "ubuntu@home-a.local") {
		t.Errorf("reached %v", reached)
	}
}

// A refusal from the site lookup names the command it came from.
func TestAdminSiteNamesItsCaller(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminSite(cfg, "talk", "vm", "apply"); err == nil || !strings.HasPrefix(err.Error(), "apply: ") {
		t.Errorf("got %v", err)
	}
}

// fileHost is an empty host with some files already on it and no manifest,
// so each of them is somebody else's: a conflict.
type fileHost struct {
	emptyHost
	files  map[string]string
	writes int
}

func (h *fileHost) ReadFile(path string) (string, bool, error) {
	content, ok := h.files[path]
	return content, ok, nil
}
func (h *fileHost) WriteFile(string, string, uint32) error { h.writes++; return nil }

// A file edited on the host refuses the apply before Pocket ID is asked
// anything and before either pass writes, as a single pass apply refuses
// before its first write.
func TestApplyRefusesAConflictBeforeContactingPocketID(t *testing.T) {
	fake := withIDPFake(t)
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	host := &fileHost{files: map[string]string{"/srv/paisans/f2a9/talk/compose.yaml": "edited on the host\n"}}
	planFor := func(hold []string) (*apply.Plan, error) {
		rendered, err := render.Build(c.cfg, c.secrets)
		if err != nil {
			return nil, err
		}
		return apply.Build("home-a", rendered, acme.Module(c.cfg.ACME.Provider), host, apply.Except(hold...))
	}
	var err error
	captureOutput(t, func() { _, err = planWithClients(c, planFor, true) })
	if err == nil || !strings.Contains(err.Error(), "/srv/paisans/f2a9/talk/compose.yaml") {
		t.Fatalf("got %v", err)
	}
	if len(fake.commands) != 0 || host.writes != 0 {
		t.Errorf("Pocket ID was called %d time(s) and %d file(s) written", len(fake.commands), host.writes)
	}

	// A dry run still shows the plan, conflict and all.
	captureOutput(t, func() { _, err = planWithClients(c, planFor, false) })
	if err != nil {
		t.Errorf("a dry run refused: %v", err)
	}
}

// A deployment with no pocket-id app has no identity step: the app is
// applied as it always was, without sign in.
func TestApplyHasNoIdentityStepWithoutPocketID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(freshSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newClientStep(stdoutReporter(), cfg, "home-a", "", filepath.Join(t.TempDir(), "secrets.yaml"), &config.Secrets{Version: 1}, nil)
	if err != nil || c != nil {
		t.Errorf("got %+v, %v", c, err)
	}
}

// Without Pocket ID's API key there is nothing to call it with. The app is
// held back, and the operator is told the key is missing and how to get it,
// not that Pocket ID is down.
func TestApplyNamesAMissingAPIKey(t *testing.T) {
	fake := withIDPFake(t)
	path := secretsWithout(t, "health")
	secrets, _ := config.LoadSecrets(path)
	delete(secrets.Apps["auth"], "static_api_key")
	if err := config.WriteSecrets(path, secrets, nil); err != nil {
		t.Fatal(err)
	}
	c := stepFor(t, "watch", path)
	var err error
	// render refuses a missing key for every site, so the step is driven
	// directly: this is what it says when it meets one.
	stdout, _ := captureOutput(t, func() {
		err = errors.Join(c.ensure(true, false), c.result())
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.heldApps(), ","); got != "health" {
		t.Errorf("held %s", got)
	}
	for _, want := range []string{"static_api_key is missing", "paisans init", "held back from this apply: health"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "until Pocket ID answers") {
		t.Errorf("a missing key is reported as Pocket ID being down:\n%s", stdout)
	}
	if len(fake.commands) != 0 {
		t.Error("Pocket ID was called without a key")
	}
}

// An --overwrite in a held stack is not written, and the end of the apply
// says so rather than dropping it silently.
func TestApplyReportsAnOverwriteItHeldBack(t *testing.T) {
	var asked []string
	lookWith(t, map[string]string{"home-a.local": "down", "home-b.local": "down"}, &asked)
	withIDPFake(t)
	c := stepFor(t, "watch", secretsWithout(t, "health"))
	log := passLog{heldOverwrites: []string{"/srv/paisans/f2a9/health/.env"}}
	var err error
	stdout, _ := captureOutput(t, func() {
		_, err = executeWithClients(c, log.pass(t, c, "health"))
		err = errors.Join(err, c.result())
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--overwrite not applied, its stack was held back: /srv/paisans/f2a9/health/.env") {
		t.Errorf("the held overwrite is not reported:\n%s", stdout)
	}
}

// apply --execute does not list the client plan: the read only planning call
// lists nothing without --verbose, the executing call reports its mutations
// as steps, and a plan warning shows once although both calls plan.
func TestApplyExecuteListsNoClientPlanAndWarnsOnce(t *testing.T) {
	fake := withIDPFake(t)
	custom := "https://wiki.example.org/custom"
	fake.others["wiki"].LaunchURL = &custom
	c := stepFor(t, "home-a", secretsWithout(t, "talk"))
	// A chosen launch URL beside a client's own, which the toolkit did not
	// set, is the warning that both plans carry.
	wiki := c.cfg.Apps["wiki"]
	settings := map[string]any{}
	for k, v := range wiki.Settings {
		settings[k] = v
	}
	settings["sso_dashboard_link"] = "/home"
	wiki.Settings = settings
	c.cfg.Apps["wiki"] = wiki
	rec := &ui.Recorder{}
	c.report = rec
	planFor := func([]string) (*apply.Plan, error) { return &apply.Plan{Site: "home-a"}, nil }
	var err error
	captureOutput(t, func() {
		if _, err = planWithClients(c, planFor, true); err == nil {
			err = c.ensure(true, false)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Has("pending", "") {
		t.Errorf("--execute listed the client plan:\n%s", rec.Lines())
	}
	if !rec.Has("done", "create OIDC client talk") {
		t.Errorf("the client was not created as a step:\n%s", rec.Lines())
	}
	warnings := 0
	for _, e := range rec.Events {
		if e.Kind == "warn" && strings.Contains(e.Text, "sso_dashboard_link") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("the warning showed %d times:\n%s", warnings, rec.Lines())
	}
}

// A client's plan lists each mutation as an item with what it sends, and,
// under --verbose, marks what Pocket ID already has done, with no label.
func TestListClientPlanMarksWhatIsPresentDone(t *testing.T) {
	plan := &oidcclient.Plan{
		Steps:   []oidcclient.Step{{Kind: oidcclient.AddSecret, Title: "create client secret for talk", Detail: "create client secret for talk: generated on this workstation. Never printed"}},
		Present: []string{"client talk (id client-1)", "group members"},
	}
	quiet := &ui.Recorder{}
	listClientPlan(quiet, "talk", "auth", "home-a", plan)
	if !quiet.Has("pending", "create client secret for talk") || quiet.Has("done", "") {
		t.Errorf("got:\n%s", quiet.Lines())
	}

	var b strings.Builder
	listClientPlan(ui.NewPlain(&b, true), "talk", "auth", "home-a", plan)
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"  todo create client secret for talk\n",
		"  ok   client talk (id client-1)\n",
		"  ok   group members\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "present ") {
		t.Errorf("a finding carries its label:\n%s", out)
	}
}

// The homeserver's client and the gate's are built like every other
// blueprint's: apply and `oidc client create` make them, at the authentication
// service's callback and at oauth2-proxy's, each with PKCE on, since both
// always send a code challenge. The gate's client admits members and admins, as
// the gate does, and is named after the community.
func TestClientDesiredForTheHomeserverAndTheGate(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	chat, ok := clientDesired(cfg, "chat", false)
	if !ok {
		t.Fatal("no client for the homeserver")
	}
	if want := "https://" + cfg.Apps["chat"].Hostname + "/upstream/callback/" + blueprints.StableULID("chat"); chat.CallbackURL != want || !chat.PKCE {
		t.Errorf("chat's client: callback %s PKCE %v, want %s with PKCE", chat.CallbackURL, chat.PKCE, want)
	}
	if chat.MemberGroup != blueprints.MembersGroup || chat.AdminGroup != blueprints.AdminsGroup || chat.LaunchURL != "" {
		t.Errorf("chat's client: member %q admin %q launch URL %q", chat.MemberGroup, chat.AdminGroup, chat.LaunchURL)
	}
	gate, ok := clientDesired(cfg, config.GateApp, true)
	if !ok {
		t.Fatal("no client for the gate")
	}
	if want := "https://" + cfg.Apps[config.GateApp].Hostname + "/oauth2/callback"; gate.CallbackURL != want || !gate.PKCE {
		t.Errorf("the gate's client: callback %s PKCE %v, want %s with PKCE", gate.CallbackURL, gate.PKCE, want)
	}
	// The gate is the community's sign in, so its client carries the
	// community's name; every other client carries its app's.
	if cfg.Community.Name == "" || gate.Name != cfg.Community.Name || chat.Name != "chat" {
		t.Errorf("client names: the gate's %q, chat's %q; want %q and chat", gate.Name, chat.Name, cfg.Community.Name)
	}
	if gate.MemberGroup != blueprints.MembersGroup || gate.AdminGroup != blueprints.AdminsGroup || gate.LaunchURL != "" || !gate.RotateSecret {
		t.Errorf("the gate's client: member %q admin %q launch URL %q rotate %v", gate.MemberGroup, gate.AdminGroup, gate.LaunchURL, gate.RotateSecret)
	}

	// Consent is skipped unless the app asks for the screen; the gate has no
	// settings and so skips it.
	if !chat.SkipConsent || !gate.SkipConsent {
		t.Errorf("consent: chat skips %v, the gate skips %v; both should", chat.SkipConsent, gate.SkipConsent)
	}
	original := cfg.Apps["chat"]
	withConsent := original
	withConsent.Settings = map[string]any{blueprints.ConsentSetting: true}
	cfg.Apps["chat"] = withConsent
	if again, _ := clientDesired(cfg, "chat", false); again.SkipConsent {
		t.Error("sso_consent: true still skips consent")
	}
	cfg.Apps["chat"] = original

	// An app's display_name names its client, and the app stays its key in
	// the secrets file.
	app := cfg.Apps["chat"]
	app.DisplayName = "Chat"
	cfg.Apps["chat"] = app
	if chat, _ = clientDesired(cfg, "chat", false); chat.Name != "Chat" || chat.App != "chat" {
		t.Errorf("with a display name, chat's client is %q for app %q", chat.Name, chat.App)
	}
}

// An extra group is created only once a person types the word. Without a
// way to ask, the app is refused and Pocket ID is sent nothing.
func TestExtraGroupCreationAsksFirst(t *testing.T) {
	p := oidcclient.Plan{Steps: []oidcclient.Step{{Kind: oidcclient.CreateGroup, Group: "moderators", Extra: true}}}
	c := &clientStep{}
	if err := c.approveExtraGroups("wiki", p); err == nil || !strings.Contains(err.Error(), "moderators") {
		t.Errorf("no way to ask, err = %v", err)
	}
	var asked string
	c.confirm = func(what string) error { asked = what; return nil }
	if err := c.approveExtraGroups("wiki", p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "moderators") {
		t.Errorf("asked %q", asked)
	}
	c.confirm = func(string) error { return errors.New("declined") }
	if err := c.approveExtraGroups("wiki", p); err == nil {
		t.Error("a declined answer let it through")
	}
}

// A planned stop in the pass that starts Pocket ID, the founding stop or a
// replica's for its databases, is no failure of that pass: its step ends
// done, saying it stopped short, nothing after it runs, and the stop comes
// back as it is, so the run pauses the site's line as it does for a site
// without Pocket ID.
func TestAPlannedStopStartingPocketIDIsNoFailure(t *testing.T) {
	for name, stop := range map[string]error{
		"founding":  foundingWait("home-a", "home-b"),
		"databases": databasesWait("home-a", "home-b"),
	} {
		t.Run(name, func(t *testing.T) {
			withIDPFake(t)
			c := stepFor(t, "home-a", secretsWithout(t, "talk"))
			rec := &ui.Recorder{}
			c.report = rec
			var log passLog
			pass := log.pass(t, c, "talk")
			pass.execute = func(*apply.Plan) error {
				log.events = append(log.events, "execute")
				return stop
			}
			plans, err := executeWithClients(c, pass)
			if err != stop {
				t.Fatalf("got %v, want the stop as it is", err)
			}
			if len(plans) != 0 || strings.Count(strings.Join(log.events, "\n"), "execute") != 1 {
				t.Errorf("went on past the stop: %q", log.events)
			}
			if rec.Has("fail", "start pocket-id") || !rec.Has("done", "start pocket-id") {
				t.Errorf("the stop read as a failure:\n%s", rec.Lines())
			}
		})
	}
}
