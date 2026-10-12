package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// fakeRunGrants makes the sites stale hold no grant record, so every entry of
// theirs is reviewed, and the rest hold what the configuration grants. The
// run's review then renders the fixture for real, so it has images to show.
func fakeRunGrants(t *testing.T, stale ...string) {
	t.Helper()
	saved, savedRender := convergeRecord, convergeRender
	convergeRecord = func(_ *config.Config, site string, g grant.Grant, _ bool) (grant.Record, error) {
		for _, s := range stale {
			if s == site {
				return grant.Record{}, nil
			}
		}
		return grant.Update(grant.Record{}, g, grant.AllScope, time.Unix(0, 0), ""), nil
	}
	convergeRender = func(cfg *config.Config, secrets *config.Secrets) (*render.Plan, error) {
		return render.Build(cfg, secrets)
	}
	t.Cleanup(func() { convergeRecord, convergeRender = saved, savedRender })
}

func convergeWith(t *testing.T, stdin io.Reader, stdout io.Writer, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--sudo=false"}, extra...), stdin, stdout)
	})
	return err
}

// apply with no --site asks once for every site, before its first step,
// and runs every step once approved.
func TestConvergeAsksOnceForEverySite(t *testing.T) {
	ran := fakeConverge(t, nil)
	fakeRunGrants(t, "home-a", "vm")
	rec := recordConverge(t)
	var out bytes.Buffer
	if err := convergeWith(t, answering(t, "approve\n"), &out, "--execute"); err != nil {
		t.Fatalf("%v\n%s", err, rec.Lines())
	}
	if n := strings.Count(out.String(), "Type approve to go on: "); n != 1 {
		t.Errorf("asked %d times:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "on home-a, vm") {
		t.Errorf("the question does not name both sites: %s", out.String())
	}
	sameLines(t, *ran, commands(blankPlan()))
	review, prepare := rec.Index("section", "review"), rec.Index("section", "prepare")
	if review < 0 || prepare < review {
		t.Errorf("the review is not before the first step: review %d, prepare %d\n%s", review, prepare, rec.Lines())
	}
	if !rec.Has("pending", "home-a") || !rec.Has("pending", "vm") || !rec.Has("done", "home-b") {
		t.Errorf("the review's lines:\n%s", rec.Lines())
	}
}

// Refused, at the prompt or for want of a terminal, nothing runs.
func TestConvergeRefusedRunsNothing(t *testing.T) {
	for name, stdin := range map[string]func(t *testing.T) io.Reader{
		"refused":     func(t *testing.T) io.Reader { return answering(t, "no\n") },
		"no terminal": func(*testing.T) io.Reader { return noTerminalStdin() },
	} {
		t.Run(name, func(t *testing.T) {
			ran := fakeConverge(t, nil)
			fakeRunGrants(t, "home-b")
			err := convergeWith(t, stdin(t), io.Discard, "--execute")
			if err == nil || len(*ran) != 0 {
				t.Errorf("err %v, ran %v", err, *ran)
			}
		})
	}
}

// With nothing to approve the run asks nothing, terminal or not.
func TestConvergeWithNothingToApproveAsksNothing(t *testing.T) {
	ran := fakeConverge(t, nil)
	fakeRunGrants(t)
	var out bytes.Buffer
	if err := convergeWith(t, noTerminalStdin(), &out, "--execute"); err != nil || out.Len() != 0 {
		t.Fatalf("err %v, asked %q", err, out.String())
	}
	sameLines(t, *ran, commands(blankPlan()))
}

// The dry run shows the review, asks nothing, and ends saying --execute
// will ask.
func TestConvergeDryRunShowsTheReview(t *testing.T) {
	fakeConverge(t, nil)
	fakeChecks(t, nil)
	fakeRunGrants(t, "home-a")
	rec := recordConverge(t)
	var out bytes.Buffer
	if err := convergeWith(t, answering(t, "approve\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("the dry run asked: %q", out.String())
	}
	if !rec.Has("pending", "home-a") || !strings.Contains(rec.Lines(), "explain: + key SHA256:") || !strings.Contains(rec.Lines(), "explain: + image ") {
		t.Errorf("the review:\n%s", rec.Lines())
	}
	if !strings.Contains(rec.Lines(), "it asks you to type approve") {
		t.Errorf("the result does not say what --execute asks:\n%s", rec.Lines())
	}
}

// A site whose record cannot be read is a failed line in a dry run, and
// stops --execute before any step.
func TestConvergeReviewNeedsEverySite(t *testing.T) {
	ran := fakeConverge(t, nil)
	fakeChecks(t, nil)
	fakeRunGrants(t)
	read := convergeRecord
	convergeRecord = func(cfg *config.Config, site string, g grant.Grant, sudo bool) (grant.Record, error) {
		if site == "watch" {
			return grant.Record{}, errors.New("ssh: connect to host watch.example.org port 22: Connection refused")
		}
		return read(cfg, site, g, sudo)
	}
	rec := recordConverge(t)
	if err := convergeWith(t, noTerminalStdin(), io.Discard); err != nil || !rec.Has("fail", "watch") {
		t.Errorf("dry run: %v\n%s", err, rec.Lines())
	}
	if err := convergeWith(t, noTerminalStdin(), io.Discard, "--execute"); err == nil || !strings.Contains(err.Error(), "watch") || len(*ran) != 0 {
		t.Errorf("execute: %v, ran %v", err, *ran)
	}
}

// The run records each site's web server directory itself, once its web
// server step has written there, with who approved it.
func TestConvergeRecordsTheWebServerDirectory(t *testing.T) {
	cfg := fixture(t)
	host := &memGrant{}
	g := grant.Grant{WebServer: &grant.WebServer{IncludeDir: "/srv/caddy.d"}}
	reviews := map[string]*siteReview{"watch": {grant: g, scope: grant.AllScope, host: host}}
	saved := reviewedByRun
	reviewedByRun = &runReview{approvedBy: "alice"}
	t.Cleanup(func() { reviewedByRun = saved })
	if err := convergeRecordWebServer(recordConverge(t), cfg, reviews); err != nil {
		t.Fatal(err)
	}
	rec := host.record(t)
	if rec.WebServer == nil || rec.WebServer.IncludeDir != "/srv/caddy.d" || rec.WebServer.ApprovedBy != "alice" || rec.Keys != nil || rec.Images != nil {
		t.Errorf("record %+v %+v", rec.WebServer, rec)
	}
}
