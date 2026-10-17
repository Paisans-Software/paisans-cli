package create

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/paisans-software/paisans-cli/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

const (
	aliceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	bobKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB bob@example.org"
)

// script joins answers one per line; "" is Enter.
func script(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

var (
	communityAnswers = []string{"Example Community", "example.org", ""}
	adminAnswers     = []string{"alice", "Alice Example", "alice@example.org", aliceKey, "", "bob", "Bob Example", "bob@example.org", bobKey, "", "n"}
	// management: the account service, its hostname, signup by invite, and
	// the code of conduct's file.
	managementAnswers = []string{"", "", "", "testdata/code-of-conduct.md"}
	mailAnswers       = []string{"smtp.example.org", "", "", "community@example.org", "", ""}
)

// every capability, its default hostnames, and members only where offered.
func appAnswers(chat bool) []string {
	out := append([]string{""}, managementAnswers...) // sign in hostname, then who manages accounts
	out = append(out,
		"", "", "", "", // forums: yes, hostname, display name, members only
		"", "", "", "", // wiki
		"", "", "", "", // blog
	)
	if chat {
		out = append(out, "y", "", "", "", "") // chat: yes, server, its display name, web client, members only
	}
	return append(out,
		"", "", "", // health page: yes, hostname, display name
		"", "", "203.0.113.50", "", "", // its machine: where (its own), name, address, ssh host, user
	)
}

var shapeAnswers = map[Shape][]string{
	ShapeOneMachine: {"1",
		"", "203.0.113.10", "", ""},
	ShapeHomeAndVPS: {"2",
		"", "home.local", "",
		"", "203.0.113.10", "", ""},
	ShapeHighAvailability: {"3",
		"", "home-a.example.org:51820", "home-a.local", "",
		"", "home-b.local", "",
		"", "203.0.113.10", "", ""},
}

var goldenNames = map[Shape]string{
	ShapeOneMachine:       "one-machine.yaml",
	ShapeHomeAndVPS:       "home-and-vps.yaml",
	ShapeHighAvailability: "high-availability.yaml",
}

func run(t *testing.T, input string) (*Wizard, string, error) {
	t.Helper()
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(input), &out))
	err := w.Ask(SectionAll)
	return w, out.String(), err
}

func TestWizardWritesEachNamedShape(t *testing.T) {
	for sh, answers := range shapeAnswers {
		t.Run(goldenNames[sh], func(t *testing.T) {
			var lines []string
			lines = append(lines, communityAnswers...)
			lines = append(lines, adminAnswers...)
			lines = append(lines, answers...)
			lines = append(lines, appAnswers(sh != ShapeOneMachine)...)
			lines = append(lines, mailAnswers...)
			w, out, err := run(t, script(lines...))
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			data, err := Render(w.Answers, testID, testOrigin)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range check(t, data).Refusals() {
				t.Errorf("refused %s: %s: %s", f.Rule, f.Key, f.Message)
			}
			golden := filepath.Join("testdata", goldenNames[sh])
			if *update {
				if err := os.WriteFile(golden, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if string(want) != string(data) {
				t.Errorf("%s differs from what the answers render; run with -update and read the diff", golden)
			}
		})
	}
}

func TestOneMachineDoesNotOfferChat(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, appAnswers(false)...)
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Chat needs a second machine") || strings.Contains(out, "Chat (Matrix") {
		t.Errorf("chat was offered on one machine:\n%s", out)
	}
	if _, ok := w.Answers.App("chat"); ok {
		t.Error("chat was declared")
	}
}

// A named shape's machine is introduced by what it does, so the name prompt
// does not read as a question about its roles.
func TestNamedShapeSaysWhatEachMachineDoes(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHighAvailability]...)
	lines = append(lines, appAnswers(true)...)
	lines = append(lines, mailAnswers...)
	_, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"Machine 1 of 3. It:",
		"Machine 3 of 3. It:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, s := range shapes[ShapeHighAvailability].sites {
		for _, r := range s.roles {
			if !strings.Contains(out, "  - "+roleWhat[r]+"\n") {
				t.Errorf("role %s is not explained:\n%s", r, out)
			}
		}
	}
}

// The members-only question names the app and says what each answer means
// for someone who is not a member.
func TestMembersOnlySaysWhatEachAnswerMeans(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, appAnswers(false)...)
	lines = append(lines, mailAnswers...)
	_, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"Who can see the forums and social?",
		"Who can see the wiki?",
		"  - yes: members only. Anyone else is sent to sign in first; other communities still federate with it.",
		"  - no: public. Anyone, including search engines and bots, can read what the app shows to visitors who are not signed in.",
		"Members only? [Y/n]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// Declining the health page leaves no health page and no machine for it.
func TestDecliningTheHealthPageDropsIt(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, "")
	lines = append(lines, managementAnswers...)
	lines = append(lines, "n", "n", "n", "n")
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, ok := w.Answers.App(healthApp); ok || w.Answers.Monitor != nil || w.Answers.MonitorOnGateway {
		t.Errorf("the health page or its machine is still declared: %+v", w.Answers)
	}
}

// Choosing the gateway puts the monitor beside it, offered by the gateway
// site's name with what it costs, and answering the machines again with the
// gateway renamed keeps it there.
func TestTheHealthPageOnTheGateway(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHomeAndVPS]...)
	lines = append(lines, "") // sign in hostname
	lines = append(lines, managementAnswers...)
	lines = append(lines,
		"n", "n", "n", // no forums, wiki or blog
		"n",        // no chat
		"", "", "", // health page: yes, hostname, display name
		"2", // on the gateway
	)
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "2) on gateway (takes the monitor down with it, no alerts when the gateway fails)"; !strings.Contains(out, want) {
		t.Errorf("missing %q:\n%s", want, out)
	}
	if !w.Answers.MonitorOnGateway || w.Answers.Monitor != nil {
		t.Fatalf("not on the gateway: %+v", w.Answers)
	}
	w.Answers.Sites[1].Name = "edge"
	sites := w.Answers.AllSites()
	if len(sites) != 2 || !sites[1].Has(config.RoleMonitor) || sites[0].Has(config.RoleMonitor) {
		t.Fatalf("the monitor is not on the renamed gateway: %+v", sites)
	}
	if w.Answers.Sites[1].Has(config.RoleMonitor) {
		t.Error("AllSites changed the answered sites")
	}
	data, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(check(t, data).Warnings()); !slices.Contains(got, "monitor-shares-a-site") || slices.Contains(got, "no-uptime-monitor") {
		t.Errorf("warnings %v", got)
	}
}

func TestAnsweringAgainKeepsEveryAnswerAsTheDefault(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHighAvailability]...)
	lines = append(lines, appAnswers(true)...)
	lines = append(lines, mailAnswers...)
	w, _, err := run(t, script(lines...))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Section{SectionCommunity, SectionOperators, SectionSites, SectionApps, SectionMail} {
		// Enter at every question: far more than any section asks.
		var out strings.Builder
		w.p = NewPrompter(strings.NewReader(strings.Repeat("\n", 40)), &out)
		if err := w.Ask(s); err != nil {
			t.Fatalf("%s: %v\n%s", s, err, out.String())
		}
		again, err := Render(w.Answers, testID, testOrigin)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Errorf("answering %s again with Enter changed the file", s)
		}
	}
}

func TestEndOfInputPartWayAborts(t *testing.T) {
	if _, _, err := run(t, script(communityAnswers...)); !errors.Is(err, ErrAborted) {
		t.Errorf("err = %v, want ErrAborted", err)
	}
}

func TestSectionFor(t *testing.T) {
	for key, want := range map[string]Section{
		"community.domain":        SectionCommunity,
		"acme.provider":           SectionCommunity,
		"operators.alice.email":   SectionOperators,
		"sites.home-a.roles":      SectionSites,
		"etcd.members":            SectionSites,
		"apps.talk.placement":     SectionApps,
		"storage.garage.sites":    SectionApps,
		"smtp.from_address":       SectionMail,
		"something.else.entirely": SectionAll,
	} {
		if got := SectionFor(key); got != want {
			t.Errorf("SectionFor(%q) = %s, want %s", key, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Paisans Staging Single": "paisans-staging-single",
		"Paisans Community":      "paisans-community",
		"example.org":            "example-org",
		"  Café & Friends!  ":    "caf-friends",
		"42 Club":                "club",
		"A Very Long Community Name That Goes On On": "a-very-long-community-name-that",
		"!!!": "",
	} {
		got := slug(in)
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
		if got != "" && checkSiteName(got) != nil {
			t.Errorf("slug(%q) = %q is not a valid site name", in, got)
		}
	}
}

// One machine's name defaults to the community's slug, then the domain's,
// since the one machine is the whole deployment.
func TestOneMachineNameDefaultsToTheCommunitySlug(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Paisans Staging Single", "paisans-staging-single"},
		{"!!!", "example-org"},
	} {
		var lines []string
		lines = append(lines, tc.name, "example.org", "")
		lines = append(lines, adminAnswers...)
		lines = append(lines, shapeAnswers[ShapeOneMachine]...)
		lines = append(lines, appAnswers(false)...)
		lines = append(lines, mailAnswers...)
		w, out, err := run(t, script(lines...))
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out, "Machine name ["+tc.want+"]") || w.Answers.Sites[0].Name != tc.want {
			t.Errorf("community %q: site = %q, want %q", tc.name, w.Answers.Sites[0].Name, tc.want)
		}
	}
}

// Answering the machines again keeps the health page beside the gateway only
// while there is exactly one gateway with a public address and no machine
// already holds the monitor role.
func TestAnsweringTheMachinesAgainSettlesTheMonitorOnTheGateway(t *testing.T) {
	gw := func(name string) Site {
		return Site{Name: name, Roles: []config.Role{config.RoleGateway}, PublicAddress: "203.0.113.10"}
	}
	home := Site{Name: "home", Roles: []config.Role{config.RoleData, config.RoleApps}}
	watch := Site{Name: "watch", Roles: []config.Role{config.RoleMonitor}, PublicAddress: "203.0.113.20"}
	for _, tc := range []struct {
		name  string
		sites []Site
		keep  bool
	}{
		{"one gateway", []Site{home, gw("edge")}, true},
		{"two gateways", []Site{gw("a"), gw("b")}, false},
		{"no public gateway", []Site{home, {Name: "edge", Roles: []config.Role{config.RoleGateway}}}, false},
		{"a monitor declared", []Site{home, gw("edge"), watch}, false},
	} {
		w := NewWizard(NewPrompter(strings.NewReader(""), &strings.Builder{}))
		w.Answers.Sites, w.Answers.MonitorOnGateway = tc.sites, true
		w.settleMonitor()
		if w.Answers.MonitorOnGateway != tc.keep {
			t.Errorf("%s: MonitorOnGateway = %v, want %v", tc.name, w.Answers.MonitorOnGateway, tc.keep)
		}
	}
}

// Answering the machines or the apps again, Enter at every question, keeps
// the health page beside the gateway: "Where it runs" defaults to it.
func TestAnsweringAgainKeepsTheHealthPageOnTheGateway(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHomeAndVPS]...)
	lines = append(lines, "")
	lines = append(lines, managementAnswers...)
	lines = append(lines, "n", "n", "n", "n", "", "", "", "2")
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []Section{SectionSites, SectionApps} {
		var again strings.Builder
		w.p = NewPrompter(strings.NewReader(strings.Repeat("\n", 40)), &again)
		if err := w.Ask(s); err != nil {
			t.Fatalf("%s: %v\n%s", s, err, again.String())
		}
		if !w.Answers.MonitorOnGateway || w.Answers.Monitor != nil {
			t.Errorf("answering %s again moved the health page: %+v", s, w.Answers)
		}
	}
}

// A display name typed at its question is written as display_name, and is
// the default when the apps are answered again. Enter takes the app's key,
// which is the client's name without one, so nothing is written for it.
func TestDisplayNameIsAskedAndKept(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHomeAndVPS]...)
	lines = append(lines, "") // sign in hostname
	lines = append(lines, managementAnswers...)
	lines = append(lines,
		"n",                // no forums
		"", "", "Wiki", "", // wiki: yes, hostname, display name, members only
		"n", "n", // no blog or chat
		"", "", "", "2", // health page: yes, hostname, display name, on the gateway
	)
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "Name shown at sign in for the wiki [wiki]: "; !strings.Contains(out, want) {
		t.Errorf("missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "Name shown at sign in for the sign in") {
		t.Errorf("Pocket ID has no client of its own, but was asked for a display name:\n%s", out)
	}
	data, err := Render(w.Answers, "0123456789abcdef", Origin{Version: "test", Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "display_name: Wiki\n") {
		t.Errorf("docs's display name is not written:\n%s", data)
	}
	if n := strings.Count(string(data), "display_name:"); n != 1 {
		t.Errorf("display_name written %d times, want only docs's:\n%s", n, data)
	}

	var again strings.Builder
	w.p = NewPrompter(strings.NewReader(strings.Repeat("\n", 40)), &again)
	if err := w.Ask(SectionApps); err != nil {
		t.Fatalf("%v\n%s", err, again.String())
	}
	if app, _ := w.Answers.App("wiki"); app.DisplayName != "Wiki" {
		t.Errorf("answering the apps again changed docs's display name to %q", app.DisplayName)
	}
}

func TestCheckDisplayName(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"Wiki", true},
		{"Café", true},
		{strings.Repeat("é", 50), true},
		{strings.Repeat("w", 51), false},
		{" Wiki", false},
		{"Cafe\u0301", false},
		{"", false},
	} {
		if err := checkDisplayName(tc.in); (err == nil) != tc.ok {
			t.Errorf("checkDisplayName(%q) = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

// The second answer to how people join writes signup: open, with the code
// of conduct still asked for and written, since everyone who registers
// agrees to it too.
func TestWizardWritesOpenSignup(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	apps := appAnswers(false)
	apps[3] = "2" // sign in hostname, who manages accounts, its hostname, how people join
	lines = append(lines, apps...)
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if w.Answers.Account.Signup != "open" {
		t.Fatalf("signup is %q, want open", w.Answers.Account.Signup)
	}
	data, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"      signup: open\n", "      code_of_conduct: |\n        # Be kind"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file lacks %q", want)
		}
	}
	for _, f := range check(t, data).Refusals() {
		t.Errorf("refused %s: %s: %s", f.Rule, f.Key, f.Message)
	}
}

// The SSH user may be left empty: the machine is then reached as the
// deployment's own user, which the question names, and the file writes no
// user for it. A name given is written.
func TestTheSSHUserMayBeLeftEmpty(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, appAnswers(false)...)
	lines = append(lines, mailAnswers...)
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(script(lines...)), &out))
	w.DefaultUser = "paisans-f2a9"
	if err := w.Ask(SectionAll); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "SSH user (blank: paisans-f2a9, the deployment's own): ") {
		t.Errorf("the question does not offer the deployment's user:\n%s", out.String())
	}
	if strings.Contains(out.String(), "must already exist") {
		t.Errorf("the question still asks for an existing user:\n%s", out.String())
	}
	data, err := Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user:") {
		t.Errorf("a user was written:\n%s", data)
	}
	for _, f := range check(t, data).Refusals() {
		t.Errorf("refused %s: %s: %s", f.Rule, f.Key, f.Message)
	}

	w.Answers.Sites[0].SSHUser = "admin"
	data, err = Render(w.Answers, testID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "      user: admin\n") {
		t.Errorf("a given user was not written:\n%s", data)
	}
}

// Two operators sharing a key is a file the loader refuses, so the second
// operator's paste of the first one's key is refused at the prompt.
func TestASecondOperatorMayNotGiveTheFirstOnesKey(t *testing.T) {
	var out strings.Builder
	w := NewWizard(NewPrompter(strings.NewReader(script(
		"alice", "Alice Example", "alice@example.org", aliceKey, "",
		"bob", "Bob Example", "bob@example.org", aliceKey, bobKey, "", "n",
	)), &out))
	if err := w.Ask(SectionOperators); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := w.Answers.Operators[1].Keys; !slices.Equal(got, []string{bobKey}) {
		t.Errorf("bob's keys = %q, want his own alone", got)
	}
	if !strings.Contains(out.String(), "this is alice's key") {
		t.Errorf("the shared key was not refused:\n%s", out.String())
	}
}

func TestKeyCheck(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&small.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	weak := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	aliceFP, err := checkKey(aliceKey)
	if err != nil {
		t.Fatal(err)
	}
	check := keyCheck(map[string]string{aliceFP: "alice"})
	for _, tc := range []struct {
		name, line string
		got        []string
		want       string
	}{
		{"own key", bobKey, nil, ""},
		{"another operator's", aliceKey, nil, "alice's key"},
		{"another operator's, another comment", strings.Replace(aliceKey, "alice@example.org", "laptop", 1), nil, "alice's key"},
		{"listed above", bobKey, []string{bobKey}, "already listed above"},
		{"not a key", "nonsense", nil, "not an SSH public key"},
		{"options", `from="10.0.0.1" ` + bobKey, nil, "carries options"},
		{"small rsa", weak, nil, "1024 bits"},
	} {
		err := check(tc.got, tc.line)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
