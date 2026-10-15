package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// The request is stdout and nothing else, so it can be redirected to the
// file the owner admits; the digest is on stderr, and it is the digest of
// exactly what stdout carried.
func TestHostRequestWritesTheRequestAndItsDigest(t *testing.T) {
	var err error
	stdout, stderr := captureOutput(t, func() {
		err = runHostRequest([]string{"--site", "vm", "--config", fixtureConfig()})
	})
	if err != nil {
		t.Fatal(err)
	}
	req, perr := admission.Parse([]byte(stdout))
	if perr != nil {
		t.Fatalf("stdout is not a request admit reads: %v\n%s", perr, stdout)
	}
	if req.User != "ubuntu" || req.Address != "10.44.0.3" || !req.Grants.PublicPorts || req.Grants.Watchdog {
		t.Errorf("not vm's request: %+v", req)
	}
	digest := admission.Digest([]byte(stdout))
	if !regexp.MustCompile(`^[0-9a-z]{4}(-[0-9a-z]{4}){3}$`).MatchString(digest) {
		t.Fatalf("digest %q is not xxxx-xxxx-xxxx-xxxx", digest)
	}
	if !strings.Contains(stderr, "digest "+digest) {
		t.Errorf("stderr does not carry the digest %s:\n%s", digest, stderr)
	}
	if strings.Contains(stdout, digest) {
		t.Errorf("the digest is on stdout, inside the request:\n%s", stdout)
	}
}

// Two runs print the same bytes, so the same digest.
func TestHostRequestIsStable(t *testing.T) {
	first, _ := captureOutput(t, func() { _ = runHostRequest([]string{"--site", "home-a", "--config", fixtureConfig()}) })
	second, _ := captureOutput(t, func() { _ = runHostRequest([]string{"--site", "home-a", "--config", fixtureConfig()}) })
	if first == "" || first != second {
		t.Errorf("two runs differ:\n%s\n%s", first, second)
	}
}

// A site the configuration does not declare, no site at all, and a stray
// argument are refused, and nothing is written to stdout.
func TestHostRequestRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"an undeclared site", []string{"--site", "home-z", "--config", fixtureConfig()}, `declares no site "home-z"`},
		{"no site", []string{"--config", fixtureConfig()}, "name the site with --site"},
		{"a stray argument", []string{"--site", "vm", "--config", fixtureConfig(), "extra"}, "takes no argument"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var err error
			stdout, _ := captureOutput(t, func() { err = runHostRequest(c.args) })
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
			if stdout != "" {
				t.Errorf("wrote to stdout:\n%s", stdout)
			}
		})
	}
}

// The line a not-admitted login tells the operator to run is this command,
// so its usage and the message agree.
func TestHostRequestUsageMatchesTheNotAdmittedLine(t *testing.T) {
	c := findCommand("host request")
	if c == nil {
		t.Fatal("host request has no help page")
	}
	if got := c.usage[0]; !strings.HasPrefix(got, "paisans host request --site <name>") || !strings.HasSuffix(got, "> <name>.admit") {
		t.Errorf("usage %q does not agree with the not-admitted message", got)
	}
}
