package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/appadmin"
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// linkFake is a Pocket ID holding the fixture's declared operator alice, and
// any other account it is told of, reached through curl configs.
type linkFake struct {
	registry *registryFake
	disabled bool
	missing  bool
	calls    []string
	issued   bool
	emailed  bool
	mutated  bool
}

func (f *linkFake) Describe() string           { return "home-a" }
func (f *linkFake) Run(string) (string, error) { return "", errors.New("unexpected Run") }
func (f *linkFake) RunInput(command, stdin string) (string, error) {
	f.calls = append(f.calls, stdin)
	switch {
	case strings.Contains(stdin, `request = "GET"`) && strings.Contains(stdin, "/api/users?"):
		if f.missing {
			return `{"data":[],"pagination":{"totalPages":0}}` + "\npaisans-http-status:200", nil
		}
		disabled := "false"
		if f.disabled {
			disabled = "true"
		}
		return `{"data":[{"id":"u-1","username":"alice","email":"alice@example.org","emailVerified":true,"isAdmin":true,"disabled":` + disabled + `}],"pagination":{"totalPages":1}}` + "\npaisans-http-status:200", nil
	case strings.Contains(stdin, "one-time-access-token"):
		f.issued = true
		return `{"token":"tok-not-real-0009"}` + "\npaisans-http-status:201", nil
	case strings.Contains(stdin, "one-time-access-email"):
		f.emailed = true
		return `{}` + "\npaisans-http-status:204", nil
	case !strings.Contains(stdin, `request = "GET"`):
		f.mutated = true
	}
	return `{"error":"no route"}` + "\npaisans-http-status:404", nil
}

func withLinkFake(t *testing.T) *linkFake {
	t.Helper()
	fake := &linkFake{registry: withRegistryFake(t)}
	saved, savedFailures := adminTransport, convergeEmailFailures
	adminTransport = func(apply.SSHTransport) appadmin.Transport { return fake }
	convergeEmailFailures = func(*config.Config, string, convergeOptions, []string) (map[string]string, error) {
		return map[string]string{}, nil
	}
	t.Cleanup(func() { adminTransport, convergeEmailFailures = saved, savedFailures })
	return fake
}

func linkArgs(extra ...string) []string {
	return append([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "alice"}, extra...)
}

// A dry run finds the declared operator's account and says what it would issue,
// and reaches nothing that changes: no link, no registry claim.
func TestOperatorMagicLinkDryRunChangesNothing(t *testing.T) {
	fake := withLinkFake(t)
	rec := withRecorder(t, true)
	var err error
	stdout := captureStdout(t, func() { err = runOperatorMagicLink(linkArgs()) })
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || fake.issued || fake.emailed || fake.mutated {
		t.Errorf("a dry run issued something: stdout %q, issued %v, emailed %v", stdout, fake.issued, fake.emailed)
	}
	if fake.registry.claims != 0 {
		t.Error("a dry run claimed the host")
	}
	if !rec.Has("pending", "issue a one-time sign-in link for alice, valid 24 hours") || !rec.Has("result", "Nothing changed") {
		t.Errorf("output:\n%s", rec.Lines())
	}
}

// An execute issues the link, prints it once on stdout and nowhere else, and
// changes nothing about the account: no create, no group, no operator flag.
func TestOperatorMagicLinkIssuesTheLinkAndNothingElse(t *testing.T) {
	fake := withLinkFake(t)
	key := fixtureAPIKey(t)
	rec := withRecorder(t, true)
	var out strings.Builder
	saved := loginLinkOut
	loginLinkOut = &out
	t.Cleanup(func() { loginLinkOut = saved })
	if err := runOperatorMagicLink(linkArgs("--execute")); err != nil {
		t.Fatal(err)
	}
	stdout := out.String()
	link := "https://id.example.org/lc/tok-not-real-0009"
	if !fake.issued || !strings.Contains(stdout, link) || strings.Count(stdout, link) != 1 {
		t.Errorf("the link was not printed once:\n%s", stdout)
	}
	if strings.Contains(rec.Lines(), link) || strings.Contains(rec.Lines()+stdout, key) {
		t.Errorf("the report carries the link or the API key:\n%s", rec.Lines())
	}
	if fake.mutated {
		t.Error("it changed the account, not only issued a link")
	}
	if fake.registry.claims != 1 {
		t.Errorf("claimed the host %d time(s), want once", fake.registry.claims)
	}
}

// Only a declared operator with an account is given a link: a name not under
// operators is refused before any host is reached, and a declared operator apply has
// not made yet, or whose account is disabled, is refused with why.
func TestOperatorMagicLinkRefusals(t *testing.T) {
	fake := withLinkFake(t)
	withRecorder(t, true)
	err := runOperatorMagicLink([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "mallory", "--execute"})
	if err == nil || !strings.Contains(problemText(err), "not a declared operator") || len(fake.calls) != 0 {
		t.Errorf("an undeclared name: %v, %d calls", err, len(fake.calls))
	}
	fake.missing = true
	if err := runOperatorMagicLink(linkArgs("--execute")); err == nil || !strings.Contains(err.Error(), "apply makes every declared operator's account") || fake.issued {
		t.Errorf("no account yet: %v", err)
	}
	fake.missing, fake.disabled = false, true
	if err := runOperatorMagicLink(linkArgs("--execute")); err == nil || !strings.Contains(err.Error(), "disabled") || fake.issued {
		t.Errorf("a disabled account: %v", err)
	}
}
