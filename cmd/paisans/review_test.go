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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
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
	if !strings.Contains(command, "/srv/paisans/f2a9/.paisans/granted.json") || !strings.Contains(command, "flock -w 60 9") {
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
// terminal at all stop it, without --yes.
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
			err := approveReviews(ui.Discard, answerer{}, c.stdin(t), &out, pendingReview(), "paisans site prepare --site home-a --execute")
			switch {
			case c.want == "" && err != nil:
				t.Errorf("not approved: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error()+" "+problemText(err), c.want)):
				t.Errorf("got %v, want %q", err, c.want)
			}
			if name != "no terminal" && !strings.HasSuffix(out.String(), "Type 'approve' (no quotes) to proceed: ") {
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
	if err := approveReviews(ui.Discard, answerer{}, noTerminalStdin(), &out, removals, "x"); err != nil || out.Len() != 0 {
		t.Errorf("err %v, printed %q", err, out.String())
	}
}

// approver names the one admin whose key the agent holds, and nobody when
// it holds none or several admins' keys.
func TestApprover(t *testing.T) {
	cfg := fixture(t)
	alice, _, _ := config.ParseKeyLine(cfg.Operators["alice"].KeyLines()[0])
	saved := agentFingerprints
	t.Cleanup(func() { agentFingerprints = saved })
	for want, held := range map[string][]string{"alice": {"SHA256:other", alice.Fingerprint}, "": {"SHA256:other"}} {
		agentFingerprints = func() []string { return held }
		if got := approver(cfg); got != want {
			t.Errorf("held %v: %q, want %q", held, got, want)
		}
	}
	cfg.Operators["bob"] = config.Operator{Name: "Bob Example", Email: "bob@example.org", Keys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"}
	bob, _, _ := config.ParseKeyLine(cfg.Operators["bob"].KeyLines()[0])
	agentFingerprints = func() []string { return []string{alice.Fingerprint, bob.Fingerprint} }
	if got := approver(cfg); got != "" {
		t.Errorf("two operators' keys named %q", got)
	}
}

// loggingSSH is quietSSH that logs each command it is given, decoded, to the
// file it returns, and fails one containing fail when fail is set.
func loggingSSH(t *testing.T, fail string) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	answerQuietly(t, log, fail)
	return log
}

func logged(t *testing.T, log string) string {
	t.Helper()
	data, _ := os.ReadFile(log)
	return string(data)
}

// checked reports whether a command in the log reads the host's admission
// record, which every command that writes does before its first change.
func checked(log string) bool {
	return strings.Contains(log, admission.Dir+"/admitted.")
}

func prepare(t *testing.T, stdin io.Reader, stdout io.Writer, args ...string) (*ui.Recorder, error) {
	t.Helper()
	rec := &ui.Recorder{Verbose_: true}
	o := hostPrepareOptions{config: fixtureConfig(), site: "home-a"}
	for _, a := range args {
		if a == "--execute" {
			o.execute = true
		}
	}
	err := hostPrepare(prepareEnv{r: rec, found: func(string, error) {}, stdin: stdin, stdout: stdout}, o)
	return rec, err
}

// site prepare with something to approve and no terminal is refused before
// the host is checked or changed; the dry run shows the review and asks
// nothing.
func TestHostPrepareRefusesWithoutATerminal(t *testing.T) {
	log := loggingSSH(t, "")
	fakeGrants(t, func(string) *grant.Record { return nil })
	rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute")
	if err == nil || !strings.Contains(problemText(err), "stdin is not a terminal") {
		t.Fatalf("got %v\n%s", err, rec.Lines())
	}
	if checked(logged(t, log)) {
		t.Errorf("the host was checked before the review was approved:\n%s", logged(t, log))
	}
	if !rec.Has("pending", "review what paisans-stack.yaml grants") || !strings.Contains(rec.Lines(), "explain: + key SHA256:") {
		t.Errorf("the review is not shown:\n%s", rec.Lines())
	}
	rec, err = prepare(t, noTerminalStdin(), io.Discard)
	if err != nil || !strings.Contains(rec.Lines(), "it asks you to type approve") {
		t.Errorf("dry run: %v\n%s", err, rec.Lines())
	}
}

// With nothing to approve site prepare runs as it always has, terminal or
// not, and leaves the record as it was.
func TestHostPrepareWithNothingToApproveRuns(t *testing.T) {
	log := loggingSSH(t, "")
	hosts := fakeGrants(t, grantedAlready(t, fixtureConfig(), fixtureSecretsPath()))
	rec, err := prepare(t, noTerminalStdin(), io.Discard, "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if !checked(logged(t, log)) || !rec.Has("result", "Prepared home-a.") {
		t.Errorf("not prepared:\n%s", rec.Lines())
	}
	if hosts["home-a"].writes != 0 {
		t.Errorf("an unchanged grant was written again")
	}
}

// Approved, site prepare records the keys it made authorized keys, with who
// approved them, once every step has succeeded; a step that fails leaves the
// record as it was, so the next run reviews the same keys again.
func TestHostPrepareRecordsTheKeysOnlyAfterSuccess(t *testing.T) {
	cfg := fixture(t)
	alice, _, _ := config.ParseKeyLine(cfg.Operators["alice"].KeyLines()[0])
	for name, fail := range map[string]string{"succeeds": "", "a step fails": "tail -c1"} {
		t.Run(name, func(t *testing.T) {
			loggingSSH(t, fail)
			hosts := fakeGrants(t, func(string) *grant.Record { return nil })
			agentFingerprints = func() []string { return []string{alice.Fingerprint} }
			var out bytes.Buffer
			rec, err := prepare(t, answering(t, "approve\n"), &out, "--execute")
			if !strings.Contains(out.String(), "Type 'approve' (no quotes) to proceed: ") {
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
			// The fixture's two operators, alice and bob, both log in to home-a.
			if got.Keys == nil || got.Keys.Login != "ubuntu" || len(got.Keys.Entries) != 2 || !slices.ContainsFunc(got.Keys.Entries, func(k grant.Key) bool { return k.Fingerprint == alice.Fingerprint }) || got.Keys.ApprovedBy != "alice" || got.Keys.AppliedAt != "2026-10-11T23:10:04Z" {
				t.Errorf("record %+v", got.Keys)
			}
			if got.Images != nil {
				t.Errorf("site prepare recorded images: %+v", got.Images)
			}
		})
	}
}

// What the operator approved is recorded before the first change, so a run
// that stops part way does not ask for it again: the next run, even without
// a terminal, goes on without asking, and records the keys with who approved
// them once it succeeds.
func TestHostPrepareDoesNotAskAgainForWhatWasApproved(t *testing.T) {
	cfg := fixture(t)
	alice, _, _ := config.ParseKeyLine(cfg.Operators["alice"].KeyLines()[0])
	loggingSSH(t, "tail -c1")
	hosts := fakeGrants(t, func(string) *grant.Record { return nil })
	agentFingerprints = func() []string { return []string{alice.Fingerprint} }
	if _, err := prepare(t, answering(t, "approve\n"), io.Discard, "--execute"); err == nil {
		t.Fatal("the failing step passed")
	}
	got := hosts["home-a"].record(t)
	if got.Keys != nil || got.Approved == nil || len(got.Approved.Entries) != 2 || !slices.ContainsFunc(got.Approved.Entries, func(a grant.Approval) bool { return a.Subject == "key "+alice.Fingerprint && a.ApprovedBy == "alice" }) {
		t.Fatalf("record after the failed run %+v %+v", got.Keys, got.Approved)
	}
	loggingSSH(t, "")
	agentFingerprints = func() []string { return nil }
	var out bytes.Buffer
	rec, err := prepare(t, noTerminalStdin(), &out, "--execute")
	if err != nil || out.Len() != 0 {
		t.Fatalf("err %v, asked %q\n%s", err, out.String(), rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "2 approved earlier, not applied yet, nothing new to approve") {
		t.Errorf("the review does not say what was approved earlier:\n%s", rec.Lines())
	}
	got = hosts["home-a"].record(t)
	if got.Keys == nil || got.Keys.ApprovedBy != "alice" || got.Approved != nil {
		t.Errorf("record after the second run %+v %+v", got.Keys, got.Approved)
	}
}

// Refused at the prompt, nothing on the host changes.
func TestHostPrepareNotApprovedChangesNothing(t *testing.T) {
	for name, answer := range map[string]string{"refused": "no\n", "end of input": ""} {
		t.Run(name, func(t *testing.T) {
			log := loggingSSH(t, "")
			hosts := fakeGrants(t, func(string) *grant.Record { return nil })
			_, err := prepare(t, answering(t, answer), io.Discard, "--execute")
			if err == nil || checked(logged(t, log)) || hosts["home-a"].writes != 0 {
				t.Errorf("err %v, checked %v, writes %d", err, checked(logged(t, log)), hosts["home-a"].writes)
			}
		})
	}
}

// apply --site, with something to approve and no terminal, is refused before
// the host's admission is checked; with nothing to approve it goes on to the
// check.
func TestApplySiteReviewsBeforeTheAdmissionCheck(t *testing.T) {
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
				err = runApply([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--site", "home-a", "--execute"}, noTerminalStdin(), io.Discard)
			})
			refused := err != nil && strings.Contains(problemText(err), "stdin is not a terminal")
			if name == "something to approve" {
				if !refused || checked(logged(t, log)) {
					t.Errorf("err %v, checked %v:\n%s", err, checked(logged(t, log)), rec.Lines())
				}
				if !strings.Contains(rec.Lines(), "explain: + image infra/") {
					t.Errorf("the images are not reviewed:\n%s", rec.Lines())
				}
				return
			}
			if refused || !checked(logged(t, log)) {
				t.Errorf("err %v, checked %v:\n%s", err, checked(logged(t, log)), rec.Lines())
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
	s, by, err := stepReview(ui.Discard, answerer{}, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, true, "x")
	if err != nil || s == nil || by != "alice" {
		t.Errorf("approved: %v %v %q", s, err, by)
	}

	reviewedByRun = &runReview{approved: map[string]grant.Review{}}
	if _, _, err := stepReview(ui.Discard, answerer{}, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, true, "x"); err == nil || !strings.Contains(problemText(err), "changed after it was approved") {
		t.Errorf("not refused: %v", err)
	}

	// A dry run under the run reads nothing: the run showed the review.
	reviewedByRun = &runReview{}
	if s, _, err := stepReview(ui.Discard, answerer{}, nil, nil, cfg, "home-a", g, grant.PrepareScope, nil, false, "x"); s != nil || err != nil {
		t.Errorf("dry run: %v %v", s, err)
	}
}

// joinConfig is the fixture declared for site add to join home-b, as
// fixtureRuns declares it: home-b holds the data role alone, and every site
// dials the others directly.
func joinConfig(t *testing.T) string {
	t.Helper()
	return joinConfigAs(t, "[data]")
}

// joinConfigAs is joinConfig with home-b declared with roles.
func joinConfigAs(t *testing.T, roles string) string {
	t.Helper()
	body, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(body), "    endpoint: vm.example.org:51820\n", "    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	s = strings.Replace(s, "  home-a:\n    roles: [data, apps]\n", "  home-a:\n    roles: [data, apps]\n    endpoint: 192.0.2.1:51820\n", 1)
	joined := strings.Replace(s, "  home-b:\n    roles: [data, apps]", "  home-b:\n    roles: "+roles, 1)
	if !strings.Contains(joined, "  home-b:\n    roles: "+roles+"\n") {
		t.Fatal("the fixture no longer declares the sites this edit expects")
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(joined), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// site add applies the new site's whole render in its stage 4, so it
// reviews the new site's images as apply --site does: with something to
// approve and no terminal it is refused before any host's admission is checked, and the
// dry run shows the review and asks nothing.
func TestSiteAddReviewsBeforeTheAdmissionCheck(t *testing.T) {
	path := joinConfig(t)
	log := loggingSSH(t, "")
	fakeGrants(t, func(string) *grant.Record { return nil })
	rec := recordConverge(t)
	err := runSiteAdd([]string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath()), "--execute"}, noTerminalStdin(), io.Discard)
	if err == nil || !strings.Contains(problemText(err), "stdin is not a terminal") || !strings.Contains(problemText(err), "paisans site add home-b --execute") {
		t.Fatalf("got %v\n%s", err, rec.Lines())
	}
	if checked(logged(t, log)) {
		t.Errorf("the hosts were checked before the review was approved:\n%s", logged(t, log))
	}
	if !strings.Contains(rec.Lines(), "explain: + image infra/") || strings.Contains(rec.Lines(), "explain: + key ") {
		t.Errorf("the review is not the new site's images:\n%s", rec.Lines())
	}

	rec = recordConverge(t)
	_ = runSiteAdd([]string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath())}, noTerminalStdin(), io.Discard)
	if !rec.Has("pending", "review what paisans-stack.yaml grants") {
		t.Errorf("the dry run does not show the review:\n%s", rec.Lines())
	}
}

// A site declared [data, apps] is joined by apply with no --site, which
// starts its apps with an apply of its own once the join is done. site add
// alone refuses it, since its stages start no app stacks; as a step of
// apply, it joins the site's data role and leaves the apps to that apply.
func TestSiteAddJoinsTheDataRoleAsAStepOfApply(t *testing.T) {
	path := joinConfigAs(t, "[data, apps]")
	quietSSH(t)
	fakeGrants(t, func(string) *grant.Record { return nil })
	args := []string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath())}
	rec := recordConverge(t)
	err := runSiteAdd(args, noTerminalStdin(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "must be exactly [data]") {
		t.Fatalf("site add alone: %v\n%s", err, rec.Lines())
	}

	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{}
	// The fake hosts run no etcd, so the join stops reading them, where the
	// same join of a site declared [data] stops.
	errText := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	asData := errText(runSiteAdd([]string{"home-b", "--config", joinConfig(t), "--secrets", mustAbs(t, fixtureSecretsPath())}, noTerminalStdin(), io.Discard))
	rec = recordConverge(t)
	got := errText(runSiteAdd(args, noTerminalStdin(), io.Discard))
	if strings.Contains(got, "must be exactly [data]") || got != asData {
		t.Errorf("as a step of apply: %s\nwant, as [data]: %s\n%s", got, asData, rec.Lines())
	}
}

// A run that stops after its review recorded the approvals, and before
// home-b joins, is run again: its review finds every entry approved on the
// host and asks nothing. site add then records only the images the join
// started, keeping the approvals for the rest of home-b's grant, so the
// apply that follows the join starts them without a refusal. HAProxy, which
// only the apps role puts on a data site, is one of them when infra.images
// overrides it.
func TestAResumedJoinKeepsTheApprovalsForItsApps(t *testing.T) {
	for _, c := range []struct {
		name    string
		haproxy string
	}{
		{"foundation infra", ""},
		{"haproxy overridden", "docker.io/library/haproxy:9.9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			full := fixture(t)
			if c.haproxy != "" {
				full.Infra.Images = map[string]string{"haproxy": c.haproxy}
			}
			resumeJoin(t, full)
		})
	}
}

// resumeJoin is a resumed run's join of home-b, declared [data, apps] in
// full, through site add's review and record and then apply --site's
// review, which fails t if it refuses.
func resumeJoin(t *testing.T, full *config.Config) {
	t.Helper()
	fakeGrants(t, func(string) *grant.Record { return nil })
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	granted := func(cfg *config.Config) grant.Grant {
		t.Helper()
		plan, err := render.Build(cfg, secrets)
		if err != nil {
			t.Fatal(err)
		}
		g, err := grant.For(cfg, plan, "home-b")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	d := full.Deployment()
	review := func(cfg *config.Config, scope grant.Scope) *siteReview {
		t.Helper()
		s, err := reviewSite(d, "home-b", granted(cfg), scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// The first run's review, approved and recorded.
	if err := recordApproval(ui.Discard, review(full, grant.AllScope), d, "alice"); err != nil {
		t.Fatal(err)
	}
	// The run again: nothing is asked.
	again := review(full, grant.AllScope)
	if n := again.review.Pending(); n != 0 {
		t.Fatalf("the resumed run asks for %d entries", n)
	}
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{approved: map[string]grant.Review{"home-b": again.review}}

	joined := joinedAsData(full, "home-b")
	g := granted(joined)
	scope, err := siteAddScope(joined, "home-b", g, true)
	if err != nil {
		t.Fatal(err)
	}
	s, by, err := stepReview(ui.Discard, answerer{}, noTerminalStdin(), io.Discard, joined, "home-b", g, scope, nil, true, "paisans site add home-b --execute")
	if err != nil {
		t.Fatalf("site add: %s", problemText(err))
	}
	if err := recordGrant(ui.Discard, s, d, by); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stepReview(ui.Discard, answerer{}, noTerminalStdin(), io.Discard, full, "home-b", granted(full), grant.ApplyScope(), nil, true, "paisans apply --site home-b --execute"); err != nil {
		t.Errorf("apply --site home-b after the join: %s", problemText(err))
	}
}

// The join's configuration gives the joining site the data role alone and
// leaves the declaration as it was read.
func TestJoinedAsDataLeavesTheDeclaration(t *testing.T) {
	cfg := fixture(t)
	joined := joinedAsData(cfg, "home-b")
	if got := joined.Sites["home-b"].Roles; !slices.Equal(got, []config.Role{config.RoleData}) {
		t.Errorf("joined as %v", got)
	}
	if got := cfg.Sites["home-b"].Roles; !slices.Equal(got, []config.Role{config.RoleData, config.RoleApps}) {
		t.Errorf("the declaration changed to %v", got)
	}
	if pinned := cfg.PinnedTo("home-b"); len(pinned) == 0 {
		t.Fatal("the fixture pins no app to home-b")
	} else if left := joined.PinnedTo("home-b"); len(left) > 0 {
		t.Errorf("the join starts %v, pinned to home-b", left)
	} else if _, ok := cfg.Apps[pinned[0]]; !ok {
		t.Errorf("the declaration lost %s", pinned[0])
	}
	if got := joinedAsData(cfg, "vm"); got != cfg {
		t.Errorf("a site without [data, apps] was changed")
	}
}

// As a step of apply with no --site, site add checks its review against the
// run's approval rather than asking, and refuses an image the run did not
// approve.
func TestSiteAddChecksTheRunsApproval(t *testing.T) {
	path := joinConfig(t)
	log := loggingSSH(t, "")
	fakeGrants(t, func(string) *grant.Record { return nil })
	saved := reviewedByRun
	t.Cleanup(func() { reviewedByRun = saved })
	reviewedByRun = &runReview{approved: map[string]grant.Review{}}
	recordConverge(t)
	err := runSiteAdd([]string{"home-b", "--config", path, "--secrets", mustAbs(t, fixtureSecretsPath()), "--execute"}, noTerminalStdin(), io.Discard)
	if err == nil || !strings.Contains(problemText(err), "changed after it was approved") || checked(logged(t, log)) {
		t.Errorf("err %v, checked %v", err, checked(logged(t, log)))
	}
}

// A foundation image's change is listed in a group of its own, first, and
// asks nothing: the team reviews foundation.yaml in the core catalog's pull
// requests. What needs approval is the last group, headed by its count, so
// the count sits over exactly the entries it counts, just above the prompt.
func TestFoundationImagesAreListedApart(t *testing.T) {
	etcd := grant.Change{Op: grant.Changed, Subject: "image infra/etcd", To: "etcd:v3.6.*", Text: "image infra/etcd: etcd:v3.5.* → etcd:v3.6.*", Foundation: true}
	app := grant.Change{Op: grant.Changed, Subject: "image talk/app", To: "mbin:1.15", Text: "image talk/app: mbin:1.14 → mbin:1.15"}
	gone := grant.Change{Op: grant.Removed, Subject: "image blog/app", Text: "image blog/app: writefreely:0.15"}
	rec := &ui.Recorder{}
	showReview(rec, "review", grant.Review{Site: "home-a", Changes: []grant.Change{app, gone, etcd}}, true)
	var got []string
	for _, e := range rec.Events {
		got = append(got, e.Kind+": "+e.Text+" "+e.Extra)
	}
	want := []string{
		"step: review ",
		"pending: review 1 entry to approve",
		"explain: foundation images (required): ",
		"explain: ~ image infra/etcd: etcd:v3.5.* → etcd:v3.6.* ",
		"explain: removed, nothing to approve: ",
		"explain: - image blog/app: writefreely:0.15 ",
		"explain: 1 entry to approve: ",
		"explain: ~ image talk/app: mbin:1.14 → mbin:1.15 ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	rec = &ui.Recorder{}
	only := []grant.Review{{Site: "home-a", Changes: []grant.Change{etcd}}}
	showReview(rec, "review", only[0], true)
	if !rec.Has("done", "review") || rec.Has("pending", "review") {
		t.Errorf("only a foundation image is pending:\n%s", rec.Lines())
	}
	var out bytes.Buffer
	if err := approveReviews(ui.Discard, answerer{}, noTerminalStdin(), &out, only, "x"); err != nil || out.Len() != 0 {
		t.Errorf("only a foundation image asked: err %v, printed %q", err, out.String())
	}
}
