package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// An Mbin app with a sender and a contact address, inheriting the
// deployment's smtp block, draws none of the mail rules, and an smtp block of
// its own is not refused as one nothing reads.
func TestMbinWithSMTPIsQuiet(t *testing.T) {
	cfg := load(t, "mbin-contact-email-not-an-email")
	app := cfg.Apps["talk"]
	app.Settings = map[string]any{"contact_email": "admins@example.org"}
	app.SMTP = &config.SMTP{FromAddress: "talk@example.org"}
	cfg.Apps["talk"] = app
	for _, f := range validate.Check(cfg).Findings {
		if strings.HasPrefix(f.Rule, "mbin-") || strings.HasPrefix(f.Rule, "smtp-") {
			t.Errorf("unexpected finding: %s", f)
		}
	}
}

// An SMTP host on the app's own block is enough: the warning is about what
// resolves, not about the deployment's block.
func TestMbinOwnSMTPHostSilencesTheWarning(t *testing.T) {
	cfg := load(t, "mbin-without-smtp")
	app := cfg.Apps["talk"]
	app.SMTP = &config.SMTP{Host: "smtp.example.org", FromAddress: "talk@example.org"}
	cfg.Apps["talk"] = app
	if validate.Check(cfg).Has("mbin-without-smtp") {
		t.Fatal("an app level smtp host still warned mbin-without-smtp")
	}
}

// A bad sender is reported at the key that set it: the app's own block when
// it names one.
func TestMbinSenderRefusalNamesTheKeyThatSetIt(t *testing.T) {
	if !refusedFor(validate.Check(load(t, "mbin-smtp-from-not-an-email")), "mbin-smtp-from-not-an-email", "apps.talk.smtp.from_address") {
		t.Fatal("the refusal did not name apps.talk.smtp.from_address")
	}
}

// A contact_email that is not a string is refused too, not passed over.
func TestMbinContactEmailMustBeAString(t *testing.T) {
	cfg := load(t, "mbin-contact-email-not-an-email")
	cfg.Apps["talk"].Settings["contact_email"] = 42
	if !refusedFor(validate.Check(cfg), "mbin-contact-email-not-an-email", "apps.talk.settings.contact_email") {
		t.Fatal("a numeric contact_email was not refused")
	}
}
