package hostadmit

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// testHost answers admit's probe from facts a test changes as it runs, and
// every other command with nothing.
type testHost struct {
	facts [][2]string
	ran   []string
	// onRun sees every command, so a test can make the host change as a
	// real one would.
	onRun func(command string)
}

const testOSRelease = "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n"

func newTestHost() *testHost {
	return &testHost{facts: [][2]string{{"passwd", "absent"}, {"engine", "absent"}}}
}

// set replaces every line key had with one line of value.
func (h *testHost) set(key, value string) {
	var kept [][2]string
	for _, kv := range h.facts {
		if kv[0] != key {
			kept = append(kept, kv)
		}
	}
	h.facts = append(kept, [2]string{key, value})
}

func (h *testHost) Describe() string { return "test" }

func (h *testHost) Run(command string) (string, error) { return h.run(command) }

func (h *testHost) RunInput(command, _ string) (string, error) { return h.run(command) }

func (h *testHost) run(command string) (string, error) {
	h.ran = append(h.ran, command)
	if h.onRun != nil {
		h.onRun(command)
	}
	if strings.Contains(command, "paisans host admit: read only") {
		var b strings.Builder
		for _, kv := range h.facts {
			fmt.Fprintf(&b, "%s %s\n", kv[0], kv[1])
		}
		return b.String() + "end\n", nil
	}
	return "", nil
}

func (h *testHost) ReadFile(path string) (string, bool, error) {
	if path == "/etc/os-release" {
		return testOSRelease, true, nil
	}
	return "", false, nil
}

func (h *testHost) WriteFile(string, string, uint32) error { return nil }

// ranMatching is every command run that contains s.
func (h *testHost) ranMatching(s string) []string {
	var out []string
	for _, c := range h.ran {
		// The probe names commands it reads with, useradd -D among them,
		// and is not a step.
		if strings.Contains(c, s) && !strings.Contains(c, "paisans host admit: read only") {
			out = append(out, c)
		}
	}
	return out
}

// testInput is the witness's request from the admission package's fixture,
// which needs no grant.
func testInput(t *testing.T) Input {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "admission", "testdata", "paisans-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	built, err := admission.Build(cfg, "vm")
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Parse(admission.Encode(built))
	if err != nil {
		t.Fatal(err)
	}
	return Input{Request: req, Digest: admission.Digest(admission.Encode(req)), Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

func discard() ui.Reporter { return ui.NewPlain(io.Discard, false) }

// withParts runs the test with ps as admit's parts.
func withParts(t *testing.T, ps []part) {
	saved := parts
	parts = ps
	t.Cleanup(func() { parts = saved })
}

// userPart creates the user when it is absent.
var userPart = part{"user", func(p *planner) error {
	if !p.f.User.Exists {
		p.step(Step{Title: "create user paisans-f2a9", Command: "useradd paisans-f2a9"})
	}
	return nil
}}

// A step that needs the user's UID is provisional until the user exists:
// Execute runs what comes before it, reads the host again, and runs the
// rest with the real UID.
func TestExecuteRebuildsOnceTheUserExists(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Steps[1].Provisional || !strings.Contains(plan.Text(), "nft uid <uid>") {
		t.Fatalf("the egress step is not provisional with <uid>:\n%s", plan.Text())
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if got := h.ranMatching("nft uid"); len(got) != 1 || got[0] != "nft uid 1001" {
		t.Errorf("the egress step ran as %q, want once with uid 1001", got)
	}
	if got := h.ranMatching("useradd"); len(got) != 1 {
		t.Errorf("useradd ran %d times", len(got))
	}
}

// The plan Execute rebuilds must be the approved one from the provisional
// step on, in everything but the values it could not know: a host that
// changed while admit ran is not acted on.
func TestExecuteStopsWhenTheRebuiltPlanDiffers(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("surprise", "yes")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			if p.f.raw["surprise"] != nil {
				p.step(Step{Title: "something new", Command: "rm -rf /"})
			}
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("rm -rf")) != 0 || len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran past the change: %q", h.ran)
	}
}

// A rebuilt step that is still provisional means the user the earlier
// steps were to create does not exist: Execute stops rather than run a
// command with <uid> in it.
func TestExecuteStopsWhenTheUserStillDoesNotExist(t *testing.T) {
	h := newTestHost()
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "still needs the user") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran a provisional step: %q", h.ran)
	}
}

// A failed step stops Execute, names the step, and quotes the host's output
// only as visible text, so an escape sequence in it cannot redraw the
// screen.
func TestExecuteStopsAtAFailureAndShowsItsOutputVisibly(t *testing.T) {
	failing := &failHost{testHost: newTestHost(), out: "boom\x1b[2J\nsecond line"}
	withParts(t, []part{{"two", func(p *planner) error {
		p.step(Step{Title: "first", Command: "fail here"})
		p.step(Step{Title: "second", Command: "never"})
		return nil
	}}})
	in := testInput(t)
	plan, err := Build(failing, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(failing, in, plan, discard())
	if err == nil || !strings.HasPrefix(err.Error(), "first: ") {
		t.Fatalf("Execute = %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), `boom\x1b[2J`) || !strings.Contains(err.Error(), "second line") {
		t.Errorf("the host's output is not shown visibly: %q", err.Error())
	}
	if slices.Contains(failing.ran, "never") {
		t.Error("Execute ran past a failed step")
	}
}

type failHost struct {
	*testHost
	out string
}

func (h *failHost) Run(command string) (string, error) {
	if command == "fail here" {
		h.ran = append(h.ran, command)
		return h.out, fmt.Errorf("exit status 1")
	}
	return h.testHost.Run(command)
}

// A step's stdin goes to the command, never onto its command line.
func TestExecuteSendsInputOnStdin(t *testing.T) {
	h := &stdinHost{testHost: newTestHost()}
	withParts(t, []part{{"keys", func(p *planner) error {
		p.step(Step{Title: "add keys", Command: "cat >> keys", Input: "ssh-ed25519 AAAA\n"})
		return nil
	}}})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if h.stdin["cat >> keys"] != "ssh-ed25519 AAAA\n" {
		t.Errorf("stdin %q", h.stdin)
	}
}

type stdinHost struct {
	*testHost
	stdin map[string]string
}

func (h *stdinHost) RunInput(command, stdin string) (string, error) {
	if h.stdin == nil {
		h.stdin = map[string]string{}
	}
	h.stdin[command] = stdin
	return h.testHost.RunInput(command, stdin)
}

// The rebuilt plan must be the approved one in every step's command, file
// and stdin once the placeholders are filled, not in its titles alone: a
// step whose command changed under the same title is not run.
func TestExecuteStopsWhenOnlyACommandChanged(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("surprise", "yes")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			command := "nft uid " + uidOf(p)
			if p.f.raw["surprise"] != nil {
				command += " && rm -rf /"
			}
			p.step(Step{Title: "load the egress table", Command: command, Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") || !strings.Contains(err.Error(), "command") {
		t.Fatalf("Execute = %v", err)
	}
	if len(h.ranMatching("nft uid")) != 0 {
		t.Errorf("Execute ran the changed step: %q", h.ran)
	}
}

// The subordinate range is a placeholder too: filled from the range the
// new user was given, a step that uses it is the approved step.
func TestExecuteFillsTheSubordinateRange(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("subuid", "paisans-f2a9:165536:65536")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			r, err := rangeOf(p)
			if err != nil {
				return err
			}
			p.step(Step{Title: "load the egress table", Command: "nft load", Input: "meta skuid { " + uidOf(p) + ", " + r + " }\n", Provisional: !p.f.User.Exists})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[1].Input != "meta skuid { <uid>, <subordinate range> }\n" {
		t.Fatalf("stdin %q", plan.Steps[1].Input)
	}
	if err := Execute(h, in, plan, discard()); err != nil {
		t.Fatal(err)
	}
	if got := h.ranMatching("nft load"); len(got) != 1 {
		t.Errorf("the egress step ran %d times", len(got))
	}
}

// Steps are compared one by one, after their counts: a title holding a
// newline is never read as two steps, and a rebuilt plan longer or shorter
// than the approved one is refused, not indexed past its end.
func TestSameStepsComparesCountsFirst(t *testing.T) {
	fill := strings.NewReplacer()
	one := []Step{{Title: "a\nb", Command: "true"}}
	two := []Step{{Title: "a", Command: "true"}, {Title: "b", Command: "rm -rf /"}}
	if _, err := sameSteps(one, two, fill); err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Errorf("one approved, two rebuilt: %v", err)
	}
	if _, err := sameSteps(two, one, fill); err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
		t.Errorf("two approved, one rebuilt: %v", err)
	}
	if _, err := sameSteps(two, two, fill); err != nil {
		t.Errorf("the same steps: %v", err)
	}
}

// A user that exists with no subordinate range, or with more than one, has
// no range a step can name: rangeOf refuses, naming /etc/subuid, and Build
// stops rather than plan a step with the placeholder in it.
func TestRangeOfRefusesAnExistingUserWithoutOneRange(t *testing.T) {
	for name, ranges := range map[string][]string{"none": nil, "two": {"paisans-f2a9:165536:65536", "paisans-f2a9:300000:65536"}} {
		t.Run(name, func(t *testing.T) {
			h := newTestHost()
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			for _, r := range ranges {
				h.facts = append(h.facts, [2]string{"subuid", r})
			}
			withParts(t, []part{{"egress", func(p *planner) error {
				r, err := rangeOf(p)
				if err != nil {
					return err
				}
				p.step(Step{Title: "load the egress table", Command: "nft " + r})
				return nil
			}}})
			_, err := Build(h, testInput(t))
			if err == nil || !strings.Contains(err.Error(), "/etc/subuid") {
				t.Fatalf("Build = %v", err)
			}
		})
	}
}

// A probed path below a link or another's directory stops admit before
// any part plans, naming the path and why, so no part can forget to.
func TestBuildRefusesAnUnsafePath(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"dir", "/srv/paisans/f2a9 link-above 0 0", "/srv/paisans/f2a9"},
		{"file", "/etc/sudoers.d/paisans-f2a9 unsafe-above", "/etc/sudoers.d/paisans-f2a9"},
		{"data-above", "unsafe-above", "where Docker's data would go"},
	} {
		h := newTestHost()
		h.facts = append(h.facts, [2]string{c.key, c.value})
		planned := false
		withParts(t, []part{{"any", func(p *planner) error { planned = true; return nil }}})
		_, err := Build(h, testInput(t))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%s %s: Build = %v", c.key, c.value, err)
		}
		if planned {
			t.Errorf("%s %s: a part planned on an unsafe host", c.key, c.value)
		}
	}
}

// Show marks a provisional step at every verbosity, so the owner can tell
// which steps admit plans again once the user exists.
func TestShowMarksAProvisionalStep(t *testing.T) {
	plan := &Plan{Steps: []Step{{Title: "make the user", Describe: "user"}, {Title: "write the egress table", Describe: "egress", Provisional: true}}}
	var b strings.Builder
	plan.Show(ui.NewPlain(&b, false))
	out := b.String()
	if strings.Count(out, "runs once the user exists") != 1 {
		t.Errorf("the provisional step is not marked once:\n%s", out)
	}
	if i, j := strings.Index(out, "write the egress table"), strings.Index(out, "runs once the user exists"); j < i {
		t.Errorf("the mark is not under the provisional step:\n%s", out)
	}
}

// A refusal while Execute reads the host again comes after the steps
// before it ran, the user among them, so it never says nothing was
// changed: it says which steps ran and that nothing more was done.
func TestARefusalOnTheRebuildSaysWhatRan(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			if p.f.User.Exists {
				return fmt.Errorf("paisans-f2a9 has no range in /etc/subuid. Nothing was changed")
			}
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: true})
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(h, in, plan, discard())
	if err == nil {
		t.Fatal("Execute went on past the refusal")
	}
	hint, explain := ui.Describe(err)
	text := hint + "\n" + explain
	if strings.Contains(text, "Nothing was changed") || !strings.Contains(text, "has no range in /etc/subuid") ||
		!strings.Contains(text, `"create user paisans-f2a9"`) || !strings.Contains(text, "Nothing more was done") {
		t.Errorf("the refusal reads:\n%s", text)
	}
	if len(hint) > 80 {
		t.Errorf("the hint is longer than 80 characters: %q", hint)
	}
}

// A step the rebuilt plan no longer has is done, not drift: installing
// wireguard-tools creates /etc/wireguard, root's and 700, so the plan
// rebuilt once the user exists has no "create /etc/wireguard". What is
// left must still be the approved steps, in order, each as approved.
func TestSameStepsAllowsAStepNowDone(t *testing.T) {
	fill := strings.NewReplacer("<uid>", "1001")
	wg := Step{Title: "create /etc/wireguard", Command: "install -d -m 700 -o root -g root /etc/wireguard"}
	helper := Step{Title: "install the WireGuard helper", Command: "true"}
	egress := Step{Title: "load the egress table", Command: "nft uid <uid>", Provisional: true}
	filled := Step{Title: "load the egress table", Command: "nft uid 1001"}
	approved := []Step{egress, wg, helper}
	if done, err := sameSteps(approved, []Step{filled, helper}, fill); err != nil || len(done) != 1 || done[0].Title != wg.Title {
		t.Errorf("a step now done: %v, done %v", err, done)
	}
	if done, err := sameSteps(approved, []Step{filled}, fill); err != nil || len(done) != 2 {
		t.Errorf("two steps now done: %v, done %v", err, done)
	}
	for name, rebuilt := range map[string][]Step{
		"added":     {filled, wg, {Title: "something new", Command: "rm -rf /"}, helper},
		"reordered": {filled, helper, wg},
		"changed":   {filled, {Title: "install the WireGuard helper", Command: "rm -rf /"}},
		"retitled":  {filled, {Title: "install another helper", Command: "true"}},
	} {
		if _, err := sameSteps(approved, rebuilt, fill); err == nil || !strings.Contains(err.Error(), "the host changed while admit ran") {
			t.Errorf("%s: sameSteps = %v", name, err)
		}
	}
}

// A step the rebuilt plan no longer has is not run, and Execute says so,
// naming it, so the owner sees every approved step either run or found
// done.
func TestExecuteNamesAStepFoundDone(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("wg", "made")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table", Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			if p.f.raw["wg"] == nil {
				p.step(Step{Title: "create /etc/wireguard", Command: "install -d /etc/wireguard"})
			}
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := Execute(h, in, plan, ui.NewPlain(&b, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "create /etc/wireguard") || !strings.Contains(b.String(), "done already") {
		t.Errorf("Execute does not name the step found done:\n%s", b.String())
	}
	if len(h.ranMatching("install -d /etc/wireguard")) != 0 {
		t.Error("the step found done ran")
	}
}

// When admit's own enable of ufw ran before the first provisional step,
// the plan Execute rebuilds there is told so, since ufw is active by then
// and the record must still say admit enabled it. Before the enable, it
// is not.
func TestExecuteTellsTheRebuildItEnabledUFW(t *testing.T) {
	for _, enableFirst := range []bool{true, false} {
		h := newTestHost()
		h.onRun = func(command string) {
			if strings.HasPrefix(command, "useradd ") {
				h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
				h.set("group", "paisans-f2a9")
			}
		}
		var told []bool
		enable := part{"firewall", func(p *planner) error {
			told = append(told, p.in.enabledUFW)
			if !p.f.User.Exists {
				p.step(Step{Title: "enable ufw", Command: enableUFW})
			}
			return nil
		}}
		keys := part{"keys", func(p *planner) error {
			p.step(Step{Title: "keys", Command: "keys " + uidOf(p), Provisional: !p.f.User.Exists})
			return nil
		}}
		if enableFirst {
			withParts(t, []part{userPart, enable, keys})
		} else {
			withParts(t, []part{userPart, keys, enable})
		}
		in := testInput(t)
		plan, err := Build(h, in)
		if err != nil {
			t.Fatal(err)
		}
		_ = Execute(h, in, plan, discard())
		if len(told) != 2 || told[0] || told[1] != enableFirst {
			t.Errorf("enable first %v: the builds were told %v", enableFirst, told)
		}
	}
}

// listing is a reporter that draws lists, recording each row a run starts,
// by its index in the plan, its title and how it ended.
type listing struct {
	ui.Recorder
	rows    []ui.ListRow
	started []string
}

func (l *listing) List(rows []ui.ListRow, _ []ui.ListWarning) ui.List {
	l.rows = rows
	return l
}

func (l *listing) Start(i int, title string) ui.Step {
	l.started = append(l.started, fmt.Sprintf("%d %s", i, title))
	return &listingStep{l: l, at: len(l.started) - 1}
}

func (l *listing) Close() {}

type listingStep struct {
	ui.Step
	l  *listing
	at int
}

func (s *listingStep) Done(result string) { s.End(ui.OK, result) }
func (s *listingStep) Fail(error)         { s.End(ui.Failed, "") }
func (s *listingStep) End(m ui.Mark, result string) {
	s.l.started[s.at] += fmt.Sprintf(" -> %d %s", m, result)
}
func (s *listingStep) Detail(string, ...any) {}
func (s *listingStep) Progress(string)       {}

// A plan shown to run is run in its own rows: each step in the row the
// owner approved it in, under its title with the placeholders filled once
// the user exists, and a step found done ended in its row as done already.
func TestExecuteRunsInThePlansRows(t *testing.T) {
	h := newTestHost()
	h.onRun = func(command string) {
		if strings.HasPrefix(command, "useradd ") {
			h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
			h.set("group", "paisans-f2a9")
			h.set("wg", "made")
		}
	}
	withParts(t, []part{
		userPart,
		{"egress", func(p *planner) error {
			p.step(Step{Title: "load the egress table for " + uidOf(p), Command: "nft uid " + uidOf(p), Provisional: !p.f.User.Exists})
			if p.f.raw["wg"] == nil {
				p.step(Step{Title: "create /etc/wireguard", Command: "install -d /etc/wireguard"})
			}
			return nil
		}},
	})
	in := testInput(t)
	plan, err := Build(h, in)
	if err != nil {
		t.Fatal(err)
	}
	r := &listing{}
	plan.ShowToRun(r)
	if len(r.rows) != len(plan.Steps) || r.rows[1].Title != "load the egress table for <uid>" || r.rows[1].Result != "runs once the user exists" {
		t.Fatalf("rows: %+v", r.rows)
	}
	if err := Execute(h, in, plan, r); err != nil {
		t.Fatal(err)
	}
	last := len(plan.Steps) - 1
	want := []string{
		fmt.Sprintf("%d create /etc/wireguard -> 0 done already", last),
		fmt.Sprintf("%d load the egress table for 1001 -> 0 ", last-1),
	}
	if got := r.started[len(r.started)-2:]; !slices.Equal(got, want) {
		t.Errorf("rows run:\n%s\nwant the last two:\n%s", strings.Join(r.started, "\n"), strings.Join(want, "\n"))
	}
	if len(r.started) != len(plan.Steps) {
		t.Errorf("%d rows started for %d steps:\n%s", len(r.started), len(plan.Steps), strings.Join(r.started, "\n"))
	}
}

// Off a terminal a plan shown to run and its run are two lists, each
// labelled, and nothing else about either changes.
func TestAPlanShownToRunIsLabelledOffATerminal(t *testing.T) {
	plan := &Plan{Steps: []Step{{Title: "make the user", Describe: "user"}, {Title: "write the egress table", Describe: "egress"}},
		Warnings: []hostprep.Warning{{Hint: "ufw is off"}}}
	var b strings.Builder
	r := ui.NewPlain(&b, false)
	plan.ShowToRun(r)
	if err := RunSteps(newTestHost(), plan, r); err != nil {
		t.Fatal(err)
	}
	want := "  plan, nothing done yet:\n  todo make the user\n  todo write the egress table\n  WARN ufw is off\n" +
		"  running:\n  ok   make the user\n  ok   write the egress table\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
	b.Reset()
	(&Plan{Steps: plan.Steps, Warnings: plan.Warnings}).Show(r)
	if strings.Contains(b.String(), ui.PlanLabel) {
		t.Errorf("a plan not run is labelled:\n%s", b.String())
	}
}
