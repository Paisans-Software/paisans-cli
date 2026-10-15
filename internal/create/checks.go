package create

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"golang.org/x/text/unicode/norm"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// The checks below judge an answer when it is typed, so a mistake is fixed
// at its question rather than found at review. They are a courtesy, not the
// rule: validate judges the whole file before it is written, and is the one
// that decides.

// adminID is the pattern `admins` keys must match (internal/config/ssh.go,
// keyName), since each is recorded on every host beside the admin's keys.
var adminID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// siteName is narrower than any key: a site's name becomes a directory, a
// container label and a line in every host's registry, so it is kept to
// what is safe in all of them.
var siteName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// unixUser is the pattern ssh.user must match (internal/config/ssh.go).
var unixUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func checkOperatorID(s string) error {
	if !adminID.MatchString(s) {
		return errors.New("a lowercase letter or digit, then lowercase letters, digits, dots, underscores or hyphens, at most 32 characters")
	}
	// Pocket ID refuses a username ending in one of these, and apply makes
	// every operator a Pocket ID user under this name.
	if strings.ContainsAny(s[len(s)-1:], "._-") {
		return errors.New("Pocket ID refuses a username ending in a dot, an underscore or a hyphen")
	}
	return nil
}

func checkFullName(s string) error {
	if first, last, _ := strings.Cut(s, " "); first == "" || strings.TrimSpace(last) == "" {
		return errors.New("a first and a last name, separated by a space")
	}
	return nil
}

func checkEmail(s string) error {
	if addr, err := mail.ParseAddress(s); err != nil || addr.Address != s {
		return errors.New("an email address alone, Eg: alice@example.org")
	}
	return nil
}

func checkKey(s string) error {
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s)); err != nil {
		return errors.New("not an SSH public key: paste one whole line of a .pub file, Eg: ssh-ed25519 AAAA... alice@laptop")
	}
	return nil
}

func checkSiteName(s string) error {
	if !siteName.MatchString(s) {
		return errors.New("a lowercase letter, then lowercase letters, digits or hyphens, at most 31 characters")
	}
	return nil
}

func checkUnixUser(s string) error {
	if !unixUser.MatchString(s) {
		return errors.New("a user name on the machine, Eg: paisans-f2a9")
	}
	return nil
}

// checkHostname is a DNS name of at least two labels, lowercase.
func checkHostname(s string) error {
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return errors.New("a full DNS name, Eg: example.org")
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return fmt.Errorf("%q is not a DNS label: lowercase letters, digits and hyphens, not starting or ending with a hyphen", l)
		}
	}
	return nil
}

// checkHost is an ssh host: a DNS name of any length, or an address.
func checkHost(s string) error {
	if net.ParseIP(s) != nil {
		return nil
	}
	for _, l := range strings.Split(s, ".") {
		if !hostLabel.MatchString(strings.ToLower(l)) {
			return errors.New("a host name or an address, Eg: home-a.local or 192.0.2.10")
		}
	}
	return nil
}

func checkIPv4(s string) error {
	if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
		return errors.New("an IPv4 address, Eg: 203.0.113.10")
	}
	return nil
}

func checkEndpoint(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return errors.New("a host and a UDP port, Eg: home-a.example.org:51820")
	}
	if err := checkPort(port); err != nil {
		return err
	}
	if net.ParseIP(host) == nil {
		return checkHostname(host)
	}
	return nil
}

func checkPort(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return errors.New("a port number from 1 to 65535")
	}
	return nil
}

func checkCount(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 9 {
		return errors.New("a number from 1 to 9")
	}
	return nil
}

// parseRoles reads a comma separated list of roles, each one of allRoles,
// none twice.
func parseRoles(s string) ([]config.Role, error) {
	var roles []config.Role
	for _, part := range strings.Split(s, ",") {
		r := config.Role(strings.TrimSpace(strings.ToLower(part)))
		if r == "" {
			continue
		}
		known := false
		for _, k := range allRoles {
			known = known || k == r
		}
		if !known {
			return nil, fmt.Errorf("%q is not a role: %s", r, roleList(allRoles))
		}
		for _, have := range roles {
			if have == r {
				return nil, fmt.Errorf("%s is listed twice", r)
			}
		}
		roles = append(roles, r)
	}
	if len(roles) == 0 {
		return nil, errors.New("at least one role")
	}
	return roles, nil
}

func roleList(roles []config.Role) string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = string(r)
	}
	return strings.Join(out, ", ")
}

// slug makes a site name of s: lowercase ASCII letters and digits, every
// other run of characters one hyphen, starting with a letter and cut to a
// site name's length. It is empty when s holds no letter.
func slug(s string) string {
	var b strings.Builder
	hyphen := false
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', b.Len() > 0 && c >= '0' && c <= '9':
			if hyphen {
				b.WriteByte('-')
			}
			b.WriteRune(c)
			hyphen = false
		case b.Len() > 0:
			hyphen = true
		}
	}
	out := b.String()
	if len(out) > 31 {
		out = strings.TrimRight(out[:31], "-")
	}
	return out
}

// notIn refuses an answer already taken, Eg: a second site of one name.
func notIn(taken []string, what string, check func(string) error) func(string) error {
	return func(s string) error {
		for _, t := range taken {
			if t == s {
				return fmt.Errorf("another %s is already called %s", what, s)
			}
		}
		return check(s)
	}
}

// checkDisplayName is validate's display-name-invalid at the prompt: a name
// Pocket ID can hold as written, since the name is how apply finds the client.
func checkDisplayName(s string) error {
	switch {
	case s == "":
		return errors.New("a name for the sign in page, Eg: Wiki")
	case utf8.RuneCountInString(s) > 50:
		return errors.New("at most 50 characters, the longest client name Pocket ID accepts")
	case strings.TrimSpace(s) != s:
		return errors.New("no space at the start or the end")
	case !norm.NFC.IsNormalString(s):
		return fmt.Errorf("Pocket ID stores this as %q; type that instead", norm.NFC.String(s))
	}
	return nil
}

// readCodeOfConduct is the Markdown file at path, refused when it cannot be
// read or holds nothing: everyone who signs up agrees to it before an account exists.
func readCodeOfConduct(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot be read: " + err.Error())
	}
	text := strings.TrimRight(string(data), "\n")
	if strings.TrimSpace(text) == "" {
		return "", errors.New("is empty; the code of conduct is the text everyone who signs up agrees to")
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f) {
			return "", errors.New("holds a control character, which the configuration cannot carry")
		}
	}
	return text, nil
}
