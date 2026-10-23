package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// commandWords are the first words after the command's name on a command
// line, so that a test can find advice that names a command.
const commandWords = `(help|validate|create|init|render|site|mesh|gateway|deployment|operator|secrets|app|oidc|web-server|catalog|blueprint|storage|prune|preflight|failover|doctor|images|version|dns)`

// namesPaisans matches a command line run as paisans, in text that should
// name the command as it was invoked.
var namesPaisans = regexp.MustCompile(`(^|[^\w/.-])paisans ` + commandWords + `([^\w-]|$)`)

// ownersCommand matches the commands the host's owner runs on the host,
// which name the release's paisans whatever this command was invoked as.
var ownersCommand = regexp.MustCompile(`sudo\s+paisans\s+site\s+(admit|evict)`)

// invokedAs runs the rest of the test as though the command were invoked as
// /opt/bin/paisans-test.
func invokedAs(t *testing.T) {
	t.Helper()
	progname.Set("/opt/bin/paisans-test")
	t.Cleanup(func() { progname.Set(progname.Default) })
}

// Every help page names the command as it was invoked, and a usage line's
// continuation lines stay under its flags.
func TestHelpNamesTheInvokedCommand(t *testing.T) {
	invokedAs(t)
	var b strings.Builder
	printTopHelp(&b)
	for group := range groupSummary {
		printGroupHelp(&b, group)
	}
	for _, c := range helpCommands {
		printCommandHelp(&b, &c, nil)
		for _, u := range c.usage {
			if !strings.HasPrefix(u, "paisans ") && !strings.HasPrefix(u, "sudo paisans ") {
				t.Errorf("%s: usage %q does not start with the command, which invokedUsage renames", c.name, u)
			}
		}
	}
	b.WriteString(executeTopic())
	page := ownersCommand.ReplaceAllString(b.String(), "")
	if m := namesPaisans.FindAllString(page, -1); len(m) > 0 {
		t.Errorf("help names paisans rather than paisans-test: %q", m)
	}
	if !strings.Contains(page, "Usage: paisans-test <command> [flags]") {
		t.Errorf("top help has no usage line for paisans-test")
	}

	b.Reset()
	printCommandHelp(&b, findCommand("site add"), nil)
	want := "  paisans-test site add <site> [--config paisans-stack.yaml] [--secrets secrets.enc.yaml]\n" +
		"                      [--execute] [--yes]\n"
	if !strings.Contains(b.String(), want) {
		t.Errorf("site add's usage is not aligned under paisans-test:\n%s\nwant:\n%s", b.String(), want)
	}
}

// Advice in an error names the command as it was invoked.
func TestAdviceNamesTheInvokedCommand(t *testing.T) {
	invokedAs(t)
	err := runSiteAdd(nil, strings.NewReader(""), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "Eg: paisans-test site add home-b") {
		t.Errorf("site add with no site: %v, want advice naming paisans-test", err)
	}
	if got := resumeAdvice(); got != "Run paisans-test deployment apply --execute again to resume." {
		t.Errorf("resumeAdvice() = %q", got)
	}
	if got := noAnchorPublish(); !strings.Contains(got, "with paisans-test deployment pull --key <path.pub>") || !strings.Contains(got, "run paisans-test operator register") {
		t.Errorf("noAnchorPublish() = %q", got)
	}
}

// Advice for the host's owner, who runs the release on another machine,
// names paisans; the line the operator runs names the command as invoked.
func TestOwnersAdviceNamesPaisans(t *testing.T) {
	invokedAs(t)
	got := admissionProblem("home-b", "f2a9", admission.ErrNotAdmitted).Error()
	for _, want := range []string{"    paisans-test site request --site home-b > home-b.admit", "    sudo paisans site admit --request home-b.admit"} {
		if !strings.Contains(got, want) {
			t.Errorf("admission problem lacks %q:\n%s", want, got)
		}
	}
}
