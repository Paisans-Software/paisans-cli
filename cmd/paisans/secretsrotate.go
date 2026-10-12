package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/doctor"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/meshrekey"
	"github.com/paisans-software/paisans-stack/internal/oidcclient"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/secretsrotate"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// rotateHost is how `secrets rotate` reaches a site. Tests replace it.
var rotateHost = func(name string, site config.Site, sudo bool) apply.Transport {
	return siteTransport(name, site, "", sudo)
}

const rotateUsage = "Usage: paisans secrets rotate <path>... | --all [--execute]"

// encryptsStoredData is why Pocket ID's encryption_key and MAS's
// mas_encryption_secret are kept (docs/specs/2026-10-12-secrets-rotate.md,
// Scope).
const encryptsStoredData = "it encrypts data already stored, so changing it makes that data unreadable, and neither Pocket ID nor Matrix Authentication Service offers a re-encryption the toolkit could drive. An evicted admin who held it also holds a copy of the data it protects, so rotating it would take nothing back"

// wireguardKept is why secrets rotate leaves the WireGuard keys alone.
const wireguardKept = "a WireGuard key is replaced by `paisans mesh rekey`, which moves it without dropping a tunnel"

// rotateStopped is what a failed health gate inside secrets rotate tells the
// operator.
const rotateStopped = "secrets rotate stopped here, with the next value kept in the secrets file; run it again to resume."

// keptSecret is a secret in scope that this run does not rotate, with why.
type keptSecret struct{ Path, Reason string }

// rotateWiring is how the rotations that keep their own staging are run: the
// existing commands, each for one app.
type rotateWiring struct {
	oidcRun, oidcRetire, s3Run func(app string) error
}

// runSecretsRotate replaces generated secrets where they are checked and
// where they are used, in staged runs that resume: internal/secretsrotate,
// and docs/specs/2026-10-12-secrets-rotate.md. Like every command that
// changes a host, it plans from what it reads and changes nothing without
// --execute; and everything that refuses the run is checked before anything
// changes.
func runSecretsRotate(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("secrets rotate", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, "actually rotate, one secret at a time, stopping at the first gate that fails")
	all := fs.Bool("all", false, "rotate every secret in scope the deployment has, in the order the spec gives")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since /srv and Docker are root's")
	var named []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		named, args = append(named, args[0]), args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	named = append(named, fs.Args()...)
	r := reporter()
	switch {
	case *all && len(named) > 0:
		return &ui.Problem{Hint: "--all takes no path", Explain: "Name the secrets to rotate, or pass --all for every one. Got " + strings.Join(named, " ") + " too. " + rotateUsage}
	case !*all && len(named) == 0:
		return &ui.Problem{Hint: "secrets rotate needs a secret", Explain: "It names the secrets to rotate by their paths in the secrets file, Eg: paisans secrets rotate cluster.standby_password, or apps.<app>.oauth for Mbin's OAuth2 keys; or --all for every one. " + rotateUsage}
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
	if *execute {
		// Every stage writes the file, and an encrypted file with no
		// recipient beside it would be written back in plaintext.
		recipients, err := config.Recipients(filepath.Dir(*secretsPath))
		if err != nil {
			return err
		}
		if secrets.Encrypted && len(recipients) == 0 {
			return fmt.Errorf("secrets rotate: %s is encrypted, but no %s beside it names a recipient, so writing a next value into it would leave it in plaintext. Nothing was changed", *secretsPath, config.SOPSConfigName)
		}
	}

	wiring := rotateWiring{
		oidcRun: func(app string) error {
			return runOIDCClientCreate(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--rotate-secret", "--execute"))
		},
		oidcRetire: func(app string) error { return retireClientSecrets(r, cfg, store, app) },
		s3Run: func(app string) error {
			return runStorageRotateKey(subcommandArgs(r, "--config", *configPath, "--secrets", *secretsPath, "--app", app, "--execute", fmt.Sprintf("--sudo=%t", *sudo)))
		},
	}
	targets, kept, err := rotateTargets(cfg, secrets, named, *all, wiring)
	if err != nil {
		return err
	}

	transports := map[string]apply.Transport{}
	for _, name := range cfg.SiteNames() {
		transports[name] = rotateHost(name, cfg.Sites[name], *sudo)
	}
	if err := settleSudo(r, cfg.SiteNames(), func(name string) apply.Transport { return transports[name] }); err != nil {
		return err
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
		Healthy: func(site string, stacks []string) error {
			for _, stack := range stacks {
				if err := apply.WaitHealthy(cfg.Deployment(), site, stack, transports[site], rotateStopped); err != nil {
					return err
				}
			}
			return nil
		},
		Now: time.Now,
	}
	var planKept []keptSecret
	run, err := ui.Get(r, "read where each secret stands", func() (*secretsrotate.Run, error) {
		run, k, err := planRotation(env, targets, *all)
		planKept = k
		return run, err
	})
	if err != nil {
		return err
	}
	kept = append(kept, planKept...)
	for i := range run.Items {
		run.Items[i].Notes = rotationNotes(cfg, run.Items[i].Rotator.Path())
	}
	showKept(r, kept)
	if !*execute || r.Verbose() {
		run.Show(r)
	}
	if len(run.Items) == 0 {
		r.Result("Nothing to rotate: every secret named is kept. Nothing changed.")
		return nil
	}
	// Every site may be handed a value, and each is written to only through
	// a claim, as apply's are.
	runners := map[string]registry.Runner{}
	for name, t := range transports {
		runners[name] = t
	}
	if err := claimHosts(r, cfg, *execute, runners); err != nil {
		return err
	}
	if !*execute {
		r.Result("Nothing changed. Re-run with --execute to rotate.")
		return nil
	}
	run.Report = r
	confirm := func(word, what string) error { return confirmWord(stdin, stdout, word, what) }
	if err := run.Execute(env, confirm); err != nil {
		return err
	}
	var done []string
	for _, it := range run.Items {
		done = append(done, it.Rotator.Path())
	}
	r.Result("%s rotated: every host that checks or uses each one runs its new value, and %s holds only the new values.", strings.Join(done, ", "), *secretsPath)
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
	return oidcclient.Retire(clientAPI(cfg, where, "", key), app, recordedClient(s, app), r)
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
// plan as `paisans apply --site <site> --only <stack>`, its refusal of a
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
		return fmt.Errorf("%s's %s differs from what the last apply recorded, so somebody edited it on the host. Nothing was changed on %s. Restore it, or run `paisans apply --site %s --only %s --overwrite %s`, then run secrets rotate again", site, c[0].Path, site, site, strings.Join(stacks, " --only "), c[0].Path)
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

// planRotation plans rs. A rotator whose precheck finds its secret kept
// (secretsrotate.KeptError: nothing checks it on this deployment) is taken
// out of the run and listed with its reason, rather than refusing the run,
// and the rest planned again. Every other refusal stands.
func planRotation(e secretsrotate.Env, rs []secretsrotate.Rotator, all bool) (*secretsrotate.Run, []keptSecret, error) {
	var kept []keptSecret
	for {
		var run *secretsrotate.Run
		var err error
		if all {
			run, err = secretsrotate.PlanAll(e, rs)
		} else {
			run, err = secretsrotate.Plan(e, rs)
		}
		var k *secretsrotate.KeptError
		if err == nil || !errors.As(err, &k) {
			return run, kept, err
		}
		i := slices.IndexFunc(rs, func(r secretsrotate.Rotator) bool { return r.Path() == k.Path })
		if i < 0 {
			return nil, nil, err
		}
		kept = append(kept, keptSecret{Path: k.Path, Reason: k.Reason})
		rs = slices.Delete(slices.Clone(rs), i, i+1)
	}
}

// rotateTargets is the rotators of a run, in order: every secret in scope
// for --all, or the named ones; then, for each rotation an earlier run left
// pending that is not among them, its own, since the engine finishes those
// first. kept is what --all leaves alone, with why.
func rotateTargets(cfg *config.Config, s *config.Secrets, named []string, all bool, w rotateWiring) ([]secretsrotate.Rotator, []keptSecret, error) {
	var rs []secretsrotate.Rotator
	var kept []keptSecret
	if all {
		rs, kept = rotateAll(cfg, s, w)
	} else {
		for _, path := range named {
			r, err := rotatorFor(cfg, path, w)
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
		r, err := rotatorFor(cfg, path, w)
		if err != nil {
			return nil, nil, fmt.Errorf("secrets rotate: %s has a rotation under way, and no rotation of it can be built to finish it: %w", path, err)
		}
		has[path] = true
		out = append(out, r)
	}
	return out, kept, nil
}

// rotatorFor is the rotation of the secret at path, or why secrets rotate
// does not rotate it.
func rotatorFor(cfg *config.Config, path string, w rotateWiring) (secretsrotate.Rotator, error) {
	parts := strings.Split(path, ".")
	last := parts[len(parts)-1]
	switch {
	case path == "external.acme_dns_token":
		return secretsrotate.DNSToken(), nil
	case strings.HasPrefix(last, "wireguard_"):
		return nil, fmt.Errorf("secrets rotate: %s is not rotated here: %s", path, wireguardKept)
	case path == "storage.garage.admin_token":
		return secretsrotate.GarageAdminToken(), nil
	case path == "storage.garage.rpc_secret":
		return secretsrotate.GarageRPC(), nil
	case len(parts) == 2 && parts[0] == "cluster" && strings.HasSuffix(last, "_password"):
		return secretsrotate.ClusterRole(strings.TrimSuffix(last, "_password")), nil
	case len(parts) == 3 && parts[0] == "sites" && last == "heartbeat_token":
		return secretsrotate.Heartbeat(parts[1]), nil
	case len(parts) == 3 && parts[0] == "oidc_clients" && last == "client_secret":
		if app, ok := cfg.Apps[parts[1]]; ok {
			if reason, ok := oidcUnsupported(parts[1], app); ok {
				return nil, fmt.Errorf("secrets rotate: %s is not rotated here: %s", path, reason)
			}
		}
		return secretsrotate.OIDCClient(parts[1], w.oidcRun, w.oidcRetire), nil
	case len(parts) == 3 && parts[0] == "apps":
		app := parts[1]
		switch last {
		case "oauth":
			return secretsrotate.MbinOAuth(app), nil
		case "encryption_key", "mas_encryption_secret":
			return nil, fmt.Errorf("secrets rotate: %s is kept: %s", path, encryptsStoredData)
		case "oauth_passphrase", "oauth_encryption_key", "oauth_private_key", "oauth_public_key":
			return nil, fmt.Errorf("secrets rotate: %s encrypts or opens Mbin's OAuth2 keypair, and is rotated with the rest of it: name apps.%s.oauth", path, app)
		case "s3_access_key_id", "s3_secret_access_key":
			return secretsrotate.AppS3(app, w.s3Run), nil
		case "database_password":
			return secretsrotate.AppDatabase(app), nil
		case "rabbitmq_password":
			return secretsrotate.MbinRabbitMQ(app), nil
		case "admin_password":
			return secretsrotate.UptimeAdmin(app), nil
		}
		if _, ok := secretsrotate.AppSelfKind(last); ok {
			return secretsrotate.AppSelf(app, last, secretsrotate.AppSelfCost(last)), nil
		}
	}
	return nil, fmt.Errorf("secrets rotate: there is no rotation for %s. It names a secret by its path in the secrets file, Eg: cluster.standby_password, or apps.<app>.oauth for Mbin's OAuth2 keys. Nothing was changed", path)
}

// oidcUnsupported is why app's OIDC client secret is not rotated here: the
// rotation is `oidc client create --rotate-secret`, which knows only some
// kinds' clients.
func oidcUnsupported(name string, app config.App) (string, bool) {
	if _, ok := clientDesired(name, app, true); ok {
		return "", false
	}
	return fmt.Sprintf("its rotation is `paisans oidc client create --rotate-secret`, which does not know what a %s client looks like, so replace it at Pocket ID by hand, set it with `paisans secrets set oidc_clients.%s.client_secret`, and apply", app.Kind, name), true
}

// rotateAll is every secret --all rotates on this deployment, in the order
// the spec gives (what works from the internet first: the DNS token, client
// secrets and signing keys; then what only the mesh reaches), each only for
// the apps and sites that have it; and what it keeps, with why. A secret the
// file holds no value for has nothing to rotate.
func rotateAll(cfg *config.Config, s *config.Secrets, w rotateWiring) ([]secretsrotate.Rotator, []keptSecret) {
	var rs []secretsrotate.Rotator
	var kept []keptSecret
	has := func(path string) bool { v, ok := s.Get(path); return ok && v != "" }
	appsOf := func(kind config.Kind) []string {
		var out []string
		for _, name := range cfg.AppNames() {
			if cfg.Apps[name].Kind == kind {
				out = append(out, name)
			}
		}
		return out
	}
	self := func(key string) {
		kind, _ := secretsrotate.AppSelfKind(key)
		for _, app := range appsOf(kind) {
			if has("apps." + app + "." + key) {
				rs = append(rs, secretsrotate.AppSelf(app, key, secretsrotate.AppSelfCost(key)))
			}
		}
	}

	if has("external.acme_dns_token") && len(cfg.CaddySites()) > 0 {
		rs = append(rs, secretsrotate.DNSToken())
	}
	for _, name := range cfg.AppNames() {
		path := "oidc_clients." + name + ".client_secret"
		if !has(path) {
			continue
		}
		if reason, ok := oidcUnsupported(name, cfg.Apps[name]); ok {
			kept = append(kept, keptSecret{path, reason})
			continue
		}
		rs = append(rs, secretsrotate.OIDCClient(name, w.oidcRun, w.oidcRetire))
	}
	for _, app := range appsOf(config.KindMbin) {
		if has("apps." + app + ".oauth_passphrase") {
			rs = append(rs, secretsrotate.MbinOAuth(app))
		}
	}
	self("app_secret")
	self("cookie_secret")
	self("session_secret")
	self("mas_signing_key")
	self("mas_matrix_secret")
	for _, app := range cfg.AppNames() {
		if has("apps." + app + ".s3_access_key_id") {
			rs = append(rs, secretsrotate.AppS3(app, w.s3Run))
		}
	}
	self("static_api_key")
	for _, app := range appsOf(config.KindUptime) {
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
		if kinds.UsesPostgres(cfg.Apps[app].Kind) && has("apps."+app+".database_password") {
			rs = append(rs, secretsrotate.AppDatabase(app))
		}
	}
	for _, app := range appsOf(config.KindMbin) {
		if has("apps." + app + ".rabbitmq_password") {
			rs = append(rs, secretsrotate.MbinRabbitMQ(app))
		}
	}
	self("valkey_password")
	self("mercure_jwt_secret")
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

	for _, app := range appsOf(config.KindPocketID) {
		if path := "apps." + app + ".encryption_key"; has(path) {
			kept = append(kept, keptSecret{path, encryptsStoredData})
		}
	}
	for _, app := range appsOf(config.KindSynapse) {
		if path := "apps." + app + ".mas_encryption_secret"; has(path) {
			kept = append(kept, keptSecret{path, encryptsStoredData})
		}
	}
	for _, site := range cfg.SiteNames() {
		if has("sites." + site + ".wireguard_private_key") {
			kept = append(kept, keptSecret{"sites.*.wireguard_private_key", wireguardKept})
			break
		}
	}
	return rs, kept
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
		return []string{fmt.Sprintf("as `paisans oidc client create --app %s --rotate-secret --execute`, then each site running %s applied in turn, each healthy before the next; the old client secrets are deleted at Pocket ID only after every site runs the new one", app, app)}
	case len(parts) == 3 && parts[0] == "apps" && parts[2] == "s3_secret_access_key":
		app := parts[1]
		return []string{fmt.Sprintf("as `paisans storage rotate-key --app %s --execute`: the new key is imported beside the old one, every site running %s is switched and healthy, and only then is the old key deleted from Garage", app, app)}
	case len(parts) == 2 && parts[0] == "cluster" && len(cfg.Cluster.Sites) == 1:
		return []string{fmt.Sprintf("the cluster has one data member, %s, so there is no replica to switch the leader to: its Patroni is started again in place, and the database is unavailable for that restart", cfg.Cluster.Sites[0])}
	case path == "storage.garage.rpc_secret":
		sites := cfg.Storage.Garage.Sites
		if len(sites) == 0 {
			return nil
		}
		return []string{fmt.Sprintf("one step for every Garage node, named for %s: %s are restarted together with the new value, and storage is unavailable until every node lists every other as healthy", sites[0], strings.Join(sites, ", "))}
	case path == "external.acme_dns_token":
		return []string{fmt.Sprintf("the token the file holds now: replace it at %s and set it with `paisans secrets set external.acme_dns_token` before this runs, or it is refused as unchanged", cfg.ACME.Provider)}
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
		Explain: fmt.Sprintf("The secrets file holds a next WireGuard key for %s, so a mesh rekey stopped part way, and a rotation's applies would render the old key back to its peers. Finish the rekey first: paisans mesh rekey %s --execute (or --all, when the run was --all), then secrets rotate. Nothing was changed.", strings.Join(pending, ", "), strings.Join(pending, " ")),
	}
}

// refuseMidRotation refuses a command that renders the deployment while a
// secret's rotation is unfinished. Its next value may already be what the
// side that checks it accepts, and some of its users may present it; apply
// renders the current value, the old one, and would hand it back to those
// users, which the checking side may no longer accept. Finishing the
// rotation promotes the next value, after which apply renders what the hosts
// run.
func refuseMidRotation(secrets *config.Secrets) error {
	pending := secrets.RotationPending()
	if len(pending) == 0 {
		return nil
	}
	return &ui.Problem{
		Hint:    "a secret's rotation is unfinished",
		Explain: fmt.Sprintf("The secrets file holds a next value for %s, so a secrets rotate stopped part way. Some hosts may already check or present that value, and apply renders the old one. Finish the rotation first: paisans secrets rotate %s --execute (or --all --execute, when the run was --all), then apply. Nothing was changed.", strings.Join(pending, ", "), strings.Join(pending, " ")),
	}
}
