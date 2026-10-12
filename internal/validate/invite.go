package validate

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

func (c *checker) inviteApps() []string {
	var out []string
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Kind == config.KindInvite {
			out = append(out, name)
		}
	}
	return out
}

func (c *checker) inviteMustBeClustered() {
	for _, name := range c.inviteApps() {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			c.refuse("invite-must-be-clustered", fmt.Sprintf("apps.%s.placement", name),
				"invite must have cluster placement",
				"is not cluster. An invite must survive the loss of a site, so the invite service keeps its state in the cluster's Postgres and runs on every apps site. Set placement: cluster.")
		}
	}
}

// inviteIdentityApp checks the pocket-id app an invite app names, and the
// settings of that app the invite flow depends on.
func (c *checker) inviteIdentityApp() {
	for _, name := range c.inviteApps() {
		idp, _ := c.cfg.Apps[name].Settings["identity_app"].(string)
		app, ok := c.cfg.Apps[idp]
		if !ok || app.Kind != config.KindPocketID {
			c.refuse("invite-identity-app", fmt.Sprintf("apps.%s.settings.identity_app", name),
				"identity_app must name a pocket-id app",
				"is %q, which is not a pocket-id app in this configuration. The invite service creates each approved member's account at the pocket-id app named here, and signs members in through it.", idp)
			continue
		}
		s := app.Settings
		if v, set := s["allow_user_signups"]; set && v != "disabled" {
			c.refuse("invite-signups-not-disabled", fmt.Sprintf("apps.%s.settings.allow_user_signups", idp),
				"pocket id signups bypass the code of conduct",
				"is %v. With an invite app, new accounts come only from invites, after the code of conduct; Pocket ID's own signup links would admit people past it. Set allow_user_signups: disabled or remove it.", v)
		}
		if v := s["email_one_time_access_as_unauthenticated"]; v != true {
			c.refuse("invite-recovery-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_unauthenticated", idp),
				"members could not recover an expired sign-in link",
				"is not true. A new member whose sign-in link expired gets a fresh one from Pocket ID's sign-in page with an email code. Set email_one_time_access_as_unauthenticated: true.")
		}
		if v, set := s["email_one_time_access_as_admin"]; set && v == false {
			c.refuse("invite-login-link-disabled", fmt.Sprintf("apps.%s.settings.email_one_time_access_as_admin", idp),
				"approved members could not be sent a sign-in link",
				"is false. The invite service has Pocket ID email each approved member a one-time sign-in link, which Pocket ID refuses while email_one_time_access_as_admin is false. Set it true or remove it.")
		}
	}
}

func (c *checker) inviteWithoutSMTP() {
	for _, name := range c.inviteApps() {
		if !c.cfg.CanMail(name) {
			c.refuse("invite-without-smtp", fmt.Sprintf("apps.%s", name),
				"invite needs mail",
				"resolves no SMTP host and from_address. The invite service emails admins when an application is waiting and tells invitees the outcome. Declare an smtp: block with a host and from_address.")
		}
	}
}

// inviteCodeOfConduct refuses a missing, non-string or blank code of
// conduct. The setting is the Markdown text itself, so nothing reads a file.
func (c *checker) inviteCodeOfConduct() {
	for _, name := range c.inviteApps() {
		text, ok := c.cfg.Apps[name].Settings["code_of_conduct"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			c.refuse("invite-code-of-conduct-missing", fmt.Sprintf("apps.%s.settings.code_of_conduct", name),
				"the code of conduct text is missing",
				"is missing, empty or not text. Every invitee agrees to the code of conduct before an account exists. Put the community's code of conduct in code_of_conduct as a block of text (code_of_conduct: |).")
		}
	}
}

func (c *checker) inviteSettingInvalid() {
	for _, name := range c.inviteApps() {
		s := c.cfg.Apps[name].Settings
		for _, key := range sortedKeys(kinds.InviteSettingRanges) {
			v, set := s[key]
			if !set {
				continue
			}
			rng := kinds.InviteSettingRanges[key]
			if n, ok := v.(int); !ok || n < rng[0] || n > rng[1] {
				c.refuse("invite-setting-invalid", fmt.Sprintf("apps.%s.settings.%s", name, key),
					"invite setting out of range",
					"is %v. It must be a whole number from %d to %d.", v, rng[0], rng[1])
			}
		}
		if v, set := s["require_approval"]; set {
			if _, ok := v.(bool); !ok {
				c.refuse("invite-setting-invalid", fmt.Sprintf("apps.%s.settings.require_approval", name),
					"invite setting out of range", "is %v. It must be true or false, unquoted.", v)
			}
		}
	}
}
