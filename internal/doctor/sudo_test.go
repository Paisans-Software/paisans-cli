package doctor

import (
	"strings"
	"testing"
)

// sudoDefaults is the start of sudo -l's answer on Ubuntu 24.04, before
// the list itself.
const sudoDefaults = "Matching Defaults entries for ubuntu on home-a:\n" +
	"    env_reset, mail_badpass,\n" +
	"    secure_path=/usr/local/sbin\\:/usr/local/bin\\:/usr/sbin\\:/usr/bin\\:/sbin\\:/bin\\:/snap/bin,\n" +
	"    use_pty\n\n"

const helperEntry = "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"

func sudoList(entries ...string) string {
	return sudoDefaults + "User ubuntu may run the following commands on home-a:\n    " + strings.Join(entries, "\n    ") + "\n"
}

// What sudo allows is exactly what host admit accepts, or the check fails
// naming what reaches past the helper, or that the helper is missing; an
// answer it cannot read is a failure, never a pass.
func TestSudoRights(t *testing.T) {
	cfg := fixture(t)
	for _, c := range []struct {
		name, out, err string
		level          Level
		line           string
		more           string
	}{
		{"helper alone", sudoList(helperEntry), "", OK, "home-a: sudo lets ubuntu run the WireGuard helper and nothing else", ""},
		{"helper and ALL", sudoList(helperEntry, "(ALL : ALL) ALL"), "", Fail, "home-a: sudo lets ubuntu reach past the WireGuard helper: (ALL : ALL) ALL", "`sudo -l -U ubuntu`"},
		{"helper and a shell on one line", sudoList(helperEntry + ", /bin/sh"), "", Fail, "reach past the WireGuard helper: " + helperEntry + ", /bin/sh", ""},
		{"helper as any user", sudoList("(ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "reach past the WireGuard helper: (ALL) NOPASSWD: /usr/local/libexec/paisans/wg-psns-f2a9", ""},
		{"helper asking a password", sudoList("(root) /usr/local/libexec/paisans/wg-psns-f2a9"), "", Fail, "reach past the WireGuard helper: (root) /usr/local/libexec/paisans/wg-psns-f2a9", ""},
		{"another deployment's helper", sudoList(helperEntry, "(root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b"), "", Fail, "reach past the WireGuard helper: (root) NOPASSWD: /usr/local/libexec/paisans/wg-psns-0a0b", ""},
		{"setenv in the Defaults", strings.Replace(sudoList(helperEntry), "use_pty", "use_pty, setenv", 1), "", Fail, "reach past the WireGuard helper: setenv: the user's variables reach the helper", ""},
		{"a specific Defaults line", sudoDefaults + "Runas and Command-specific defaults for ubuntu:\n    Defaults!/usr/local/libexec/paisans/wg-psns-f2a9 setenv\n\nUser ubuntu may run the following commands on home-a:\n    " + helperEntry + "\n", "", Fail, "set for one runas user or command alone", ""},
		{"not allowed", "Sorry, user ubuntu may not run sudo on home-a.\n", "exit status 1", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", "`paisans host admit`"},
		{"password required", "sudo: a password is required\n", "exit status 1", Fail, "home-a: sudo does not let ubuntu run the WireGuard helper", ""},
		{"another user refused", "Sorry, user root may not run sudo on home-a.\n", "exit status 1", Warn, "home-a: could not read what sudo lets ubuntu run", "recover:\n  1. The host's owner runs `LC_ALL=C sudo -n -l` as ubuntu"},
		{"refusal beside a rule", "Sorry, user ubuntu may not run sudo on home-a.\n    (ALL : ALL) ALL\n", "", Warn, "home-a: could not read what sudo lets ubuntu run", "recover:\n  1. The host's owner runs `LC_ALL=C sudo -n -l` as ubuntu"},
		{"unrecognised", "something else\n", "", Warn, "home-a: could not read what sudo lets ubuntu run", "recover:\n  1. The host's owner runs `LC_ALL=C sudo -n -l` as ubuntu"},
		{"translated", "Benutzer ubuntu darf die folgenden Befehle auf home-a ausführen:\n    " + helperEntry + "\n", "", Warn, "home-a: could not read what sudo lets ubuntu run", "recover:\n  1. The host's owner runs `LC_ALL=C sudo -n -l` as ubuntu"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := SudoRights(cfg, []SudoProbe{{Site: "home-a", Out: c.out, Err: c.err}})
			if len(fs) != 1 {
				t.Fatalf("want one finding:\n%s", dump(fs))
			}
			f := fs[0]
			if f.Level != c.level || !strings.Contains(f.Line, c.line) || !strings.Contains(strings.Join(f.More, "\n"), c.more) {
				t.Errorf("got %v %q\n%s", f.Level, f.Line, strings.Join(f.More, "\n"))
			}
		})
	}
}

// A deployment whose id names no WireGuard helper is a configuration
// error, reported as one, not as a host that needs admitting again.
func TestSudoRightsWithoutAHelperName(t *testing.T) {
	cfg := fixture(t)
	cfg.ID = "not-hex"
	fs := SudoRights(cfg, []SudoProbe{{Site: "home-a", Out: sudoList(helperEntry)}})
	if len(fs) != 1 {
		t.Fatalf("want one finding:\n%s", dump(fs))
	}
	f := fs[0]
	more := strings.Join(f.More, "\n")
	if f.Level != Fail || !strings.Contains(f.Line, "home-a: cannot name the WireGuard helper to look for") || !strings.Contains(f.Line, "is not a deployment's WireGuard interface name") ||
		!strings.Contains(more, "`id`") || strings.Contains(more, "host admit") {
		t.Errorf("got %v %q\n%s", f.Level, f.Line, more)
	}
}
