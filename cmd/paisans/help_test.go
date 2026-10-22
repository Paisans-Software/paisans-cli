package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Each way of asking for help reaches its page, and a request for one
// command's page is cut to its name and --help, so nothing else on the line
// can make the command run.
func TestResolveHelp(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		page  string // "top", "execute", a group's name, or "" to run
		asked bool
		run   []string
	}{
		{args: nil, page: "top"},
		{args: []string{"help"}, page: "top", asked: true},
		{args: []string{"--help"}, page: "top", asked: true},
		{args: []string{"-h"}, page: "top", asked: true},
		{args: []string{"help", "execute"}, page: "execute", asked: true},
		{args: []string{"site"}, page: "site"},
		{args: []string{"site", "help"}, page: "site", asked: true},
		{args: []string{"site", "--help"}, page: "site", asked: true},
		{args: []string{"help", "site"}, page: "site", asked: true},
		{args: []string{"oidc", "client", "help"}, page: "oidc client", asked: true},
		{args: []string{"site", "add", "help"}, asked: true, run: []string{"site", "add", "--help"}},
		{args: []string{"help", "site", "add"}, asked: true, run: []string{"site", "add", "--help"}},
		{args: []string{"site", "add", "home-b", "--execute", "--help"}, asked: true, run: []string{"site", "add", "--help"}},
		{args: []string{"deployment", "apply", "-h"}, asked: true, run: []string{"deployment", "apply", "--help"}},
		{args: []string{"oidc", "client", "create", "help"}, asked: true, run: []string{"oidc", "client", "create", "--help"}},
		{args: []string{"secrets", "set", "help"}, asked: true, run: []string{"secrets", "set", "--help"}},
		// help after the first argument is an argument: a site may be named
		// help, and the command says what it makes of it.
		{args: []string{"site", "add", "home-b", "help"}, run: []string{"site", "add", "home-b", "help"}},
		{args: []string{"secrets", "set", "x", "--", "--help"}, run: []string{"secrets", "set", "x", "--", "--help"}},
		{args: []string{"deployment", "apply", "--site", "home-a"}, run: []string{"deployment", "apply", "--site", "home-a"}},
		{args: []string{"bogus"}, run: []string{"bogus"}},
		{args: []string{"site", "bogus"}, run: []string{"site", "bogus"}},
	} {
		helpAsked = false
		page, asked, run := resolveHelp(tc.args)
		got := ""
		if page != nil {
			var b strings.Builder
			page(&b)
			got = b.String()
		}
		want := ""
		switch tc.page {
		case "":
		case "top":
			var b strings.Builder
			printTopHelp(&b)
			want = b.String()
		case "execute":
			want = executeTopic()
		default:
			var b strings.Builder
			printGroupHelp(&b, tc.page)
			want = b.String()
		}
		if got != want {
			t.Errorf("%q: page\n%s\nwant\n%s", tc.args, got, want)
		}
		if asked != tc.asked {
			t.Errorf("%q: asked = %v", tc.args, asked)
		}
		if !slices.Equal(run, tc.run) {
			t.Errorf("%q: run %q, want %q", tc.args, run, tc.run)
		}
		if tc.page == "" && helpAsked != tc.asked {
			t.Errorf("%q: helpAsked = %v", tc.args, helpAsked)
		}
	}
	helpAsked = false
}

// The first page fits an 80 column terminal and lists every command once,
// and every command it lists has a line to list it by.
func TestTopHelpListsEveryCommandOnce(t *testing.T) {
	var b strings.Builder
	printTopHelp(&b)
	for _, line := range strings.Split(b.String(), "\n") {
		if len(line) > helpWidth {
			t.Errorf("%d columns: %s", len(line), line)
		}
	}
	listed := map[string]int{}
	for _, s := range helpSections {
		for _, name := range s.names {
			listed[name]++
			if findCommand(name) == nil && groupSummary[name] == "" {
				t.Errorf("%s is listed with nothing to say about it", name)
			}
		}
	}
	for _, c := range helpCommands {
		first := strings.Fields(c.name)[0]
		if listed[first] != 1 {
			t.Errorf("%s: its first word is listed %d times", c.name, listed[first])
		}
		if c.summary == "" || c.detail == "" || len(c.usage) == 0 {
			t.Errorf("%s: missing a summary, a detail or a usage line", c.name)
		}
	}
}

// Every command's prose wraps inside 80 columns.
func TestCommandHelpWraps(t *testing.T) {
	for _, c := range helpCommands {
		var b strings.Builder
		printCommandHelp(&b, &c, nil)
		_, prose, _ := strings.Cut(b.String(), "\n\n")
		for _, line := range strings.Split(prose, "\n") {
			if len(line) > helpWidth {
				t.Errorf("%s: %d columns: %s", c.name, len(line), line)
			}
		}
	}
}

// Every flag set named in the source has a page, so --help on any command
// shows its page rather than a pointer to one that does not exist. Names the
// source builds rather than spells are listed by hand.
func TestEveryFlagSetHasAPage(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	literal := regexp.MustCompile(`newFlagSet\("([^"]+)",`)
	names := []string{"web-server show", "web-server check", "dns init", "dns prune"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, m := range literal.FindAllStringSubmatch(string(src), -1) {
			names = append(names, m[1])
		}
	}
	for _, name := range names {
		if findCommand(name) == nil {
			t.Errorf("%s has a flag set and no help page", name)
		}
	}
}

func TestWrap(t *testing.T) {
	if got := wrap("one two three four", 9); got != "one two\nthree\nfour" {
		t.Errorf("got %q", got)
	}
}

// The site group prepares a host and lists what is on it; nothing in the
// help offers a key allowlist.
func TestSiteHasNoAllowlistCommands(t *testing.T) {
	for _, name := range []string{"site allow", "site disallow"} {
		if findCommand(name) != nil {
			t.Errorf("%s has a help page", name)
		}
	}
	for what, text := range map[string]string{"the site group's summary": groupSummary["site"], "help execute": executeTopic()} {
		if strings.Contains(text, "allowlist") || strings.Contains(text, "site allow") {
			t.Errorf("%s still offers an allowlist:\n%s", what, text)
		}
	}
}

// A backquoted word in a flag's usage is read by the flag package as the
// name of its value, so --help would print that word in place of the type.
// No flag's usage carries one.
func TestNoFlagUsageIsBackquoted(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	define := regexp.MustCompile(`\bfs\.[A-Z][A-Za-z]*\(.*` + "`")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for n, line := range strings.Split(string(src), "\n") {
			if define.MatchString(line) {
				t.Errorf("%s:%d: a flag usage with a backquote: %s", f, n+1, strings.TrimSpace(line))
			}
		}
	}
}
