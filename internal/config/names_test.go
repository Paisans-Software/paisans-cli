package config_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Each exported check is the rule the loader holds its field to, so a
// request read without a configuration is held to the same.
func TestNameChecks(t *testing.T) {
	for _, c := range []struct {
		check func(string) bool
		name  string
		good  []string
		bad   []string
	}{
		{config.ValidUserName, "user", []string{"paisans-f2a9", "ubuntu", "_svc"}, []string{"", "Root", "1abc", "a b", "a;b", "abcdefghijklmnopqrstuvwxyzabcdefg"}},
		{config.ValidOperatorName, "operator", []string{"alice", "0day", "a.b_c-d"}, []string{"", "Alice", "-a", "a b", "a/b"}},
		{config.ValidAppName, "app", []string{"talk", "status-2"}, []string{"", "Talk", "a.b", "a_b", "-a"}},
		{config.ValidHostname, "hostname", []string{"status.example.org"}, []string{"", "a b.example.org", "a;b", "-a.example.org", "a..example.org", strings.Repeat("a", 64) + ".org", "Status.example.org", "status", "status.example.org:443"}},
	} {
		for _, s := range c.good {
			if !c.check(s) {
				t.Errorf("%s %q refused", c.name, s)
			}
		}
		for _, s := range c.bad {
			if c.check(s) {
				t.Errorf("%s %q accepted", c.name, s)
			}
		}
	}
}

// A listen port is written as the request writes it, without leading zeros,
// so the loader accepts no spelling host request refuses.
func TestListenHostPortRefusesALeadingZero(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:08480", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1", "[::1]:8480"} {
		if _, _, ok := (config.WebServer{Listen: listen}).ListenHostPort(); ok {
			t.Errorf("%q accepted", listen)
		}
	}
	if host, port, ok := (config.WebServer{Listen: "127.0.0.1:8480"}).ListenHostPort(); !ok || host != "127.0.0.1" || port != 8480 {
		t.Errorf("127.0.0.1:8480 read as %q %d %v", host, port, ok)
	}
}

// The owner's web server is handed an app on its own host: loopback, or the
// site's own mesh address, and nothing else. validate and a request both ask
// this one function.
func TestProxiesToThisHost(t *testing.T) {
	for _, c := range []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:8480", true},
		{"127.1.2.3:8480", true},
		{"10.44.0.4:8480", true},
		{"10.44.0.1:8480", false},
		{"192.168.1.20:8480", false},
		{"100.101.102.103:8480", false},
		{"169.254.169.254:80", false},
		{"0.0.0.0:8480", false},
		{"203.0.113.20:8480", false},
		{"127.0.0.1:08480", false},
		{"127.0.0.1:0", false},
		{"::ffff:127.0.0.1:8480", false},
		{"[::1]:8480", false},
		{"localhost:8480", false},
	} {
		if got := config.ProxiesToThisHost(c.listen, "10.44.0.4"); got != c.want {
			t.Errorf("%q: got %v, want %v", c.listen, got, c.want)
		}
	}
}
