package create

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"", "", "203.0.113.50", "", "", // its machine: add, name, address, ssh host, user
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
			data, err := Render(w.Answers, testID)
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

func TestDecliningTheMonitorMachineDropsTheStatusPage(t *testing.T) {
	var lines []string
	lines = append(lines, communityAnswers...)
	lines = append(lines, adminAnswers...)
	lines = append(lines, shapeAnswers[ShapeOneMachine]...)
	lines = append(lines, "", "n", "n", "n", "", "", "n")
	lines = append(lines, mailAnswers...)
	w, out, err := run(t, script(lines...))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, ok := w.Answers.App(statusApp); ok || w.Answers.Monitor != nil {
		t.Errorf("the status page or its machine is still declared: %+v", w.Answers)
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
	first, err := Render(w.Answers, testID)
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
		again, err := Render(w.Answers, testID)
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
