package blueprints

import (
	"strings"
	"testing"
)

// noSnippet is a blueprint's templates without the gateway's routing: a
// blueprint with hostname: none ships no caddy.snippet.tmpl.
func noSnippet(name string) bool { return name != SnippetFor(PrimaryRole) }

// A blueprint that serves no website says hostname: none in the format that
// adds it, and reads back as one that takes no hostname.
func TestHostnameNoneParses(t *testing.T) {
	d, err := Parse("example", []byte(minimalV5+"hostname: none\n"), noSnippet)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Hostless {
		t.Errorf("%+v, want hostless", d)
	}
	plain, err := Parse("example", []byte(minimalV5), always)
	if err != nil || plain.Hostless {
		t.Errorf("a blueprint without the key: %+v, %v, want one that takes a hostname", plain, err)
	}
}

// hostname: none needs the format that adds it, so a toolkit that cannot read
// it refuses the format and uses an older catalog release, rather than
// requiring a hostname of an app that has nothing to serve on it.
func TestHostnameNoneNeedsV5(t *testing.T) {
	for _, doc := range []string{minimal, minimalV2, minimalV3, minimalV4} {
		_, err := Parse("example", []byte(doc+"hostname: none\n"), noSnippet)
		if err == nil || !strings.Contains(err.Error(), "hostname") || !strings.Contains(err.Error(), APIVersionV5) {
			t.Errorf("%v, want a refusal naming hostname and %s", err, APIVersionV5)
		}
	}
}

// none is the key's one value, and a blueprint with no hostname declares
// nothing that lives on one.
func TestHostnameNoneRefusesWhatNeedsAHostname(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"another value", minimalV5 + "hostname: required\n", "hostname: is \"required\""},
		{"a caddy snippet", minimalV5 + "hostname: none\n", "caddy.snippet.tmpl"},
		{"oidc", minimalV5 + "hostname: none\noidc: {callback: /cb}\n", "oidc"},
		{"hostname roles", minimalV5 + "hostname: none\nhostname_roles: [media]\n", "hostname_roles"},
		{"a gate", minimalV5 + "hostname: none\ngate: {gateable: true}\n", "gate"},
		{"object storage", minimalV5 + "hostname: none\nstorage: {s3: true}\n", "storage.s3"},
		{"federation", minimalV5 + "hostname: none\nfederation: {federates: true}\n", "federation"},
		{"a required link", minimalV5 + "hostname: none\nconsumes:\n  - {role: example-role, setting: example}\nsettings:\n  example: {type: string}\n", "consumes[0].optional"},
		{"a provided hostname", minimalV5 + "hostname: none\nprovides:\n  - {role: example-role, values: {url: \"'https://' + app.hostname\"}}\n", "app.hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			has := noSnippet
			if tc.name == "a caddy snippet" {
				has = always
			}
			_, err := Parse("example", []byte(tc.doc), has)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

// The reserved ops-notifier serves nothing but its health route over the
// mesh, so it takes no hostname.
func TestOpsNotifierTakesNoHostname(t *testing.T) {
	if !Hostless("ops-notifier") {
		t.Error("ops-notifier takes a hostname")
	}
	if Hostless("wikijs") || Hostless("uptime") {
		t.Error("a blueprint that serves a website is hostless")
	}
}
