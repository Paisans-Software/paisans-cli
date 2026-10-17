package hostadmit_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/version"
)

var update = flag.Bool("update", false, "rewrite the golden plans")

// goldenCase is one host, one site's request and the owner's flags, with
// the plan admit draws for them in testdata/plans/<name>.txt, or the
// refusal in its place when admit refuses.
type goldenCase struct {
	name  string
	host  func() *fakeHost
	site  string
	flags hostadmit.Flags
	// focus are substrings of the titles of the steps whose bodies the
	// golden shows, the steps the case is about. A case in fullPlans has
	// none, and neither does a refusal.
	focus []string
}

// fullPlans are the goldens that show every step's body, each with why it
// is one. Every other plan's golden is trimmed to its case's focus (see
// trimmed), so a change to one step's body churns these and the cases
// that focus on that step, and no others.
var fullPlans = map[string]string{
	"model-blank-witness":   "a blank Ubuntu 24.04 host, admitted as a witness: the whole install, with the WireGuard helper's script",
	"helper-dialled":        "a gateway (home-a, public ports and the watchdog): its allow rules, sysctls and dialled helper",
	"helper-no-listen-port": "a storage site that listens on no WireGuard port, so its helper and claim differ from the rest",
	"docker-blank-jammy":    "a blank Ubuntu 22.04 host: the jammy apt repository and packages",
	"firewall-enable-ufw":   "an inactive ufw that --enable-ufw turns on, with the fallback armed before it",
	"datadir-given":         "a data directory and the watchdog on a site whose user is community, not paisans-<token>",
}

// allCases is every golden case of every task's file, for the checks that
// hold of every plan admit draws (TestNoRootStepTouchesTheUsersPaths).
var allCases []goldenCase

func addCases(cs ...goldenCase) []goldenCase {
	allCases = append(allCases, cs...)
	return cs
}

// golden is the plan's text, or "refused: <error>" for a refusal: either
// is pinned, so a refusal's wording is reviewed like a plan's.
func golden(t *testing.T, c goldenCase) (string, *hostadmit.Plan) {
	t.Helper()
	plan, err := hostadmit.Build(c.host(), input(t, c.site, c.flags))
	if err != nil {
		return "refused: " + err.Error() + "\n", nil
	}
	return withHelperDigest(c.name, plan).Text(), plan
}

// helperInFull are the goldens that show the WireGuard helper's script
// in full. hostprep's own goldens pin it, so every other plan shows its
// digest in its place, and a change to the script churns these alone.
var helperInFull = map[string]bool{"model-blank-witness": true, "helper-dialled": true, "helper-no-listen-port": true}

// withHelperDigest is plan, with the helper's content as its digest unless
// name is in helperInFull.
func withHelperDigest(name string, plan *hostadmit.Plan) *hostadmit.Plan {
	if helperInFull[name] {
		return plan
	}
	short := *plan
	short.Steps = slices.Clone(plan.Steps)
	for i, s := range short.Steps {
		if s.File != nil && strings.HasPrefix(s.File.Path, apply.HelperDir+"/") {
			f := *s.File
			f.Content = "<the WireGuard helper hostprep renders, sha256 " + sum(f.Content) + ">\n"
			short.Steps[i].File = &f
		}
	}
	return &short
}

func runGolden(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkGoldenText(t, c.name, goldenText(t, c))
		})
	}
}

// goldenText is what testdata/plans/<name>.txt holds for c: a refusal or
// a plan in fullPlans in full, any other plan trimmed to c's focus.
func goldenText(t *testing.T, c goldenCase) string {
	t.Helper()
	got, plan := golden(t, c)
	switch {
	case plan == nil:
		if len(c.focus) > 0 {
			t.Errorf("%s is refused, so its focus %q has no step to match: remove it", c.name, c.focus)
		}
		return got
	case fullPlans[c.name] != "":
		if len(c.focus) > 0 {
			t.Errorf("%s is in fullPlans, which shows every step, so its focus %q does nothing: remove it", c.name, c.focus)
		}
		return got
	}
	return trimmed(t, c, got, plan)
}

// trimmed is text, plan's Text, with the header, every step's title line,
// the whole of each step whose title holds one of c's focus substrings,
// and every present, foreign and warning line. The titles pin which steps
// run and in what order at a line each; a focused step's body pins what
// the case is about. A focus that matches no title fails the test, so a
// renamed step does not leave a case pinning nothing.
func trimmed(t *testing.T, c goldenCase, text string, plan *hostadmit.Plan) string {
	t.Helper()
	focused := func(title string) bool {
		return slices.ContainsFunc(c.focus, func(f string) bool { return strings.Contains(title, f) })
	}
	for _, f := range c.focus {
		if !slices.ContainsFunc(plan.Steps, func(s hostadmit.Step) bool { return strings.Contains(s.Title, f) }) {
			var titles []string
			for _, s := range plan.Steps {
				titles = append(titles, "  "+s.Title)
			}
			t.Errorf("%s: focus %q matches no step's title. Its steps are:\n%s", c.name, f, strings.Join(titles, "\n"))
		}
	}
	// Text separates its header, each step and each present, foreign or
	// warning line with a blank line, and indents every line within one,
	// so a blank line is always a boundary.
	blocks := strings.Split(strings.TrimSuffix(text, "\n"), "\n\n")
	var b strings.Builder
	b.WriteString(blocks[0])
	titleOnly := false
	for _, block := range blocks[1:] {
		first, _, _ := strings.Cut(block, "\n")
		if rest, ok := strings.CutPrefix(first, "step "); ok {
			if _, title, _ := strings.Cut(rest, ": "); !focused(title) {
				if titleOnly {
					b.WriteString("\n")
				} else {
					b.WriteString("\n\n")
				}
				b.WriteString(first)
				titleOnly = true
				continue
			}
		} else if !strings.HasPrefix(first, "present: ") && !strings.HasPrefix(first, "foreign: ") && !strings.HasPrefix(first, "warning: ") {
			t.Fatalf("%s: a block of the plan's text starts %q, which is no step, present, foreign or warning line", c.name, first)
		}
		b.WriteString("\n\n")
		b.WriteString(block)
		titleOnly = false
	}
	b.WriteString("\n")
	return b.String()
}

// Trimming a plan to every one of its steps gives its text back, so what
// a trimmed golden holds of a focused step is all of it.
func TestTrimmedToEveryStepIsTheWholePlan(t *testing.T) {
	c := goldenCase{name: "model-blank-witness", host: newBlank, site: "vm"}
	text, plan := golden(t, c)
	for _, s := range plan.Steps {
		c.focus = append(c.focus, s.Title)
	}
	if got := trimmed(t, c, text, plan); got != text {
		t.Errorf("trimmed to every step:\n%s", diff(text, got))
	}
}

// Every name in fullPlans is a case's, and every plan a case draws with
// steps is either in fullPlans or focused on some of them: a new case
// chooses which steps it is about.
func TestEveryPlanIsFullOrFocused(t *testing.T) {
	names := map[string]bool{}
	for _, c := range allCases {
		names[c.name] = true
		if fullPlans[c.name] != "" || len(c.focus) > 0 {
			continue
		}
		if _, plan := golden(t, c); plan != nil && len(plan.Steps) > 0 {
			t.Errorf("%s plans steps but has no focus: name the steps it is about, or add it to fullPlans", c.name)
		}
	}
	for name := range fullPlans {
		if !names[name] {
			t.Errorf("fullPlans names %s, which is no case", name)
		}
	}
	for name := range helperInFull {
		if fullPlans[name] == "" {
			t.Errorf("helperInFull names %s, which fullPlans does not", name)
		}
	}
}

// checkGoldenText compares got with testdata/plans/<name>.txt, or writes
// it there under -update: runGolden's comparison, for a text a test built
// by hand.
func checkGoldenText(t *testing.T, name, got string) {
	t.Helper()
	checkGoldenAt(t, filepath.Join("testdata", "plans", name+".txt"), got)
}

// checkGoldenAt is checkGoldenText for a golden file at path.
func checkGoldenAt(t *testing.T, path, got string) {
	t.Helper()
	// The claim and the record name this binary's version, which every
	// release bump changes; the golden plans read the same across one.
	got = strings.ReplaceAll(got, `"toolkit_version":"`+version.Version+`"`, `"toolkit_version":"<release>"`)
	got = strings.ReplaceAll(got, `"toolkit_version": "`+version.Version+`"`, `"toolkit_version": "<release>"`)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/hostadmit/ -update, then read the file)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from what admit plans now:\n%s", path, diff(string(want), got))
	}
}

// diff is the first line where want and got part, with its neighbours.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\n- %s\n+ %s", i+1, a, b)
		}
	}
	return "(equal)"
}

// userPaths are the paths the deployment's user owns or can replace: its
// home and its directory under /srv/paisans.
var userPaths = []string{"/home/paisans-f2a9", "/home/community", "/srv/paisans/f2a9/"}

// asUserPrefix starts every command admit runs as the deployment's user.
const asUserPrefix = "runuser -u "

// checkUserPaths names each step of plan that reaches a path the
// deployment's user controls as root: a file written by the transport,
// which is root, or a command that names the path and is not run as the
// user. /srv/paisans/<token> itself, which root creates and hands over, is
// not in userPaths; everything below it is.
func checkUserPaths(plan *hostadmit.Plan) []string {
	var bad []string
	for _, s := range plan.Steps {
		for _, p := range userPaths {
			if s.File != nil && strings.HasPrefix(s.File.Path, p) {
				bad = append(bad, s.Title+": writes "+s.File.Path+" as root")
			}
			if strings.Contains(s.Command, p) && !strings.HasPrefix(s.Command, asUserPrefix) {
				bad = append(bad, s.Title+": runs as root on "+p)
			}
		}
	}
	return bad
}

// Every plan of every case reaches the user's paths only as the user, so
// a link the user planted cannot redirect a root write or read.
func TestNoRootStepTouchesTheUsersPaths(t *testing.T) {
	bad := &hostadmit.Plan{Steps: []hostadmit.Step{
		{Title: "write keys", Command: "cat >> /home/paisans-f2a9/.ssh/authorized_keys"},
		{Title: "write daemon.json", File: &hostprep.File{Path: "/home/paisans-f2a9/.config/docker/daemon.json"}},
	}}
	if got := checkUserPaths(bad); len(got) != 2 {
		t.Fatalf("the check itself misses a root write: %q", got)
	}
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, b := range checkUserPaths(plan) {
			t.Errorf("%s: %s", c.name, b)
		}
	}
}

// modelCases are the plan model's own: a blank host, before any part plans
// a step, so the header that names the site, its deployment and its user is
// pinned. Each later task's parts add their steps to it.
var modelCases = addCases(goldenCase{name: "model-blank-witness", host: newBlank, site: "vm"})

func TestModelPlans(t *testing.T) { runGolden(t, modelCases) }

// The plan names the site its request is for, so an owner holding requests
// for two sites of one deployment sees which one is being admitted.
func TestPlanNamesTheSite(t *testing.T) {
	plan, err := hostadmit.Build(newBlank(), input(t, "home-b", hostadmit.Flags{Watchdog: true}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Site != "home-b" || !strings.Contains(strings.SplitN(plan.Text(), "\n", 2)[0], "site home-b") {
		t.Errorf("site %q, header %q", plan.Site, strings.SplitN(plan.Text(), "\n", 2)[0])
	}
}
