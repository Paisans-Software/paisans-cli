package validate

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
)

// clientNameMax is the longest client name Pocket ID accepts:
// dto.OidcClientUpdateDto's name is `binding:"required,max=50"`
// (dto/oidc_dto.go:42 at v2.14.0), which counts characters, not bytes.
const clientNameMax = 50

// displayNameInvalid refuses a display_name Pocket ID would refuse, or one it
// would store as something other than what the configuration says.
//
// The name is how apply finds the client again, so a name Pocket ID changes
// on the way in is never found under it. Pocket ID normalises the name to NFC
// (the `unorm:"nfc"` tag on the same field), so a name in another form is
// found only by its recorded ID, and every apply plans to rename the client
// to a name it can never hold. Surrounding space is refused because it is
// invisible in the file and on the sign in page alike.
func (c *checker) displayNameInvalid() {
	for _, name := range c.cfg.AppNames() {
		dn := c.cfg.Apps[name].DisplayName
		if dn == "" {
			continue
		}
		key := fmt.Sprintf("apps.%s.display_name", name)
		var why string
		switch {
		case utf8.RuneCountInString(dn) > clientNameMax:
			why = fmt.Sprintf("is %d characters, and Pocket ID refuses a client name longer than %d.", utf8.RuneCountInString(dn), clientNameMax)
		case strings.TrimSpace(dn) != dn:
			why = "begins or ends with space, which nobody can see in the file or on the sign in page. Remove it."
		case !norm.NFC.IsNormalString(dn):
			why = fmt.Sprintf("is not in Unicode normal form C, which Pocket ID stores client names in, so the client would never be found under this name. Write it as %q.", norm.NFC.String(dn))
		default:
			continue
		}
		c.refuse("display-name-invalid", key, "display name is not one Pocket ID can hold", "%s", why)
	}
}

// duplicateClientName refuses two apps whose clients would share a name.
// apply finds a client by its name before its recorded ID, so the second app
// would find the first's client and plan against it.
//
// Every client name is claimed, not only the ones display_name sets: an app
// with none is named after itself, and the gate after the community.
func (c *checker) duplicateClientName() {
	first := map[string]string{} // client name -> the app that took it first
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if _, ok := blueprints.OIDCClient(app.Kind, app.Hostname, name); !ok {
			continue
		}
		client := c.cfg.ClientName(name)
		other, taken := first[client]
		if !taken {
			first[client] = name
			continue
		}
		// The finding goes on a display_name that caused it, since that is
		// the line to change: this app's, else the other's, else the app.
		at, by := name, other
		if app.DisplayName == "" && c.cfg.Apps[other].DisplayName != "" {
			at, by = other, name
		}
		key := fmt.Sprintf("apps.%s", at)
		if c.cfg.Apps[at].DisplayName != "" {
			key += ".display_name"
		}
		c.refuse("duplicate-client-name", key, "two apps' clients have the same name",
			"names its client at Pocket ID %q, which is also %s's. apply finds a client by its name, so one app would take the other's client. Set apps.%s.display_name to a name no other app's client has.", client, by, at)
	}
}

// displayNameWithoutAClient warns about a display_name on an app whose kind
// has no client at Pocket ID, where it names nothing.
func (c *checker) displayNameWithoutAClient() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.DisplayName == "" {
			continue
		}
		if _, ok := blueprints.OIDCClient(app.Kind, app.Hostname, name); ok {
			continue
		}
		c.warn("display-name-without-a-client", fmt.Sprintf("apps.%s.display_name", name),
			"display name does nothing on this app",
			"is set, but the %s blueprint has no client at Pocket ID, so nothing shows it. Remove it.", app.Kind)
	}
}
