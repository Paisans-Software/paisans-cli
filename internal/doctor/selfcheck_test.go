package doctor

import (
	"strings"
	"testing"
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
