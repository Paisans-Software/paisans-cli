package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/doctor"
	"github.com/paisans-software/paisans-cli/internal/meshrekey"
	"github.com/paisans-software/paisans-cli/internal/oidcclient"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/secretsrotate"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
	"golang.org/x/term"
)

// rotateHost is how `secrets rotate` reaches a site. Tests replace it.
var rotateHost = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// rotateDNSProvider makes the provider a next DNS token is tried against, the
// one dns init uses. Tests replace it: none may reach a real provider.
var rotateDNSProvider = dns.For

// readRotateSecret asks on the controlling terminal, with echo off, for the
// next value of a pasted secret every host using it runs: it says the path,
// that it is replaced at its issuer now, and how to stop. An empty answer
// stops the run. Tests replace it, since a test has no terminal to type into.
var readRotateSecret = func(path, issuer string) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fd := int(tty.Fd())
	fmt.Fprintf(tty, "\nEvery host using %s runs the value the secrets file holds.\nReplace it at %s now, then paste the new one here. Where %s keeps the old one valid beside it, revoke the old one there once this rotation finishes.\n", path, issuer, issuer)
	return askPassword(tty, "Paste it, or press Enter to stop with nothing changed: ", 0, false, func() ([]byte, error) { return term.ReadPassword(fd) })
}

func rotateUsage() string {
	return "Usage: " + progname.Name() + " secrets rotate <path>... | --all [--restart] [--replaced <path>]... [--execute]"
}

// replacedEarlier is how the plan lists a pasted secret named with
// --replaced.
const replacedEarlier = "replaced earlier, hosts already run it: the value the secrets file holds is what every host using it runs, so there is nothing to hand out"

// wireguardKept is why secrets rotate leaves the WireGuard keys alone.
func wireguardKept() string {
	return "a WireGuard key is replaced by `" + progname.Name() + " mesh rekey`, which moves it without dropping a tunnel"
}

// backupKeyKept is why the restic password and WAL-G's key are kept. restic
// wraps its repository's master key in each password, so a new password
// leaves the key it opens unchanged, and WAL-G has no re-encryption at all.
const backupKeyKept = "it encrypts every backup already taken, and a new one would not re-encrypt them: whoever held the old one can still read any copy they made, so rotating it would take nothing back. What keeps an operator who left out of the backups is the destinations' credentials"

// destinationKept is why a backup destination's credentials are kept.
func destinationKept() string {
	return "it is issued by the provider behind the destination: issue a new key there, pipe it in with `" + progname.Name() + " secrets set`, apply, then delete the old key at the provider"
}

// rotateStopped is what a failed health gate inside secrets rotate tells the
// operator.
const rotateStopped = "secrets rotate stopped here, with the next value kept in the secrets file; run it again to resume."

// keptSecret is a secret in scope that this run does not rotate, with why.
type keptSecret struct{ Path, Reason string }

// rotateWiring is how the rotations that keep their own staging are run: the
// existing commands, each for one app.
type rotateWiring struct {
	oidcRun, oidcRetire, s3Run func(app string) error
	// cfg is the deployment, whose apps' blueprints say how each of their
	// secrets rotates.
	cfg *config.Config
}

// runSecretsRotate replaces secrets where they are checked and
// where they are used, in staged runs that resume: internal/secretsrotate,
// and docs/specs/2026-10-12-secrets-rotate.md. Like every command that
// changes a host, it plans from what it reads and changes nothing without
// --execute; and everything that refuses the run is checked before anything
// changes.
func runSecretsRotate(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("secrets rotate", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually rotate, one secret at a time, stopping at the first gate that fails")
	all := fs.Bool("all", false, "rotate every secret in scope the deployment has, in the order the spec gives")
	restart := fs.Bool("restart", false, "with --all: discard what an earlier --all run left and rotate every secret again (refused while a rotation is pending)")
	var replaced pathList
	fs.Var(&replaced, "replaced", "a pasted secret already replaced at its issuer and applied, which every host using it runs: listed, not rotated (repeatable)")
	answers := yesFlag(fs)
	var named []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		named, args = append(named, args[0]), args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	named = append(named, fs.Args()...)
	ans, err := answers()
	if err != nil {
		return err
	}
	r := reporter()
	switch {
	case *all && len(named) > 0:
		return &ui.Problem{Hint: "--all takes no path", Explain: "Name the secrets to rotate, or pass --all for every one. Got " + strings.Join(named, " ") + " too. " + rotateUsage()}
	case *restart && !*all:
		return &ui.Problem{Hint: "--restart takes --all", Explain: "--restart starts an --all run's full list again, discarding what an earlier --all run left. " + rotateUsage()}
	case !*all && len(named) == 0:
		return &ui.Problem{Hint: "secrets rotate needs a secret", Explain: "It names the secrets to rotate by their paths in the secrets file, Eg: " + progname.Name() + " secrets rotate cluster.standby_password, or apps.<app>.<unit> for a unit of secrets an app's blueprint groups, such as apps.talk.oauth; or --all for every one. " + rotateUsage()}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	store := secretsStore{path: *secretsPath, id: cfg.ID}
	secrets, err := store.Load()
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	if err := refuseRotateMidRekey(secrets); err != nil {
		return err
	}
	if err := refuseReplacedPending(secrets, replaced); err != nil {
		return err
	}
	if *all {
		if err := refuseUnfilled(cfg, store, *secretsPath); err != nil {
			return err
		}
	}
	if *execute {
		// Every stage writes the file, and an encrypted file with no
		// recipient beside it would be written back in plaintext.
		recipients, err := config.Recipients(filepath.Dir(*secretsPath))
		if err != nil {
			return err
		}
		if secrets.Encrypted && len(recipients) == 0 {
			return config.NoRecipientProblem(*secretsPath)
		}
	}

	wiring := rotateWiring{
		cfg: cfg,
		oidcRun: func(app string) error {
			return runOIDCClientCreate(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--rotate-secret", "--execute"))
		},
		oidcRetire: func(app string) error { return retireClientSecrets(r, cfg, store, app) },
		s3Run: func(app string) error {
			return runStorageRotateKey(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--execute"))
		},
	}
	targets, kept, err := rotateTargets(cfg, secrets, named, *all, wiring)
	if err != nil {
		return err
	}
	targets, replacedRs, err := takeReplaced(targets, replaced, wiring)
	if err != nil {
		return err
	}

	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = rotateHost(name, cfg.Sites[name])
	}
	if err := reachEverySite(r, cfg, transports); err != nil {
		return err
	}
	if err := ui.Run(r, "check etcd and Patroni", func() error { return meshrekey.ClusterHealth(cfg, transports) }); err != nil {
		return fmt.Errorf("secrets rotate: %w. A password changed on a limping cluster can leave a member unable to rejoin, so nothing is rotated until it is healthy. Nothing was changed", err)
	}

	env := secretsrotate.Env{
		Cfg:   cfg,
		Store: store,
		Sites: transports,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			return rotateApply(r, cfg, transports[site], site, s, stacks)
		},
		MailFrom: func(site, path string, s *config.Secrets, stacks []string) error {
			return checkMailForRotation(r, cfg, s, path, site, stacks)
		},
		Healthy: func(site string, stacks []string) error {
			for _, stack := range stacks {
				if err := apply.WaitHealthy(cfg.Deployment(), site, stack, transports[site], rotateStopped); err != nil {
					return err
				}
			}
			return nil
		},
		Now:         time.Now,
		DNSProvider: rotateDNSProvider,
		Mail:        localSMTPEnv(),
		Warn:        onceEach(r.Note),
	}
	run, err := ui.Get(r, "read where each secret stands", func() (*secretsrotate.Run, error) {
		if err := checkReplaced(env, replacedRs); err != nil {
			return nil, err
		}
		return planRotation(env, targets, *all, *restart)
	})
	if err != nil {
		return err
	}
	for _, k := range run.Kept {
		kept = append(kept, keptSecret{Path: k.Path, Reason: k.Reason})
	}
	for i := range run.Items {
		run.Items[i].Notes = rotationNotes(cfg, run.Items[i].Rotator.Path())
	}
	showKept(r, kept)
	if !*execute || r.Verbose() {
		run.Show(r)
	} else {
		run.ShowResume(r)
	}
	if len(run.Items) == 0 {
		return nothingToRotate(r, env, run, *execute)
	}
	// Asked before any host's admission is checked, so a refusal reaches
	// no host.
	confirm := func(word, what string) error { return confirmWord(ans, stdin, stdout, word, what) }
	if *execute {
		if err := run.ConfirmSignOut(confirm); err != nil {
			return err
		}
		if err := askPasted(env, run, stdin); err != nil {
			return err
		}
	}
	// Every site may be handed a value, and each is written to only once its
	// admission is checked, as apply's are.
	runners := map[string]registry.Runner{}
	for name, t := range transports {
		runners[name] = t
	}
	if _, err := admittedHosts(r, cfg, runners); err != nil {
		return err
	}
	if !*execute {
		r.Result("Nothing changed. Re-run with --execute to rotate.")
		return nil
	}
	run.Report = r
	if err := run.Execute(env, confirm); err != nil {
		return err
	}
	var done []string
	for _, it := range run.Items {
		done = append(done, it.Rotator.Path())
	}
	again := ""
	if run.Resumed {
		again = " The secrets the earlier --all run finished were not rotated again."
	}
	r.Result("%s rotated: every host that checks or uses each one runs its new value, and %s holds only the new values.%s", strings.Join(done, ", "), *secretsPath, again)
	return nil
}

// nothingToRotate ends a run with nothing left to rotate. A resumed --all
// whose every path left is now kept still has the file recording them, and
// executing it clears that record, so the next --all is a full one rather
// than another that does nothing.
func nothingToRotate(r ui.Reporter, env secretsrotate.Env, run *secretsrotate.Run, execute bool) error {
	if !run.Resumed {
		r.Result("Nothing to rotate: every secret named is kept. Nothing changed.")
		return nil
	}
	if !execute {
		r.Result("Nothing to rotate: every secret the earlier --all run left is kept. Nothing changed. Re-run with --execute to clear what the secrets file records as left.")
		return nil
	}
	if err := run.Execute(env, nil); err != nil {
		return err
	}
	r.Result("Nothing to rotate: every secret the earlier --all run left is kept, and the secrets file no longer records that run. The secrets it finished were not rotated again.")
	return nil
}

// askPasted asks for the next value of every pasted secret in the run whose
// value every host using it runs, all before any host is reached, so the run
// never stops part way to wait on a person. Each answer is refused, with
// nothing changed, when it is empty, unchanged, too long to be a credential,
// or not accepted where the secret is checked (secretsrotate.Run.Answer). Run
// without a terminal it is refused, with the way to set the value first.
func askPasted(env secretsrotate.Env, run *secretsrotate.Run, stdin io.Reader) error {
	asked := run.Asked()
	if len(asked) == 0 {
		return nil
	}
	if !stdinIsTerminal(stdin) {
		var paths, steps []string
		for _, a := range asked {
			paths = append(paths, a.Path)
			steps = append(steps, fmt.Sprintf("replace %s at %s and set it with `"+progname.Name()+" secrets set %s < file`", a.Path, a.Issuer, a.Path))
		}
		return fmt.Errorf("secrets rotate: every host using %s runs the value the secrets file holds, so the new value is asked for at a terminal, and stdin is not one. Without one, %s, then run secrets rotate again, which hands out the value the file holds. Nothing was changed", strings.Join(paths, ", "), strings.Join(steps, "; "))
	}
	for _, a := range asked {
		value, err := readRotateSecret(a.Path, a.Issuer)
		ui.Secret(value)
		if err != nil {
			return fmt.Errorf("secrets rotate: reading %s: %w. Nothing was changed", a.Path, err)
		}
		if len(value) > maxSecret {
			return fmt.Errorf("secrets rotate %s: more than %d bytes, which is not a credential this file holds. Nothing was changed", a.Path, maxSecret)
		}
		if err := run.Answer(env, a.Path, value); err != nil {
			return err
		}
	}
	return nil
}

// subcommandArgs is args, with --verbose when r is verbose, for a command
// secrets rotate runs in its place in the order: a command's flags reset the
// verbosity they share.
func subcommandArgs(r ui.Reporter, args ...string) []string {
	if r.Verbose() {
		args = append(args, "--verbose")
	}
	return args
}

// retireClientSecrets deletes every secret of app's client at Pocket ID but
// the one the secrets file now records (oidcclient.Retire). The file is read
// again here, after `oidc client create --rotate-secret` recorded the new
// secret, since a value read before it would name the old secret as the one
// to keep and delete the new one.
func retireClientSecrets(r ui.Reporter, cfg *config.Config, store secretsrotate.Store, app string) error {
	s, err := store.Load()
	if err != nil {
		return err
	}
	idp := pocketIDApp(cfg)
	if idp == "" {
		return fmt.Errorf("the configuration declares no pocket-id app holding %s's client", app)
	}
	where, err := pocketIDSite(r, cfg, idp, "", "secrets rotate")
	if err != nil {
		return err
	}
	key, _ := s.Get("apps." + idp + ".static_api_key")
	if key == "" {
		return fmt.Errorf("secrets apps.%s.static_api_key is empty, so Pocket ID cannot be asked to delete %s's old client secrets", idp, app)
	}
	return oidcclient.Retire(context.Background(), clientAPI(cfg, where, "", key), app, recordedClient(s, app), r)
}

// reachEverySite refuses the run when any site does not answer over ssh: a
// site that keeps the old value breaks when the old one is retired.
func reachEverySite(r ui.Reporter, cfg *config.Config, transports map[string]apply.Transport) error {
	r.Section("reach")
	var silent []string
	var last error
	for _, name := range cfg.SiteNames() {
		step := r.Step(name)
		if _, err := transports[name].Run(doctor.ReachCommand); err != nil {
			step.Fail(err)
			silent, last = append(silent, name), err
			continue
		}
		step.Done("")
	}
	if len(silent) == 0 {
		return nil
	}
	verb := "does"
	if len(silent) > 1 {
		verb = "do"
	}
	return fmt.Errorf("secrets rotate: %s %s not answer over ssh (%v). A site that is not handed a secret's new value keeps the old one, and breaks when the old one is retired, so nothing is rotated until every site answers. Nothing was changed", strings.Join(silent, ", "), verb, last)
}

// rotateApply is a scoped apply of stacks on site, rendered from s, the
// secrets with the next value promoted, and never from the file: the same
// plan as `paisans deployment apply --site <site> --only <stack>`, its refusal of a
// file edited on the host and its health gate. A rotation rewrites
// environments, not images, so every deployed build is kept.
func rotateApply(r ui.Reporter, cfg *config.Config, t apply.Transport, site string, s *config.Secrets, stacks []string) error {
	if t == nil {
		return fmt.Errorf("%s: no way to reach it", site)
	}
	plan, err := scopedApplyPlan(cfg, s, site, t, true, stacks...)
	if err != nil {
		return err
	}
	if c := plan.Conflicts(); len(c) > 0 {
		return fmt.Errorf("%s's %s differs from what the last apply recorded, so somebody edited it on the host. Nothing was changed on %s. Restore it, or run `"+progname.Name()+" deployment apply --site %s --only %s --overwrite %s`, then run secrets rotate again", site, c[0].Path, site, site, strings.Join(stacks, " --only "), c[0].Path)
	}
	plan.Report = r
	return apply.Execute(plan, t)
}

// showKept lists the secrets in scope that this run keeps, each with why.
func showKept(r ui.Reporter, kept []keptSecret) {
	if len(kept) == 0 {
		return
	}
	r.Section("kept")
	for _, k := range kept {
		r.Step(k.Path).End(ui.OK, "kept")
		r.Explain(k.Reason)
	}
}

// planRotation plans rs as a run of named secrets, an --all run, or an
// --all run started again. A rotator whose precheck finds its secret kept
// (secretsrotate.KeptError: nothing checks it on this deployment) is listed
// in the run's Kept with its reason, rather than refusing the run.
func planRotation(e secretsrotate.Env, rs []secretsrotate.Rotator, all, restart bool) (*secretsrotate.Run, error) {
	switch {
	case restart:
		return secretsrotate.PlanAllRestart(e, rs)
	case all:
		return secretsrotate.PlanAll(e, rs)
	}
	return secretsrotate.Plan(e, rs)
}

// refuseReplacedPending refuses --replaced for a secret whose rotation is
// under way: some hosts may run the next value and some the old, and only
// finishing the rotation settles which.
func refuseReplacedPending(s *config.Secrets, replaced []string) error {
	for _, path := range replaced {
		if slices.Contains(s.RotationPending(), path) {
			return fmt.Errorf("secrets rotate: --replaced %s, but a rotation of it is under way, so its hosts may run either value. Finish it with "+progname.Name()+" secrets rotate %s --execute. Nothing was changed", path, path)
		}
	}
	return nil
}

// takeReplaced is targets with each secret --replaced names kept in its
// place rather than rotated (secretsrotate.Keep), or added kept when targets
// lacks it, and the rotators of those, which must be pasted: only a secret
// issued elsewhere can have been replaced and applied without a rotation,
// and a generated one named here would be left as the departed operator knew
// it. Kept in its place, a path an earlier --all run left is taken off that
// run's list rather than refused as one with no rotation.
func takeReplaced(targets []secretsrotate.Rotator, replaced []string, w rotateWiring) ([]secretsrotate.Rotator, []secretsrotate.Rotator, error) {
	var rs []secretsrotate.Rotator
	for _, path := range replaced {
		if slices.ContainsFunc(rs, func(r secretsrotate.Rotator) bool { return r.Path() == path }) {
			continue
		}
		r, err := rotatorFor(path, w)
		if err != nil {
			return nil, nil, err
		}
		if !secretsrotate.Pasted(r) {
			return nil, nil, fmt.Errorf("secrets rotate: --replaced takes a pasted secret (the DNS token or an SMTP password), and %s is generated here, so only a rotation replaces it. Nothing was changed", path)
		}
		rs = append(rs, r)
	}
	out := slices.Clone(targets)
	for _, r := range rs {
		k := secretsrotate.Keep(r, replacedEarlier)
		if i := slices.IndexFunc(out, func(t secretsrotate.Rotator) bool { return t.Path() == r.Path() }); i >= 0 {
			out[i] = k
		} else {
			out = append(out, k)
		}
	}
	return out, rs, nil
}

// checkReplaced refuses a secret --replaced names unless every host using it
// is read to run the value the secrets file holds, the same reading a pasted
// secret's precheck makes. One a host does not run yet, or whose hosts
// cannot be read, still has a value to hand out, and rotating it is how that
// is done. The run lists the rest as kept (takeReplaced).
func checkReplaced(e secretsrotate.Env, rs []secretsrotate.Rotator) error {
	for _, r := range rs {
		ok, err := secretsrotate.HostsRun(e, r)
		if err != nil {
			return fmt.Errorf("secrets rotate: --replaced %s, and whether its hosts run the value the secrets file holds cannot be told: %w. Nothing was changed", r.Path(), err)
		}
		if !ok {
			return fmt.Errorf("secrets rotate: --replaced %s, but not every host using it runs the value the secrets file holds, so it still has a value to hand out. Rotate it without --replaced. Nothing was changed", r.Path())
		}
	}
	return nil
}

// rotateTargets is the rotators of a run, in order: every secret in scope
// for --all, or the named ones; then, for each rotation an earlier run left
// pending that is not among them, its own, since the engine finishes those
// first. kept is what --all leaves alone, with why.
func rotateTargets(cfg *config.Config, s *config.Secrets, named []string, all bool, w rotateWiring) ([]secretsrotate.Rotator, []keptSecret, error) {
	var rs []secretsrotate.Rotator
	var kept []keptSecret
	if all {
		var err error
		if rs, kept, err = rotateAll(cfg, s, w); err != nil {
			return nil, nil, err
		}
	} else {
		for _, path := range named {
			r, err := rotatorFor(path, w)
			if err != nil {
				return nil, nil, err
			}
			rs = append(rs, r)
		}
	}
	has := map[string]bool{}
	var out []secretsrotate.Rotator
	for _, r := range rs {
		if !has[r.Path()] {
			has[r.Path()] = true
			out = append(out, r)
		}
	}
	for _, path := range s.RotationPending() {
		if has[path] {
			continue
		}
		r, err := rotatorFor(path, w)
		if err != nil {
			return nil, nil, fmt.Errorf("secrets rotate: %s has a rotation under way, and no rotation of it can be built to finish it: %w. %s", path, err, cannotFinish)
		}
		has[path] = true
		out = append(out, r)
	}
	return out, kept, nil
}

// rotatorFor is the rotation of the secret at path, or why secrets rotate
// does not rotate it.
func rotatorFor(path string, w rotateWiring) (secretsrotate.Rotator, error) {
	parts := strings.Split(path, ".")
	last := parts[len(parts)-1]
	switch {
	case path == "external.acme_dns_token":
		return secretsrotate.DNSToken(), nil
	case path == "external.smtp_password", len(parts) == 3 && parts[0] == "apps" && last == "smtp_password":
		return secretsrotate.SMTPPassword(path), nil
	case path == "backup.restic_password", path == "backup.walg_libsodium_key":
		return nil, fmt.Errorf("secrets rotate: %s is kept: %s", path, backupKeyKept)
	case len(parts) == 4 && parts[0] == "backup" && parts[1] == "destinations":
		return nil, fmt.Errorf("secrets rotate: %s is kept: %s", path, destinationKept())
	case strings.HasPrefix(last, "wireguard_"):
		return nil, fmt.Errorf("secrets rotate: %s is not rotated here: %s", path, wireguardKept())
	case path == "storage.garage.admin_token":
		return secretsrotate.GarageAdminToken(), nil
	case path == "storage.garage.rpc_secret":
		return secretsrotate.GarageRPC(), nil
	case len(parts) == 2 && parts[0] == "cluster" && strings.HasSuffix(last, "_password"):
		return secretsrotate.ClusterRole(strings.TrimSuffix(last, "_password")), nil
	case len(parts) == 3 && parts[0] == "sites" && last == "heartbeat_token":
		return secretsrotate.Heartbeat(parts[1]), nil
	case len(parts) == 3 && parts[0] == "oidc_clients" && last == "client_secret":
		return secretsrotate.OIDCClient(parts[1], w.oidcRun, w.oidcRetire), nil
	case len(parts) == 3 && parts[0] == "apps":
		app := parts[1]
		if r, err, ok := declaredRotator(w.cfg, path, app, last); ok {
			return r, err
		}
		switch last {
		case "s3_access_key_id", "s3_secret_access_key":
			return secretsrotate.AppS3(app, w.s3Run), nil
		case "database_password":
			return secretsrotate.AppDatabase(app), nil
		case "admin_password":
			return secretsrotate.UptimeAdmin(app), nil
		case "operators_heartbeat_token", "dns_super_admins_heartbeat_token", "dns_2fa_heartbeat_token":
			return secretsrotate.ContinuityHeartbeat(app, last), nil
		case blueprints.HeartbeatTokenKey:
			if w.cfg != nil {
				if _, ok := blueprints.HeartbeatOf(w.cfg.Apps[app].Blueprint); ok {
					return secretsrotate.AppHeartbeat(app), nil
				}
			}
		}
		if _, ok := secretsrotate.AppSelfBlueprint(last); ok {
			return secretsrotate.AppSelf(app, last, costOf(w.cfg, app, last)), nil
		}
	}
	return nil, fmt.Errorf("secrets rotate: there is no rotation for %s. It names a secret by its path in the secrets file, Eg: cluster.standby_password, or apps.<app>.<unit> for a unit of secrets an app's blueprint groups. Nothing was changed", path)
}

// declaredRotator is the rotation of apps.<app>.<key> as the app's blueprint
// declares it, and false when the blueprint says nothing of it: a group of
// secrets rotated as a unit, a key never rotated or rotated only with its
// group, or one changed inside the running app.
func declaredRotator(cfg *config.Config, path, app, key string) (secretsrotate.Rotator, error, bool) {
	if cfg == nil {
		return nil, nil, false
	}
	blueprint := cfg.Apps[app].Blueprint
	if slices.Contains(blueprints.SecretGroups(blueprint), key) {
		return secretsrotate.AppGroup(cfg, app, key), nil, true
	}
	spec, ok := blueprints.SecretOf(blueprint, key)
	switch {
	case !ok:
		return nil, nil, false
	case spec.NeverRotate != "":
		return nil, fmt.Errorf("secrets rotate: %s is kept: %s", path, spec.NeverRotate), true
	case spec.Group != "":
		return nil, fmt.Errorf("secrets rotate: %s is rotated with the rest of its unit: name apps.%s.%s", path, app, spec.Group), true
	case spec.Rotate != nil:
		return secretsrotate.InApp(cfg, app, key), nil, true
	}
	return nil, nil, false
}

// costOf is what rotating an app's own secret costs, read through its
// blueprint when the deployment is known.
func costOf(cfg *config.Config, app, key string) secretsrotate.Cost {
	if cfg == nil {
		return secretsrotate.Cost{}
	}
	return secretsrotate.CostOf(cfg, app, key)
}

// refuseUnfilled refuses --all on a secrets file that lacks a secret init
// generates: --all is every secret the deployment has, and a run that
// skipped one would leave it as the operator who left knew it, or never made.
func refuseUnfilled(cfg *config.Config, store secretsrotate.Store, path string) error {
	fresh, err := store.Load()
	if err != nil {
		return err
	}
	filled, err := secretsgen.Fill(cfg, fresh)
	if err != nil {
		return fmt.Errorf("secrets rotate: reading which secrets %s lacks: %w", path, err)
	}
	if filled.Changed() {
		return fmt.Errorf("secrets rotate --all: %s lacks %s, which `"+progname.Name()+" init` generates. Run it, and apply, first. Nothing was changed", path, strings.Join(filled.Generated, ", "))
	}
	return nil
}

// rotateAll is every secret --all rotates on this deployment, in the order
// the spec gives (what works from the internet first: the DNS token, client
// secrets and signing keys; then what only the mesh reaches), each only for
// the apps and sites that have it; and what it keeps, with why. The command
// refuses --all on a file that lacks a generated secret (refuseUnfilled), so
// a generated secret with no value is one the configuration does not need. A
// pasted secret with no value, or that nothing renders, has nothing to rotate.
func rotateAll(cfg *config.Config, s *config.Secrets, w rotateWiring) ([]secretsrotate.Rotator, []keptSecret, error) {
	var rs []secretsrotate.Rotator
	var kept []keptSecret
	has := func(path string) bool { v, ok := s.Get(path); return ok && v != "" }
	appsOf := func(blueprint config.Blueprint) []string {
		var out []string
		for _, name := range cfg.AppNames() {
			if cfg.Apps[name].Blueprint == blueprint {
				out = append(out, name)
			}
		}
		return out
	}
	// self is every secret an app's own stack checks whose blueprint gives
	// it the cost named: those that sign members out go with the other
	// signing keys, early; those that only restart a stack go last.
	self := func(cost string) {
		for _, app := range cfg.AppNames() {
			blueprint := cfg.Apps[app].Blueprint
			for _, key := range blueprints.SelfRotated(blueprint) {
				if spec, _ := blueprints.SecretOf(blueprint, key); spec.RotationCost == cost && has("apps."+app+"."+key) {
					rs = append(rs, secretsrotate.AppSelf(app, key, secretsrotate.CostOf(cfg, app, key)))
				}
			}
		}
	}

	if has("external.acme_dns_token") && len(cfg.CaddySites()) > 0 {
		rs = append(rs, secretsrotate.DNSToken())
	}
	// The other pasted secrets, with the DNS token: each issued by a
	// provider on the internet, and used only where an app logs in to send
	// mail. One no app logs in with has nothing at the provider to replace.
	smtp := []string{"external.smtp_password"}
	for _, app := range cfg.AppNames() {
		smtp = append(smtp, "apps."+app+".smtp_password")
	}
	for _, path := range smtp {
		if !has(path) || !secretsgen.SMTPPasswordUsed(cfg, s, path) {
			continue
		}
		used, err := secretsrotate.Carries(cfg, s, path)
		if err != nil {
			return nil, nil, err
		}
		if used {
			rs = append(rs, secretsrotate.SMTPPassword(path))
		}
	}
	for _, name := range cfg.AppNames() {
		if !has("oidc_clients." + name + ".client_secret") {
			continue
		}
		rs = append(rs, secretsrotate.OIDCClient(name, w.oidcRun, w.oidcRetire))
	}
	// Each unit of secrets a blueprint groups.
	for _, app := range cfg.AppNames() {
		groups := blueprints.SecretGroups(cfg.Apps[app].Blueprint)
		for _, group := range groups {
			if members := blueprints.SecretGroup(cfg.Apps[app].Blueprint, group); has("apps." + app + "." + members[0].Name) {
				rs = append(rs, secretsrotate.AppGroup(cfg, app, group))
			}
		}
	}
	self(blueprints.CostSignsOut)
	for _, app := range cfg.AppNames() {
		if has("apps." + app + ".s3_access_key_id") {
			rs = append(rs, secretsrotate.AppS3(app, w.s3Run))
		}
	}
	self("")
	for _, app := range appsOf(config.BlueprintUptime) {
		if has("apps." + app + ".admin_password") {
			rs = append(rs, secretsrotate.UptimeAdmin(app))
		}
	}
	if len(cfg.Cluster.Sites) > 0 {
		for _, role := range []string{"superuser", "admin", "standby"} {
			if has("cluster." + role + "_password") {
				rs = append(rs, secretsrotate.ClusterRole(role))
			}
		}
	}
	for _, app := range cfg.AppNames() {
		if blueprints.UsesPostgres(cfg.Apps[app].Blueprint) && has("apps."+app+".database_password") {
			rs = append(rs, secretsrotate.AppDatabase(app))
		}
	}
	// Each secret a blueprint changes inside the running app.
	for _, app := range cfg.AppNames() {
		blueprint := cfg.Apps[app].Blueprint
		for _, key := range blueprints.AppSecrets(blueprint) {
			if spec, _ := blueprints.SecretOf(blueprint, key); spec.Rotate != nil && has("apps."+app+"."+key) {
				rs = append(rs, secretsrotate.InApp(cfg, app, key))
			}
		}
	}
	self(blueprints.CostRestart)
	if len(cfg.Storage.Garage.Sites) > 0 {
		if has("storage.garage.admin_token") {
			rs = append(rs, secretsrotate.GarageAdminToken())
		}
		if has("storage.garage.rpc_secret") {
			rs = append(rs, secretsrotate.GarageRPC())
		}
	}
	for _, site := range cfg.SiteNames() {
		if has("sites." + site + ".heartbeat_token") {
			rs = append(rs, secretsrotate.Heartbeat(site))
		}
	}
	for _, app := range appsOf(config.BlueprintPocketID) {
		for _, key := range secretsrotate.ContinuityHeartbeats {
			if has("apps." + app + "." + key) {
				rs = append(rs, secretsrotate.ContinuityHeartbeat(app, key))
			}
		}
	}
	// Each app's own heartbeat, for a blueprint that declares one.
	for _, app := range cfg.AppNames() {
		if _, ok := blueprints.HeartbeatOf(cfg.Apps[app].Blueprint); ok && has("apps."+app+"."+blueprints.HeartbeatTokenKey) {
			rs = append(rs, secretsrotate.AppHeartbeat(app))
		}
	}

	// Each secret a blueprint says is never rotated, with why.
	for _, app := range cfg.AppNames() {
		blueprint := cfg.Apps[app].Blueprint
		for _, key := range blueprints.AppSecrets(blueprint) {
			if spec, _ := blueprints.SecretOf(blueprint, key); spec.NeverRotate != "" && has("apps."+app+"."+key) {
				kept = append(kept, keptSecret{"apps." + app + "." + key, spec.NeverRotate})
			}
		}
	}
	for _, path := range []string{"backup.restic_password", "backup.walg_libsodium_key"} {
		if has(path) {
			kept = append(kept, keptSecret{path, backupKeyKept})
		}
	}
	// A destination paisans-stack.yaml no longer declares is an orphan, which
	// secrets prune reports; it is not kept.
	if cfg.Backup != nil {
		for _, d := range cfg.Backup.Destinations {
			path := "backup.destinations." + d.Name
			if has(path+".access_key_id") || has(path+".secret_access_key") {
				kept = append(kept, keptSecret{"backup.destinations.*", destinationKept()})
				break
			}
		}
	}
	for _, site := range cfg.SiteNames() {
		if has("sites." + site + ".wireguard_private_key") {
			kept = append(kept, keptSecret{"sites.*.wireguard_private_key", wireguardKept()})
			break
		}
	}
	return rs, kept, nil
}

// rotationNotes is what the plan says about the rotation of path beyond its
// cost: when the old secret stops being valid where another command retires
// it, a restart in place where there is nothing to switch over to, and what
// a step named for one site covers.
func rotationNotes(cfg *config.Config, path string) []string {
	parts := strings.Split(path, ".")
	switch {
	case len(parts) == 3 && parts[0] == "oidc_clients":
		app := parts[1]
		return []string{fmt.Sprintf("as `"+progname.Name()+" oidc client create --app %s --rotate-secret --execute`, then each site running %s applied in turn, each healthy before the next; the old client secrets are deleted at Pocket ID only after every site runs the new one", app, app)}
	case len(parts) == 3 && parts[0] == "apps" && parts[2] == "s3_secret_access_key":
		app := parts[1]
		return []string{fmt.Sprintf("as `"+progname.Name()+" storage rotate-key --app %s --execute`: the new key is imported beside the old one, every site running %s is switched and healthy, and only then is the old key deleted from Garage", app, app)}
	case len(parts) == 2 && parts[0] == "cluster" && len(cfg.Cluster.Sites) == 1:
		return []string{fmt.Sprintf("the cluster has one data member, %s, so there is no replica to switch the leader to: its Patroni is started again in place, and the database is unavailable for that restart", cfg.Cluster.Sites[0])}
	case path == "storage.garage.rpc_secret":
		sites := cfg.Storage.Garage.Sites
		if len(sites) == 0 {
			return nil
		}
		return []string{fmt.Sprintf("one step for every Garage node, named for %s: %s are restarted together with the new value, and storage is unavailable until every node lists every other as healthy", sites[0], strings.Join(sites, ", "))}
	}
	return nil
}

// refuseRotateMidRekey refuses secrets rotate while a mesh rekey is
// unfinished: its scoped applies render the mesh files with the current
// WireGuard key, which a site with a next key may no longer run, and would
// cut that site's tunnels in the middle of a rotation.
func refuseRotateMidRekey(secrets *config.Secrets) error {
	pending := secrets.RekeyPending()
	if len(pending) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    "a mesh rekey is unfinished",
		Explain: fmt.Sprintf("The secrets file holds a next WireGuard key for %s, so a mesh rekey stopped part way, and a rotation's applies would render the old key back to its peers. Finish the rekey first: "+progname.Name()+" mesh rekey %s --execute (or --all, when the run was --all), then secrets rotate. Nothing was changed.", strings.Join(pending, ", "), strings.Join(pending, " ")),
	}
}

// refuseMidRotation refuses command, one that renders the deployment from
// the secrets file and reaches a host, while a secret's rotation is
// unfinished. Its next value may already be what the side that checks it
// accepts, and some of its users may present it; command renders the current
// value, the old one, and would hand it back to those users, which the
// checking side may no longer accept. Finishing the rotation promotes the
// next value, after which command renders what the hosts run. The commands
// secrets rotate itself runs in its place in the order (storage rotate-key,
// oidc client create) run when no rotation is pending: each rotation before
// them is promoted before the next starts.
func refuseMidRotation(secrets *config.Secrets, command string) error {
	pending := secrets.RotationPending()
	if len(pending) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    "a secret's rotation is unfinished",
		Explain: fmt.Sprintf("The secrets file holds a next value for %s, so a secrets rotate stopped part way. Some hosts may already check or present that value, and %s renders the old one. Finish the rotation first: "+progname.Name()+" secrets rotate %s --execute (or --all --execute, when the run was --all), then %s. %s Nothing was changed.", strings.Join(pending, ", "), command, strings.Join(pending, " "), command, cannotFinish),
	}
}

// cannotFinish is where a refusal over a rotation that cannot finish sends
// the operator.
const cannotFinish = secretsrotate.CannotFinish

// onceEach is note, saying each hint once however often it is given: the
// plan and stage 2 both try a value, and both find the same thing untried.
func onceEach(note func(hint, detail string)) func(hint, detail string) {
	said := map[string]bool{}
	return func(hint, detail string) {
		if !said[hint] {
			said[hint] = true
			note(hint, detail)
		}
	}
}
