package main

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// smtpEnv is how the mail checks reach a mail server: from a site, through
// the site's own ssh (apply.SSHTransport.Dial), or through ssh[site] when
// the command was given --ssh for it. The SMTP conversation runs here; the
// site only carries the connection. Tests replace it, since none may reach a
// real site or mail server.
var smtpEnv = func(cfg *config.Config, ssh map[string]string) smtpcheck.Env {
	return smtpcheck.Env{Dial: func(site, address string) (net.Conn, error) {
		return siteTransport(site, cfg.Sites[site], ssh[site]).Dial(address)
	}}
}

// localSMTPEnv is how a credential is tried before it is written or handed
// out (secrets set, secrets rotate): from this machine, dialling the mail
// server directly, since whether the provider takes a password and a sender
// is the same from anywhere and no site need be deployed yet. Tests replace
// it, since none may reach a real mail server.
var localSMTPEnv = func() smtpcheck.Env {
	return smtpcheck.Env{Dial: func(_, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: smtpcheck.DefaultTimeout}).Dial("tcp", address)
	}}
}

// mailChecks is every login apply or validate tries on sites (nil for every
// site): smtpcheck.Logins, narrowed to the apps in only when the apply is
// scoped to some stacks.
func mailChecks(cfg *config.Config, secrets *config.Secrets, sites, only []string) []smtpcheck.Check {
	checks := smtpcheck.Logins(cfg, secrets, sites)
	if len(only) == 0 {
		return checks
	}
	var out []smtpcheck.Check
	for _, c := range checks {
		c.Apps = slices.DeleteFunc(slices.Clone(c.Apps), func(app string) bool { return !slices.Contains(only, app) })
		if len(c.Apps) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// mailFailure is the refusal or warning for a login that failed: the apps,
// where their settings are declared, the server's reply, and what to do. It
// never carries the password.
func mailFailure(cfg *config.Config, failed *smtpcheck.Error) (hint, explain string) {
	c := failed.Check
	hint = c.Whose() + " does not log in to its mail server"
	login := "sends without logging in"
	if c.PasswordPath != "" {
		login = fmt.Sprintf("logs in as %s with %s", c.SMTP.Username, c.PasswordPath)
	}
	explain = fmt.Sprintf("%s, declared by %s, sends through %s with %s and %s. From %s the server answered: %s.",
		c.Whose(), c.Setting(cfg), c.Address(), c.Security(), login, c.Site, failed.Reply())
	if c.PasswordPath != "" {
		explain += fmt.Sprintf(" Fix the setting, or set the password the mail provider issued with `%s secrets set %s`.", progname.Cmd(), c.PasswordPath)
	} else {
		explain += " Fix the setting."
	}
	return hint, explain
}

// checkMailForApply is apply's mail preflight, before any host is changed:
// every app on sites (only, when scoped) logs in to its mail server from its
// site, and nothing is sent. The first failure refuses the apply, naming the
// app, its setting and the server's reply. A check that cannot be made, Eg:
// a relay only the app's own network resolves, is a warning.
func checkMailForApply(r ui.Reporter, cfg *config.Config, secrets *config.Secrets, sites, only []string, ssh map[string]string) error {
	checks := mailChecks(cfg, secrets, sites, only)
	if len(checks) == 0 {
		return nil
	}
	r.Section("mail")
	return tryMailFromSites(r, cfg, secrets, checks, ssh, " Then apply again. Nothing was changed.", " Nothing was changed.")
}

// checkMailForRotation is how a rotation tries the SMTP password at path
// from site: every app among stacks there that logs in with it logs in from
// site with the value handed holds, as apply's own check does, and nothing
// is sent. Stage 2 runs it for every site that will be handed the password,
// pasted or set beforehand, before stage 3 hands it to any, since this
// machine may not have reached the server.
func checkMailForRotation(r ui.Reporter, cfg *config.Config, handed *config.Secrets, path, site string, stacks []string) error {
	var checks []smtpcheck.Check
	for _, c := range mailChecks(cfg, handed, []string{site}, stacks) {
		if c.PasswordPath == path {
			checks = append(checks, c)
		}
	}
	after := " No site was handed it."
	return tryMailFromSites(r, cfg, handed, checks, nil, after, after)
}

// tryMailFromSites logs in with each of checks from its site, after says
// what was left unchanged by a refusal, and reached by one that could not
// be made.
func tryMailFromSites(r ui.Reporter, cfg *config.Config, secrets *config.Secrets, checks []smtpcheck.Check, ssh map[string]string, after, unreached string) error {
	if len(checks) == 0 {
		return nil
	}
	env := smtpEnv(cfg, ssh)
	for _, c := range checks {
		step := r.Step(c.Title())
		if c.Skip != "" {
			step.End(ui.Warned, "not checked")
			r.Explain(c.Whose() + " is not checked: " + c.Skip + ".")
			continue
		}
		password := ""
		if c.PasswordPath != "" {
			password, _ = secrets.Get(c.PasswordPath)
			ui.Secret(password)
		}
		if err := smtpcheck.Login(env, c, password); err != nil {
			step.Fail(err)
			var failed *smtpcheck.Error
			if errors.As(err, &failed) && failed.Dialled() {
				hint, explain := mailFailure(cfg, failed)
				return &ui.Problem{Hint: hint, Explain: explain + after, Cause: err}
			}
			return &ui.Problem{Hint: c.From() + " could not be reached to try " + c.Whose(), Explain: "every app's mail settings are tried from its site before anything there changes." + unreached, Cause: err}
		}
		if c.PasswordPath != "" {
			step.Done("logs in")
		} else {
			step.Done("answers")
		}
	}
	return nil
}

// checkMailForValidate is validate's mail check: every app's settings logged
// in with from a site running it, nothing sent, and every failure a warning,
// since validate judges the file and a mail server is outside it. A site
// that cannot be reached, or a secrets file that cannot be read, skips the
// check with a warning rather than failing, so validate still works away
// from the hosts. It returns how many warnings it gave.
func checkMailForValidate(r ui.Reporter, cfg *config.Config, secretsPath string) int {
	checks := smtpcheck.Logins(cfg, &config.Secrets{}, nil)
	if len(checks) == 0 {
		return 0
	}
	r.Section("mail")
	secrets, err := config.LoadDeploymentSecrets(secretsPath, cfg.ID)
	if err != nil {
		r.Note("the mail settings were not checked", fmt.Sprintf("the secrets file could not be read (%v), and the check logs in with the passwords it holds. Run validate where the secrets file and its key are, or pass --offline to check the file alone", ui.Redact(err.Error())))
		return 1
	}
	env := smtpEnv(cfg, nil)
	warned := 0
	unreachable := map[string]bool{}
	for _, c := range smtpcheck.Logins(cfg, secrets, nil) {
		step := r.Step(c.Title())
		switch {
		case c.Skip != "":
			step.End(ui.Warned, "not checked")
			r.Explain(c.Whose() + " is not checked: " + c.Skip + ".")
			warned++
			continue
		case unreachable[c.Site]:
			step.End(ui.Warned, "skipped")
			r.Explain(c.Site + " could not be reached, so " + c.Whose() + " was not checked.")
			warned++
			continue
		}
		password := ""
		if c.PasswordPath != "" {
			password, _ = secrets.Get(c.PasswordPath)
			ui.Secret(password)
		}
		err := smtpcheck.Login(env, c, password)
		var failed *smtpcheck.Error
		switch {
		case err == nil && c.PasswordPath != "":
			step.Done("logs in")
		case err == nil:
			step.Done("answers")
		case errors.As(err, &failed) && failed.Dialled():
			step.End(ui.Warned, "does not log in")
			_, explain := mailFailure(cfg, failed)
			r.Explain(explain + " `" + progname.Cmd() + " deployment apply` refuses until it does.")
			warned++
		default:
			unreachable[c.Site] = true
			step.End(ui.Warned, "skipped")
			why := ui.Redact(err.Error())
			if errors.Is(err, apply.ErrUnreachable) {
				why = "ssh could not connect"
			}
			r.Explain(fmt.Sprintf("%s could not be reached (%s), so %s was not checked. validate works without the hosts; apply checks again before it changes anything.", c.Site, strings.TrimSuffix(why, "."), c.Whose()))
			warned++
		}
	}
	return warned
}

// trySMTPPassword is how `secrets set` tries an SMTP password before writing
// it: for each set of settings an app logs in with under key, from this
// machine, it logs in with value and sends the operators the test message
// from the app's sender (smtpcheck.ForPassword). secrets already holds value
// at key, in memory only. A server that answers and refuses refuses, and the
// caller writes nothing; done says what an interrupted walk already wrote.
// A server this machine cannot reach, and settings that cannot be tried
// from here, are warned about and left untried: apply tries the login from
// each site before it changes anything.
func trySMTPPassword(r ui.Reporter, cfg *config.Config, secrets *config.Secrets, key, value, done string) error {
	ui.Secret(value)
	checks := smtpcheck.ForPassword(cfg, secrets, key)
	if len(checks) == 0 {
		return nil
	}
	env := localSMTPEnv()
	r.Section("mail")
	var to []string
	for _, c := range checks {
		step := r.Step(c.Title())
		if c.Skip != "" {
			step.End(ui.Warned, "not tried")
			r.Explain(key + " is not tried for " + c.Whose() + ": " + c.Skip + ". `" + progname.Cmd() + " deployment apply` tries it from the site.")
			continue
		}
		if to == nil {
			var err error
			if to, err = smtpcheck.Recipients(cfg); err != nil {
				step.Fail(err)
				return &ui.Problem{Hint: "no one to send the test message to", Explain: fmt.Sprintf("%s is tried by sending a test message, and %s. %s.", key, err, done)}
			}
		}
		err := smtpcheck.Send(env, c, value, smtpcheck.TestMail(cfg, c, key, progname.Cmd()+" secrets set", to))
		if err == nil {
			step.Done("logs in; test message sent to " + strings.Join(to, ", "))
			continue
		}
		var failed *smtpcheck.Error
		if errors.As(err, &failed) && failed.Dialled() {
			step.Fail(err)
			return &ui.Problem{
				Hint:    key + " does not work for " + c.Whose(),
				Explain: fmt.Sprintf("%s logs in to %s with %s as %s. From %s the server answered: %s. Check the password at the mail provider, and the settings under %s, then set it again. %s.", c.Whose(), c.Address(), c.Security(), c.SMTP.Username, c.From(), failed.Reply(), c.Setting(cfg), done),
				Cause:   err,
			}
		}
		step.End(ui.Warned, "not reached")
		why := err.Error()
		if errors.As(err, &failed) {
			why = failed.Reply()
		}
		r.Explain(fmt.Sprintf("%s could not be reached from %s (%s), so %s is written untried. `%s deployment apply` tries it from each site running %s before it changes anything.", c.Address(), c.From(), strings.TrimSuffix(ui.Redact(why), "."), key, progname.Cmd(), appsOf(c)))
	}
	return nil
}

// appsOf is a check's apps, as a sentence names them.
func appsOf(c smtpcheck.Check) string {
	return strings.TrimSuffix(c.Whose(), "'s mail")
}
