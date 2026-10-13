package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// sudoFake is a host whose sudo is settled up front. Settling it logs
// "prompt <site>" when it wants a password, as the prompt would be asked
// then, and returns err; a command that reaches it logs "reach <site>".
type sudoFake struct {
	runningHost
	site   string
	nopass bool
	err    error
	log    *[]string
}

func (f sudoFake) SettlesSudo() bool { return true }

func (f sudoFake) PrepareSudo() error {
	if !f.nopass {
		*f.log = append(*f.log, "prompt "+f.site)
	}
	return f.err
}

func (f sudoFake) Run(command string) (string, error) {
	*f.log = append(*f.log, "reach "+f.site)
	return f.runningHost.Run(command)
}

func (f sudoFake) ReadFile(path string) (string, bool, error) {
	*f.log = append(*f.log, "reach "+f.site)
	return f.runningHost.ReadFile(path)
}

func (f sudoFake) RunInput(command, stdin string) (string, error) {
	*f.log = append(*f.log, "reach "+f.site)
	return f.runningHost.RunInput(command, stdin)
}

// fakeSudoHosts makes every site converge reaches a sudoFake, with errs by
// site and nopass the sites whose sudo wants no password. It returns the log
// every host writes to.
func fakeSudoHosts(t *testing.T, nopass []string, errs map[string]error) *[]string {
	t.Helper()
	log := &[]string{}
	saved := convergeHost
	convergeHost = func(name string, _ config.Site, sudo bool) apply.Transport {
		if !sudo {
			t.Errorf("%s reached without sudo", name)
		}
		return sudoFake{runningHost{failingHost{match: "\x00"}}, name, slices.Contains(nopass, name), errs[name], log}
	}
	t.Cleanup(func() { convergeHost = saved })
	return log
}

// convergeSudo is converge with sudo on.
func convergeSudo(t *testing.T, extra ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = runApply(append([]string{"--config", fixtureConfig(), "--secrets", fixtureSecretsPath(), "--sudo=true"}, extra...), strings.NewReader(""), io.Discard)
	})
	return err
}

// sudoLines is the sudo section's lines: each site's mark and name.
func sudoLines(rec *ui.Recorder) []string {
	var out []string
	in := false
	for _, e := range rec.Events {
		switch {
		case e.Kind == "section":
			in = e.Text == "sudo"
		case in && e.Kind != "step":
			line := e.Kind + " " + e.Text
			if e.Extra != "" {
				line += ": " + e.Extra
			}
			out = append(out, line)
		}
	}
	return out
}

// apply without --site asks every host for its sudo password before any
// step: the sudo section comes first after the deployment and the version
// the run names, one ✓ line per site in name order,
// and a host that wants no password is never asked but still has its line.
func TestConvergeAsksEverySudoPasswordFirst(t *testing.T) {
	for _, args := range [][]string{nil, {"--execute"}} {
		ran := fakeConverge(t, nil)
		rec := recordConverge(t)
		log := fakeSudoHosts(t, []string{"vm"}, nil)
		convergeReadInitial = func(_ *config.Config, m string, _ bool) (render.EtcdInitial, bool, error) {
			*log = append(*log, "reach "+m)
			return render.EtcdInitial{}, false, nil
		}
		savedRun := convergeRun
		convergeRun = func(a []string) error {
			*log = append(*log, "step "+a[0])
			return savedRun(a)
		}
		if err := convergeSudo(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		sameLines(t, sudoLines(rec), []string{"done home-a", "done home-b", "done vm", "done watch"})
		sameLines(t, (*log)[:3], []string{"prompt home-a", "prompt home-b", "prompt watch"})
		for _, l := range (*log)[3:] {
			if strings.HasPrefix(l, "prompt") {
				t.Errorf("%v: asked after the first step:\n%s", args, strings.Join(*log, "\n"))
			}
		}
		if d, s := rec.Index("section", "deployment"), rec.Index("section", "sudo"); d != 0 || s < d {
			t.Errorf("%v: the deployment is not named ahead of sudo:\n%s", args, rec.Lines())
		}
		if s, e := rec.Index("section", "sudo"), rec.Index("section", "etcd members"); s < 0 || s > e {
			t.Errorf("%v: the sudo section is not before the etcd reads:\n%s", args, rec.Lines())
		}
		if len(args) > 0 && len(*ran) == 0 {
			t.Errorf("%v: no step ran", args)
		}
	}
}

// With init to run first, the passwords are asked before it, since init
// reads every host too.
func TestConvergeAsksSudoBeforeInit(t *testing.T) {
	ran := fakeConverge(t, nil)
	recordConverge(t)
	log := fakeSudoHosts(t, nil, nil)
	saved := convergeRead
	reads := 0
	convergeRead = func(path, secrets string) (*config.Config, convergeState, error) {
		reads++
		cfg, err := config.Load(fixtureConfig())
		return cfg, convergeState{NeedsInit: reads == 1}, err
	}
	t.Cleanup(func() { convergeRead = saved })
	savedRun := convergeRun
	convergeRun = func(a []string) error {
		*log = append(*log, "step "+a[0])
		return savedRun(a)
	}
	if err := convergeSudo(t, "--execute"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) == 0 || (*ran)[0] != "init" {
		t.Fatalf("init did not run first: %v", *ran)
	}
	sameLines(t, (*log)[:5], []string{"prompt home-a", "prompt home-b", "prompt vm", "prompt watch", "step init"})
	if n := strings.Count(strings.Join(*log, "\n"), "prompt home-a"); n != 1 {
		t.Errorf("home-a settled %d times", n)
	}
}

// A password sudo refuses stops the run at once, before any step and
// before the next host is asked, with sudo's own problem.
func TestARefusedSudoPasswordStopsBeforeAnyStep(t *testing.T) {
	ran := fakeConverge(t, nil)
	rec := recordConverge(t)
	refusal := &ui.Problem{Hint: "sudo on admin@home-b did not accept the password", Cause: apply.ErrSudo}
	log := fakeSudoHosts(t, nil, map[string]error{"home-b": refusal})
	read := false
	convergeReadInitial = func(*config.Config, string, bool) (render.EtcdInitial, bool, error) {
		read = true
		return render.EtcdInitial{}, false, nil
	}
	err := convergeSudo(t, "--execute")
	if !errors.Is(err, apply.ErrSudo) || !strings.Contains(err.Error(), "did not accept the password") {
		t.Fatalf("err = %v", err)
	}
	sameLines(t, sudoLines(rec), []string{"done home-a", "fail home-b: " + refusal.Error()})
	sameLines(t, *log, []string{"prompt home-a", "prompt home-b"})
	if read || len(*ran) != 0 {
		t.Errorf("went on after the refusal: read %v, ran %v", read, *ran)
	}
}

// A host that cannot be reached up front is not a stop: its line waits, the
// run goes on, and the first step that reaches it reports it.
func TestAnUnreachableHostIsLeftToItsStep(t *testing.T) {
	down := &ui.Problem{Hint: "admin@home-a cannot be reached over ssh", Cause: apply.ErrUnreachable}
	ran := fakeConverge(t, map[string]error{"host prepare --site home-a": down})
	rec := recordConverge(t)
	fakeSudoHosts(t, nil, map[string]error{"home-a": down})
	err := convergeSudo(t, "--execute")
	if err == nil || !strings.Contains(err.Error(), "apply stopped at host prepare --site home-a") || !errors.Is(err, apply.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	lines := sudoLines(rec)
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "waiting home-a: not reached") {
		t.Errorf("sudo section:\n%s", strings.Join(lines, "\n"))
	}
	sameLines(t, lines[1:], []string{"done home-b", "done vm", "done watch"})
	// The other hosts are still prepared; nothing after prepare runs.
	sameLines(t, *ran, []string{"host prepare --site home-a", "host prepare --site home-b", "host prepare --site vm", "host prepare --site watch"})
}

// With --sudo=false there is nothing to ask, and no sudo section.
func TestConvergeWithoutSudoHasNoSudoSection(t *testing.T) {
	fakeConverge(t, nil)
	rec := recordConverge(t)
	fakeSudoHosts(t, nil, nil)
	saved := convergeHost
	// The real transport, whose sudo is what is checked; only the
	// architecture read is answered here.
	convergeHost = func(name string, site config.Site, sudo bool) apply.Transport {
		return archTransport{siteTransport(name, site, "", sudo), "amd64"}
	}
	t.Cleanup(func() { convergeHost = saved })
	if err := converge(t); err != nil {
		t.Fatal(err)
	}
	if rec.Has("section", "sudo") {
		t.Errorf("a sudo section without sudo:\n%s", rec.Lines())
	}
}

// withSpare is the fixture with one more site, spare, that nothing is
// pinned to, so site remove takes it out rather than refusing, and its
// secrets beside it.
func withSpare(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	spare := "  spare:\n    roles: [apps]\n    address: 10.44.0.6\n    ssh:\n      host: spare.local\n      user: ubuntu\n      keys: [alice]\n  vm:\n"
	body := strings.Replace(string(data), "  vm:\n", spare, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := os.ReadFile(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	key := "  spare:  { wireguard_private_key: \"REREREREREREREREREREREREREREREREREREREREREQ=\", heartbeat_token: \"4444444444444444444444444444eeee\" }\n  watch:"
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.yaml"), []byte(strings.Replace(string(secrets), "  watch:", key, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// site remove asks every host it reaches for its sudo password before it
// reads any of them, and never the host of a --host-gone site.
func TestSiteRemoveAsksEverySudoPasswordFirst(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"prompt home-a", "prompt home-b", "prompt spare", "prompt vm", "prompt watch"}},
		{[]string{"--host-gone"}, []string{"prompt home-a", "prompt home-b", "prompt vm", "prompt watch"}},
	} {
		savedSite, savedReg := removeSiteHost, registryHost
		log := &[]string{}
		removeSiteHost = func(name string, _ config.Site, _ bool) apply.Transport {
			return sudoFake{runningHost{failingHost{match: "\x00"}}, name, false, nil, log}
		}
		registryHost = func(string, config.Site, string, bool) registry.Runner { return &recordFake{} }
		captureStdout(t, func() {
			// The fakes answer no etcd, so the run stops at its first read
			// of the hosts; what matters is what came before it.
			_ = runSiteRemove(append([]string{"spare", "--config", withSpare(t)}, c.args...), strings.NewReader(""), &bytes.Buffer{})
		})
		removeSiteHost, registryHost = savedSite, savedReg
		if len(*log) <= len(c.want) {
			t.Fatalf("%v: no host was read after sudo: %v", c.args, *log)
		}
		sameLines(t, (*log)[:len(c.want)], c.want)
		for _, l := range (*log)[len(c.want):] {
			if strings.HasPrefix(l, "prompt") {
				t.Errorf("%v: asked after a host was read:\n%s", c.args, strings.Join(*log, "\n"))
			}
		}
	}
}

// site remove --force asks its one host first too.
func TestForcedSiteRemoveAsksSudoFirst(t *testing.T) {
	savedSite, savedReg := removeSiteHost, registryHost
	t.Cleanup(func() { removeSiteHost, registryHost = savedSite, savedReg })
	log := &[]string{}
	removeSiteHost = func(name string, _ config.Site, _ bool) apply.Transport {
		return sudoFake{runningHost{failingHost{match: "\x00"}}, name, false, nil, log}
	}
	registryHost = func(string, config.Site, string, bool) registry.Runner { return &recordFake{} }
	var err error
	captureStdout(t, func() {
		err = runSiteRemove([]string{"watch", "--force", "--config", fixtureConfig(), "--secrets", fixtureSecretsPath()}, strings.NewReader(""), &bytes.Buffer{})
	})
	if len(*log) < 2 || (*log)[0] != "prompt watch" || slices.Contains((*log)[1:], "prompt watch") {
		t.Errorf("err %v, log:\n%s", err, strings.Join(*log, "\n"))
	}
}
