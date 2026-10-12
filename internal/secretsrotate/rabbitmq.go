package secretsrotate

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// RabbitMQ keeps its accounts in its data directory. The image's
// RABBITMQ_DEFAULT_USER and RABBITMQ_DEFAULT_PASS create the first account
// only when the broker starts on an empty data directory (the Docker
// Official Image for rabbitmq, README, "Environment variables": the two
// variables set the default user, "applied only on first start", and the
// broker's own documentation on the default user says the same; taken from
// those documents and not fetched for this). The compose file keeps the data
// directory on the host (rabbitmq_data), so rendering the next value into
// .env changes nothing the broker checks. Stage 2 therefore changes the
// password inside the broker, with rabbitmqctl, on each site that runs it
// (every site renders its own rabbitmq beside the app, and its own data
// directory), and stage 3 hands the Mbin stack the same value.
//
// Between the two, the broker takes the next value while amqproxy and the
// messenger consumers still present the old one: connections they already
// hold stay open, and a new one is refused until stage 3 restarts the stack on
// that site. That is the restart the plan names.

// mbinMQUser is the account RABBITMQ_DEFAULT_USER names in the Mbin kind's
// .env.secret.tmpl, and the one MESSENGER_TRANSPORT_DSN logs in as.
const mbinMQUser = "mbin"

type mbinRabbitMQ struct {
	Generated
	app string
}

// MbinRabbitMQ is the rotation of apps.<app>.rabbitmq_password: the password
// of the mbin account inside the RabbitMQ broker the app's stack runs on each
// of its sites, then the stack itself. An app whose queue is not rabbitmq runs
// no broker and has no such account: Precheck reports the secret kept.
func MbinRabbitMQ(app string) Rotator {
	return mbinRabbitMQ{Generated: Generated{SecretPath: "apps." + app + ".rabbitmq_password"}, app: app}
}

// mqGenerate is where a new value comes from; a test replaces it.
var mqGenerate = secretsgen.Replacement

// Next is a new password that does not begin with a dash, drawn again until
// it does not.
func (r mbinRabbitMQ) Next(e Env, s *config.Secrets) (string, error) {
	for i := 0; i < 64; i++ {
		v, err := mqGenerate(r.SecretPath, s)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(v, "-") {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: no password without a leading dash after 64 draws", r.SecretPath)
}

func (r mbinRabbitMQ) Cost() Cost { return Cost{Downtime: mbinRestart} }

func (r mbinRabbitMQ) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if app.Kind != config.KindMbin {
		return fmt.Errorf("apps.%s is %s, and only Mbin runs RabbitMQ", r.app, app.Kind)
	}
	if q := kinds.MbinQueue(app.Settings); q != kinds.MbinQueueRabbitMQ {
		return &KeptError{Path: r.Path(), Reason: fmt.Sprintf("apps.%s runs its queue on %s and starts no RabbitMQ, so no account has this password", r.app, q)}
	}
	return runsSomewhere(e, r.app)
}

// ctl is how rabbitmqctl is reached on a site, with the password on stdin.
// rabbitmqctl takes a password only as an argument (change_password and
// authenticate_user are documented as `<username> <password>`, with no stdin
// form; taken from the rabbitmqctl man page and not fetched for this), so a
// shell inside the container reads it from stdin and passes it on. The
// password therefore sits in rabbitmqctl's argv for the length of the call,
// and a container's processes are visible in the host's process table too, so
// it is in both the container's and the host's `ps` and /proc for that
// moment. That is accepted because rabbitmqctl offers no other way in and the
// call is short; what is kept out is the ssh command line, the shell history
// and every log. A positional argument that begins with "-" is read as an
// option, which is why a value that does that is never made and never sent.
func (r mbinRabbitMQ) ctl(e Env, verb string) string {
	return fmt.Sprintf(`%s exec -T rabbitmq sh -c 'IFS= read -r p; exec rabbitmqctl %s %s "$p"'`,
		e.Cfg.Deployment().ComposeCmd(r.app), verb, mbinMQUser)
}

func (r mbinRabbitMQ) sendable(next string) error {
	if strings.HasPrefix(next, "-") {
		return fmt.Errorf(`%s: the password begins with a dash, which rabbitmqctl would read as an option, so it cannot be set or checked. Remove the pending value as README.md, "A rotation that cannot finish", says, and run again for a new one`, r.Path())
	}
	if next == "" || strings.ContainsAny(next, "\n\x00") {
		return fmt.Errorf("%s: the password is empty or holds a newline or NUL byte, which cannot be sent to the broker", r.Path())
	}
	ui.Secret(next)
	return nil
}

// Checked changes the password inside the broker on every site that runs one.
// It sets the same account to the same value, so it is safe to run again.
func (r mbinRabbitMQ) Checked(e Env, next string) error {
	if err := r.sendable(next); err != nil {
		return err
	}
	for _, u := range appUses(e, r.app) {
		t, ok := e.Sites[u.Site]
		if !ok {
			return fmt.Errorf("%s: no way to reach it", u.Site)
		}
		if out, err := t.RunInput(r.ctl(e, "change_password"), next+"\n"); err != nil {
			return fmt.Errorf("%s: changing the broker's password: %w: %s", u.Site, err, lastLine(out))
		}
	}
	return nil
}

// Accepts reports whether the broker on every site authenticates the account
// with next. A broker that refuses the password answers "Failed to
// authenticate"; anything else that stops the check says nothing about the
// password and is an error.
func (r mbinRabbitMQ) Accepts(e Env, next string) (bool, error) {
	if err := r.sendable(next); err != nil {
		return false, err
	}
	all := true
	for _, u := range appUses(e, r.app) {
		t, ok := e.Sites[u.Site]
		if !ok {
			return false, fmt.Errorf("%s: no way to reach it", u.Site)
		}
		out, err := t.RunInput(r.ctl(e, "authenticate_user"), next+"\n")
		if err == nil {
			continue
		}
		if strings.Contains(out, "Failed to authenticate") {
			all = false
			continue
		}
		return false, fmt.Errorf("%s: asking the broker to authenticate: %w: %s", u.Site, err, lastLine(out))
	}
	return all, nil
}

func (r mbinRabbitMQ) Users(e Env) []Use { return appUses(e, r.app) }
