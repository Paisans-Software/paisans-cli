package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/garage"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// garage-key-is-malformed has to stop `render`, not only `init`: `render` and
// `deployment apply` both call config.LoadSecrets directly and never call
// secretsgen.Fill, so a key hand edited into the secrets file after the last
// `init` would otherwise reach the rendered artifacts, and from there a host,
// with nothing ever having looked at it. This test proves the wiring, not
// just the check: it drives runRender exactly as the CLI would, over a
// fixture where the configuration is otherwise fine and only the secret is
// broken.
func TestRunRenderRefusesAMalformedGarageKey(t *testing.T) {
	out := t.TempDir()
	err := runRender([]string{
		"--config", filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"),
		"--secrets", filepath.Join("testdata", "garage-key-is-malformed-secrets.yaml"),
		"--out", out,
	})
	if err == nil {
		t.Fatal("expected render to refuse a configuration with a malformed Garage key")
	}
	if !strings.Contains(err.Error(), "garage-key-is-malformed") {
		t.Fatalf("the error should name the rule garage-key-is-malformed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "blog.s3_access_key_id") {
		t.Fatalf("the error should name the offending field, got: %v", err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("render wrote %d file(s) to --out before refusing the key", len(entries))
	}
}

// storage init is the path CheckGarageKeys exists for: a malformed key that
// reaches `garage key import` fails partway through provisioning, after
// earlier keys in the same run are already imported and cannot be imported
// again. This proves the check runs before anything reaches a transport, by
// using an ssh destination that cannot be dialled and confirming the error
// still names the malformed key rather than a connection failure.
func TestRunStorageInitRefusesAMalformedGarageKeyBeforeReachingAHost(t *testing.T) {
	err := runStorageInit([]string{
		"--config", filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"),
		"--secrets", filepath.Join("testdata", "garage-key-is-malformed-secrets.yaml"),
		"--site", "home-a",
		"--ssh", "nonexistent.invalid",
	})
	if err == nil {
		t.Fatal("expected storage init to refuse a configuration with a malformed Garage key")
	}
	if !strings.Contains(err.Error(), "garage-key-is-malformed") {
		t.Fatalf("the error should name the rule garage-key-is-malformed, got: %v", err)
	}
	if strings.Contains(err.Error(), "nonexistent.invalid") {
		t.Fatalf("the check should stop before ssh is ever tried, got: %v", err)
	}
}

// Plan.Show is the one thing that puts a provisioning plan on the operator's
// terminal, and a key import step's command carries that app's S3 secret as
// a positional argument because `garage key import` takes no other form.
// Reporting Step.Command would put the secret in a scrollback buffer and a
// log every time an operator ran a dry run, which is the ordinary case
// rather than a failure. It reports Describe, and this test is what keeps it
// that way: a future field added to the plan's output has to be checked here
// before it reaches a screen. Reporting a run holds to the same rule.
func TestGaragePlanReportsNoSecret(t *testing.T) {
	const secret = "e1d9a0a2e1d9a0a2e1d9a0a2e1d9a0a2e1d9a0a2e1d9a0a2e1d9a0a2e1d9a0a2"
	plan := &garage.Plan{
		Site: "home-a",
		Steps: []garage.Step{{
			Title:    "import key for talk",
			Describe: "import the S3 key for talk",
			Command:  "docker compose -f /srv/paisans/f2a9/infra/compose.yaml exec -T garage /garage key import GKfacadefacadefacadefacade " + secret + " --yes -n talk",
			Secret:   secret,
		}},
		Present: []string{"bucket: talk-uploads already exists"},
	}

	rec := &ui.Recorder{Verbose_: true}
	plan.Show(rec)
	if err := garage.Report(plan, echoHost{}, rec); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(rec.Lines(), secret) {
		t.Errorf("the reporter was given an S3 secret:\n%s", rec.Lines())
	}
	if !rec.Has("pending", "import key for talk") || !rec.Has("detail", "import the S3 key for talk") {
		t.Errorf("the plan must still say what the step does:\n%s", rec.Lines())
	}
	if !rec.Has("done", "bucket: talk-uploads already exists") {
		t.Errorf("the plan must still report what was already there:\n%s", rec.Lines())
	}
	if !rec.Has("done", "import key for talk") {
		t.Errorf("the run must report the step done:\n%s", rec.Lines())
	}
}

type echoHost struct{}

func (echoHost) Run(string) (string, error) { return "", nil }
func (echoHost) Describe() string           { return "ubuntu@home-a" }

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written, restoring os.Stdout afterwards. printGaragePlan writes to
// os.Stdout directly, so this is the only way to see what an operator sees.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	w.Close()
	return <-done
}

// dns init has to refuse offline wherever it can: a configuration that cannot
// name its records, or a provider whose record management does not exist,
// must stop before the secrets are used to contact anything. The fixture
// declares desec and gives the gateway no public_address.
func TestRunDNSInitRefusesBeforeContactingAProvider(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml")
	secrets := fixtureSecretsPath()

	err := runDNSInit([]string{"--config", fixture, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "sites.vm.public_address is not set") {
		t.Fatalf("want a refusal naming the missing public_address, got %v", err)
	}

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	withAddress := strings.Replace(string(data), "    endpoint: vm.example.org:51820\n",
		"    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	if withAddress == string(data) {
		t.Fatal("the fixture no longer has the line this test edits")
	}
	dir := t.TempDir()
	edited := filepath.Join(dir, "paisans-stack.yaml")
	if err := os.WriteFile(edited, []byte(withAddress), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runDNSInit([]string{"--config", edited, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "desec record management is not implemented yet") {
		t.Fatalf("want the unimplemented provider refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "fixture-not-a-secret-desec") {
		t.Fatalf("the token leaked into an error: %v", err)
	}

	// dns prune shares the same setup, so it refuses in the same places.
	err = runDNSPrune([]string{"--config", fixture, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "sites.vm.public_address is not set") {
		t.Fatalf("prune: want a refusal naming the missing public_address, got %v", err)
	}
	err = runDNSPrune([]string{"--config", edited, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "desec record management is not implemented yet") {
		t.Fatalf("prune: want the unimplemented provider refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "fixture-not-a-secret-desec") {
		t.Fatalf("prune: the token leaked into an error: %v", err)
	}
}

// dns init and dns prune check the token from this workstation before the
// first records call, so a token filtered away from it is named as that,
// with the address, rather than as a bare HTTP 403. The check is handed the
// token from the secrets file, and nothing after it runs.
func TestDNSCommandsCheckTheTokenFromThisWorkstation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "    endpoint: vm.example.org:51820\n",
		"    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	edited = strings.Replace(edited, "  provider: desec\n", "  provider: cloudflare\n", 1)
	if !strings.Contains(edited, "provider: cloudflare") || !strings.Contains(edited, "public_address") {
		t.Fatal("the fixture no longer has the lines this test edits")
	}
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	saved := workstationTokenCheck
	defer func() { workstationTokenCheck = saved }()
	var checked []dns.TokenCheck
	var tokens []string
	workstationTokenCheck = func(_ context.Context, check dns.TokenCheck, token string) dns.TokenResult {
		checked, tokens = append(checked, check), append(tokens, token)
		return dns.TokenResult{
			Status: dns.TokenFailed, Place: check.Place, Code: 9109, Address: "203.0.113.7",
			Finding: "cloudflare refuses the DNS token from " + check.From + " (203.0.113.7)",
			Advice:  "Add the address to the token's Client IP Address Filtering, or remove the filter.",
		}
	}
	for name, run := range map[string]func([]string) error{"dns init": runDNSInit, "dns prune": runDNSPrune} {
		err := run([]string{"--config", path, "--secrets", fixtureSecretsPath()})
		if err == nil || !strings.Contains(err.Error(), "cloudflare refuses the DNS token from this workstation (203.0.113.7)") {
			t.Errorf("%s: want the workstation's refusal, got %v", name, err)
		}
	}
	if len(checked) != 2 {
		t.Fatalf("want one check per command, got %d", len(checked))
	}
	for i, c := range checked {
		if c.Provider != "cloudflare" || c.Domain != "example.org" || c.Place != dns.WorkstationPlace || tokens[i] != "fixture-not-a-secret-desec" {
			t.Errorf("checked %+v with %q", c, tokens[i])
		}
	}
}

// Inside apply, dns init does not repeat the configuration's findings, which
// apply's own configuration section already showed. Run by hand, it shows
// them. The fixture's garage is consistency dangerous, which warns; desec
// stops the command after the findings and before any provider is called.
func TestDNSInitInsideApplyLeavesTheFindingsToIt(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "render", "testdata", "deployment.yaml")
	secrets := fixtureSecretsPath()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "    endpoint: vm.example.org:51820\n",
		"    endpoint: vm.example.org:51820\n    public_address: 203.0.113.10\n", 1)
	edited = strings.Replace(edited, "    replication: 2\n", "    replication: 2\n    consistency: dangerous\n", 1)
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, inside := range []bool{false, true} {
		rec := &ui.Recorder{}
		savedReporter, savedInside := reporterOverride, insideApply
		reporterOverride, insideApply = rec, inside
		_ = runDNSInit([]string{"--config", path, "--secrets", secrets})
		reporterOverride, insideApply = savedReporter, savedInside
		if shown := rec.Has("warn", "garage consistency is dangerous"); shown == inside {
			t.Errorf("inside apply %t: the garage warning shown %t, events %v", inside, shown, rec.Events)
		}
	}
}

// dns init's closing line names only the counts that are not zero.
func TestDNSInitResultOmitsZeroCounts(t *testing.T) {
	for _, c := range []struct {
		updates, creates int
		want             string
	}{
		{0, 3, "Created 3 records at cloudflare."},
		{2, 0, "Updated 2 records at cloudflare."},
		{1, 1, "Updated 1 record and created 1 record at cloudflare."},
		{0, 0, "Created 0 records at cloudflare."},
	} {
		if got := dnsInitResult(c.updates, c.creates, "cloudflare"); got != c.want {
			t.Errorf("%d updates, %d creates: %q, want %q", c.updates, c.creates, got, c.want)
		}
	}
}
