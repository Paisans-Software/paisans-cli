package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// PastedSecret reports whether the value at a dotted key is one a person
// can type or paste rather than one the toolkit generates: a credential
// another party issued (external, a backup destination's key, an app's SMTP
// password), what apply records from a running service and an operator may
// record by hand instead (oidc_clients, pocket_id_groups), and an operator's
// address at the DNS provider. Each is one line: a token, a password, an ID or
// an address. What the toolkit generates is not one of them, and some of it,
// a PEM key, is several lines.
func PastedSecret(key string) bool {
	parts := strings.Split(key, ".")
	switch {
	case len(parts) == 2 && parts[0] == "external":
		return true
	case len(parts) == 4 && parts[0] == "backup" && parts[1] == "destinations":
		return true
	case len(parts) == 3 && parts[0] == "apps" && parts[2] == "smtp_password":
		return true
	case len(parts) == 3 && parts[0] == "oidc_clients":
		return true
	case len(parts) >= 2 && parts[0] == "pocket_id_groups":
		return true
	case len(parts) == 3 && parts[0] == "operators" && parts[2] == "dns_provider_email":
		return true
	}
	return false
}

// CheckPastedSecret refuses a control character, or a Unicode line or
// paragraph separator, in a pasted secret (PastedSecret). Such a value is
// rendered into an env file, one variable to a line, and a line break in it
// would start a line of its own that sets any other variable of that service,
// Eg: Pocket ID's STATIC_API_KEY. No credential any provider issues contains
// one, so its presence is a paste or a decryption gone wrong. The error names
// the key and the character's code point, never the value.
func CheckPastedSecret(key, value string) error {
	if !PastedSecret(key) {
		return nil
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("secrets %s: holds a control character (%U). Each pasted secret is rendered onto one line of an env file, where a line break would add a line setting another variable, so it is refused. Set it again without one: paisans secrets set %s < value", key, r, key)
		}
	}
	return nil
}

// checkPasted is CheckPastedSecret on every pasted secret the file holds,
// and on a rotation's next value for one, in key order, so the first refusal
// is the same on every run. The key being replaced is passed over, so that
// `secrets set` can repair the file that holds it.
func (s *Secrets) checkPasted(replacing string) error {
	values := map[string]string{}
	for k, v := range s.External {
		values["external."+k] = v
	}
	for name, d := range s.Backup.Destinations {
		values["backup.destinations."+name+".access_key_id"] = d.AccessKeyID
		values["backup.destinations."+name+".secret_access_key"] = d.SecretAccessKey
	}
	for app, keys := range s.Apps {
		if v, ok := keys["smtp_password"].(string); ok {
			values["apps."+app+".smtp_password"] = v
		}
	}
	for app, c := range s.OIDCClients {
		values["oidc_clients."+app+".client_id"] = c.ClientID
		values["oidc_clients."+app+".client_secret"] = c.ClientSecret
	}
	for name, id := range s.PocketIDGroups {
		values["pocket_id_groups."+name] = id
	}
	for name, o := range s.Operators {
		values["operators."+name+".dns_provider_email"] = o.DNSProviderEmail
	}
	keys := make([]string, 0, len(values)+len(s.Rotations))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rotations []string
	for k := range s.Rotations {
		rotations = append(rotations, k)
	}
	sort.Strings(rotations)
	for _, k := range keys {
		if k == replacing {
			continue
		}
		if err := CheckPastedSecret(k, values[k]); err != nil {
			return pastedProblem(s.Path, k, err)
		}
	}
	for _, k := range rotations {
		if err := CheckPastedSecret(k, s.Rotations[k]); err != nil {
			return pastedProblem(s.Path, "rotations."+k, err)
		}
	}
	return nil
}

// ErrPastedControl is what a secrets file refused for a pasted secret
// holding a control character wraps, so a caller that otherwise words its
// own refusal can pass this one through: it names the key and the repair,
// and never the value.
var ErrPastedControl = errors.New("a pasted secret holds a control character")

// pastedProblem is a secrets file refused for a pasted value checkPasted
// found, said by key.
func pastedProblem(path, key string, err error) error {
	name := "a stored version of the secrets"
	if path != "" {
		name = ui.ShortPath(path)
	}
	fix := "Replace it with `paisans secrets set " + key + " < value`, which reads the file with this one value passed over."
	if strings.HasPrefix(key, "rotations.") {
		fix = "It is the next value of a `paisans secrets rotate` under way. Edit it out with `sops " + ui.ShortPath(path) + "` and run the rotation again."
	}
	return &ui.Problem{
		Hint:    fmt.Sprintf("%s: %s holds a control character", name, key),
		Explain: "Each pasted secret is rendered onto one line of an env file, where a line break in it would add a line setting another variable of that service, so the file is not read. " + fix,
		Cause:   fmt.Errorf("%s: %w: %w", name, ErrPastedControl, err),
	}
}
