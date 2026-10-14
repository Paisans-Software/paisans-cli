package render

import (
	"fmt"
	"strconv"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
)

// accountValues is what the account blueprint's templates need beyond any app's.
type accountValues struct {
	// IdentityURL is the public URL of the pocket-id app identity_app names,
	// from its hostname in paisans-stack.yaml and nothing else. The service is
	// given it twice, as its OIDC issuer and as the API it creates accounts
	// at, and the two are always the same value.
	IdentityURL string
	// APIKey is that app's static API key.
	APIKey string
	// Accent is that app's accent_color, quoted for a .env, and empty when
	// it is unset or `default`, so the service keeps its own.
	Accent string
	// Invites is settings.invites. With it off the service is told nothing
	// that belongs to invites, and the code of conduct renders empty.
	Invites bool
	// InviteHostname is where invitees are served, from
	// blueprints.InviteHostname, and empty with invites off.
	InviteHostname string
	// CoC is the code_of_conduct setting's text, rendered verbatim.
	CoC string
	// Settings is every setting in blueprints.AccountSettingDefaults, declared
	// or defaulted, as the service spells it.
	Settings map[string]string
}

// accountValuesFor reads the identity app the account app names, never one
// found by search, and the code of conduct from the app's own settings.
func (p *planner) accountValuesFor(name string) (accountValues, error) {
	app := p.cfg.Apps[name]
	idp, _ := app.Settings["identity_app"].(string)
	idApp, ok := p.cfg.Apps[idp]
	if !ok {
		return accountValues{}, fmt.Errorf("apps.%s.settings.identity_app: %q is not an app in this configuration", name, idp)
	}
	coc, _ := app.Settings["code_of_conduct"].(string)
	v := accountValues{
		Invites:        blueprints.AccountInvites(app),
		InviteHostname: blueprints.InviteHostname(p.cfg, app),
		IdentityURL:    "https://" + idApp.Hostname,
		APIKey:         p.appSecret(idp, "static_api_key"),
		CoC:            coc,
		Settings:       map[string]string{},
	}
	if accent, ok := idApp.Settings["accent_color"].(string); ok && accent != "default" {
		quoted, err := envquote(accent)
		if err != nil {
			return accountValues{}, fmt.Errorf("apps.%s.settings.accent_color: %w", idp, err)
		}
		v.Accent = quoted
	}
	for key, def := range blueprints.AccountSettingDefaults {
		v.Settings[key] = def
		switch x := app.Settings[key].(type) {
		case int:
			v.Settings[key] = strconv.Itoa(x)
		case bool:
			v.Settings[key] = strconv.FormatBool(x)
		}
	}
	return v, nil
}
