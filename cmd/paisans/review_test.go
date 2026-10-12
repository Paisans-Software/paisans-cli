package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// memGrant is a host's grant record, kept in memory: a read returns it, and
// a write the record's own command would make replaces it.
type memGrant struct {
	mu      sync.Mutex
	content string
	found   bool
	writes  int
}

func (m *memGrant) Describe() string { return "ubuntu@home-a.example.org" }

func (m *memGrant) ReadFile(string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.content, m.found, nil
}

var grantPayload = regexp.MustCompile(`printf %s '([A-Za-z0-9+/=]+)' \| base64 -d > "\$tmp"`)

func (m *memGrant) Run(command string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.Contains(command, "/var/lib/paisans/granted.") || !strings.Contains(command, "flock -w 60 9") {
		return "", fmt.Errorf("not a grant write under the lock: %s", command)
	}
	data, err := base64.StdEncoding.DecodeString(grantPayload.FindStringSubmatch(command)[1])
	if err != nil {
		return "", err
	}
	m.content, m.found = string(data), true
	m.writes++
	return "", nil
}

func (m *memGrant) record(t *testing.T) grant.Record {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.found {
		return grant.Record{}
	}
	rec, err := grant.Parse(m.content)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// fakeGrants points grantRunner at one memGrant per site, each holding what
// hold returns for it (nothing, for nil), and returns them.
func fakeGrants(t *testing.T, hold func(site string) *grant.Record) map[string]*memGrant {
	t.Helper()
	hosts := map[string]*memGrant{}
	var mu sync.Mutex
	saved, savedNow, savedAgent := grantRunner, grantNow, agentFingerprints
	grantRunner = func(site string, _ registry.Runner) registry.Runner {
		mu.Lock()
		defer mu.Unlock()
		if hosts[site] == nil {
			hosts[site] = &memGrant{}
			if rec := hold(site); rec != nil {
				hosts[site].content, hosts[site].found = grant.Encode(*rec), true
			}
		}
		return hosts[site]
	}
	grantNow = func() time.Time { return time.Date(2026, 10, 11, 23, 10, 4, 0, time.UTC) }
	agentFingerprints = func() []string { return nil }
	t.Cleanup(func() { grantRunner, grantNow, agentFingerprints = saved, savedNow, savedAgent })
	return hosts
}

// grantedAlready is site's record holding exactly the fixture's grant.
func grantedAlready(t *testing.T, configPath, secretsPath string) func(string) *grant.Record {
	t.Helper()
	cfg := mustLoad(t, configPath)
	secrets, err := config.LoadSecrets(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return func(site string) *grant.Record {
		g, err := grant.For(cfg, plan, site)
		if err != nil {
			t.Fatal(err)
		}
		rec := grant.Update(grant.Record{}, g, grant.AllScope, time.Unix(0, 0), "")
		return &rec
	}
}

// answering is a stdin the operator types answer into, at a terminal, and
// the closing of which is end of input. isTerminal says it is one.
func answering(t *testing.T, answer string) *os.File {
	t.Helper()
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(answer)
	w.Close()
	t.Cleanup(func() { isTerminal = saved; r.Close() })
	return r
}

// noTerminalStdin is a stdin that is not a terminal: a script's.
func noTerminalStdin() io.Reader { return strings.NewReader("approve\n") }

func pendingReview() []grant.Review {
	return []grant.Review{{Site: "home-a", Changes: []grant.Change{{Op: grant.Added, Subject: "key SHA256:b", To: "x", Text: "key SHA256:b (bob) may log in as ubuntu"}}}}
}

// The prompt: typed approve goes on; anything else, end of input, and no
// terminal at all stop it, and no flag is consulted.
func TestApproveReviews(t *testing.T) {
	for name, c := range map[string]struct {
		stdin func(t *testing.T) io.Reader
		want  string
	}{
		"approved":    {func(t *testing.T) io.Reader { return answering(t, "approve\n") }, ""},
		"refused":     {func(t *testing.T) io.Reader { return answering(t, "yes\n") }, `"yes" is not approve`},
		"end":         {func(t *testing.T) io.Reader { return answering(t, "") }, "no answer read"},
		"no terminal": {func(*testing.T) io.Reader { return noTerminalStdin() }, "stdin is not a terminal"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := approveReviews(ui.Discard, c.stdin(t), &out, pendingReview(), "paisans host prepare --site home-a --execute")
			switch {
			case c.want == "" && err != nil:
				t.Errorf("not approved: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error()+" "+problemText(err), c.want)):
				t.Errorf("got %v, want %q", err, c.want)
			}
			if name != "no terminal" && !strings.HasSuffix(out.String(), "Type approve to go on: ") {
				t.Errorf("the prompt: %q", out.String())
			}
		})
	}
}

func problemText(err error) string {
	var p *ui.Problem
	if errors.As(err, &p) {
		return p.Hint + " " + p.Explain
	}
	return ""
}

// Nothing to approve asks nothing, terminal or not: a removal only takes
// away.
func TestNothingToApproveAsksNothing(t *testing.T) {
	removals := []grant.Review{{Site: "home-a", Changes: []grant.Change{{Op: grant.Removed, Subject: "key SHA256:c", Text: "key SHA256:c (carol)"}}}}
	var out bytes.Buffer
	if err := approveReviews(ui.Discard, noTerminalStdin(), &out, removals, "x"); err != nil || out.Len() != 0 {
		t.Errorf("err %v, printed %q", err, out.String())
	}
}

// approver names the one admin whose key the agent holds, and nobody when
// it holds none or several admins' keys.
func TestApprover(t *testing.T) {
	cfg := fixture(t)
	alice, _, _ := config.ParseKeyLine(cfg.Admins["alice"].KeyLines()[0])
	saved := agentFingerprints
	t.Cleanup(func() { agentFingerprints = saved })
	for want, held := range map[string][]string{"alice": {"SHA256:other", alice.Fingerprint}, "": {"SHA256:other"}} {
		agentFingerprints = func() []string { return held }
		if got := approver(cfg); got != want {
			t.Errorf("held %v: %q, want %q", held, got, want)
		}
	}
	cfg.Admins["bob"] = config.Admin{Name: "Bob Example", Email: "bob@example.org", Keys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"}
	bob, _, _ := config.ParseKeyLine(cfg.Admins["bob"].KeyLines()[0])
	agentFingerprints = func() []string { return []string{alice.Fingerprint, bob.Fingerprint} }
	if got := approver(cfg); got != "" {
		t.Errorf("two admins' keys named %q", got)
	}
}

// loggingSSH is quietSSH that logs each command it is given, decoded, to the
// file it returns, and fails one containing fail when fail is set.
func loggingSSH(t *testing.T, fail string) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	script := strings.Replace(quietHost, "case \"$cmd\" in\n", "printf '%s\\n' \"$cmd\" >> '"+log+"'\ncase \"$cmd\" in\n", 1)
	if fail != "" {
		script = strings.Replace(script, "case \"$cmd\" in\n", "case \"$cmd\" in\n*'"+fail+"'*) echo failed; exit 1;;\n", 1)
	}
	sshAnswering(t, script)
	return log
}

func logged(t *testing.T, log string) string {
	t.Helper()
	data, _ := os.ReadFile(log)
	return string(data)
}

// claimed reports whether a command in the log claims the host, which is
// the first change any command that writes makes.
func claimed(log string) bool {
	return strings.Contains(log, registry.Path) && strings.Contains(log, "flock -w")
}

func prepare(t *testing.T, stdin io.Reader, stdout io.Writer, args ...string) (*ui.Recorder, error) {
	t.Helper()
	rec := &ui.Recorder{Verbose_: true}
	o := hostPrepareOptions{config: fixtureConfig(), site: "home-a", sudo: false}
	for _, a := range args {
		if a == "--execute" {
			o.execute = true
		}
	}
	err := hostPrepare(prepareEnv{r: rec, found: func(string, error) {}, stdin: stdin, stdout: stdout}, o)
	return rec, err
}

// host prepare with something to approve and no terminal is refused before
// the host is claimed or changed; the dry run shows the review and asks
// nothing.
func TestHostPrepareRefusesWithoutATerminal(t *testing.T) {
	log := loggingSSH(t, "")
	fakeGrants(t, func(string) *grant.Record { return nil })
	rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute")
	if err == nil || !strings.Contains(problemText(err), "stdin is not a terminal") {
		t.Fatalf("got %v\n%s", err, rec.Lines())
	}
	if claimed(logged(t, log)) {
		t.Errorf("the host was claimed before the review was approved:\n%s", logged(t, log))
	}
	if !rec.Has("pending", "review what paisans.yaml grants") || !strings.Contains(rec.Lines(), "explain: + key SHA256:") {
		t.Errorf("the review is not shown:\n%s", rec.Lines())
	}
	rec, err = prepare(t, noTerminalStdin(), io.Discard)
	if err != nil || !strings.Contains(rec.Lines(), "--execute asks for approve") {
		t.Errorf("dry run: %v\n%s", err, rec.Lines())
	}
}

// With nothing to approve host prepare runs as it always has, terminal or
// not, and leaves the record as it was.
func TestHostPrepareWithNothingToApproveRuns(t *testing.T) {
	log := loggingSSH(t, "")
	hosts := fakeGrants(t, grantedAlready(t, fixtureConfig(), fixtureSecretsPath()))
	rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if !claimed(logged(t, log)) || !rec.Has("result", "Prepared home-a.") {
		t.Errorf("not prepared:\n%s", rec.Lines())
	}
	if hosts["home-a"].writes != 0 {
		t.Errorf("an unchanged grant was written again")
	}
}

// Approved, host prepare records the keys it made authorized keys, with who
// approved them, once every step has succeeded; a step that fails leaves the
// record as it was, so the next run reviews the same keys again.
func TestHostPrepareRecordsTheKeysOnlyAfterSuccess(t *testing.T) {
	cfg := fixture(t)
	alice, _, _ := config.ParseKeyLine(cfg.Admins["alice"].KeyLines()[0])
	for name, fail := range map[string]string{"succeeds": "", "a step fails": "awk -v fp="} {
		t.Run(name, func(t *testing.T) {
			loggingSSH(t, fail)
			hosts := fakeGrants(t, func(string) *grant.Record { return nil })
			agentFingerprints = func() []string { return []string{alice.Fingerprint} }
			var out bytes.Buffer
			rec, err := prepare(t, answering(t, "approve\n"), &out, "--execute")
			if !strings.Contains(out.String(), "Type approve to go on: ") {
				t.Errorf("not asked: %q", out.String())
			}
			got := hosts["home-a"].record(t)
			if fail != "" {
				if err == nil || got.Keys != nil {
					t.Errorf("err %v, record %+v", err, got.Keys)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, rec.Lines())
			}
			if got.Keys == nil || got.Keys.Login != "ubuntu" || len(got.Keys.Entries) != 1 || got.Keys.Entries[0].Fingerprint != alice.Fingerprint || got.Keys.ApprovedBy != "alice" || got.Keys.AppliedAt != "2026-10-11T23:10:04Z" {
				t.Errorf("record %+v", got.Keys)
			}
			if got.Images != nil {
				t.Errorf("host prepare recorded images: %+v", got.Images)
			}
		})
	}
}

// Refused at the prompt, nothing on the host changes.
func TestHostPrepareNotApprovedChangesNothing(t *testing.T) {
	for name, answer := range map[string]string{"refused": "no\n", "end of input": ""} {
		t.Run(name, func(t *testing.T) {
			log := loggingSSH(t, "")
			hosts := fakeGrants(t, func(string) *grant.Record { return nil })
			_, err := prepare(t, answering(t, answer), io.Discard, "--execute")
			if err == nil || claimed(logged(t, log)) || hosts["home-a"].writes != 0 {
				t.Errorf("err %v, claimed %v, writes %d", err, claimed(logged(t, log)), hosts["home-a"].writes)
			}
		})
	}
}

// apply --site, with something to approve and no terminal, is refused before
// the host is claimed; with nothing to approve it goes on to the claim.
func TestApplySiteReviewsBeforeTheClaim(t *testing.T) {
	for name, hold := range map[string]func(string) *grant.Record{
		"something to approve": func(string) *grant.Record { return nil },
		"nothing to approve":   grantedAlready(t, fixtureConfig(), fixtureSecretsPath()),
	} {
		t.Run(name, func(t *testing.T) {
			log := loggingSSH(t, "")
			fakeGrants(t, hold)
			rec := recordConverge(t)
			var err error
			captureStdout(t, func() {
				err = runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--sudo=false", "--site", "home-a", "--execute"}, noTerminalStdin(), io.Discard)
			})
			refused := err != nil && strings.Contains(problemText(err), "stdin is not a terminal")
			if name == "something to approve" {
				if !refused || claimed(logged(t, log)) {
					t.Errorf("err %v, claimed %v:\n%s", err, claimed(logged(t, log)), rec.Lines())
				}
				if !strings.Contains(rec.Lines(), "explain: + image infra/") {
					t.Errorf("the images are not reviewed:\n%s", rec.Lines())
				}
				return
			}
			if refused || !claimed(logged(t, log)) {
				t.Errorf("err %v, claimed %v:\n%s", err, claimed(logged(t, log)), rec.Lines())
			}
		})
	}
}

// A step apply with no --site runs checks its review against the run's
// approval: what was approved goes on with the run's approver, and anything
// else, a change made after the approval, is refused.
func TestAStepChecksTheRunsApproval(t *testing.T) {
	cfg := fixture(t)
	fakeGrants(t, func(string) *grant.Record { return nil })
	g, _ := grant.For(cfg, nil, "home-a")
	full := grant.Compare("home-a", g, grant.Record{}, grant.PrepareScope)
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })

	reviewedByRun = &runReview{approved: map[string]grant.Review{"home-a": full}, approvedBy: "alice"}
	s, by, err := stepReview(ui.Discard, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, true, "x")
	if err != nil || s == nil || by != "alice" {
		t.Errorf("approved: %v %v %q", s, err, by)
	}

	reviewedByRun = &runReview{approved: map[string]grant.Review{}}
	if _, _, err := stepReview(ui.Discard, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, true, "x"); err == nil || !strings.Contains(problemText(err), "changed after it was approved") {
		t.Errorf("not refused: %v", err)
	}

	// A dry run under the run reads nothing: the run showed the review.
	reviewedByRun = &runReview{}
	if s, _, err := stepReview(ui.Discard, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, false, "x"); s != nil || err != nil {
		t.Errorf("dry run: %v %v", s, err)
	}
}
