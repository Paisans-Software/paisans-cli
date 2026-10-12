package create

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

const aliceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"

// script joins answers one per line; "" is Enter.
func script(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

var (
	communityAnswers = []string{"Example Community", "example.org", ""}
	adminAnswers     = []string{"alice", "Alice Example", "alice@example.org", aliceKey, "", "n"}
	mailAnswers      = []string{"smtp.example.org", "", "", "community@example.org", "", ""}
)

// every capability, its default hostnames, and members only where offered.
func appAnswers(chat bool) []string {
	out := []string{
		"",         // sign in hostname
		"", "", "", // forums: yes, hostname, members only
		"", "", "", // wiki
		"", "", "", // blog
	}
	if chat {
		out = append(out, "y", "", "", "") // chat: yes, server, web client, members only
	}
	return append(out,
		"", "", // status page: yes, hostname
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
		"  yes: members only. Anyone else is sent to sign in first;",
		"  no:  public. Anyone, including search engines and bots, can read what\n" +
			"       the app shows to visitors who are not signed in.",
		"Members only? [Y/n]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// Declining the status page leaves no status page and no machine for it.
func TestDecliningTheStatusPageDropsIt(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, "", "n", "n", "n", "n")
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, ok := w.Answers.App(statusApp); ok || w.Answers.Monitor != nil || w.Answers.MonitorOnGateway {
		t.Errorf("the status page or its machine is still declared: %+v", w.Answers)
	}
}

// Choosing the gateway puts the monitor beside it, offered by the gateway
// site's name with what it costs, and answering the machines again with the
// gateway renamed keeps it there.
func TestTheStatusPageOnTheGateway(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeHomeAndVPS]...)
	lines = append(lines,
		"",            // sign in hostname
		"n", "n", "n", // no forums, wiki or blog
		"n",    // no chat
		"", "", // status page: yes, hostname
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
	for _, s := range []Section{SectionCommunity, SectionAdmins, SectionSites, SectionApps, SectionMail} {
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
		"admins.alice.email":      SectionAdmins,
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
		if !strings.Contains(out, "Name ["+tc.want+"]") || w.Answers.Sites[0].Name != tc.want {
			t.Errorf("community %q: site = %q, want %q", tc.name, w.Answers.Sites[0].Name, tc.want)
		}
	}
}

// Answering the machines again keeps the status page beside the gateway only
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
