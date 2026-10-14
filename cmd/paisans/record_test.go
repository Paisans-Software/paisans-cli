package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// recordFake is a gateway that runs the deployment record's write command as
// the shell would: refused on a changed hash, else the file replaced.
type recordFake struct {
	files map[string]string
	ran   []string
	down  bool
}

var (
	recordPathRe = regexp.MustCompile(`f='([^']+)'`)
	recordSumRe  = regexp.MustCompile(`\[ "\$cur" = '([^']*)' \]`)
	recordDataRe = regexp.MustCompile(`printf %s '([^']*)' \| base64 -d`)
)

func (h *recordFake) Describe() string { return "ubuntu@vm.example.org" }

func (h *recordFake) ReadFile(p string) (string, bool, error) {
	if h.down {
		return "", false, errors.New("ssh: connect to host vm.example.org port 22: Operation timed out")
	}
	c, ok := h.files[p]
	return c, ok, nil
}

func (h *recordFake) Run(command string) (string, error) {
	if h.down {
		return "", errors.New("ssh: connect to host vm.example.org port 22: Operation timed out")
	}
	if out, ok := userRead(h.files, command); ok {
		return out, nil
	}
	h.ran = append(h.ran, command)
	path := recordPathRe.FindStringSubmatch(command)[1]
	cur := "none"
	if c, ok := h.files[path]; ok {
		s := sha256.Sum256([]byte(c))
		cur = hex.EncodeToString(s[:])
	}
	if recordSumRe.FindStringSubmatch(command)[1] != cur {
		return deployrecord.ChangedMarker + "\n", errors.New("exit status 1")
	}
	data, _ := base64.StdEncoding.DecodeString(recordDataRe.FindStringSubmatch(command)[1])
	h.files[path] = string(data)
	return "", nil
}

// userReadRe is the path in deployrecord.ReadAsUser's command.
var userReadRe = regexp.MustCompile(`^if \[ -f '([^']+)' \] && \[ -r `)

// userRead answers deployrecord.ReadAsUser's command from files, as the ssh
// user would see a record the write left readable, an empty one as absent.
// ok is false for any other command.
func userRead(files map[string]string, command string) (string, bool) {
	m := userReadRe.FindStringSubmatch(command)
	if m == nil {
		return "", false
	}
	if c := files[m[1]]; c != "" {
		return c, true
	}
	return "__PAISANS_ABSENT__\n", true
}

func TestApplyRecordsTheDeploymentOnAGateway(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	gw := &recordFake{files: map[string]string{}}
	if err := recordApplied(&ui.Recorder{}, cfg, "vm", func(string) registry.Runner { return gw }); err != nil {
		t.Fatal(err)
	}
	rec, found, _ := readRecord(gw, cfg.Deployment())
	if !found || !rec.Lists("sites", "home-b") || !rec.Lists("apps", "talk") {
		t.Errorf("%+v %v", rec, found)
	}
	other := &recordFake{files: map[string]string{}}
	if err := recordApplied(&ui.Recorder{}, cfg, "home-b", func(string) registry.Runner { return other }); err != nil || len(other.files) != 0 || len(other.ran) != 0 {
		t.Errorf("a non gateway was written: %v %v", err, other.files)
	}
}

func TestForgetInRecordsReachesEveryGatewayAndWarnsForOneDown(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm", "monitor-a"}})}}
	saved := registryHost
	registryHost = func(name string, _ config.Site, _ string, _ bool) registry.Runner { return vm }
	t.Cleanup(func() { registryHost = saved })
	rec := &ui.Recorder{}
	forgetInRecords(rec, cfg, deployrecord.Record{Sites: []string{"monitor-a"}}, "monitor-a", true, true)
	if r, _, _ := readRecord(vm, cfg.Deployment()); r.Lists("sites", "monitor-a") {
		t.Error("vm still lists monitor-a")
	}
	vm.down = true
	rec = &ui.Recorder{}
	forgetInRecords(rec, cfg, deployrecord.Record{Sites: []string{"monitor-b"}}, "monitor-b", true, true)
	if !strings.Contains(rec.Lines(), "vm missed this change to the deployment record") {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
}

func TestForgetAppCleansTheRecordWhenNothingElseIsLeft(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	deployed := deployrecord.Record{Apps: []string{"talk", "uptime"}, PocketIDGroups: []string{"members", "retired"}}
	names := appRecordNames(cfg, "uptime", deployed)
	if strings.Join(names.Apps, ",") != "uptime" || strings.Join(names.PocketIDGroups, ",") != "retired" {
		t.Errorf("%+v", names)
	}
}

// A dry run that would take a name out of a record says so, so a removal
// with nothing else left still points at --execute.
func TestForgetInRecordsDryRunSaysWhetherARecordListsIt(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm", "monitor-a"}})}}
	saved := registryHost
	registryHost = func(string, config.Site, string, bool) registry.Runner { return vm }
	t.Cleanup(func() { registryHost = saved })
	if !forgetInRecords(&ui.Recorder{}, cfg, deployrecord.Record{Sites: []string{"monitor-a"}}, "monitor-a", false, true) {
		t.Error("a listed name is not reported as left")
	}
	if forgetInRecords(&ui.Recorder{}, cfg, deployrecord.Record{Sites: []string{"monitor-b"}}, "monitor-b", false, true) {
		t.Error("an unlisted name is reported as left")
	}
	if len(vm.ran) != 0 {
		t.Error("a dry run wrote")
	}
}

// Reading the record, and writing it, are steps, so a spinner shows while
// each gateway is reached over ssh.
func TestForgetInRecordsReachesGatewaysInAStep(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm"}})}}
	saved := registryHost
	registryHost = func(string, config.Site, string, bool) registry.Runner { return vm }
	t.Cleanup(func() { registryHost = saved })
	rec := &ui.Recorder{}
	forgetInRecords(rec, cfg, deployrecord.Record{Sites: []string{"monitor-b"}}, "monitor-b", false, true)
	if !rec.Has("done", "read the deployment record") {
		t.Errorf("the dry run's read is not a step:\n%s", rec.Lines())
	}
	rec = &ui.Recorder{}
	forgetInRecords(rec, cfg, deployrecord.Record{Sites: []string{"monitor-b"}}, "monitor-b", true, true)
	if !rec.Has("done", "forget monitor-b in the deployment record") {
		t.Errorf("a run with nothing to forget has no step:\n%s", rec.Lines())
	}
}

// A malformed record stops a gateway's apply with the way out, not "run it
// again".
func TestApplyNamesTheWayOutOfAMalformedRecord(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	gw := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): "{"}}
	err := recordApplied(&ui.Recorder{}, cfg, "vm", func(string) registry.Runner { return gw })
	if err == nil || !strings.Contains(err.Error(), "rm "+deployrecord.Path(cfg.Deployment())) || strings.Contains(err.Error(), "run it again") {
		t.Errorf("err = %v", err)
	}
}

// The record lines sit under a section of their own, not the last site's.
func TestForgetInRecordsHasItsOwnSection(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"monitor-a"}})}}
	saved := registryHost
	registryHost = func(string, config.Site, string, bool) registry.Runner { return vm }
	t.Cleanup(func() { registryHost = saved })
	rec := &ui.Recorder{}
	forgetInRecords(rec, cfg, deployrecord.Record{Sites: []string{"monitor-a"}}, "monitor-a", false, true)
	s, i := rec.Index("section", "deployment record"), rec.Index("pending", "forget monitor-a")
	if s < 0 || i < s {
		t.Errorf("section %d, pending %d:\n%s", s, i, rec.Lines())
	}
}

// withGateway is cfg with one more site, name, holding the gateway role.
func withGateway(cfg *config.Config, name string) *config.Config {
	out := *cfg
	out.Sites = map[string]config.Site{}
	for k, v := range cfg.Sites {
		out.Sites[k] = v
	}
	out.Sites[name] = config.Site{Roles: []config.Role{config.RoleGateway}}
	return &out
}

// apply of a gateway writes every gateway it reaches, and reports one down.
func TestApplyRecordsOnEveryGatewayAndReportsOneDown(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{}}
	down := &recordFake{files: map[string]string{}, down: true}
	rec := &ui.Recorder{}
	hosts := func(gw string) registry.Runner { return map[string]registry.Runner{"vm": vm, "vm2": down}[gw] }
	if err := recordApplied(rec, withGateway(cfg, "vm2"), "vm", hosts); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := readRecord(vm, cfg.Deployment()); !found {
		t.Error("vm was not written")
	}
	if !strings.Contains(rec.Lines(), "vm2 missed this change") {
		t.Errorf("no warning:\n%s", rec.Lines())
	}
}

// With every gateway down the step does not claim the record was updated.
func TestARecordChangeThatReachedNoGatewaySaysSo(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	down := &recordFake{files: map[string]string{}, down: true}
	rec := &ui.Recorder{}
	if err := recordApplied(rec, cfg, "vm", func(string) registry.Runner { return down }); err != nil {
		t.Fatal(err)
	}
	if rec.Has("done", "record the deployment") {
		t.Errorf("the step ended as done:\n%s", rec.Lines())
	}
	if !rec.Has("fail", "record the deployment") {
		t.Errorf("the step did not say it reached no gateway:\n%s", rec.Lines())
	}
}

// readRecord is the deployed names the record on h holds, and whether it
// holds one.
func readRecord(h registry.Runner, d deployment.Deployment) (deployrecord.Record, bool, error) {
	r, found, missing := deployrecord.Gather(map[string]registry.Runner{"gw": h}, d)
	if err := missing["gw"]; err != nil && !errors.Is(err, deployrecord.ErrNoRecord) {
		return r, false, err
	}
	return r, found > 0, nil
}

func TestAChangedRecordSaysRunAgain(t *testing.T) {
	rec := &ui.Recorder{}
	reportMissed(rec, deployrecord.Result{Missed: map[string]error{"vm": fmt.Errorf("ubuntu@vm.example.org: %w", deployrecord.ErrChanged)}})
	if !strings.Contains(rec.Lines(), "run this command again") || strings.Contains(rec.Lines(), "brought up to date") {
		t.Errorf("%s", rec.Lines())
	}
}

func TestConfirmWordAsksOnALineOfItsOwn(t *testing.T) {
	saved := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = saved })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("home-a\n")
	w.Close()
	defer r.Close()
	var out bytes.Buffer
	what := "This cleans deployment 00000000-0000-4000-8000-000000000000 (example.org), site home-a, off ubuntu@192.0.2.10:22, and nothing says whether it still runs."
	if err := confirmWord(r, &out, "home-a", what); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if last := lines[len(lines)-1]; last != "Type 'home-a' (no quotes) to proceed: " {
		t.Errorf("the prompt's line is %q, want it alone", last)
	}
	if lines[len(lines)-2] != what {
		t.Errorf("the explanation is %q, want it whole on one line for the terminal to wrap", lines[len(lines)-2])
	}
}

// deniedFake is a gateway whose record the ssh user may not read, one
// written with other modes: the user's read is denied and only ReadFile, the
// sudo read, gives it.
type deniedFake struct{ *recordFake }

func (d deniedFake) Run(command string) (string, error) {
	if userReadRe.MatchString(command) {
		return "__PAISANS_DENIED__\n", nil
	}
	return d.recordFake.Run(command)
}

// sudoLog makes registryHost answer with gw for every site, logging whether
// each was reached through sudo.
func sudoLog(t *testing.T, gw registry.Runner) *[]string {
	t.Helper()
	log := &[]string{}
	saved := registryHost
	registryHost = func(name string, _ config.Site, _ string, sudo bool) registry.Runner {
		*log = append(*log, fmt.Sprintf("%s sudo=%v", name, sudo))
		return gw
	}
	t.Cleanup(func() { registryHost = saved })
	return log
}

// An apply of a site that is not a gateway reads the gateway's record as the
// ssh user, so it asks for no gateway's sudo password.
func TestApplyOfANonGatewayReadsTheRecordWithoutSudo(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := &recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm", "monitor-a"}})}}
	log := sudoLog(t, vm)
	rec := recordForWarnings(&ui.Recorder{}, cfg, applyRecordHosts(cfg, "watch", "", &recordFake{}, true))
	if rec == nil || !rec.Lists("sites", "monitor-a") {
		t.Fatalf("record %+v", rec)
	}
	if strings.Join(*log, ",") != "vm sudo=false" {
		t.Errorf("reached %v", *log)
	}
}

// A record the ssh user may not read is read through sudo, as the command
// reaches the gateway.
func TestADeniedRecordIsReadThroughSudo(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	vm := deniedFake{&recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm", "monitor-a"}})}}}
	log := sudoLog(t, vm)
	rec := recordForWarnings(&ui.Recorder{}, cfg, applyRecordHosts(cfg, "watch", "", &recordFake{}, true))
	if rec == nil || !rec.Lists("sites", "monitor-a") {
		t.Fatalf("record %+v", rec)
	}
	if strings.Join(*log, ",") != "vm sudo=false,vm sudo=true" {
		t.Errorf("reached %v", *log)
	}
}

// The applied gateway's own sudo read goes through the apply's transport.
func TestApplyOfAGatewayFallsBackToItsOwnTransport(t *testing.T) {
	cfg, _ := config.Load(fixtureConfig())
	own := deniedFake{&recordFake{files: map[string]string{deployrecord.Path(cfg.Deployment()): deployrecord.Encode(deployrecord.Record{Sites: []string{"vm", "monitor-a"}})}}}
	log := sudoLog(t, own)
	rec := recordForWarnings(&ui.Recorder{}, cfg, applyRecordHosts(cfg, "vm", "", own, true))
	if rec == nil || !rec.Lists("sites", "monitor-a") {
		t.Fatalf("record %+v", rec)
	}
	if strings.Join(*log, ",") != "vm sudo=false" {
		t.Errorf("reached %v", *log)
	}
}
