// Command paisans renders, checks and applies a paisans deployment
// declaration.
//
// validate, render and create touch nothing outside the working directory. init
// reads every site to choose a mesh subnet, and writes only local files.
// `host prepare`, `apply` and `storage init` reach a machine: each reads it to
// show what it would do, and changes nothing on it unless told to with
// --execute. apply without --site may, in its dry run, add missing generated
// secrets to the local secrets file, as init would, and says so when it does.
// `doctor` reaches every site to report what is stuck and changes nothing.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/garage"
	"github.com/paisans-software/paisans-cli/internal/gatewaymove"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/hostarch"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/imagedigest"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/storageadd"
	"github.com/paisans-software/paisans-cli/internal/topology"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

func main() {
	page, asked, run := resolveHelp(os.Args[1:])
	switch {
	case page != nil && asked:
		page(os.Stdout)
		return
	case page != nil:
		page(os.Stderr)
		os.Exit(2)
	}
	os.Args = append(os.Args[:1], run...)
	var err error
	switch os.Args[1] {
	case "validate":
		err = runValidate(os.Args[2:])
	case "create":
		err = runCreate(os.Args[2:], os.Stdin, os.Stdout)
	case "init":
		err = runInit(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	case "apply":
		err = runApply(os.Args[2:], os.Stdin, os.Stdout)
	case "host":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "prepare":
			err = runHostPrepare(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "deployments":
			err = runHostDeployments(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "request":
			err = runHostRequest(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "admit":
			err = runHostAdmit(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "evict":
			err = runHostEvict(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "list":
			err = runHostList(os.Args[3:], os.Stdout)
		default:
			os.Exit(groupError(os.Stderr, "host", "host takes one subcommand, list, prepare, deployments, request, admit or evict"))
		}
	case "site":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "add":
			err = runSiteAdd(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "remove":
			err = runSiteRemove(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "list":
			err = runSiteList(os.Args[3:], os.Stdout)
		default:
			os.Exit(groupError(os.Stderr, "site", "site takes one subcommand, list, add or remove"))
		}
	case "mesh":
		err = runMesh(os.Args[2:])
	case "gateway":
		err = runGateway(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:], os.Stdin, os.Stdout)
	case "operator":
		err = runOperator(os.Args[2:], os.Stdin, os.Stdout)
	case "secrets":
		err = runSecrets(os.Args[2:])
	case "app":
		err = runApp(os.Args[2:])
	case "oidc":
		err = runOIDC(os.Args[2:])
	case "web-server":
		err = runWebServer(os.Args[2:])
	case "catalog":
		err = runCatalog(os.Args[2:], os.Stdout)
	case "blueprint":
		err = runBlueprint(os.Args[2:], os.Stdout)
	case "storage":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "init":
			err = runStorageInit(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "add":
			err = runStorageAdd(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "rotate-key":
			err = runStorageRotateKey(os.Args[3:])
		default:
			os.Exit(groupError(os.Stderr, "storage", "storage takes one subcommand, init, add or rotate-key"))
		}
	case "prune":
		err = runPrune(os.Args[2:])
	case "preflight":
		err = runPreflight(os.Args[2:])
	case "failover":
		err = runFailover(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "images":
		err = runImages(os.Args[2:], os.Stdout)
	case "version", "--version":
		err = runVersion(os.Args[2:], os.Stdout)
	case "dns":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "init":
			err = runDNSInit(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "prune":
			err = runDNSPrune(os.Args[3:])
		default:
			os.Exit(groupError(os.Stderr, "dns", "dns takes one subcommand, init or prune"))
		}
	default:
		os.Exit(usageError(os.Stderr, fmt.Sprintf("unknown command %q", os.Args[1])))
	}
	// Every shared ssh connection the command opened is closed before the
	// process exits, whatever the outcome. os.Exit runs no deferred call, so
	// this comes first on both paths.
	apply.CloseConnections()
	// A command whose flags parse with ContinueOnError returns ErrHelp once
	// its flag set has printed its page for --help: help, not a failure.
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		os.Exit(reportError(os.Stderr, err, verboseRun))
	}
}

// usageError prints msg in the error form, then the list of commands, and
// returns the status for a command line paisans cannot run.
func usageError(w io.Writer, msg string) int {
	ui.PrintError(w, errors.New(msg), false)
	fmt.Fprintln(w)
	printTopHelp(w)
	return 2
}

// groupError is usageError for a group given a subcommand it does not have:
// msg, then the group's own page.
func groupError(w io.Writer, group, msg string) int {
	ui.PrintError(w, errors.New(msg), false)
	fmt.Fprintln(w)
	printGroupHelp(w, group)
	return 2
}

// reportError prints the error that ends a command, in the refusal's form
// (see ui.PrintError), and returns the status to exit with.
func reportError(w io.Writer, err error, verbose bool) int {
	// Not a failure: --check found a build that would move or an override
	// that lags, and the table already says which. It exits 3 so a schedule
	// tells it from an error.
	if errors.Is(err, errImagesMove) {
		return 3
	}
	ui.PrintError(w, err, verbose)
	// Not a failure: a gate is waiting on Garage, and the next run resumes
	// there. EX_TEMPFAIL (sysexits.h), "try again later", so a script can
	// tell it from one.
	if errors.Is(err, storageadd.ErrWaiting) {
		return 75
	}
	// Not a failure either: the command did what it was asked, and what is
	// left is the operator's to do.
	var warn *ui.Warning
	if errors.As(err, &warn) {
		return 0
	}
	return 1
}

func runValidate(args []string) error {
	fs := newFlagSet("validate", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets, whose SMTP passwords the mail check logs in with (default: secrets.enc.yaml beside the config)")
	offline := fs.Bool("offline", false, "check the file alone, reaching no site: the mail settings are not tried")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportValidation(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "Nothing can be rendered or applied from it until then.")
	}
	// The mail settings are tried once the file is one a host may be
	// reached for, and only warn: see checkMailForValidate.
	warned := 0
	if !*offline {
		if *secretsPath == "" {
			*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
		}
		warned = checkMailForValidate(r, cfg, *secretsPath)
	}
	switch {
	case len(result.Findings) == 0 && warned == 0:
		r.Result("%s: no problems found", *configPath)
	case warned > 0:
		r.Result("%s: %s from the mail check", *configPath, plural(warned, "warning"))
	}
	return nil
}

// runInit generates the secrets a configuration needs.
//
// It is safe to re-run, and re-running is how a site or an app added later
// gets its secrets: nothing already set is replaced, because regenerating a
// WireGuard key breaks every peer that trusted the old one and regenerating a
// database password locks an application out of a role that still holds the
// old one.
//
// It changes no host. It reads every site that has admitted the deployment,
// to settle the mesh subnet before anything is deployed (see initAdmissions
// and settleMesh), and that is all; a site that has not is skipped, with a
// warning. Standing a
// deployment up is `apply`, and that is a separate decision from having
// credentials to stand it up with.
func runInit(args []string) error {
	fs := newFlagSet("init", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	ageKeyFile := fs.String("age-private-key-file", "", "keep a newly made age private key in this file instead of the keystore")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	r := reporter()
	// The id comes first: everything a host holds is named from it, and a
	// declaration without one cannot even be loaded. One that exists is never
	// replaced.
	id, added, err := config.EnsureID(*configPath)
	if err != nil {
		return err
	}
	if added {
		s := r.Step("write deployment id")
		s.Detail("%s: wrote deployment id %s. It never changes; every name and path this deployment holds on a host is derived from it.", *configPath, id)
		s.Done("")
	}
	cfg, err := config.LoadForInit(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "Secrets are not generated for a configuration that cannot be deployed.")
	}
	// The mesh subnet next, while nothing is deployed: it is the one value
	// that has to be checked against every host before the first apply,
	// and cannot change after it.
	// Every site is asked whether it admitted the deployment, and only an
	// admitted one is read further: on any other the deployment's user does
	// not exist yet, and host admit checks the subnet there.
	hosts := initHosts(cfg)
	admitted, unread := initAdmissions(r, cfg, hosts)
	wrote, err := settleMesh(r, cfg, *configPath, hosts, admitted, unread, meshRandom)
	if err != nil {
		return err
	}
	if wrote {
		if cfg, err = config.Load(*configPath); err != nil {
			return err
		}
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	if err := ensureAgeKey(r, cfg.ID, *configPath, *secretsPath, *ageKeyFile); err != nil {
		return err
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		secrets = &config.Secrets{Version: config.SecretsVersion, ID: cfg.ID}
	case err != nil:
		return err
	}
	warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, func(gw string) registry.Runner {
		if !slices.Contains(admitted, gw) {
			return notAdmittedHost{gw}
		}
		return hosts[gw]
	}))

	filled, recipients, err := fillSecrets(cfg, *secretsPath, secrets)
	var failed *secretsWriteError
	if err != nil && !errors.As(err, &failed) {
		return err
	}

	if !filled.Changed() {
		r.Detail("%s already has every generated secret (%d)", *secretsPath, len(filled.Kept))
		reportOwed(r, filled)
		if len(filled.Owed) > 0 {
			r.Result(setOwedNext)
			return nil
		}
		r.Result("Every generated secret is already present. Nothing written.")
		return nil
	}

	// Names, never values. A secret printed to a terminal is in a scrollback
	// buffer, and often in a multiplexer's log as well.
	write := r.Step("write secrets")
	write.Detail("%s", *secretsPath)
	for _, name := range filled.Generated {
		write.Detail("+ %s", name)
	}
	if failed != nil {
		write.Fail(failed.err)
		return failed.err
	}
	counts := ""
	if r.Verbose() {
		counts = fmt.Sprintf("%d generated, %d kept", len(filled.Generated), len(filled.Kept))
	}
	write.Done(counts)
	if len(recipients) == 0 {
		warnPlaintext(r, *secretsPath)
	} else {
		r.Detail("Encrypted to %d age recipient(s) from %s.", len(recipients), config.SOPSConfigName)
	}
	reportOwed(r, filled)
	if len(filled.Owed) > 0 {
		r.Result(setOwedNext)
		return nil
	}
	r.Result("Secrets written.")
	return nil
}

// fillSecrets is how init fills the secrets file, and the converge dry run
// with it: every generated secret secrets lacks for cfg is generated into it,
// a secret that has a value is never changed, and when anything was
// generated the file at path is written, encrypted to the age recipients in
// the .sops.yaml beside it. It returns what was generated and kept, and the
// recipients. A failure to write is a *secretsWriteError, after which the
// result still names what was generated.
func fillSecrets(cfg *config.Config, path string, secrets *config.Secrets) (secretsgen.Result, []string, error) {
	filled, err := secretsgen.Fill(cfg, secrets)
	if err != nil {
		return filled, nil, err
	}
	recipients, err := config.Recipients(filepath.Dir(path))
	if err != nil {
		return filled, nil, err
	}
	if !filled.Changed() {
		return filled, recipients, nil
	}
	if err := config.WriteSecrets(path, secrets, recipients); err != nil {
		return filled, recipients, &secretsWriteError{err}
	}
	return filled, recipients, nil
}

// secretsWriteError is fillSecrets failing to write the file it filled.
type secretsWriteError struct{ err error }

func (e *secretsWriteError) Error() string { return e.err.Error() }
func (e *secretsWriteError) Unwrap() error { return e.err }

// setOwedNext is init's result when a secret only the operator can provide
// is unset: secrets set asks for each one at a terminal.
const setOwedNext = "Next: run `paisans secrets set`."

// reportOwed warns of each secret only the operator can provide. Leaving
// these silent would let an operator believe an install is finished when
// certificates are still missing. What apply records itself is not a
// warning: each kind is one pending line naming the apps, with what apply
// makes for each under --verbose.
func reportOwed(r ui.Reporter, filled secretsgen.Result) {
	for _, owed := range filled.Owed {
		r.Warn("Secret for "+owed.Name+" needs to be set", owed.Why)
	}
	var order []string
	byKind := map[string][]secretsgen.Pending{}
	for _, p := range filled.Pending {
		if _, ok := byKind[p.What]; !ok {
			order = append(order, p.What)
		}
		byKind[p.What] = append(byKind[p.What], p)
	}
	for _, what := range order {
		pending := byKind[what]
		apps := make([]string, len(pending))
		for i, p := range pending {
			apps[i] = p.App
		}
		verb := "are"
		if len(pending) == 1 {
			verb = "is"
		}
		s := r.Step(fmt.Sprintf("%s (%s) %s created by apply", plural(len(pending), what), strings.Join(apps, ", "), verb))
		for _, p := range pending {
			s.Detail("%s: %s", p.Name, p.Why)
		}
		s.End(ui.Pending, "")
	}
}

func runRender(args []string) error {
	fs := newFlagSet("render", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the sops encrypted secrets (default: secrets.enc.yaml beside the config)")
	out := fs.String("out", "", "directory to write artifacts into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	if *out == "" {
		return fmt.Errorf("render: --out is required. Artifacts are written to a local directory and pushed by a later step")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "Nothing was rendered.")
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	// render does not call secretsgen.Fill, so a key hand edited into the file
	// after the last `init` is never looked at unless this is checked here too.
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	if err := requireACMEToken(cfg, secrets, cfg.SiteNames()); err != nil {
		return err
	}

	plan, err := render.Build(cfg, secrets)
	if err != nil {
		return err
	}
	if err := render.Write(plan, *out); err != nil {
		return err
	}
	r.Result("Rendered %s to %s.", plural(len(plan.Files), "file"), *out)
	return nil
}

// runApply compares one site against what is rendered for it, and changes
// nothing unless told to. Without --site it converges every site instead, by
// runConverge.
//
// A dry run by default is not politeness. This is the only command that
// reaches a machine, the machine it reaches is running a community, and the
// difference between "show me" and "do it" should be a flag an operator typed
// rather than a habit they formed.
//
// An app that signs in through Pocket ID has its client ensured first, by
// the identity step in clients.go: declaring the app is the approval for
// its client, so `--execute` creates and records it like any other secret.
func runApply(args []string, stdin io.Reader, stdout io.Writer) error {
	return referUnreachable(applyCommand(args, stdin, stdout))
}

// applyCommand is runApply before an unreachable site's referral to doctor
// is added to its error.
func applyCommand(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("apply", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	site := fs.String("site", "", "the site to apply, by the name it has in the configuration")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "actually write files and restart services")
	var overwrite pathList
	fs.Var(&overwrite, "overwrite", "replace this conflicting file although it differs from the last apply's record (repeatable)")
	var recreate pathList
	var only pathList
	fs.Var(&only, "only", "apply only this stack's files and actions, and leave the rest of the site as it is (repeatable)")
	fs.Var(&recreate, "recreate", "replace every container of this stack with `up -d --force-recreate`, even if nothing changed (repeatable)")
	minFree := fs.String("min-free", "3G", "free space Docker's data root must have before a stack pulls an image, Eg: 2G")
	keepImages := fs.Bool("keep-images", false, "leave the images this apply supersedes on the host, Eg: to keep one to roll back to")
	mustAll := fs.Bool("require-all", false, "exit 1 when the run is degraded: a site pending or down")
	answers := yesFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("apply: %q is not a flag, and apply takes no arguments. Give the configuration with --config %s", fs.Arg(0), fs.Arg(0))
	}
	ans, err := answers()
	if err != nil {
		return err
	}
	r := reporter()
	needFree, err := apply.ParseSize(*minFree)
	if err != nil {
		return fmt.Errorf("apply: --min-free: %w", err)
	}
	defer cacheDigests()()
	if *site == "" {
		if *destination != "" || len(overwrite) > 0 || len(only) > 0 || len(recreate) > 0 {
			return fmt.Errorf("apply: --ssh, --overwrite, --only and --recreate are about one site, and apply without --site converges every site. Name it with --site")
		}
		verbose := fs.Lookup("verbose").Value.(flag.Getter).Get().(bool)
		return runConverge(r, convergeOptions{Config: *configPath, Secrets: *secretsPath, Execute: *execute, Verbose: verbose, KeepImages: *keepImages, MinFree: *minFree, RequireAll: *mustAll, answers: ans, stdin: stdin, stdout: stdout})
	}

	// The deployment and the version open the apply. As a step of apply
	// without --site the run named them once for every site.
	identity := &runIdentity{}
	if reviewedByRun == nil {
		identity = openIdentity(r, *configPath)
		defer identity.closeIdentity(r)
	}
	// The files are read once: the bytes loaded here are what is applied and
	// what is published as the version applied.
	cfg, paisansBytes, err := config.LoadWithBytes(*configPath)
	if err != nil {
		return err
	}
	identity.id = cfg.ID
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	declared, ok := cfg.Sites[*site]
	if !ok {
		return fmt.Errorf("apply: %s declares no site %q. Declared sites are %s", *configPath, *site, strings.Join(cfg.SiteNames(), ", "))
	}
	// Every apply, --site included, classifies every site, so one site's
	// files never disagree with another's about who is in the mesh: as a
	// step of apply without --site, by the run's classification; alone, by
	// its own. A --site that is pending or down is refused here, before its
	// host is reached. From here on cfg is the effective topology, and
	// declaration the configuration as written.
	declaration := cfg
	cfg, sites, err := effectiveConfig(r, declaration, *site)
	if err != nil {
		return err
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, secretsBytes, err := config.LoadDeploymentSecretsWithBytes(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	loaded := configstore.Files{Paisans: paisansBytes, Secrets: secretsBytes}
	if err := refuseMidRekey(secrets); err != nil {
		return err
	}
	if err := refuseMidRotation(secrets, "apply"); err != nil {
		return err
	}
	// apply does not call secretsgen.Fill either, and this is the path that
	// actually reaches a host: a malformed key has to stop here, not just
	// print a confusing failure partway through provisioning on the machine.
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	if err := requireACMEToken(cfg, secrets, []string{*site}); err != nil {
		return err
	}
	// The site's architecture is read first of all that reaches the host,
	// once nothing above refuses the files. As a step of apply without
	// --site the run recorded it already, and the read only compares; on
	// its own, apply --site records it, and the next apply without --site
	// publishes it. The bytes applied are read again after a write.
	archHost := siteTransport(*site, declared, *destination)
	read, err := readArchitectures(r, cfg, []string{*site}, func(string) hostarch.Runner { return archHost })
	if err != nil {
		return err
	}
	if declaration, err = recordArchitectures(r, *configPath, declaration, read, *execute && reviewedByRun == nil); err != nil {
		return err
	}
	cfg = sites.Effective(declaration)
	if *execute && reviewedByRun == nil {
		if _, loaded.Paisans, err = config.LoadWithBytes(*configPath); err != nil {
			return err
		}
	}
	declared = cfg.Sites[*site]
	// The version this applies is gathered before the review, so a fork or
	// a stale base is refused before the operator is asked anything. As a
	// step of apply without --site the run did this once for every site.
	var store storeVersion
	if reviewedByRun == nil {
		if store, err = applyStore(r, *configPath, *secretsPath, &loaded, cfg, *execute, ans, stdin, stdout); err != nil {
			return err
		}
		identity.applied(store.held())
	} else if v := reviewedByRun.version; v != nil && (!bytes.Equal(v.files.Paisans, loaded.Paisans) || !bytes.Equal(v.files.Secrets, loaded.Secrets)) {
		// Every step applies the run's current version, which a step that
		// records at Pocket ID advances: files edited otherwise are not what
		// the store holds.
		return &ui.Problem{Hint: "the configuration changed after the run published it", Explain: config.FileName + " or secrets.enc.yaml is not what this run published as " + v.sha[:12] + ", so applying it would put on " + *site + " a version the store does not hold. Nothing was changed on " + *site + " by this step. Run paisans apply --execute again, which publishes the files as they are now."}
	}

	transport := siteTransport(*site, declared, *destination)
	// As a step of apply without --site (a checkReporter) it leaves the
	// record and the secrets to the run, which checks them once.
	if _, step := r.(checkReporter); !step {
		warnSecrets(r, declaration, secrets, recordForWarnings(r, cfg, applyRecordHosts(cfg, *site, *destination)))
		// Every app this apply starts on the site logs in to its mail
		// server from there first, and a failure refuses before anything
		// changes. As a step of apply without --site the run tried every
		// site's before its first step.
		if err := checkMailForApply(r, cfg, secrets, []string{*site}, only, map[string]string{*site: *destination}); err != nil {
			return err
		}
	}
	r.Section(fmt.Sprintf("%s (%s)", *site, transport.Describe()))
	done := r.Step("check mesh subnet")
	err = checkMeshLive(cfg, *site, transport)
	if err != nil {
		done.Fail(err)
	} else {
		// Only once it is true: a check that could not run found nothing.
		done.Detail("nothing on %s overlaps the mesh subnet", *site)
		done.Done("")
	}
	if err != nil {
		return err
	}
	host, err := hostGate(r, cfg, *site, transport)
	if err != nil {
		return err
	}
	// The review comes before the first change: an
	// operator who does not approve leaves the host exactly as it was. Its
	// images are the render's, as written, never the host's.
	rendered, err := render.Build(cfg, secrets)
	if err != nil {
		return err
	}
	g, err := grant.For(cfg, rendered, *site)
	if err != nil {
		return err
	}
	review, approvedBy, err := stepReview(r, ans, stdin, stdout, cfg, *site, g, grant.ApplyScope(only...), transport, *execute, "paisans apply --site "+*site+" --execute")
	if err != nil {
		return err
	}
	// Approved, and before the first change: the host's record is checked
	// against the version and the version published, so the store holds
	// what is applied even if the apply stops part way.
	// As a step of apply without --site, the run published the version
	// once, and this step records it. Only apply records it: host prepare
	// and site add apply no version.
	switch {
	case store != nil && review != nil:
		if err := store.check(r, map[string]grant.Record{*site: review.record}, *execute, stdin, stdout); err != nil {
			return err
		}
		if review.version, err = store.publish(r, *execute); err != nil {
			return err
		}
		if review.version != nil {
			identity.applied(review.version.sha, review.version.revision)
		}
	case reviewedByRun != nil && review != nil:
		review.version = reviewedByRun.version
	}
	if _, err := admittedHosts(r, cfg, map[string]registry.Runner{*site: transport}); err != nil {
		return err
	}

	// The identity step: every app this site starts that signs in through
	// Pocket ID gets its client ensured before it renders. Planned here,
	// read only, so the dry run shows it and an app it must hold back is
	// left out of the plan below.
	clients, err := newClientStep(r, cfg, *site, *destination, *secretsPath, secrets, only)
	if err != nil {
		return err
	}
	if clients != nil {
		clients.confirm = func(what string) error { return confirmWord(ans, stdin, stdout, "create", what) }
	}
	options := append([]apply.Option{apply.Overwrite(overwrite...), apply.Recreate(recreate...), apply.MinFree(needFree), apply.Only(only...)}, pendingOptions(sites)...)
	// An app this site shares with others stays on the build they run,
	// unless the apply plans them all: as a step of apply without --site it
	// does, and every site resolves through the run's one cache, so none is
	// read here. See planningEverySite.
	if !planningEverySite {
		peers, err := readPeers(r, cfg, *site, only)
		if err != nil {
			return err
		}
		options = append(options, apply.OtherSites(peers...))
	}
	// On a shared host an image the toolkit renders (caddy, postgres) may
	// be what a foreign project runs from, so none is removed.
	if *keepImages || host.Shared() {
		options = append(options, apply.KeepImages())
	}
	// planFor plans the site holding back the named app stacks, as a later
	// pass after the done ones. See executeWithClients. The reporter goes to
	// Build, so planning announces what it reads from the host as Execute
	// announces what it does there. Only the first plan reports its reads: a
	// later pass reads the same host again, and saying so twice is noise.
	// Every plan reports its own Execute.
	reads := r
	planFor := func(hold []string, done []*apply.Plan) (*apply.Plan, error) {
		p, err := planSiteApply(cfg, secrets, *site, transport, slices.Concat(options, []apply.Option{apply.Except(hold...), apply.After(done...), apply.Report(reads)})...)
		reads = ui.Discard
		if err != nil {
			return nil, err
		}
		p.Report = r
		return p, nil
	}
	plan, err := planWithClients(clients, func(hold []string) (*apply.Plan, error) { return planFor(hold, nil) }, *execute)
	if err != nil {
		return err
	}

	// A site running etcd, or configured to, is checked against the live
	// membership. Only this site is asked unless it is a configured member,
	// so applying a site with nothing to do with etcd reaches no other host.
	transports := map[string]apply.Transport{*site: transport}
	if contains(cfg.Etcd.Members, *site) {
		for _, name := range cfg.Etcd.Members {
			if name != *site {
				transports[name] = siteTransport(name, cfg.Sites[name], "")
			}
		}
	}

	// A member being founded is the one case where the order sites are
	// applied in matters: the witness first, and no wait for a primary while
	// another founding member has not started. See apply.WitnessFirstRefusal.
	founding := false
	var running map[string]bool
	if contains(cfg.Etcd.Members, *site) {
		recorded, err := ui.Get(r, "read the etcd record", func() (bool, error) {
			_, recorded, err := apply.ReadEtcdInitial(transport, cfg.Deployment())
			return recorded, err
		})
		if err != nil {
			return err
		}
		founding = !recorded
	}
	if founding {
		if running, err = ui.Get(r, "find the running etcd members", func() (map[string]bool, error) {
			return apply.EtcdRunning(cfg, transports)
		}); err != nil {
			return err
		}
		if plan.Bootstrap != nil {
			plan.Bootstrap.EtcdUnstarted = apply.FoundingUnstarted(cfg, *site, running)
		}
	}
	// A dry run is the plan. --execute reports progress instead, and shows
	// the plan first only with --verbose, since its steps say the same.
	// Either way the plan's notes show, and a later pass's new ones too.
	noted := map[string]bool{}
	presentPlan(r, plan, *execute, noted)
	if err := printLeftovers(r, declaration, *site, host.Inventory, plan); err != nil {
		return err
	}

	if err := apply.EtcdGates(cfg, plan, transports, founding, running); err != nil {
		return err
	}

	if !*execute {
		err := clients.result()
		changes := len(plan.Writes()) + len(plan.Actions)
		if plan.WireGuard != apply.WireGuardNone {
			changes++
		}
		if clients != nil {
			changes += clients.steps
		}
		// The sweep removes images and writes nothing, so it is its own part
		// of the summary rather than a change.
		summary := count(changes, "change")
		if len(plan.Sweep) > 0 {
			summary = strings.TrimPrefix(summary+", "+plural(len(plan.Sweep), "old image")+" to sweep", ", ")
		}
		// The review ends the site's plan: what --execute asks approval
		// for. As a step of apply without --site there is none here: the
		// run shows one for every site.
		pending := 0
		if review != nil {
			showReview(r, "review what "+config.FileName+" grants", review.review, false)
			pending = review.review.Pending()
		}
		// As a step of apply without --site, the web server is the run's,
		// which lists it once.
		if _, step := r.(checkReporter); !step {
			convergeWebServerPending(r, cfg, webServerScope{site: *site})
		}
		// A plan --execute would refuse, Eg: a file edited on the host, is
		// where the run would stop, not work it would do.
		dryRunFound(summary, apply.Refusal(plan))
		switch {
		case changes == 0 && len(plan.Sweep) == 0 && pending == 0:
			r.Result("%s is up to date. Nothing to apply.", *site)
		case changes == 0 && len(plan.Sweep) == 0:
			r.Result("%s is up to date. Nothing changed; --execute asks you to approve the review above and records it.", *site)
		case pending > 0:
			r.Result("Nothing changed. Re-run with --execute to apply; it asks you to type %s for the review above.", approveWord)
		default:
			r.Result("Nothing changed. Re-run with --execute to apply.")
		}
		// Alone, a degraded plan fails --require-all as the apply would;
		// as a step, the run says so once.
		if _, step := r.(checkReporter); !step && err == nil {
			return requireAll(sites, *mustAll)
		}
		return err
	}

	plans := []*apply.Plan{plan}
	if clients == nil {
		if err := apply.Execute(plan, transport); err != nil {
			return err
		}
	} else {
		pass := sitePass{
			plan: func(hold []string, done []*apply.Plan) (*apply.Plan, error) {
				p, err := planFor(hold, done)
				if err != nil {
					return nil, err
				}
				if plan.Bootstrap != nil && p.Bootstrap != nil {
					p.Bootstrap.EtcdUnstarted = plan.Bootstrap.EtcdUnstarted
				}
				return p, nil
			},
			execute: func(p *apply.Plan) error { return apply.Execute(p, transport) },
			notes:   func(p *apply.Plan) { reportNotes(r, p, noted) },
		}
		plans, err = executeWithClients(clients, pass)
		// What the apply recorded at Pocket ID is published before the
		// host records its version, whether or not the apply went on.
		store, id := store, identity
		if run := reviewedByRun; run != nil {
			store, id = run.store, run.identity
		}
		if perr := review.publishWritten(r, clients.written, store, id); perr != nil {
			if err == nil || paused(err) {
				return perr
			}
			r.Warn("what this apply recorded at Pocket ID was not published", sentence(errLine(perr))+" The next apply publishes secrets.enc.yaml as it is then.")
		}
		if err != nil {
			return err
		}
	}
	// The check after the apply looks at what every pass acted on.
	acted := &apply.Plan{}
	written := 0
	for _, p := range plans {
		acted.Actions = append(acted.Actions, p.Actions...)
		written += len(p.Writes())
	}
	if err := checkStandby(r, cfg, acted, *site, transport, func(name string) apply.Transport {
		return siteTransport(name, cfg.Sites[name], "")
	}); err != nil {
		return err
	}
	if err := recordApplied(r, declaration, sites, *site, func(gw string) registry.Runner {
		if gw == *site {
			return transport
		}
		return registryHost(gw, cfg.Sites[gw], "")
	}); err != nil {
		return err
	}
	// The gateway is asked from outside last, once its Caddy has had the
	// whole apply to start: a port another server holds shows nothing to
	// the host's own checks.
	if err := checkGatewayAnswers(r, cfg, *site); err != nil {
		return err
	}
	// What the steps applied is recorded once they have all succeeded.
	if review != nil {
		if err := recordGrant(r, review, cfg.Deployment(), approvedBy); err != nil {
			return err
		}
	}
	return siteApplied(r, cfg, *configPath, *site, *destination, written, sites, *mustAll, clients.result())
}

// planningEverySite is set while apply without --site runs, for every
// `apply --site` step it runs: the run plans every site, so a clustered app
// moves on all of them together, through the run's one digest cache, and no
// step holds it on the build the others run. It is the run that says so, not
// how a step reports, since a step reporting quietly is not a step covering
// every site.
//
// A run that stops after some sites of a clustered app moved leaves them on
// two builds; an `apply --site` then refuses, naming each site's, and the
// next apply without --site moves the rest.
var planningEverySite bool

// planEverySite sets planningEverySite for a run, and returns what puts it
// back.
func planEverySite() (restore func()) {
	saved := planningEverySite
	planningEverySite = true
	return func() { planningEverySite = saved }
}

// readPeers reads, for each app site runs that runs on other sites as well,
// its compose file on each of them, for apply.OtherSites: an apply of one
// site keeps a clustered app on the build its other sites run. With only, it
// reads the apps named there alone, since no other stack moves. A site that
// cannot be read is reported to Build, which decides what that means for the
// app, rather than stopping here.
func readPeers(r ui.Reporter, cfg *config.Config, site string, only []string) ([]apply.PeerCompose, error) {
	sites := render.AppSites(cfg)
	var peers []apply.PeerCompose
	for _, app := range cfg.AppNames() {
		if !contains(sites[app], site) || len(sites[app]) < 2 || (len(only) > 0 && !contains(only, app)) {
			continue
		}
		others := map[string]apply.Transport{}
		for _, name := range sites[app] {
			if name != site {
				others[name] = siteTransport(name, cfg.Sites[name], "")
			}
		}
		read, err := ui.Get(r, "read "+app+" on its other sites", func() ([]apply.PeerCompose, error) {
			return apply.ReadPeers(cfg.Deployment(), app, others), nil
		})
		if err != nil {
			return nil, err
		}
		peers = append(peers, read...)
	}
	return peers, nil
}

// siteApplied is how apply --site --execute ends once site's stacks are up:
// the web server step for the apps served by site's web server, when it has
// managed_by: external, reaching site as the apply did, then the result, or
// what the step left. err is the apply's own problem, if it has one. As a
// step of apply without --site (a checkReporter) it leaves the web server
// to the run, which does it once every step has run.
//
// Alone, with sites pending or down, it ends degraded, and with required
// (--require-all) that is a failure. As a step, the run says so once, at its
// end.
func siteApplied(r ui.Reporter, cfg *config.Config, configPath, site, ssh string, written int, sites *topology.Classification, required bool, err error) error {
	_, step := r.(checkReporter)
	degraded := sites != nil && sites.Degraded()
	if !step {
		if end, broken := webServerStep(r, cfg, configPath, webServerScope{site: site, ssh: ssh, degraded: degraded}); end != nil {
			err = webServerEnd(end, broken, err)
			if !degraded {
				return err
			}
			// A warning is the run's end, under the degraded result line,
			// which says what was applied; a failure is its own end.
			var w *ui.Warning
			if errors.As(err, &w) {
				r.Result("%s. %s applied to %s.", degradedResult(sites), plural(written, "file"), site)
			}
			return degradedErr(sites, required, err)
		}
	}
	if step || !degraded {
		r.Result("Applied %s to %s.", plural(written, "file"), site)
		return err
	}
	r.Result("%s. %s applied to %s.", degradedResult(sites), plural(written, "file"), site)
	return degradedErr(sites, required, err)
}

// checkStandby is the deployment level gate after an apply that acted on a
// Pocket ID stack running on more than one site: every stack passed its own
// gate, active or standing by, and this asks every site that exactly one is
// active. The applied site is reached as the apply reached it, the others
// through their ssh sections. See apply.CheckOneActive.
func checkStandby(r ui.Reporter, cfg *config.Config, plan *apply.Plan, site string, transport apply.Transport, other func(string) apply.Transport) error {
	acted := map[string]bool{}
	for _, action := range plan.Actions {
		acted[action.Stack] = true
	}
	sites := render.AppSites(cfg)
	for _, app := range apply.StandbyApps(cfg) {
		if !acted[app] {
			continue
		}
		transports := map[string]apply.Transport{}
		for _, name := range sites[app] {
			if name == site {
				transports[name] = transport
			} else {
				transports[name] = other(name)
			}
		}
		// The sites' states are the check's detail on success. On failure
		// the table is the evidence the error points at ("each site
		// above"), so it is shown whatever the verbosity.
		s := r.Step("check one pocket-id " + app + " is active")
		var seen bytes.Buffer
		if err := apply.CheckOneActive(cfg, app, transports, &seen); err != nil {
			s.Fail(err)
			r.Refuse("pocket-id "+app+" instances", seen.String())
			return fmt.Errorf("%w. The apply itself finished; this is the check after it", err)
		}
		s.Detail("%s", strings.TrimRight(seen.String(), "\n"))
		s.Done("")
	}
	return nil
}

// imageDigests is what resolves the tags images are named by (each app's,
// and the gateway's Caddy) to the builds they name while apply plans; see
// apply.ResolveImages. Tests replace it, so that none reaches a registry.
var imageDigests apply.Resolver = imagedigest.Registry{}

func init() {
	// A command that starts a stack where none ran resolves its tracks
	// through the same resolver apply does.
	apply.FreshImages = func() apply.Resolver { return imageDigests }
}

// cacheDigests puts an imagedigest.Cache in front of imageDigests for the
// rest of an apply, and returns what puts it back. Every plan the apply makes
// then names the build its first plan resolved each tag to: without --site
// that is the pull stage's plan of each site, then each site's apply and pass
// two, so every site of a clustered app moves to one build; with it, each
// pass of the one site's apply. An apply run as a step of another finds the
// cache in place and keeps it, so the whole run shares one. Tests that
// replace imageDigests still decide what it answers; the cache only stops it
// answering twice.
func cacheDigests() (restore func()) {
	if _, cached := imageDigests.(*imagedigest.Cache); cached {
		return func() {}
	}
	saved := imageDigests
	c := imagedigest.Cached(saved)
	if _, real := saved.(imagedigest.Registry); real {
		// Only the real registry's answers are kept across runs: a test's
		// fake answers what the test needs.
		if dir, err := blueprints.CacheDir(); err == nil {
			c.Store = filepath.Join(dir, "platforms")
		}
	}
	imageDigests = c
	return func() { imageDigests = saved }
}

// planSiteApply is everything apply decides for one site, without doing any
// of it: the etcd flags the site's member was born with, the render, the plan
// and the databases it bootstraps. `storage rotate-key` switches an app with
// exactly this, scoped by apply.Only, so a rotation's switch is the apply an
// operator would have typed rather than a second path to the same host.
//
// Images are resolved through imageDigests unless options say otherwise: they
// follow it, so a caller's apply.ResolveImages(nil) keeps every deployed
// build.
// siteRenderOptions are the facts read from site's host that its render
// needs. An etcd member keeps the flags it was born with, so that
// etcd.members growing never changes a running member's compose file (see
// render.EtcdInitialPath). A site that keeps the monitor while the gateway
// role has left it, and whose host still holds the gateway's record or a
// move's marker, is still the gateway DNS points at: it keeps every route
// until gateway move hands it off. Only a gateway is ever given the record,
// so a plain monitor never renders as one.
func siteRenderOptions(cfg *config.Config, site string, transport apply.Transport) ([]render.Option, error) {
	var out []render.Option
	if contains(cfg.Etcd.Members, site) {
		initial, found, err := apply.ReadEtcdInitial(transport, cfg.Deployment())
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, render.WithEtcdInitial(site, initial))
		}
	}
	if s := cfg.Sites[site]; !s.Has(config.RoleGateway) && s.RunsCaddy() {
		departing, err := gatewaymove.HoldsRecordOrMarker(transport, cfg.Deployment())
		if err != nil {
			return nil, err
		}
		if departing {
			out = append(out, render.WithDepartingGateway(site))
		}
	}
	return out, nil
}

func planSiteApply(cfg *config.Config, secrets *config.Secrets, site string, transport apply.Transport, options ...apply.Option) (*apply.Plan, error) {
	renderOptions, err := siteRenderOptions(cfg, site, transport)
	if err != nil {
		return nil, err
	}
	rendered, err := render.Build(cfg, secrets, renderOptions...)
	if err != nil {
		return nil, err
	}
	options = append([]apply.Option{apply.DatabaseApps(apply.ClusterDatabaseApps(cfg)...), apply.ResolveImages(imageDigests), apply.Arch(cfg.Sites[site].Arch), apply.Serves(apply.ServedHostnames(cfg))}, options...)
	plan, err := apply.Build(site, rendered, acme.Module(cfg.ACME.Provider), transport, options...)
	if err != nil {
		return nil, err
	}
	databases, err := apply.Databases(cfg, secrets, site)
	if err != nil {
		return nil, err
	}
	plan.WithDatabases(databases)
	return plan, nil
}

// runStorageInit provisions Garage object storage on one site: the cluster
// layout, each app's S3 key, and its bucket. It is modelled on runApply,
// right down to the dry run by default, because it is the other command that
// reaches a host.
//
// It must run after the infrastructure stack is up, since Garage has to be
// reachable to be asked what it already has.
func runStorageInit(args []string) error {
	fs := newFlagSet("storage init", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	site := fs.String("site", "", "the site to provision, by the name it has in the configuration")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "actually create what is missing")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	r := reporter()
	if *site == "" {
		return fmt.Errorf("storage init: --site is required. A site at a time is deliberate, the same reason apply --site takes one")
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
	declared, ok := cfg.Sites[*site]
	if !ok {
		return fmt.Errorf("storage init: %s declares no site %q. Declared sites are %s", *configPath, *site, strings.Join(cfg.SiteNames(), ", "))
	}
	if cfg, err = storageConfig(r, cfg, *site); err != nil {
		return err
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	// A malformed key has to stop here, before it reaches `garage key import`
	// partway through provisioning: earlier keys in the same run would
	// already be imported and cannot be imported again.
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}

	transport := siteTransport(*site, declared, *destination)
	r.Section(fmt.Sprintf("%s (%s)", *site, transport.Describe()))
	if _, err := admittedHosts(r, cfg, map[string]registry.Runner{*site: transport}); err != nil {
		return err
	}
	plan, err := ui.Get(r, "read Garage", func() (*garage.Plan, error) {
		return garage.Build(*site, cfg, secrets, transport)
	})
	if err != nil {
		return err
	}
	// A dry run is the plan. --execute reports progress instead, and shows
	// the plan first only with --verbose, since its steps say the same.
	if !*execute || r.Verbose() {
		plan.Show(r)
	}

	if !*execute {
		dryRunFound(count(len(plan.Steps), "change"), nil)
		if len(plan.Steps) == 0 {
			r.Result("%s is provisioned. Nothing to do.", *site)
			return nil
		}
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	if err := garage.Report(plan, transport, r); err != nil {
		return err
	}
	r.Result("Provisioned %s on %s.", plural(len(plan.Steps), "step"), *site)
	return nil
}

// runHostPrepare takes one site's host to the state apply assumes. It is
// modelled on runStorageInit: probe read only, print, and change the host only
// with --execute. It needs no secrets; nothing it installs is a credential.
func runHostPrepare(args []string) error {
	fs := newFlagSet("host prepare", flag.ExitOnError)
	reporter := commonFlags(fs)
	o := hostPrepareFlags(fs)
	answers := yesFlag(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// dryRunFound is read when it is called: apply's check sets it around
	// the run.
	found := func(summary string, stop error) { dryRunFound(summary, stop) }
	ans, err := answers()
	if err != nil {
		return err
	}
	return hostPrepare(prepareEnv{r: reporter(), found: found, answers: ans, stdin: os.Stdin, stdout: os.Stdout}, *o)
}

// hostPrepareOptions are host prepare's flags.
type hostPrepareOptions struct {
	config, site, ssh string
	execute           bool
	// all prepares every site, one at a time: see hostPrepareAll.
	all bool
	// shown is set once a run of --all has shown the configuration's
	// findings, so each later site does not show them again.
	shown bool
}

// hostPrepareFlags registers host prepare's own flags on fs.
func hostPrepareFlags(fs *flag.FlagSet) *hostPrepareOptions {
	o := &hostPrepareOptions{}
	fs.StringVar(&o.config, "config", config.FileName, "path to the deployment declaration")
	fs.StringVar(&o.site, "site", "", "the site to prepare, by the name it has in the configuration")
	fs.BoolVar(&o.all, "all", false, "prepare every site the configuration declares, one at a time, in its order")
	fs.StringVar(&o.ssh, "ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	fs.BoolVar(&o.execute, "execute", false, "actually write what is missing")
	return o
}

// prepareEnv is what one run of host prepare reports through. Typed by hand
// it is the command's reporter and the process's dryRunFound; apply without
// --site, which prepares several hosts at once, gives each its own, so that
// no host's run touches what another's is using.
type prepareEnv struct {
	// r is what the command reports through.
	r ui.Reporter
	// found is told a dry run's summary, as dryRunFound is.
	found func(summary string, stop error)
	// retries, when set, receives the command's ssh retries in place of
	// the process's retry log.
	retries io.Writer
	// answers, stdin and stdout are how the review asks for approval, as
	// runSiteRemove takes them. Under apply with no --site, which asks once
	// for every site, they are unused.
	answers answerer
	stdin   io.Reader
	stdout  io.Writer
}

// runHostPrepareIn is host prepare run with args through env, on none of the
// process's globals: its flags parse into its own variables, and nothing is
// routed. -v is accepted and changes nothing here; env.r is already as
// verbose as the run.
func runHostPrepareIn(env prepareEnv, args []string) error {
	fs := newFlagSet("host prepare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var verbose bool
	verboseFlags(fs, &verbose)
	o := hostPrepareFlags(fs)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("host prepare: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host prepare: %q is extra", fs.Arg(0))
	}
	return hostPrepare(env, *o)
}

// hostPrepare is host prepare with its flags parsed, reporting through env.
func hostPrepare(env prepareEnv, o hostPrepareOptions) error {
	r := env.r
	if o.all {
		return hostPrepareAll(env, o)
	}
	if o.site == "" {
		return fmt.Errorf("host prepare: name the site with --site, or pass --all for every one")
	}

	cfg, err := config.Load(o.config)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	if !o.shown {
		reportFindings(r, o.config, result)
	}
	if result.Refused() {
		return refused(o.config, len(result.Refusals()), "")
	}
	declared, ok := cfg.Sites[o.site]
	if !ok {
		return fmt.Errorf("host prepare: %s declares no site %q. Declared sites are %s", o.config, o.site, strings.Join(cfg.SiteNames(), ", "))
	}

	transport := siteTransport(o.site, declared, o.ssh)
	if env.retries != nil {
		transport.RetryLog = env.retries
	}
	r.Section(fmt.Sprintf("%s (%s)", o.site, transport.Describe()))
	// See runApply: host prepare records its own site's architecture, which
	// the next apply publishes. As a step of apply without --site, where
	// every host prepares at once, the run recorded every key already and
	// this never writes.
	read, err := readArchitectures(r, cfg, []string{o.site}, func(string) hostarch.Runner { return transport })
	if err != nil {
		return err
	}
	if cfg, err = recordArchitectures(r, o.config, cfg, read, o.execute && reviewedByRun == nil); err != nil {
		return err
	}
	done := r.Step("check mesh subnet")
	err = checkMeshLive(cfg, o.site, transport)
	if err != nil {
		done.Fail(err)
		return err
	}
	// Only once it is true: a check that could not run found nothing.
	done.Detail("nothing on %s overlaps the mesh subnet", o.site)
	done.Done("")
	if _, err := hostGate(r, cfg, o.site, transport); err != nil {
		return err
	}
	// The review comes before the first change: an
	// operator who does not approve leaves the host exactly as it was.
	g, err := grant.For(cfg, nil, o.site)
	if err != nil {
		return err
	}
	review, approvedBy, err := stepReview(r, env.answers, env.stdin, env.stdout, cfg, o.site, g, grant.PrepareScope, transport, o.execute, "paisans host prepare --site "+o.site+" --execute")
	if err != nil {
		return err
	}
	recs, err := admittedHosts(r, cfg, map[string]registry.Runner{o.site: transport})
	if err != nil {
		return err
	}
	plan, err := ui.Get(r, "read the host", func() (*hostprep.Plan, error) {
		return hostprep.Build(o.site, cfg, transport, recs[o.site])
	})
	if err != nil {
		return err
	}
	// A dry run is the plan. --execute reports progress instead, and shows
	// the plan first only with --verbose, since its steps say the same.
	if !o.execute || r.Verbose() {
		plan.Show(r)
	} else {
		plan.Warn(r)
	}

	if !o.execute {
		// The kinds of change the plan makes, which apply's dry run shows
		// on this step's line. As a step of it there is no review here:
		// apply shows one for every site.
		pending := 0
		if review != nil {
			showReview(r, "review what "+config.FileName+" grants", review.review, false)
			pending = review.review.Pending()
		}
		env.found(strings.Join(plan.Kinds(), ", "), nil)
		switch {
		case len(plan.Steps) == 0 && pending == 0:
			r.Result("%s is prepared. Nothing to do.", o.site)
		case len(plan.Steps) == 0:
			r.Result("%s is prepared. Nothing changed; --execute asks you to approve the review above and records it.", o.site)
		case pending > 0:
			r.Result("Nothing changed. Re-run with --execute to apply; it asks you to type %s for the review above.", approveWord)
		default:
			r.Result("Nothing changed. Re-run with --execute to apply.")
		}
		return nil
	}
	if err := hostprep.Execute(plan, transport, r); err != nil {
		return err
	}
	if err := recordGrant(r, review, cfg.Deployment(), approvedBy); err != nil {
		return err
	}
	r.Result("Prepared %s.", o.site)
	return nil
}

// hostPrepareAll is host prepare --all: every site the configuration
// declares, in its order, each as host prepare --site would prepare it, with
// its own review. It stops at the first site that fails, and since a prepared host
// plans nothing, running it again resumes there.
func hostPrepareAll(env prepareEnv, o hostPrepareOptions) error {
	if o.site != "" || o.ssh != "" {
		return &ui.Problem{Hint: "--all takes no --site or --ssh", Explain: "--all prepares every site through its own ssh section. Prepare one site with --site, reaching it through --ssh if need be, or pass --all alone."}
	}
	cfg, err := config.Load(o.config)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(env.r, o.config, result)
	if result.Refused() {
		return refused(o.config, len(result.Refusals()), "")
	}
	names := cfg.SiteNames()
	for _, name := range names {
		site := o
		site.all, site.site, site.shown = false, name, true
		if err := hostPrepare(env, site); err != nil {
			next := "Run paisans host prepare --all again to resume."
			if o.execute {
				next = "Run paisans host prepare --all --execute again to resume; a prepared host plans nothing."
			}
			return convergeStop("host prepare --all stopped at "+name, err, next)
		}
	}
	return nil
}

// insideApply is set while apply runs dns init or gateway move as one of its
// steps. Apply has already shown the configuration's findings and ends on a
// result of its own, so the step shows neither.
var insideApply bool

// runDNSInit creates the public DNS records a deployment needs, at the
// provider acme.provider names, using the token that already answers ACME
// challenges. It is modelled on runStorageInit: a dry run by default, and
// only what is missing is created.
//
// It reaches no host. The workstation talks to the provider's API and to
// nothing else, so it takes no --site and no --ssh.
func runDNSInit(args []string) error {
	r, cfg, wants, provider, execute, err := dnsSetup("dns init", "actually create the missing records", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := ui.Get(r, "read the zone's records", func() (*dns.Plan, error) {
		return dns.Build(ctx, provider, cfg.Deployment(), wants)
	})
	if err != nil {
		return err
	}
	// A dry run is the plan. --execute reports progress instead, and shows
	// the plan first only with --verbose, since its steps say the same.
	if !execute || r.Verbose() || len(plan.Conflicts()) > 0 {
		plan.Show(r)
	}

	if !execute {
		if n := len(plan.Conflicts()); n > 0 {
			dryRunFound("", fmt.Errorf("%s with the provider's: %w", plural(n, "record conflict"), dns.ErrConflict))
			r.Result("%s conflict with the provider's. Resolve %s before --execute, which creates nothing while one stands.", plural(n, "record"), map[bool]string{true: "it", false: "them"}[n == 1])
			return nil
		}
		creates := ""
		if n := len(plan.Creates()); n > 0 {
			creates = plural(n, "record") + " to create"
		}
		dryRunFound(creates, nil)
		if len(plan.Creates()) == 0 {
			r.Result("Every record is present. Nothing to create.")
			return nil
		}
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	if err := dns.Execute(ctx, provider, plan, r); err != nil {
		return err
	}
	if !insideApply {
		r.Result("Created %s at %s.", plural(len(plan.Creates()), "record"), plan.Provider)
	}
	return nil
}

// runDNSPrune deletes the address records dns init created that the
// configuration no longer implies, under the rules internal/dns/prune.go
// states. A dry run by default, like dns init, and it reaches no host.
func runDNSPrune(args []string) error {
	var vouched pathList
	r, cfg, wants, provider, execute, err := dnsSetup("dns prune", "actually delete the records listed as remove", args, func(fs *flag.FlagSet) {
		fs.Var(&vouched, "name", "vouch that this exact name, outside community.domain and no longer configured, was this deployment's (repeatable)")
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := ui.Get(r, "read the zone's records", func() (*dns.PrunePlan, error) {
		return dns.BuildPrune(ctx, provider, cfg, wants, vouched...)
	})
	if err != nil {
		return err
	}
	if !execute || r.Verbose() {
		plan.Show(r)
	}

	if !execute {
		if len(plan.Removes()) == 0 {
			r.Result("No record to delete.")
			return nil
		}
		r.Result("Nothing changed. Re-run with --execute to apply.")
		return nil
	}
	if err := dns.ExecutePrune(ctx, provider, plan, r); err != nil {
		return err
	}
	r.Result("Deleted %s at %s.", plural(len(plan.Removes()), "record"), plan.Provider)
	return nil
}

// dnsSetup is what both dns subcommands do before contacting the provider:
// validate, derive the wanted records, then open the secrets. The records
// are worked out before the secrets are opened or the provider is contacted,
// so a configuration that cannot name its records is refused offline.
func dnsSetup(name, executeHelp string, args []string, extra ...func(*flag.FlagSet)) (ui.Reporter, *config.Config, []dns.Want, dns.Provider, bool, error) {
	fs := newFlagSet(name, flag.ExitOnError)
	reporter := commonFlags(fs)
	for _, add := range extra {
		add(fs)
	}
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	execute := fs.Bool("execute", false, executeHelp)
	if err := parseFlags(fs, args); err != nil {
		return nil, nil, nil, nil, false, err
	}
	r := reporter()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	result := validate.Check(cfg)
	if !insideApply {
		reportFindings(r, *configPath, result)
	}
	if result.Refused() {
		return nil, nil, nil, nil, false, refused(*configPath, len(result.Refusals()), "")
	}
	// dns init publishes the effective topology's records, so none for a
	// pending site. dns prune is left on the declaration: it removes, and
	// a record a run did not publish is not one to take away.
	if name == "dns init" {
		if cfg, _, err = effectiveConfig(r, cfg, ""); err != nil {
			return nil, nil, nil, nil, false, err
		}
	}
	wants, err := dns.Desired(cfg)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}

	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	provider, err := dns.For(cfg.ACME.Provider, secrets.External["acme_dns_token"])
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	if err := checkWorkstationToken(r, cfg, secrets.External["acme_dns_token"]); err != nil {
		return nil, nil, nil, nil, false, err
	}
	return r, cfg, wants, provider, *execute, nil
}

// workstationTokenCheck is the DNS token check from this workstation. A
// package variable so that a test can stand in for the provider, since no
// test may reach a real one.
var workstationTokenCheck = func(ctx context.Context, check dns.TokenCheck, token string) dns.TokenResult {
	return check.FromWorkstation(ctx, token)
}

// checkWorkstationToken asks the provider, before the first records call,
// whether it accepts the token from this workstation. The provider judges
// the address a call comes from, so a token filtered to the gateway fails
// here, and this says so in place of a bare HTTP 403 from the records call
// after it.
func checkWorkstationToken(r ui.Reporter, cfg *config.Config, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	step := r.Step("check DNS token")
	result := workstationTokenCheck(ctx, dns.TokenCheck{
		Provider: cfg.ACME.Provider,
		Domain:   cfg.Community.Domain,
		Place:    dns.WorkstationPlace,
		From:     "this workstation",
	}, token)
	if result.Status != dns.TokenFailed {
		step.Detail("%s", result.Finding)
		step.Done("")
		return nil
	}
	step.Fail(errors.New(result.Finding))
	return &ui.Problem{
		Hint:    result.Finding,
		Explain: result.Advice + " No record was read or changed.\n`paisans doctor` checks the token from every place that uses it.",
	}
}

// warnUnencrypted is the warning every command that opens the secrets gives
// for a file that is not under sops. The file is accepted, since fixtures and
// examples are plain, so it is a warning and not a refusal.
func warnUnencrypted(r ui.Reporter, path string) {
	r.Warn(path+" is not encrypted", "That is accepted for fixtures and examples; a real deployment keeps its secrets under sops.")
}

// pathList collects a repeatable flag. --overwrite takes one path each time it
// is given, so that every file replaced against its record was named on its
// own: a pattern or a blanket switch would let one decision cover files the
// operator never looked at. --recreate takes one stack each time, for the same
// reason.
type pathList []string

func (l *pathList) String() string { return strings.Join(*l, ",") }

func (l *pathList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// siteTransport is how a command reaches a site: its ssh section, or the
// --ssh override verbatim. The override replaces the whole section rather
// than one part of it, so what is used is always either everything the file
// says or exactly what the operator typed, never a blend of the two.
//
// Every command that acts on a deployment reaches its sites this way, as the
// login user and without sudo: that user is the deployment's own, and the
// most a command can reach is what that user owns. Its first command asks
// the user's UID, once a run (loginFor), and every command runs with
// DOCKER_HOST at that user's rootless daemon. A site that names no user is
// reached as the deployment's default, and its transport says so, so a
// host that refuses it is reported as not admitted. Its first login as that
// default, in a run with --execute, writes the name into the site's ssh.user
// (recordSSHUser). name is the site's name in paisans-stack.yaml, for
// messages and for the section that write goes in; it plays no part in
// reaching the host.
func siteTransport(name string, site config.Site, override string) apply.SSHTransport {
	var t apply.SSHTransport
	if override != "" {
		t = apply.SSHTransport{Site: name, Destination: override}
	} else {
		// validate has already refused a bad key, so problems are empty here.
		keys, _ := site.SSH.AuthorizedKeys()
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k.Line
		}
		user := site.SSHUser()
		t = apply.SSHTransport{Site: name, User: user, Host: site.SSHHost(), Port: site.SSH.PortOrDefault(), PublicKeys: lines, DefaultUser: site.SSH.User == "" && user != ""}
	}
	var key string
	var record func()
	if path := site.SSHUserFile(); override == "" && name != "" && path != "" {
		key, record = path+"\x00"+name, recordSSHUser(path, name, site.SSHUser())
	}
	t.Login = loginFor(t.Describe(), key, record)
	return t
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
