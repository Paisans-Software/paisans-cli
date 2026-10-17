package main

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// allowYes sets PAISANS_ALLOW_YES=1 for the test, as a script that means
// --yes does.
func allowYes(t *testing.T) {
	t.Helper()
	t.Setenv(allowYesVariable, "1")
}

// yes is the answerer --yes makes.
var yes = answerer{yes: true}

// Every typed word a command asks for is answered by --yes, with no
// terminal, and each answer is printed on a line of its own after what it
// agrees to.
func TestYesAnswersEveryTypedWord(t *testing.T) {
	withTerminal(t, false)
	for _, word := range []string{approveWord, "create", rollbackWord, partialWord, "prune", "sign out", "home-b", "paisans-f2a9", "talk", "revoked", "lose writes", "lose objects"} {
		var out bytes.Buffer
		if err := confirmWord(yes, strings.NewReader(""), &out, word, "This does something to "+word+"."); err != nil {
			t.Errorf("%s: %v", word, err)
		}
		if !strings.Contains(out.String(), "This does something to "+word+".\napproved by --yes: "+word+"\n") {
			t.Errorf("%s: printed %q", word, out.String())
		}
	}
}

// Without --yes a typed word is still asked at a terminal and refused off
// one, and nothing says it was approved.
func TestWithoutYesTheWordIsStillTyped(t *testing.T) {
	var out bytes.Buffer
	if err := confirmWord(answerer{}, noTerminalStdin(), &out, "prune", "x"); err == nil || !strings.Contains(err.Error(), "not one") {
		t.Errorf("off a terminal: %v", err)
	}
	out.Reset()
	if err := confirmWord(answerer{}, answering(t, "prune\n"), &out, "prune", "x"); err != nil {
		t.Errorf("typed: %v", err)
	}
	if strings.Contains(out.String(), "approved by --yes") {
		t.Errorf("printed an approval by --yes: %q", out.String())
	}
}

// The words that go through helpers of their own: the partial gather, the
// app's name, the site's name, and each question of a removal with --host-gone.
func TestYesAnswersTheWordsBehindTheirHelpers(t *testing.T) {
	withTerminal(t, false)
	var out bytes.Buffer
	if err := confirmPartial(yes, nil, &out, configstore.Gathered{}, 3); err != nil || !strings.Contains(out.String(), "approved by --yes: "+partialWord+"\n") {
		t.Errorf("partial: %v %q", err, out.String())
	}
	out.Reset()
	if err := confirmName(yes, nil, &out, "talk"); err != nil || !strings.Contains(out.String(), "approved by --yes: talk\n") {
		t.Errorf("app remove: %v %q", err, out.String())
	}
	out.Reset()
	if err := confirmSite(yes, nil, &out, "home-b"); err != nil || !strings.Contains(out.String(), "approved by --yes: home-b\n") {
		t.Errorf("site remove: %v %q", err, out.String())
	}
	out.Reset()
	questions := []question{{word: "revoked", what: "a"}, {word: "home-b", what: "b"}, {word: "lose writes", what: "c", danger: true}, {word: "lose objects", what: "d", danger: true}}
	if err := confirmCutoff(yes, nil, &out, questions); err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if !strings.Contains(out.String(), "approved by --yes: "+q.word+"\n") {
			t.Errorf("--host-gone: %s not answered: %q", q.word, out.String())
		}
	}
}

// Apply's review, under --yes, prints one line per entry it approves,
// naming the key or image, and asks nothing.
func TestYesApprovesTheReviewEntryByEntry(t *testing.T) {
	withTerminal(t, false)
	reviews := []grant.Review{
		{Site: "home-a", Changes: []grant.Change{
			{Op: grant.Added, Subject: "key SHA256:b", To: "x", Text: "key SHA256:b (bob) may log in as ubuntu"},
			{Op: grant.Removed, Subject: "key SHA256:c", Text: "key SHA256:c (carol)"},
		}},
		{Site: "home-b", Changes: []grant.Change{
			{Op: grant.Added, Subject: "image talk/app", To: "y", Text: "image talk/app ghcr.io/example/talk:1"},
		}},
	}
	var out bytes.Buffer
	if err := approveReviews(ui.Discard, yes, nil, &out, reviews, "x"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "approved by --yes: home-a: ") || !strings.Contains(lines[0], "key SHA256:b (bob)") ||
		!strings.HasPrefix(lines[1], "approved by --yes: home-b: ") || !strings.Contains(lines[1], "image talk/app") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// --yes refuses unless PAISANS_ALLOW_YES=1, before anything is read, and
// the refusal names the variable and says why.
func TestYesNeedsTheEnvironmentVariable(t *testing.T) {
	path, _ := requestFile(t, "vm")
	for _, value := range []string{"", "0", "yes", "true"} {
		t.Setenv(allowYesVariable, value)
		for name, run := range map[string]func() error{
			"site admit": func() error {
				return runSiteAdmit([]string{"--request", path, "--public-ports", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"site evict": func() error {
				return runSiteEvict([]string{"--deployment", fixtureID(t), "--user", "paisans-f2a9", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"secrets prune": func() error {
				return runSecretsPrune([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"secrets rotate": func() error {
				return runSecretsRotate([]string{"--all", "--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"app remove": func() error {
				return runAppRemove([]string{"talk", "--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"site remove": func() error {
				return runSiteRemove([]string{"home-b", "--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"site add": func() error {
				return runSiteAdd([]string{"home-b", "--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"apply": func() error {
				return applyCommand([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"site prepare": func() error { return runSitePrepare([]string{"--config", "/nonexistent/x.yaml", "--yes"}) },
			"gateway move": func() error { return gatewayMoveCommand([]string{"--config", "/nonexistent/x.yaml", "--yes"}) },
			"config publish": func() error {
				return runConfigPublish([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"config pull": func() error {
				return runConfigPull([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"config recover": func() error {
				return runConfigRecover([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
			"operator register": func() error {
				return runOperatorRegister([]string{"--config", "/nonexistent/x.yaml", "--yes"}, strings.NewReader(""), io.Discard)
			},
		} {
			noHost(t)
			err := run()
			text := ""
			if err != nil {
				text = err.Error() + " " + problemText(err)
			}
			if !strings.Contains(text, allowYesVariable+"=1") || !strings.Contains(text, "copied") || !strings.Contains(text, "Nothing was read or changed") {
				t.Errorf("%s with %s=%q: %v", name, allowYesVariable, value, text)
			}
		}
	}
}

// secrets prune --without-record, answered by --yes with no terminal.
func TestYesAnswersPrune(t *testing.T) {
	allowYes(t)
	configPath, secretsPath := writeFixtureSecrets(t, func(s *config.Secrets) { s.Sites["monitor-a"] = config.SiteSecrets{WireGuardPrivateKey: "x"} })
	withRecords(t, map[string]string{}, "vm")
	var err error
	out := &bytes.Buffer{}
	captureStdout(t, func() {
		err = runSecretsPrune([]string{"--config", configPath, "--secrets", secretsPath, "--without-record", "--execute", "--yes"}, strings.NewReader(""), out)
	})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := config.LoadSecrets(secretsPath); s.Sites["monitor-a"].WireGuardPrivateKey != "" {
		t.Error("the secret was kept")
	}
	if !strings.Contains(out.String(), "approved by --yes: prune\n") {
		t.Errorf("printed:\n%s", out.String())
	}
}

// Apply without --site hands --yes on to its apply and gateway move
// steps, and to no other.
func TestConvergeHandsOnYes(t *testing.T) {
	o := convergeOptions{Config: "c.yaml", answers: yes}
	for _, args := range [][]string{{"apply", "--site", "home-a"}, {"gateway", "move"}} {
		if got := convergeFlags(args, o); !slices.Contains(got, "--yes") {
			t.Errorf("%q: %q", args, got)
		}
	}
	for _, args := range [][]string{{"init"}, {"site", "prepare", "--site", "home-a"}, {"site", "add", "home-b"}, {"storage", "init", "--site", "home-a"}} {
		if got := convergeFlags(args, o); slices.Contains(got, "--yes") {
			t.Errorf("%q: %q", args, got)
		}
	}
	if got := convergeFlags([]string{"apply", "--site", "home-a"}, convergeOptions{Config: "c.yaml"}); slices.Contains(got, "--yes") {
		t.Errorf("without --yes: %q", got)
	}
	if got := handOffArgs("home-a", handOffFlags{config: "c.yaml", yes: true}, true); !slices.Contains(got, "--yes") {
		t.Errorf("hand-off: %q", got)
	}
}

// Each refusal of a confirmation for want of a terminal says --yes with
// PAISANS_ALLOW_YES=1 answers it.
func TestNoTerminalRefusalsNameYes(t *testing.T) {
	withTerminal(t, false)
	const want = "--yes and " + allowYesVariable + "=1"
	var b bytes.Buffer
	for name, err := range map[string]error{
		"typed word":  confirmWord(answerer{}, noTerminalStdin(), &b, "prune", "x"),
		"review":      approveReviews(ui.Discard, answerer{}, noTerminalStdin(), &b, pendingReview(), "x"),
		"app name":    confirmName(answerer{}, noTerminalStdin(), &b, "talk"),
		"cutoff":      confirmCutoff(answerer{}, noTerminalStdin(), &b, []question{{word: "home-b", what: "x"}}),
		"site remove": noTerminal("x"),
	} {
		if err == nil || !strings.Contains(err.Error()+" "+problemText(err), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
