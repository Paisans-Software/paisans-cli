package hostadmit_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const helper = "/usr/local/libexec/paisans/wg-psns-f2a9"

const user = "paisans-f2a9"

// ReadSudoList reads sudo -l as Ubuntu 24.04 prints it, as root for a user
// or as the user itself, with LF or CRLF line ends: its Defaults split
// into entries, each rule, each specific line; a refusal is a list of
// nothing; anything else is unread.
func TestReadSudoList(t *testing.T) {
	b, err := os.ReadFile("testdata/sudo/setenv-helper.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(b), strings.ReplaceAll(string(b), "\n", "\r\n")} {
		l, ok := hostadmit.ReadSudoList(user, strings.Split(strings.TrimRight(text, "\r\n"), "\n"))
		if !ok ||
			!slices.Equal(l.Defaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/local/sbin\:/usr/local/bin\:/usr/sbin\:/usr/bin\:/sbin\:/bin\:/snap/bin`, "use_pty"}) ||
			!slices.Equal(l.Rules, []string{hostadmit.HelperRule(helper)}) ||
			!slices.Equal(l.Specific, []string{"Defaults!" + helper + " setenv"}) {
			t.Errorf("%q: %+v, %v", text, l, ok)
		}
	}
	var l hostadmit.SudoList
	var ok bool
	l, ok = hostadmit.ReadSudoList(user, []string{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset, mail_badpass,", `    secure_path=/usr/bin\:/bin,`, "    use_pty", "",
		"User paisans-f2a9 may run the following commands on host:", "    " + hostadmit.HelperRule(helper)})
	if !ok || !slices.Equal(l.Defaults, []string{"env_reset", "mail_badpass", `secure_path=/usr/bin\:/bin`, "use_pty"}) {
		t.Errorf("wrapped Defaults: %+v", l)
	}
	for _, refused := range []string{"User paisans-f2a9 is not allowed to run sudo on host.", "Sorry, user paisans-f2a9 may not run sudo on host."} {
		if l, ok := hostadmit.ReadSudoList(user, []string{refused}); !ok || len(l.Rules)+len(l.Defaults)+len(l.Specific) != 0 {
			t.Errorf("%q: %+v, %v", refused, l, ok)
		}
	}
	for _, lines := range [][]string{
		{""},
		{"something else"},
		{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset", "Something sudo never printed"},
		{"Sorry, user root may not run sudo on host."},
		{"Sorry, user paisans-f2a9x may not run sudo on host."},
		{"Sorry, user paisans-f2a9 may not run sudo on host.", "something else"},
		{"Matching Defaults entries for paisans-f2a9 on host:", "    env_reset", "", "Sorry, user paisans-f2a9 may not run sudo on host."},
		{"Sorry, user paisans-f2a9 may not run sudo on host.", "User paisans-f2a9 may run the following commands on host:", "    " + hostadmit.HelperRule(helper)},
		{"Benutzer paisans-f2a9 darf die folgenden Befehle auf host ausführen:", "    " + hostadmit.HelperRule(helper)},
	} {
		if l, ok := hostadmit.ReadSudoList(user, lines); ok {
			t.Errorf("%q read as %+v", lines, l)
		}
	}
}

// BeyondHelper is every rule but the helper's, every Defaults entry that
// hands the helper the user's environment or path, and every specific line.
func TestBeyondHelper(t *testing.T) {
	l := hostadmit.SudoList{
		Rules:    []string{hostadmit.HelperRule(helper), "(ALL : ALL) ALL", "(root) NOPASSWD: " + helper + ", /bin/sh"},
		Defaults: []string{"env_reset", "use_pty", "setenv", "!env_reset"},
		Specific: []string{"Defaults!" + helper + " setenv"},
	}
	rules, defaults := l.BeyondHelper(helper)
	if !slices.Equal(rules, []string{"(ALL : ALL) ALL", "(root) NOPASSWD: " + helper + ", /bin/sh"}) {
		t.Errorf("rules %q", rules)
	}
	if !slices.Equal(defaults, []string{
		"setenv: the user's variables reach the helper",
		"!env_reset: the user's whole environment reaches the helper",
		"Defaults!" + helper + " setenv: set for one runas user or command alone",
	}) {
		t.Errorf("defaults %q", defaults)
	}
	if r, d := (hostadmit.SudoList{Rules: []string{hostadmit.HelperRule(helper)}, Defaults: []string{"env_reset", "use_pty"}}).BeyondHelper(helper); len(r)+len(d) != 0 {
		t.Errorf("admit's own line reaches past it: %q %q", r, d)
	}
}
