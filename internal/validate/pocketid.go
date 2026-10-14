package validate

import (
	"fmt"
	"net/mail"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// The pocket-id blueprint's application configuration is rendered from its
// settings and the smtp block, with UI_CONFIG_DISABLED=true (see
// blueprints.PocketIDSettings). Pocket ID validates that configuration when it
// starts and refuses to start on a value it does not accept, so every rule
// here refuses what would otherwise be found as a crash looping container
// after an apply, on every apps site at once.

// pocketIDApps is every pocket-id app, in AppNames order.
func (c *checker) pocketIDApps() []string {
	var out []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint == config.BlueprintPocketID {
			out = append(out, name)
		}
	}
	return out
}

// pocketIDSettingInvalid refuses a setting whose value Pocket ID would refuse
// at startup, or one the admin page's form would not have let anybody save.
func (c *checker) pocketIDSettingInvalid() {
	for _, name := range c.pocketIDApps() {
		for _, p := range blueprints.PocketIDSettingProblems(c.cfg.Apps[name].Settings) {
			c.refuse("pocket-id-setting-invalid", fmt.Sprintf("apps.%s.settings.%s", name, p.Key),
				"pocket id setting has an invalid value",
				"%s Pocket ID reads its whole configuration from the rendered .env and will not start on a value it refuses.", p.Message)
		}
	}
}

// pocketIDAccountEditOn refuses allow_own_account_edit set true. The
// blueprint renders it false regardless (see
// blueprints.PocketIDAccountEditSetting), so a true here would say something
// the deployment does not do.
func (c *checker) pocketIDAccountEditOn() {
	for _, name := range c.pocketIDApps() {
		if on, _ := c.cfg.Apps[name].Settings[blueprints.PocketIDAccountEditSetting].(bool); !on {
			continue
		}
		c.refuse("pocket-id-account-edit-on", fmt.Sprintf("apps.%s.settings.%s", name, blueprints.PocketIDAccountEditSetting),
			"pocket id account editing cannot be turned on",
			"is true, and the pocket-id blueprint always renders ALLOW_OWN_ACCOUNT_EDIT=false. A Pocket ID username is the identity other apps know a person by, such as their fediverse handle in Mbin, and those apps do not follow a rename; with editing off nobody can change their own username or email, which is also how apply finds each declared admin's account. Set it false or remove it. An administrator can still edit any account from Pocket ID's admin page, and a rename made there breaks the same identities.")
	}
}

// pocketIDSettingUnknown warns about a pocket-id setting the toolkit does not
// read. A setting no template asks for renders nothing, which for this
// blueprint most likely means a misspelt Pocket ID option that its admin page
// can no longer set either.
func (c *checker) pocketIDSettingUnknown() {
	for _, name := range c.pocketIDApps() {
		for _, key := range sortedKeys(c.cfg.Apps[name].Settings) {
			if _, ok := blueprints.PocketIDSettingByKey(key); ok || slices.Contains(blueprints.PocketIDOtherSettings, key) {
				continue
			}
			c.warn("pocket-id-setting-unknown", fmt.Sprintf("apps.%s.settings.%s", name, key),
				"pocket id setting is not read by the toolkit",
				"is not a setting the pocket-id blueprint reads, so it renders nothing. The settings are listed in README.md, \"Pocket ID's configuration is rendered from "+config.FileName+"\"; an option the toolkit does not model goes under config, by its variable name.")
		}
	}
}

// pocketIDConfigKeyHasASetting refuses a passthrough key for a variable the
// blueprint renders from a setting or from the smtp block, whether or not that
// setting is declared. Rendering refuses a key the .env already holds; this
// also refuses one whose setting is absent today, because the day somebody
// declares the setting the two would collide, and until then the value has
// two places it could be changed.
func (c *checker) pocketIDConfigKeyHasASetting() {
	for _, name := range c.pocketIDApps() {
		for _, key := range sortedKeys(c.cfg.Apps[name].Config) {
			owner, ok := blueprints.PocketIDEnvOwner(key)
			if !ok {
				continue
			}
			c.refuse("pocket-id-config-key-has-a-setting", fmt.Sprintf("apps.%s.config.%s", name, key),
				"pocket id config key duplicates a setting",
				"is rendered by %s. Set it there and remove it from config, so the value has one place to be changed.", owner)
		}
	}
}

// pocketIDOperatorUsername refuses an operator whose name Pocket ID would
// not take as a username, when the deployment runs Pocket ID: apply makes
// each declared operator a Pocket ID user under that name. Pocket ID wants a
// letter or digit at both ends (dto/validations.go:21-25 at tag v2.14.0). The
// operators section already holds names to a letter or digit first and
// lowercase letters, digits, dots, underscores and hyphens after, so the one
// rule left is the last character. Refused here, it stops before any host is
// reached, rather than as a 400 from Pocket ID halfway through a run.
func (c *checker) pocketIDOperatorUsername() {
	if len(c.pocketIDApps()) == 0 {
		return
	}
	for _, id := range sortedKeys(c.cfg.Operators) {
		if last := id[len(id)-1]; last == '.' || last == '_' || last == '-' {
			c.refuse("pocket-id-operator-username", "operators."+id,
				"operator name is not a pocket id username",
				"ends in %q. apply makes each operator a Pocket ID user under this name, and Pocket ID takes a username only if it ends in a letter or digit. Rename the operator, and every site's ssh.keys entry for them.", string(last))
		}
	}
}

// pocketIDManagement checks who manages accounts. management is required and
// never defaulted, because it decides whether anybody is a Pocket ID
// administrator. Under paisans the account service manages signup, bans and
// recovery: an account app is required, the settings the toolkit renders for
// it are refused when declared, since a declared one could only disagree,
// and mail is required, since recovery is an email code. Under pocket-id,
// Pocket ID's own admin pages do, and an account app is refused: two places
// that each think they decide who joins would disagree.
func (c *checker) pocketIDManagement() {
	accounts := c.accountApps()
	for _, name := range c.pocketIDApps() {
		app := c.cfg.Apps[name]
		key := fmt.Sprintf("apps.%s.settings.%s", name, blueprints.PocketIDManagementSetting)
		switch blueprints.PocketIDManagement(app.Settings) {
		case "":
			v, set := app.Settings[blueprints.PocketIDManagementSetting]
			why := "is missing."
			if set {
				why = fmt.Sprintf("is %v.", v)
			}
			c.refuse("pocket-id-management-unset", key,
				"who manages accounts must be declared",
				"%s Set management: paisans, where the account service manages signup, bans and recovery and nobody is a Pocket ID administrator, or management: pocket-id, where Pocket ID's own admin pages do and every operator is its administrator. It is never defaulted, because it decides who administers the identity provider.", why)
		case blueprints.ManagementPaisans:
			for _, k := range sortedKeys(blueprints.PocketIDManagedSettings) {
				if _, set := app.Settings[k]; set {
					c.refuse("pocket-id-setting-managed", fmt.Sprintf("apps.%s.settings.%s", name, k),
						"the toolkit renders this setting under management: paisans",
						"is set, and under management: paisans the toolkit renders it as %v, because the account service manages accounts. A declared value could only disagree. Remove it.", blueprints.PocketIDManagedSettings[k])
				}
			}
			if len(accounts) == 0 {
				c.refuse("pocket-id-without-account", key,
					"management: paisans needs an account app",
					"is paisans, and no app has blueprint: account. Under paisans the account service manages signup, bans and recovery, and nobody else can: Pocket ID's signup is closed and nobody is its administrator. Declare an account app, or set management: pocket-id.")
			}
			if !c.cfg.CanMail(name) {
				c.refuse("pocket-id-paisans-without-smtp", key,
					"management: paisans needs mail",
					"is paisans, and %s's smtp settings resolve no host or no from_address. A member whose sign-in link expired recovers with an email code, which the toolkit turns on under paisans and which cannot be sent without mail. Declare an smtp block with a host and from_address, in the top level or in apps.%s.smtp.", name, name)
			}
		case blueprints.ManagementPocketID:
			for _, a := range accounts {
				c.refuse("account-under-pocket-id-management", fmt.Sprintf("apps.%s", a),
					"an account app needs management: paisans",
					"is an account app, and %s declares management: pocket-id, where Pocket ID's own admin pages manage signup, bans and recovery. The account service would manage the same accounts from a second place. Remove the account app, or set %s's management to paisans.", key, name)
			}
		}
	}
}

// pocketIDEmailWithoutSMTP refuses an email toggle turned on without the
// SMTP host and sender it needs. Pocket ID's admin page refuses the same
// combination (app-config-email-form.svelte:53-88 at tag v2.14.0); at startup
// it is not checked, and every mail the toggle promises then fails.
func (c *checker) pocketIDEmailWithoutSMTP() {
	for _, name := range c.pocketIDApps() {
		on := blueprints.PocketIDEmailEnabled(c.cfg.Apps[name].Settings)
		if len(on) == 0 {
			continue
		}
		smtp := c.cfg.SMTPFor(name)
		var missing []string
		if smtp.Host == "" {
			missing = append(missing, "host")
		}
		if smtp.FromAddress == "" {
			missing = append(missing, "from_address")
		}
		if len(missing) == 0 {
			continue
		}
		c.refuse("pocket-id-email-without-smtp", fmt.Sprintf("apps.%s.settings.%s", name, on[0]),
			"pocket id mail is on without an smtp host or sender",
			"turns on mail from Pocket ID (%s), but its smtp settings resolve no %s. Declare them in the top level smtp block or in apps.%s.smtp, or turn the mail off.",
			strings.Join(on, ", "), strings.Join(missing, " and no "), name)
	}
}

// pocketIDSMTPSender checks the sender Pocket ID is given. SMTP_FROM must be
// an email address (binding:"omitempty,email", dto/app_config_dto.go:30), so
// anything else stops it from starting. A host with no sender at all is only
// risky: Pocket ID starts, and every mail it sends has an empty From.
func (c *checker) pocketIDSMTPSender() {
	for _, name := range c.pocketIDApps() {
		smtp := c.cfg.SMTPFor(name)
		if smtp.Host == "" {
			continue
		}
		key := "smtp.from_address"
		if o := c.cfg.Apps[name].SMTP; o != nil && o.FromAddress != "" {
			key = fmt.Sprintf("apps.%s.smtp.from_address", name)
		}
		if smtp.FromAddress == "" {
			c.warn("pocket-id-smtp-without-sender", key,
				"pocket id smtp has no sender",
				"is not set, so %s's mail would be sent with an empty sender. Pocket ID starts, but a mail server is likely to refuse it. Set from_address.", name)
			continue
		}
		if addr, err := mail.ParseAddress(smtp.FromAddress); err != nil || addr.Address != smtp.FromAddress {
			c.refuse("pocket-id-smtp-from-not-an-email", key,
				"pocket id sender is not an email address",
				"is %q, which %s renders as SMTP_FROM, and Pocket ID will not start unless that is an email address alone, Eg: hello@example.org.", smtp.FromAddress, name)
		}
	}
}

// pocketIDSMTPFromName warns about a sender name set on the Pocket ID app's
// own smtp block. Pocket ID has no such setting: its mail goes out under
// APP_NAME (email/module.go:212), so the name is not used. The deployment's
// own from_name is not warned about, because the other blueprints that send
// mail use it.
func (c *checker) pocketIDSMTPFromName() {
	for _, name := range c.pocketIDApps() {
		o := c.cfg.Apps[name].SMTP
		if o == nil || o.FromName == "" {
			continue
		}
		c.warn("pocket-id-smtp-from-name", fmt.Sprintf("apps.%s.smtp.from_name", name),
			"pocket id ignores a sender name",
			"is set, and Pocket ID has no sender name setting: its mail is sent under its app name. Set apps.%s.settings.app_name to change it, and remove from_name here.", name)
	}
}
