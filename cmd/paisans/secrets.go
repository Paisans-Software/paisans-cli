package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// maxSecret bounds what `secrets set` reads. The largest secret this file
// holds is a PEM signing key of a few kilobytes; anything near this size is a
// wrong pipe, not a credential.
const maxSecret = 64 << 10

// runSecrets dispatches `paisans secrets <subcommand>`.
func runSecrets(args []string) error {
	switch {
	case len(args) >= 1 && args[0] == "list":
		return runSecretsList(args[1:], os.Stdout)
	case len(args) >= 1 && args[0] == "set":
		return runSecretsSet(args[1:], os.Stdin)
	case len(args) >= 1 && args[0] == "prune":
		return runSecretsPrune(args[1:], os.Stdin, os.Stdout)
	case len(args) >= 1 && args[0] == "rotate":
		return runSecretsRotate(args[1:], os.Stdin, os.Stdout)
	}
	return fmt.Errorf("secrets takes one subcommand, list, set, prune or rotate: %s secrets list, %s secrets set <dotted.key> [--secrets path] < value, %s secrets set at a terminal to be asked for each owed secret, %s secrets prune [--execute], or %s secrets rotate <path>... | --all [--execute]", progname.Cmd(), progname.Cmd(), progname.Cmd(), progname.Cmd(), progname.Cmd())
}

// hasSecrets reports whether the secrets file holds anything under key, a
// section and a name: sites.<n>, apps.<n> (with its sign-in client) or
// pocket_id_groups.<n>.
func hasSecrets(s *config.Secrets, key string) bool {
	kind, name, _ := strings.Cut(key, ".")
	switch kind {
	case "sites":
		_, ok := s.Sites[name]
		return ok
	case "apps":
		_, app := s.Apps[name]
		_, client := s.OIDCClients[name]
		return app || client
	case "pocket_id_groups":
		_, ok := s.PocketIDGroups[name]
		return ok
	}
	return false
}

// runSecretsPrune removes the secrets that name something paisans-stack.yaml no
// longer declares: a removed site's WireGuard key and heartbeat token, a
// removed app's passwords and sign-in client, a Pocket ID group nothing
// names. It lists them by key, never by value, and changes nothing without
// --execute. A sign-in client's secret going does not delete the client at
// Pocket ID, which it says.
func runSecretsPrune(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("secrets prune", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	id := idFlag(fs)
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually remove them and write the file")
	withoutRecord := fs.Bool("without-record", false, "trust "+config.FileName+" alone, when no gateway's deployment record can be read; asks for the word prune at a terminal")
	answers := yesFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	if _, err := ensureDraft(*configPath, *id); err != nil {
		return err
	}
	r := reporter()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	var deployed *deployrecord.Record
	if !*withoutRecord {
		if len(cfg.GatewaySites()) == 0 {
			return fmt.Errorf("secrets prune: %s declares no gateway, so there is no deployment record to read. Pass --without-record to trust "+config.FileName+" alone. Nothing was changed", *configPath)
		}
		rec, missing := readDeploymentRecord(r, cfg, func(gw string) registry.Runner { return registryHost(gw, cfg.Sites[gw], "") })
		if len(missing) > 0 {
			gw := sortedKeys(missing)[0]
			return fmt.Errorf("secrets prune: the deployment record on %s could not be read (%v), so nothing says whether what "+config.FileName+" no longer declares was removed or is still running. Apply the gateway first, or pass --without-record to trust "+config.FileName+" alone. Nothing was changed", gw, missing[gw])
		}
		deployed = &rec
	}
	for _, o := range secretsgen.Dropped(cfg, deployed) {
		hint := o.Key + " " + o.Why
		if hasSecrets(secrets, o.Key) {
			hint += "; its secrets are kept"
		}
		r.Note(hint, o.Leaves)
	}
	orphans := secretsgen.Orphans(cfg, secrets, deployed)
	if len(orphans) == 0 {
		r.Result("%s names nothing "+config.FileName+" does not declare. Nothing to prune.", *secretsPath)
		return nil
	}
	for _, o := range orphans {
		detail := "removed by `" + progname.Cmd() + " secrets prune --execute`"
		if o.Leaves != "" {
			detail = o.Leaves
		}
		r.Note("secrets: "+o.Key+" "+o.Why, detail)
	}
	if !*execute {
		r.Result("Nothing changed: %d secret(s) to remove. Re-run with --execute to remove them.", len(orphans))
		return nil
	}
	recipients, err := config.Recipients(filepath.Dir(*secretsPath))
	if err != nil {
		return err
	}
	if secrets.Encrypted && len(recipients) == 0 {
		return config.NoRecipientProblem(*secretsPath)
	}
	if *withoutRecord {
		if err := confirmWord(ans, stdin, stdout, "prune", ui.Danger(stdout, fmt.Sprintf("This permanently removes the %d secret(s) above, trusting "+config.FileName+" alone.", len(orphans)))); err != nil {
			return fmt.Errorf("secrets prune: %w", err)
		}
	}
	s := r.Step("prune secrets")
	secretsgen.Prune(secrets, orphans)
	if err := config.WriteSecrets(*secretsPath, secrets, recipients); err != nil {
		s.Fail(err)
		return err
	}
	s.Done(fmt.Sprintf("%d removed", len(orphans)))
	r.Result("%s no longer names anything "+config.FileName+" does not declare.", *secretsPath)
	if len(recipients) == 0 {
		warnUnencrypted(r, *secretsPath)
	}
	return nil
}

// runSecretsSet writes one value, read from stdin, into the encrypted secrets
// file. Given a key at a terminal, it asks for that key's value with echo off
// instead of reading stdin. Given no key at a terminal, it asks for each owed
// secret in turn (see setOwedSecrets).
//
// It exists so a pasted credential never touches a terminal or an editor. The
// alternatives each leave a copy somewhere: an argument is in shell history
// and in `ps`, an echoed prompt is in a scrollback buffer, and
// `sops secrets.enc.yaml` opens the whole decrypted file in an editor that may
// keep swap or backup files. A pipe from a keychain or another host leaves
// none of those, and neither does a prompt with echo off.
//
// It refuses a key that is neither already set nor owed from elsewhere. A
// generated secret is created by `init`, and creating one by hand here would
// skip the shape checks init applies; changing one that exists is a rotation,
// which is allowed.
func runSecretsSet(args []string, stdin io.Reader) error {
	fs := newFlagSet("secrets set", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	id := idFlag(fs)
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	if len(args) > 0 && isHelpWord(args[0]) {
		return fs.Parse(args[:1])
	}
	key := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		key, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := ensureDraft(*configPath, *id); err != nil {
		return err
	}
	r := reporter()
	if fs.NArg() > 0 {
		return fmt.Errorf("secrets set: the value is read from stdin and never from an argument, which shell history and `ps` would both keep. Got extra argument(s)")
	}

	// With no key, one value on a pipe could belong to any owed secret, so
	// the walk needs a terminal to ask on. With a key at a terminal, the
	// value is asked for with echo off rather than read from stdin, where
	// typing it would echo.
	atTerminal := stdinIsTerminal(stdin)
	if key == "" && !atTerminal {
		return fmt.Errorf("secrets set: name the key first, as in `%s secrets set external.acme_dns_token < token`. Run it at a terminal with no key to be asked for each owed secret in turn, or with a key to be asked for that one", progname.Cmd())
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	// With a key, a control character already in that key's value does not
	// refuse the file, since setting the key is how it is repaired.
	secrets, err := config.LoadDeploymentSecretsReplacing(*secretsPath, cfg.ID, key)
	if err != nil {
		return err
	}

	if key == "" {
		return setOwedSecrets(r, cfg, secrets, *secretsPath)
	}

	if err := secretKeyAllowed(cfg, secrets, key); err != nil {
		return err
	}
	// Refused before the prompt, not after the operator has pasted
	// something that cannot be written.
	recipients, err := writableRecipients(secrets, *secretsPath)
	if err != nil {
		return err
	}

	var value string
	if atTerminal {
		_, set := secrets.Get(key)
		value, err = readKeySecret(key, set)
		if err != nil {
			return fmt.Errorf("secrets set: reading %s: %w. Nothing was written", key, err)
		}
		ui.Secret(value)
		if value == "" {
			r.Result("Nothing was written: %s is unchanged.", key)
			return nil
		}
		if len(value) > maxSecret {
			return fmt.Errorf("secrets set: %s: more than %d bytes, which is not a credential this file holds. Nothing was written", key, maxSecret)
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(stdin, maxSecret+1))
		if err != nil {
			return fmt.Errorf("secrets set: reading stdin: %w", err)
		}
		if len(data) > maxSecret {
			return fmt.Errorf("secrets set: more than %d bytes on stdin, which is not a credential this file holds", maxSecret)
		}
		value = string(data)
	}
	// One trailing newline, because `echo` and most password managers add
	// exactly one and no credential ends in one. Only one: a value that ends
	// in two kept the second on purpose.
	if strings.HasSuffix(value, "\r\n") {
		value = strings.TrimSuffix(value, "\r\n")
	} else {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return fmt.Errorf("secrets set: %s: nothing on stdin. An empty value is refused rather than written, since it reads as set and renders as missing", key)
	}
	if err := config.CheckPastedSecret(key, value); err != nil {
		return fmt.Errorf("secrets set: %w. Nothing was written", err)
	}

	if err := secrets.Set(key, value); err != nil {
		return err
	}
	// The same check render and apply make, so a malformed key is refused
	// here rather than at the next render.
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	// An SMTP password logs in and sends the operators a test message
	// first, so one that does not work is never written.
	if smtpcheck.IsPasswordPath(key) {
		if err := trySMTPPassword(r, cfg, secrets, key, value, "Nothing was written"); err != nil {
			return err
		}
	}

	if err := config.WriteSecrets(*secretsPath, secrets, recipients); err != nil {
		return err
	}
	// The name, never the value.
	r.Result("Set %s.", key)
	if len(recipients) == 0 {
		warnUnencrypted(r, *secretsPath)
	}
	return nil
}

// setOwedSecrets asks at the terminal for each secret only the operator can
// provide, saying why each is owed, and writes each answer before asking for
// the next, so an interrupted walk keeps what was already typed. A blank
// answer skips the key, which stays owed and is named at the end. A pending
// secret, such as a sign in client, is not asked for: apply records it.
func setOwedSecrets(r ui.Reporter, cfg *config.Config, secrets *config.Secrets, secretsPath string) error {
	owed := secretsgen.OwedSecrets(cfg, secrets)
	if len(owed) == 0 {
		r.Result("Nothing is owed: every secret only you can provide is set.")
		return nil
	}
	// Refused before the first prompt, not after the operator has pasted
	// something that cannot be written.
	recipients, err := writableRecipients(secrets, secretsPath)
	if err != nil {
		return err
	}
	var done, skipped []string
	for _, o := range owed {
		value, err := readOwedSecret(o)
		if err != nil {
			return fmt.Errorf("secrets set: reading %s: %w. %s", o.Name, err, setSoFar(done))
		}
		if value == "" {
			skipped = append(skipped, o.Name)
			continue
		}
		ui.Secret(value)
		if len(value) > maxSecret {
			return fmt.Errorf("secrets set: %s: more than %d bytes, which is not a credential this file holds. %s", o.Name, maxSecret, setSoFar(done))
		}
		if err := config.CheckPastedSecret(o.Name, value); err != nil {
			return fmt.Errorf("secrets set: %w. %s", err, setSoFar(done))
		}
		if err := secrets.Set(o.Name, value); err != nil {
			return err
		}
		if smtpcheck.IsPasswordPath(o.Name) {
			if err := trySMTPPassword(r, cfg, secrets, o.Name, value, setSoFar(done)); err != nil {
				return err
			}
		}
		s := r.Step("set " + o.Name)
		if err := config.WriteSecrets(secretsPath, secrets, recipients); err != nil {
			s.Fail(err)
			return err
		}
		s.Done("")
		done = append(done, o.Name)
	}
	for _, key := range skipped {
		r.Note("secrets: "+key+" was skipped and is still owed", progname.Cmd()+" secrets set "+key+" < value")
	}
	r.Result("Set %d of %d owed secret(s).", len(done), len(owed))
	if len(recipients) == 0 {
		warnUnencrypted(r, secretsPath)
	}
	return nil
}

// setSoFar says what an interrupted walk already wrote.
func setSoFar(done []string) string {
	if len(done) == 0 {
		return "Nothing was written"
	}
	return "Already written: " + strings.Join(done, ", ")
}

// readOwedSecret asks for one owed secret on the controlling terminal, with
// echo off, after saying why it is owed. An empty answer is a skip. Tests
// replace it, since a test has no terminal to type into.
var readOwedSecret = func(o secretsgen.Owed) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fd := int(tty.Fd())
	fmt.Fprintf(tty, "\n%s is %s.\n", o.Name, o.Why)
	return askPassword(tty, "Paste it, or press Enter to skip: ", 0, false, func() ([]byte, error) { return term.ReadPassword(fd) })
}

// readKeySecret asks for the value of one named key on the controlling
// terminal, with echo off, saying whether it replaces a value already set.
// The value set is never shown. An empty answer writes nothing. Tests
// replace it, since a test has no terminal to type into.
var readKeySecret = func(key string, set bool) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fd := int(tty.Fd())
	if set {
		fmt.Fprintf(tty, "\n%s is set; what you paste replaces it.\n", key)
	} else {
		fmt.Fprintf(tty, "\n%s is not set.\n", key)
	}
	return askPassword(tty, "Paste it, or press Enter to write nothing: ", 0, false, func() ([]byte, error) { return term.ReadPassword(fd) })
}

// writableRecipients is the age recipients the secrets file is written to,
// refusing a file that is encrypted when no recipient is named beside it,
// since writing it back would leave it in plaintext.
func writableRecipients(secrets *config.Secrets, secretsPath string) ([]string, error) {
	recipients, err := config.Recipients(filepath.Dir(secretsPath))
	if err != nil {
		return nil, err
	}
	if secrets.Encrypted && len(recipients) == 0 {
		return nil, config.NoRecipientProblem(secretsPath)
	}
	return recipients, nil
}

// secretKeyAllowed accepts a key that is already set, a key `init` reports as
// owed from elsewhere, and any key under external, which is by definition
// the section for pasted credentials. Everything else is refused, and an
// unknown top level section most loudly, because a typo there would write a
// value nothing reads.
func secretKeyAllowed(cfg *config.Config, secrets *config.Secrets, key string) error {
	section := key
	if i := strings.IndexByte(key, '.'); i >= 0 {
		section = key[:i]
	}
	switch section {
	case "cluster", "storage", "backup", "sites", "apps", "external", "oidc_clients":
	default:
		return fmt.Errorf("secrets set: %s: there is no %q section. Secrets live under cluster, storage, backup, sites, apps, external and oidc_clients", key, section)
	}
	if _, set := secrets.Get(key); set {
		return nil
	}
	if section == "external" {
		return nil
	}
	for _, name := range secretsgen.ExpectedNames(cfg, secrets) {
		if key == name || strings.HasPrefix(key, name+".") {
			return nil
		}
	}
	return errors.New("secrets set: " + key + " is neither set nor owed from elsewhere. A generated secret is created by `" + progname.Cmd() + " init`, which fills in what is missing; this command sets a pasted or captured one, or replaces one that exists")
}

// requireACMEToken refuses to render or apply a site running the toolkit's
// Caddy, a gateway or a monitor serving its own hostname, without the DNS
// provider's token.
//
// Without it the rendered Caddy starts, loads its configuration, and fails
// every DNS-01 challenge, so it serves no certificate for any hostname. That
// is discovered by the first visitor, not by the apply, so it is a refusal
// rather than the "still owed" note init prints.
func requireACMEToken(cfg *config.Config, secrets *config.Secrets, sites []string) error {
	if secrets.External["acme_dns_token"] != "" {
		return nil
	}
	for _, site := range sites {
		if cfg.Sites[site].RunsCaddy() {
			return fmt.Errorf("%s runs the toolkit's Caddy (a gateway, or a monitor with managed_by: paisans), and secrets external.acme_dns_token is empty, so it could obtain no certificate. Get a token from %s scoped to this zone, then pipe it in: `%s secrets set external.acme_dns_token < token-file`", site, cfg.ACME.Provider, progname.Cmd())
		}
	}
	return nil
}
