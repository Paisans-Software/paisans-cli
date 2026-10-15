package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetSSHUser(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"absent, in a block, with comments",
			"sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    ssh:\n      # reached on the LAN\n      host: home-a.local   # its mDNS name\n      keys: [alice]\n",
			"sites:\n  # the home server\n  home-a:\n    roles: [data, apps]\n    ssh:\n      # reached on the LAN\n      user: paisans-f2a9\n      host: home-a.local   # its mDNS name\n      keys: [alice]\n",
		},
		{
			"absent, in a flow mapping",
			"sites:\n  home-a:\n    ssh: {host: home-a.local, keys: [alice]}\n",
			"sites:\n  home-a:\n    ssh: {user: paisans-f2a9, host: home-a.local, keys: [alice]}\n",
		},
		{
			"present, the same name",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n",
		},
		{
			"present, the same name quoted",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
		},
		{
			"written empty, double quoted",
			"sites:\n  home-a:\n    ssh:\n      user: \"\"\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"\n      keys: [alice]\n",
		},
		{
			"written empty, single quoted",
			"sites:\n  home-a:\n    ssh:\n      user: ''\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: 'paisans-f2a9'\n      keys: [alice]\n",
		},
		{
			"written as ~, with a comment",
			"sites:\n  home-a:\n    ssh:\n      user: ~   # filled in on first contact\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9   # filled in on first contact\n      keys: [alice]\n",
		},
		{
			"only the named site",
			"sites:\n  home-a:\n    ssh:\n      keys: [alice]\n  vm:\n    ssh:\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]\n  vm:\n    ssh:\n      keys: [alice]\n",
		},
		{
			"written empty, in a flow mapping",
			"sites:\n  home-a:\n    ssh: {user: \"\", host: home-a.local, keys: [alice]}\n",
			"sites:\n  home-a:\n    ssh: {user: \"paisans-f2a9\", host: home-a.local, keys: [alice]}\n",
		},
		{
			"written empty, quoted, with a trailing comment",
			"sites:\n  home-a:\n    ssh:\n      user: \"\"   # set on first login\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
		},
		{
			"present, quoted, with a trailing comment",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
			"sites:\n  home-a:\n    ssh:\n      user: \"paisans-f2a9\"   # set on first login\n      keys: [alice]\n",
		},
		{
			"absent, with CRLF line endings",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      keys: [alice]\r\n",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: paisans-f2a9\r\n      keys: [alice]\r\n",
		},
		{
			"written empty, with CRLF line endings",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: \"\"\r\n      keys: [alice]\r\n",
			"sites:\r\n  home-a:\r\n    ssh:\r\n      user: \"paisans-f2a9\"\r\n      keys: [alice]\r\n",
		},
		{
			"ssh last, with no final newline",
			"sites:\n  home-a:\n    roles: [data]\n    ssh:\n      keys: [alice]",
			"sites:\n  home-a:\n    roles: [data]\n    ssh:\n      user: paisans-f2a9\n      keys: [alice]",
		},
		{
			"ssh last, in a flow mapping, with no final newline",
			"sites:\n  home-a:\n    ssh: {keys: [alice]}",
			"sites:\n  home-a:\n    ssh: {user: paisans-f2a9, keys: [alice]}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := setSSHUser([]byte(c.in), "home-a", "paisans-f2a9")
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, c.want)
			}
		})
	}
}

func TestSetSSHUserRefuses(t *testing.T) {
	cases := []struct{ name, in, site, user, want string }{
		{"another name", "sites:\n  home-a:\n    ssh:\n      user: ubuntu\n      keys: [alice]\n", "home-a", "paisans-f2a9", "sites.home-a.ssh.user already names ubuntu"},
		{"a user written with no value", "sites:\n  home-a:\n    ssh:\n      user:\n      keys: [alice]\n", "home-a", "paisans-f2a9", "written with no value"},
		{"a user written with no value, in a flow mapping", "sites:\n  home-a:\n    ssh: {user: , keys: [alice]}\n", "home-a", "paisans-f2a9", "written with no value"},
		{"an undeclared site", "sites:\n  home-a:\n    ssh:\n      keys: [alice]\n", "home-z", "paisans-f2a9", "sites.home-z is not declared"},
		{"a site with no ssh section", "sites:\n  home-a:\n    roles: [data]\n", "home-a", "paisans-f2a9", "sites.home-a.ssh is not a section"},
		{"a name this toolkit refuses", "sites:\n  home-a:\n    ssh:\n      keys: [alice]\n", "home-a", "bob; rm", "is not a user name this toolkit accepts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := setSSHUser([]byte(c.in), c.site, c.user); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

// A name YAML would read as another type is written quoted, so the file
// loads back as the same string.
func TestSetSSHUserQuotesANameYAMLWouldReadAsAnotherType(t *testing.T) {
	for _, name := range []string{"null", "true"} {
		t.Run(name, func(t *testing.T) {
			out, err := setSSHUser([]byte("sites:\n  home-a:\n    ssh:\n      keys: [alice]\n"), "home-a", name)
			if err != nil {
				t.Fatal(err)
			}
			if want := "sites:\n  home-a:\n    ssh:\n      user: \"" + name + "\"\n      keys: [alice]\n"; string(out) != want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, want)
			}
			var back struct {
				Sites map[string]struct {
					SSH struct {
						User string `yaml:"user"`
					} `yaml:"ssh"`
				} `yaml:"sites"`
			}
			if err := yaml.Unmarshal(out, &back); err != nil || back.Sites["home-a"].SSH.User != name {
				t.Fatalf("read back as %q, %v", back.Sites["home-a"].SSH.User, err)
			}
		})
	}
}

// A user written as null names nobody, so asking for the name null fills it
// in; only a string that already is the name counts as the same name.
func TestSetSSHUserFillsANullUserNamedNull(t *testing.T) {
	out, err := setSSHUser([]byte("sites:\n  home-a:\n    ssh:\n      user: null\n      keys: [alice]\n"), "home-a", "null")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sites:\n  home-a:\n    ssh:\n      user: \"null\"\n      keys: [alice]\n"; string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}
