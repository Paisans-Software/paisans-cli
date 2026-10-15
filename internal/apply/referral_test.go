package apply_test

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/diagnosis"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// gateHost is a stack with one restarting service, whose log tail is logs.
type gateHost struct {
	psHost
	service, logs string
}

func (h gateHost) Run(command string) (string, error) {
	if strings.Contains(command, " logs ") {
		return h.logs, nil
	}
	return fmt.Sprintf(`{"Service":%q,"State":"restarting","ExitCode":1}`, h.service), nil
}

// stagingTLS is oauth2-proxy's line from the staging apply that stopped at
// `wait for gate`, with the domain replaced by example.org.
const stagingTLS = `oauth2-proxy-1  | [2026/10/15 09:12:44] [provider.go:55] Get "https://id.example.org/.well-known/openid-configuration": remote error: tls: internal error`

var gatewayServes = diagnosis.Served{"id.example.org": "vm"}

// unhealthyOutput is what the operator reads by default when the gate fails.
func unhealthyOutput(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("a restarting service passed the gate")
	}
	var b bytes.Buffer
	ui.PrintError(&b, err, false)
	return b.String()
}

func TestATLSFailureOnAServedHostnameSaysCaddyHasNoCertificate(t *testing.T) {
	err := apply.WaitServing(apply.Fixture, "home-a", "gate", gateHost{service: "oauth2-proxy", logs: stagingTLS + "\n"}, "The apply stopped here", gatewayServes)
	out := unhealthyOutput(t, err)
	for _, want := range []string{
		"stack gate is not healthy",
		"oauth2-proxy: [2026/10/15 09:12:44] [provider.go:55] Get",
		"oauth2-proxy: Caddy on vm has no certificate for id.example.org, so it refuses TLS for it. Run paisans doctor to see why it could not get one.",
		"The apply stopped here",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("default output lacks %q:\n%s", want, out)
		}
	}
	// The referral comes before what was and was not done, which stays last.
	if strings.Index(out, "Run paisans doctor") > strings.Index(out, "The apply stopped here") {
		t.Errorf("the referral follows the stop:\n%s", out)
	}
}

// The same line for a hostname Paisans does not serve gets no referral: the
// output is today's, cause and stop alone.
func TestATLSFailureOnAnotherHostnameIsReportedAsToday(t *testing.T) {
	logs := strings.ReplaceAll(stagingTLS, "id.example.org", "login.example.net") + "\n"
	served := apply.WaitServing(apply.Fixture, "home-a", "gate", gateHost{service: "oauth2-proxy", logs: logs}, "The apply stopped here", gatewayServes)
	plain := apply.WaitHealthy(apply.Fixture, "home-a", "gate", gateHost{service: "oauth2-proxy", logs: logs}, "The apply stopped here")
	if strings.Contains(served.Error(), "paisans doctor") {
		t.Errorf("a referral for a hostname nobody here serves:\n%v", served)
	}
	if served.Error() != plain.Error() {
		t.Errorf("an unrecognised failure changed its text:\n%v\nwant\n%v", served, plain)
	}
	if _, explain := ui.Describe(plain); !strings.HasPrefix(explain, "oauth2-proxy: [2026/10/15") || !strings.HasSuffix(explain, "The apply stopped here") || strings.Count(explain, "\n") != 1 {
		t.Errorf("today's explanation is the cause line and the stop:\n%s", explain)
	}
}

func TestADatabaseFailureRefersToPatroni(t *testing.T) {
	err := apply.WaitServing(apply.Fixture, "home-a", "mbin", gateHost{service: "php", logs: "php-1  | SQLSTATE[08006] [7] could not connect to server: Connection refused\n"}, "The apply stopped here", gatewayServes)
	out := unhealthyOutput(t, err)
	if !strings.Contains(out, "php: It cannot reach its database. Run paisans doctor, whose Patroni check says whether there is a primary.") {
		t.Errorf("no database referral:\n%s", out)
	}
}

// What the gate already printed for etcd, a cause doctor has no check
// naming, is unchanged.
func TestAnUnrecognisedFailureKeepsTodaysText(t *testing.T) {
	served := apply.WaitServing(apply.Fixture, "home-a", "infra", logsHost{logs: etcdLogs}, "The apply stopped here", gatewayServes)
	plain := apply.WaitHealthy(apply.Fixture, "home-a", "infra", logsHost{logs: etcdLogs}, "The apply stopped here")
	if served.Error() != plain.Error() || strings.Contains(served.Error(), "doctor") {
		t.Errorf("got\n%v\nwant\n%v", served, plain)
	}
}

// The hostnames a Caddy site serves: every gateway hostname to the gateway,
// and a monitor's own to the monitor whose Caddy serves it.
func TestServedHostnamesAreTheGatewaysAndTheMonitors(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := apply.ServedHostnames(cfg)
	for host, site := range map[string]string{"id.example.org": "vm", "talk.example.org": "vm", "health.example.org": "watch"} {
		if got[host] != site {
			t.Errorf("%s: served by %q, want %q (all: %v)", host, got[host], site, got)
		}
	}
}

// An ssh failure names its site, so a caller that knows nothing of the
// transport can refer the operator to doctor for that site.
func TestAnUnreachableErrorNamesItsSite(t *testing.T) {
	err := fmt.Errorf("reading: %w", &ui.Problem{Hint: "x cannot be reached over ssh", Cause: &apply.UnreachableError{Site: "home-b", Err: apply.ErrUnreachable}})
	if site := apply.UnreachableSite(err); site != "home-b" {
		t.Errorf("site %q", site)
	}
	if !errors.Is(err, apply.ErrUnreachable) {
		t.Error("not ErrUnreachable")
	}
	if site := apply.UnreachableSite(errors.New("boom")); site != "" {
		t.Errorf("site %q from an unrelated error", site)
	}
}

// A real transport's ssh failure carries the site it was built for.
func TestATransportThatCannotConnectNamesItsSite(t *testing.T) {
	s := &sshScript{answers: []sshAnswer{{sshTimeout, 255}, {sshTimeout, 255}, {sshTimeout, 255}}}
	s.install(t)
	tr := sudoTransport(apply.NewSudoAuth(nil))
	tr.Site = "home-b"
	_, err := tr.Run("true")
	if site := apply.UnreachableSite(err); site != "home-b" {
		t.Fatalf("site %q from %v", site, err)
	}
}
