package doctor

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/selfcheck"
)

// ourCaddy is a site that answered as this deployment.
var ourCaddy = SelfCheckProbe{Site: "watch", Host: "health.example.org", Address: "203.0.113.20"}

func TestSelfChecks(t *testing.T) {
	cfg := fixture(t)
	conflict := ourCaddy
	conflict.Conflict, conflict.Err = 443, `443 on watch answers, but not as this deployment: status 200, body "hello"`
	silent := ourCaddy
	silent.Err = "443 on watch does not answer https://health.example.org/.well-known/paisans/f2a9 through 203.0.113.20: connection refused"
	fs := SelfChecks(cfg, []SelfCheckProbe{ourCaddy, {Site: "vm", Unchecked: "no public_address to ask through"}, conflict, silent})
	if len(fs) != 4 {
		t.Fatalf("want four findings:\n%s", dump(fs))
	}
	for i, c := range []struct {
		level Level
		line  string
		more  []string
	}{
		{OK, "watch: answers as this deployment on 443 and 80, asked for health.example.org through 203.0.113.20", nil},
		{Warn, "vm: not asked from outside: no public_address to ask through", []string{"/.well-known/paisans/f2a9"}},
		{Fail, `watch: 443 on watch answers, but not as this deployment: status 200, body "hello"`, []string{"/.well-known/paisans/f2a9", "`sudo ss -Hltnp`", "web_server.managed_by: external", "`paisans gateway move`"}},
		{Fail, "watch: 443 on watch does not answer", []string{"hairpin NAT"}},
	} {
		f := fs[i]
		if f.Level != c.level || !strings.HasPrefix(f.Line, c.line) {
			t.Errorf("%d: got %v %q", i, f.Level, f.Line)
		}
		text := strings.Join(f.More, "\n")
		for _, want := range c.more {
			if !strings.Contains(text, want) {
				t.Errorf("%d lacks %q:\n%s", i, want, text)
			}
		}
	}
}

// When 443 answers as this deployment and 80 does not, the advice names
// the HTTP request that got no answer.
func TestSelfCheckNamesThePortThatWasSilent(t *testing.T) {
	cfg := fixture(t)
	silent := ourCaddy
	silent.Silent, silent.URL = 80, "http://health.example.org/.well-known/paisans/f2a9"
	silent.Err = "80 on watch does not answer http://health.example.org/.well-known/paisans/f2a9 through 203.0.113.20: connection refused"
	for _, down := range []bool{false, true} {
		silent.Down = down
		f := SelfChecks(cfg, []SelfCheckProbe{silent})[0]
		text := strings.Join(f.More, "\n")
		if !strings.Contains(text, "203.0.113.20 for http://health.example.org/.well-known/paisans/f2a9 from this workstation, once, and nothing answered") || strings.Contains(text, "https://") {
			t.Errorf("down %v:\n%s", down, text)
		}
	}
}

// A refusal and a timeout each get their own advice: a refusal points at
// what holds the address, a timeout at what lies on the way.
func TestSelfCheckAdvisesByHowThePortWasSilent(t *testing.T) {
	cfg := fixture(t)
	for name, c := range map[string]struct {
		why       selfcheck.Silence
		want, not []string
	}{
		"refused": {selfcheck.Refused,
			[]string{"203.0.113.20 for https://health.example.org/.well-known/paisans/f2a9 from this workstation, once, and the connection was refused", "nothing listens on 443", "containers watch"},
			[]string{"nothing answered"}},
		"timed out": {selfcheck.TimedOut,
			[]string{"203.0.113.20 for https://health.example.org/.well-known/paisans/f2a9 from this workstation, once, and nothing answered before it timed out", "dropped", "hairpin NAT", "forwards 80 and 443 to watch"},
			nil},
		"unexplained": {selfcheck.Unexplained,
			[]string{"once, and nothing answered.", "hairpin NAT"},
			nil},
	} {
		silent := ourCaddy
		silent.Silent, silent.Why = 443, c.why
		silent.Err = "443 on watch does not answer https://health.example.org/.well-known/paisans/f2a9 through 203.0.113.20: whatever"
		f := SelfChecks(cfg, []SelfCheckProbe{silent})[0]
		text := strings.Join(f.More, "\n")
		if f.Level != Fail {
			t.Errorf("%s: level %v", name, f.Level)
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s lacks %q:\n%s", name, w, text)
			}
		}
		for _, w := range c.not {
			if strings.Contains(text, w) {
				t.Errorf("%s has %q:\n%s", name, w, text)
			}
		}
	}
}
