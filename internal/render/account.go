package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
)

// accountValues is what the account blueprint's templates need beyond any app's.
type accountValues struct {
	// IssuerURL is the public URL of the deployment's one pocket-id app,
	// from its hostname in paisans-stack.yaml: the URL Pocket ID names itself
	// by as an OIDC issuer, which a token's issuer must match.
	IssuerURL string
	// APIURL is where the service calls Pocket ID's API with the static API
	// key: its own site's HAProxy on the mesh, at pocketIDAPIPort, which
	// routes to the active instance. The key never reaches the public
	// hostname.
	APIURL string
	// PrivateNetworks is the deployment's mesh.subnet, which the service
	// accepts APIURL in alongside the private ranges, so a mesh declared
	// outside them still reaches Pocket ID.
	PrivateNetworks string
	// APIKey is that app's static API key.
	APIKey string
	// Accent is that app's accent_color, quoted for a .env, and empty when
	// it is unset or `default`, so the service keeps its own.
	Accent string
	// Signup is settings.signup, blueprints.SignupInvite or SignupOpen. Under
	// open signup the service is told nothing that belongs to invite links.
	Signup string
	// JoinHostname is where people with no account yet are served, from
	// blueprints.JoinHostname.
	JoinHostname string
	// Operators is the operators' usernames, the keys of the operators
	// section, sorted and comma separated. The service refuses to ban them
	// or to change their address or name.
	Operators string
	// CoC is the code_of_conduct setting's text, rendered verbatim.
	CoC string
	// Settings is every setting in blueprints.AccountSettingDefaults, declared
	// or defaulted, as the service spells it.
	Settings map[string]string
}

// accountValuesFor reads the deployment's one pocket-id app, which validate
// requires of a deployment with an account app, and the code of conduct from
// the app's own settings, for the instance on site.
func (p *planner) accountValuesFor(name, site string) (accountValues, error) {
	app := p.cfg.Apps[name]
	idp, ok := p.cfg.PocketIDApp()
	if !ok {
		return accountValues{}, fmt.Errorf("apps.%s: an account app needs exactly one pocket-id app in this configuration", name)
	}
	idApp := p.cfg.Apps[idp]
	coc, _ := app.Settings["code_of_conduct"].(string)
	v := accountValues{
		Signup:          blueprints.AccountSignup(app),
		JoinHostname:    blueprints.JoinHostname(p.cfg, app),
		Operators:       strings.Join(sortedKeys(p.cfg.Operators), ","),
		IssuerURL:       "https://" + idApp.Hostname,
		APIURL:          fmt.Sprintf("http://%s:%d", p.meshAddress(site), pocketIDAPIPort),
		PrivateNetworks: p.mesh,
		APIKey:          p.appSecret(idp, "static_api_key"),
		CoC:             coc,
		Settings:        map[string]string{},
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
