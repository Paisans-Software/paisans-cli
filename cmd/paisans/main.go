// Command paisans renders, checks and applies a paisans deployment
// declaration.
//
// validate and render touch nothing outside the working directory. init
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

	"github.com/paisans-software/paisans-stack/internal/acme"
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/dns"
	"github.com/paisans-software/paisans-stack/internal/garage"
	"github.com/paisans-software/paisans-stack/internal/grant"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
	"github.com/paisans-software/paisans-stack/internal/registry"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
	"github.com/paisans-software/paisans-stack/internal/storageadd"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

const usage = `paisans renders and checks a community stack declaration.

Usage:
  paisans validate [--config paisans.yaml]
  paisans init     [--config paisans.yaml] [--secrets secrets.enc.yaml] [--sudo=false]
  paisans render   [--config paisans.yaml] [--secrets secrets.enc.yaml] --out ./out
  paisans host prepare --site <name> [--config paisans.yaml] [--ssh <destination>]
               [--execute]
  paisans host deployments --ssh <user@host[:port]> [--sudo=false]
  paisans host allow <site> <admin>... [--config paisans.yaml] [--ssh <destination>]
               [--execute]
  paisans host disallow <site> <admin|fingerprint>... [--config paisans.yaml]
               [--ssh <destination>] [--execute]
  paisans apply    [--config paisans.yaml] [--secrets secrets.enc.yaml]
                   [--min-free <size>] [--keep-images] [--execute]
                   every site, from paisans.yaml to a running stack, in order
  paisans apply    --site <name> [--config paisans.yaml] [--secrets secrets.enc.yaml]
                   [--ssh <destination>] [--only <stack>]... [--overwrite <path>]...
                   [--recreate <stack>]... [--min-free <size>] [--keep-images] [--execute]
  paisans site add <site> [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--execute]
  paisans site remove <site> [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--host-gone] [--delete-data] [--force --ssh <user@host[:port]>] [--execute]
  paisans site remove --force --ssh <user@host[:port]> --id <id or token>
               [--delete-data] [--sudo=false] [--execute]
  paisans gateway move [--config paisans.yaml] [--secrets secrets.enc.yaml] [--execute]
  paisans storage init --site <name> [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--ssh <destination>] [--execute]
  paisans storage add [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--change-replication] [--wait <duration>] [--stop-test] [--execute]
  paisans storage rotate-key --app <name> [--config paisans.yaml]
               [--secrets secrets.enc.yaml] [--execute]
  paisans prune    --site <name> [--config paisans.yaml] [--ssh <destination>] [--execute]
  paisans preflight --site <new site> [--config paisans.yaml]
  paisans failover test [--config paisans.yaml] [--execute]
  paisans doctor   [--config paisans.yaml] [--site <name>]... [--sudo]
  paisans dns init [--config paisans.yaml] [--secrets secrets.enc.yaml] [--execute]
  paisans dns prune [--config paisans.yaml] [--secrets secrets.enc.yaml] [--name <fqdn>]... [--execute]
  paisans config publish [--config paisans.yaml] [--merge] [--execute]
  paisans config pull [--config paisans.yaml] [--root <sha>] [--execute]
  paisans config log [--config paisans.yaml] [--root <sha>]
  paisans config recover --from <user@host[:port]>... [--root <sha>] [--id <id>]
               [--config paisans.yaml] [--execute]
  paisans admin register [--config paisans.yaml] [--key <path.pub>] [--root <sha>] [--execute]
  paisans secrets set <dotted.key> [--config paisans.yaml] [--secrets secrets.enc.yaml] < value
  paisans secrets prune [--config paisans.yaml] [--secrets secrets.enc.yaml] [--execute]
  paisans app admin create --app <pocket-id app> --username <u> --email <e>
               [--first-name <f>] [--last-name <l>] [--login-link]
               [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--site <name>] [--ssh <destination>] [--execute]
  paisans app remove <app> [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--delete-data] [--execute]
  paisans oidc client create --app <name> [--rotate-secret]
               [--config paisans.yaml] [--secrets secrets.enc.yaml]
               [--site <name>] [--ssh <destination>] [--execute]
  paisans web-server show  --app <name> [--config paisans.yaml] [--sudo=false]
  paisans web-server check --app <name> [--config paisans.yaml] [--sudo=false]

Commands:
  validate   Load the configuration and report every problem found.
  init       Give the configuration an id if it has none; until the
             deployment is on any site, reach every site and keep
             mesh.subnet only if it overlaps nothing there, else roll a
             random /24 that overlaps nothing and move each site's address
             into it; then generate the secrets it needs, filling in only
             what is missing, and say what is still owed from elsewhere.
  render     Validate, then write per site artifacts to a local directory.
  host       prepare: take a blank host to the state apply assumes: Docker,
             the WireGuard tools, a firewall, and a watchdog on a data site.
             Installs only what is missing. Writes nothing without --execute.
             deployments: list every deployment in the registry of the host
             --ssh names (id, token, domain, site, roles and root, and a
             Caddy kept for /srv/caddy.d), and any deployment directory or
             labelled compose project no registry entry names. Needs no
             paisans.yaml; changes nothing.
             allow: add each admin's key fingerprints to the site's host's
             key allowlist, /etc/paisans/ssh-allow.<user>, the keys host
             prepare may add there; a new list also holds every key a
             deployment already claims. disallow: take keys off it, by
             admin or fingerprint; a key already authorized keeps working.
             Both are the host owner's: --execute asks for the site's name
             at a terminal. Write nothing without --execute.
  apply      Without --site: take every site from paisans.yaml to a running
             stack, running init, host prepare, apply, site add, storage
             and dns init in the order the deployment needs. With --site:
             compare that site's rendered artifacts with what is on its
             host and show what would change. Changes nothing on a
             host without --execute; without --site, the dry run may add
             missing generated secrets to the local secrets file, and
             says so when it does. Both, and host prepare, review the keys
             and images paisans.yaml grants against what each host last
             applied; --execute asks for the word approve at a terminal
             when anything is added or changed, and asks nothing when
             nothing is.
  site       add: join a new data site to the running cluster in eight gated
             stages: preflight, mesh, etcd (learners, then promoted), the
             Patroni replica, synchronous mode, HAProxy, each existing
             replica's patroni.env, one replica restart at a time, and each
             monitor site's uptime stack, so it checks the new site. Reads
             every site and plans only what differs, so a re-run resumes.
             Writes nothing without --execute.
             remove: take a site out of the running deployment in gated
             stages: its data out (the Patroni leader switched to
             the Sync Standby, its Garage node out of the layout and the
             objects moved), out of the cluster (its etcd member, and the
             mesh, HAProxy and gateway routes on every remaining site, and
             each replica's patroni.env, one at a time; one data site out
             of two and a witness takes the witness out of etcd too),
             its host cleaned of everything provably this deployment's
             (a gateway's Caddy kept, reduced to the host owner's sites in
             /srv/caddy.d, while there are any), the A and AAAA records
             dns init made for its public address deleted at the DNS
             provider, and its entry out of paisans.yaml.
             Refuses the only gateway, data or apps site, a site an app
             is pinned to, and an unhealthy cluster. --host-gone skips
             the host; --delete-data deletes its data after the site's
             name is typed at a terminal. Writes nothing without
             --execute.
             remove --force --ssh <host> --id <id or token>, with no
             paisans.yaml: clean that host of the deployment its registry
             names, as --force does, from the registry entry alone.
             DNS is not modified. --execute asks for the site's name at
             a terminal.
  gateway    move: move the gateway role to the site paisans.yaml gives it,
             from the site that still serves as the gateway, in gated
             stages: carry the deployment record, check the new gateway
             answers for every hostname through its own address, add its
             address records beside the old one's, delete the old one's
             (only after that check, and only records dns init made for
             it), wait out their TTL, then stop the old gateway's Caddy.
             Resumes after a fix. Writes nothing without --execute.
  storage    init: provision object storage on a site: each app's key and
             bucket, and the layout when it is the only Garage site.
             add: join every site in storage.garage.sites into one Garage
             cluster, in gated stages: connect, layout, sync, provision,
             media routes, a smoke test; and, with --change-replication,
             Garage's reset for another replication factor. Exits at a
             stage waiting on Garage (status 75) and resumes on the next
             run. Both create only what is missing, and write nothing
             without --execute.
             rotate-key: replace one app's S3 key, in gated stages: the
             new pair into the secrets (the old one kept beside it), the
             new key imported and granted, the app applied alone on every
             site running it, a probe written, read and deleted with the
             new key, and only then the old key deleted from Garage and
             the secrets. Resumes from the secrets file. Writes nothing
             without --execute.
  prune      List one site's dangling Docker volumes, with size and top level
             entries, and say which carry this deployment's label.
             Removes those with --execute; every other volume is kept.
  preflight  The read only checks site add runs first, for the site being
             added and every site already running. Changes nothing.
  failover   test: switch the Patroni primary to another data site and
             back, checking the cluster and every app before and after
             each switch. Interrupts writes briefly, twice. Changes
             nothing without --execute.
  doctor     Reach every site, or the ones --site names, and report what is
             stuck: unreachable sites, etcd quorum and version, the Patroni
             leader and why a replica will not promote, containers that are
             down, Pocket ID's active instance, and clocks. Prints how to
             recover. Reaches hosts, changes nothing; exits 1 on any FAIL.
  dns        init: create the public DNS records the configuration implies,
             at the provider named by acme.provider: addresses, and records
             saying every name without mail (community.mail_domains, smtp
             senders) sends and receives none. Creates only what is
             missing, never updates or deletes, and refuses if any record
             conflicts.
             prune: delete the records dns init created that the
             configuration no longer implies: only one carrying init's
             comment, at a name under community.domain or one the
             configuration names, and no longer wanted; an A or AAAA
             pointing at a site's public address, or a TXT or MX holding
             exactly a no-mail content init writes. Lists every other record init marked, with why
             it is kept, and lists the zone again to confirm each delete.
             Both write nothing without --execute.
  config     The signed copies of paisans.yaml, secrets.enc.yaml and
             .sops.yaml every site keeps. Each gathers every site's
             versions first and trusts them from your own signing key or a
             root pinned with --root. publish: sign the local files as a
             version on the sites' newest and push it to every site;
             --merge builds on every head of a fork. pull: move the local
             files to the sites' newest version, or, over local edits,
             show the difference and put its files in .paisans/incoming/.
             log: list every version, its signer, and the sites holding
             it. recover: onto an empty directory, gather from the --from
             hosts and every site the newest version names, and write its
             files. pull and recover ask for the word partial at a
             terminal when fewer than half the sites answered, and every
             subcommand asks for rollback when the newest version is
             older than one seen here before. With --execute each site
             first syncs with its peers; without it, and in log, no site
             is changed and only .paisans/ here is written.
  admin      register: sign an attestation of the sites' newest version
             with your own key, so trust starts from your signature: the
             key in your ssh-agent listed under your entry in admins, or
             the one --key names. Signs only a version equal to your local
             files, or, with no files and a root pinned, the one the pin
             leads to, and then writes its files. Records the key in
             .paisans/signing-key, which config publish signs with from
             then on. Changes only the sites' configuration stores, and
             nothing without --execute.
  secrets    set: read one value from stdin and write it into the encrypted
             secrets file, printing only its name. For credentials issued
             elsewhere, so they never touch a terminal or an editor.
  app        admin create: make sure a user exists, is verified and is an
             administrator of Pocket ID, and is in every admin group the
             apps signing in through it read. There is no password: a
             created account gets a one-time login link, printed once, to
             register a passkey with. Writes nothing without --execute.
             remove: take an app that has left paisans.yaml off every
             host, once a whole apply on each site has marked its files
             left over: its containers and networks, its rendered files
             (one edited on the host is kept), its stack directory when
             nothing else is in it, and its Pocket ID client when the
             secrets file records that client's ID. --delete-data also
             deletes its database and role, its Garage bucket and keys,
             its named volumes and its data directories, after the app's
             name is typed at a terminal. Leaves its secrets and DNS
             records, and says so. Writes nothing without --execute.
  oidc       client create: create an app's client at the deployment's
             Pocket ID, with the groups the app reads, and record its ID and
             secret in the secrets file. The secret is never printed.
             Writes nothing without --execute. Supports the mbin, outline,
             writefreely and uptime kinds.
  web-server show: for an app pinned to a monitor site, print what the web
             server in front of it must do (terminate TLS for its hostname,
             pass Host, set X-Forwarded-For and X-Forwarded-Proto), filled
             in for the web server it finds on the monitor over ssh, read
             only, or for Caddy, nginx and Apache when it finds none.
             check: from this machine, check that the hostname resolves to
             the monitor, that /healthz answers with a valid certificate,
             that sign in redirects with an https callback, and that the
             published port is closed from outside. Reads only; exits 1 on
             any FAIL. With the toolkit's own Caddy in front (managed_by:
             paisans), only the first two apply.

host prepare, host deployments, host allow, host disallow, apply, prune, site
add, site remove, storage init, storage add, storage rotate-key, app admin
create, app remove, oidc client create, secrets prune, preflight, failover test,
doctor, init, config and admin register are the only commands that
reach a host. Each reads it to plan, and changes it only with --execute; host
allow and host disallow change only a host's key allowlist, which is the host
owner's, and claim nothing; config and admin register claim nothing, and their
--execute only starts each site's configuration sync and, for publish and
register, adds a version to its store; host deployments, preflight,
doctor and init have no --execute and never change it; secrets prune only reads
the gateways' deployment records, and its --execute writes the local secrets
file alone. With --execute, a command first claims each host it writes to in
/var/lib/paisans/registry.json, and refuses if another deployment there holds
this one's token, WireGuard interface or listen port, or a mesh subnet
overlapping this one's. apply and host prepare also refuse when anything else on
the host overlaps the mesh subnet. dns init and dns prune reach no host, only
the DNS provider's API, and change it only with --execute; site remove also
deletes the removed site's records there. web-server show and web-server check
change nothing: check looks at a monitor's public hostname as any visitor could,
and with managed_by: external both read the monitor's web server over ssh, show
always and check when a check fails.
Everything else writes files locally and stops.

Every command takes -v or --verbose. By default each step is one line; with it,
the reasons, values, request bodies and command output behind each step show
too. A failure always says what went wrong and what to do; with -v, the
underlying error shows under it too.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = runValidate(os.Args[2:])
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
		case len(os.Args) >= 3 && os.Args[2] == "allow":
			err = runHostAllow(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "disallow":
			err = runHostDisallow(os.Args[3:], os.Stdin, os.Stdout)
		default:
			os.Exit(usageError(os.Stderr, "host takes one subcommand, prepare, deployments, allow or disallow"))
		}
	case "site":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "add":
			err = runSiteAdd(os.Args[3:], os.Stdin, os.Stdout)
		case len(os.Args) >= 3 && os.Args[2] == "remove":
			err = runSiteRemove(os.Args[3:], os.Stdin, os.Stdout)
		default:
			os.Exit(usageError(os.Stderr, "site takes one subcommand, add or remove"))
		}
	case "gateway":
		err = runGateway(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:], os.Stdin, os.Stdout)
	case "admin":
		err = runAdmin(os.Args[2:], os.Stdin, os.Stdout)
	case "secrets":
		err = runSecrets(os.Args[2:])
	case "app":
		err = runApp(os.Args[2:])
	case "oidc":
		err = runOIDC(os.Args[2:])
	case "web-server":
		err = runWebServer(os.Args[2:])
	case "storage":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "init":
			err = runStorageInit(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "add":
			err = runStorageAdd(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "rotate-key":
			err = runStorageRotateKey(os.Args[3:])
		default:
			os.Exit(usageError(os.Stderr, "storage takes one subcommand, init, add or rotate-key"))
		}
	case "prune":
		err = runPrune(os.Args[2:])
	case "preflight":
		err = runPreflight(os.Args[2:])
	case "failover":
		err = runFailover(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "dns":
		switch {
		case len(os.Args) >= 3 && os.Args[2] == "init":
			err = runDNSInit(os.Args[3:])
		case len(os.Args) >= 3 && os.Args[2] == "prune":
			err = runDNSPrune(os.Args[3:])
		default:
			os.Exit(usageError(os.Stderr, "dns takes one subcommand, init or prune"))
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		os.Exit(usageError(os.Stderr, fmt.Sprintf("unknown command %q", os.Args[1])))
	}
	// Every shared ssh connection the command opened is closed before the
	// process exits, whatever the outcome. os.Exit runs no deferred call, so
	// this comes first on both paths.
	apply.CloseConnections()
	if err != nil {
		os.Exit(reportError(os.Stderr, err, verboseRun))
	}
}

// usageError prints msg in the error form, then the usage, and returns the
// status for a command line paisans cannot run.
func usageError(w io.Writer, msg string) int {
	ui.PrintError(w, errors.New(msg), false)
	fmt.Fprintf(w, "\n%s", usage)
	return 2
}

// reportError prints the error that ends a command, in the refusal's form
// (see ui.PrintError), and returns the status to exit with.
func reportError(w io.Writer, err error, verbose bool) int {
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
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
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
	if len(result.Findings) == 0 {
		r.Result("%s: no problems found", *configPath)
	}
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "Nothing can be rendered or applied from it until then.")
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
// It changes no host. It reads every site, to settle the mesh subnet before
// anything is deployed (see settleMesh), and that is all. Standing a
// deployment up is `apply`, and that is a separate decision from having
// credentials to stand it up with.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	ageKeyFile := fs.String("age-private-key-file", "", "keep a newly made age private key in this file instead of the keystore")
	sudo := fs.Bool("sudo", true, "read each site through sudo, since the host registry is root's")
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
	hosts := initHosts(cfg, *sudo)
	wrote, err := settleMesh(r, cfg, *configPath, hosts, meshRandom)
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
	warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, func(gw string, elevated bool) registry.Runner {
		if elevated {
			return hosts[gw]
		}
		return initHost(gw, cfg.Sites[gw], false)
	}))

	filled, recipients, err := fillSecrets(cfg, *secretsPath, secrets)
	var failed *secretsWriteError
	if err != nil && !errors.As(err, &failed) {
		return err
	}

	if !filled.Changed() {
		r.Detail("%s already has every generated secret (%d)", *secretsPath, len(filled.Kept))
		reportOwed(r, filled)
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
	write.Done(fmt.Sprintf("%d generated, %d kept", len(filled.Generated), len(filled.Kept)))
	if len(recipients) == 0 {
		warnPlaintext(r, *secretsPath)
	} else {
		r.Detail("Encrypted to %d age recipient(s) from %s.", len(recipients), config.SOPSConfigName)
	}
	reportOwed(r, filled)
	r.Result("Generated %s, kept %d.", plural(len(filled.Generated), "secret"), len(filled.Kept))
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

// reportOwed warns of what the toolkit will not invent. Leaving these silent
// would let an operator believe an install is finished when sign in and
// certificates are both still missing.
func reportOwed(r ui.Reporter, filled secretsgen.Result) {
	for _, owed := range filled.Owed {
		r.Warn(owed.Name+" needs a decision", owed.Why)
	}
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
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
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
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
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since /srv and /etc are not the deploy user's")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("apply: %q is not a flag, and apply takes no arguments. Give the configuration with --config %s", fs.Arg(0), fs.Arg(0))
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
		return runConverge(r, convergeOptions{Config: *configPath, Secrets: *secretsPath, Execute: *execute, Sudo: *sudo, Verbose: verbose, KeepImages: *keepImages, MinFree: *minFree, stdin: stdin, stdout: stdout})
	}

	// The files are read once: the bytes loaded here are what is applied and
	// what is published as the version applied.
	cfg, paisansBytes, err := config.LoadWithBytes(*configPath)
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
		return fmt.Errorf("apply: %s declares no site %q. Declared sites are %s", *configPath, *site, strings.Join(cfg.SiteNames(), ", "))
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
	// apply does not call secretsgen.Fill either, and this is the path that
	// actually reaches a host: a malformed key has to stop here, not just
	// print a confusing failure partway through provisioning on the machine.
	if err := secretsgen.CheckGarageKeys(cfg, secrets); err != nil {
		return err
	}
	if err := requireACMEToken(cfg, secrets, []string{*site}); err != nil {
		return err
	}
	// The version this applies is gathered before the review, so a fork or
	// a stale base is refused before the operator is asked anything. As a
	// step of apply without --site the run did this once for every site.
	var store storeVersion
	if reviewedByRun == nil {
		if store, err = applyStore(r, *configPath, *secretsPath, &loaded, cfg, *execute, stdin, stdout); err != nil {
			return err
		}
	} else if v := reviewedByRun.version; v != nil && (!bytes.Equal(v.files.Paisans, loaded.Paisans) || !bytes.Equal(v.files.Secrets, loaded.Secrets)) {
		// The run published one version and every step applies it: files
		// edited since are not what the store holds.
		return &ui.Problem{Hint: "the configuration changed after the run published it", Explain: "paisans.yaml or secrets.enc.yaml is not what this run published as " + v.sha[:12] + ", so applying it would put on " + *site + " a version the store does not hold. Nothing was changed on " + *site + " by this step. Run paisans apply --execute again, which publishes the files as they are now."}
	}

	transport := siteTransport(*site, declared, *destination, *sudo)
	// As a step of apply without --site (a checkReporter) it leaves the
	// record and the secrets to the run, which checks them once.
	if _, step := r.(checkReporter); !step {
		warnSecrets(r, cfg, secrets, recordForWarnings(r, cfg, applyRecordHosts(cfg, *site, *destination, transport, *sudo)))
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
	// The review comes before the claim, which is the first change: an
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
	review, approvedBy, err := stepReview(r, stdin, stdout, cfg, *site, g, grant.ApplyScope(only...), transport, *execute, "paisans apply --site "+*site+" --execute")
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
		if review.version, err = store.publish(r, *execute, "before the first change"); err != nil {
			return err
		}
	case reviewedByRun != nil && review != nil:
		review.version = reviewedByRun.version
	}
	if err := claimHosts(r, cfg, *execute, map[string]registry.Runner{*site: transport}); err != nil {
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
	options := []apply.Option{apply.Overwrite(overwrite...), apply.Recreate(recreate...), apply.MinFree(needFree), apply.Only(only...)}
	// An app this site shares with others stays on the build they run,
	// unless the apply plans them all: as a step of apply without --site it
	// does, and every site resolves through the run's one cache, so none is
	// read here. See planningEverySite.
	if !planningEverySite {
		peers, err := readPeers(r, cfg, *site, only, *sudo)
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
				transports[name] = siteTransport(name, cfg.Sites[name], "", *sudo)
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
	if err := printLeftovers(r, cfg, *site, host.Inventory, plan); err != nil {
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
			showReview(r, "review what paisans.yaml grants", review.review, false)
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
		default:
			r.Result("Nothing changed. Re-run with --execute to apply.")
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
		if plans, err = executeWithClients(clients, pass); err != nil {
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
		return siteTransport(name, cfg.Sites[name], "", *sudo)
	}); err != nil {
		return err
	}
	if err := recordApplied(r, cfg, *site, func(gw string) registry.Runner {
		if gw == *site {
			return transport
		}
		return registryHost(gw, cfg.Sites[gw], "", *sudo)
	}); err != nil {
		return err
	}
	// What the steps applied is recorded once they have all succeeded:
	// the images now, and the web server's directory once its step has
	// written there. As a step of apply with no --site, the web server is
	// the run's, which records it.
	if review != nil {
		images := *review
		images.scope.WebServer = false
		if err := recordGrant(r, &images, cfg.Deployment(), approvedBy); err != nil {
			return err
		}
	}
	err = siteApplied(r, cfg, *configPath, *site, *destination, *sudo, written, clients.result())
	if _, step := r.(checkReporter); err == nil && !step && review != nil {
		web := *review
		web.scope = grant.Scope{WebServer: true}
		return recordGrant(r, &web, cfg.Deployment(), approvedBy)
	}
	return err
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
func readPeers(r ui.Reporter, cfg *config.Config, site string, only []string, sudo bool) ([]apply.PeerCompose, error) {
	sites := render.AppSites(cfg)
	var peers []apply.PeerCompose
	for _, app := range cfg.AppNames() {
		if !contains(sites[app], site) || len(sites[app]) < 2 || (len(only) > 0 && !contains(only, app)) {
			continue
		}
		others := map[string]apply.Transport{}
		for _, name := range sites[app] {
			if name != site {
				others[name] = siteTransport(name, cfg.Sites[name], "", sudo)
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
func siteApplied(r ui.Reporter, cfg *config.Config, configPath, site, ssh string, sudo bool, written int, err error) error {
	if _, step := r.(checkReporter); !step {
		if end, broken := webServerStep(r, cfg, configPath, sudo, webServerScope{site: site, ssh: ssh}); end != nil {
			return webServerEnd(end, broken, err)
		}
	}
	r.Result("Applied %s to %s.", plural(written, "file"), site)
	return err
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
	imageDigests = imagedigest.Cached(saved)
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
func planSiteApply(cfg *config.Config, secrets *config.Secrets, site string, transport apply.Transport, options ...apply.Option) (*apply.Plan, error) {
	// An etcd member keeps the flags it was born with, read from its host,
	// so that etcd.members growing never changes a running member's compose
	// file. See render.EtcdInitialPath.
	var renderOptions []render.Option
	if contains(cfg.Etcd.Members, site) {
		initial, found, err := apply.ReadEtcdInitial(transport, cfg.Deployment())
		if err != nil {
			return nil, err
		}
		if found {
			renderOptions = append(renderOptions, render.WithEtcdInitial(site, initial))
		}
	}
	rendered, err := render.Build(cfg, secrets, renderOptions...)
	if err != nil {
		return nil, err
	}
	options = append([]apply.Option{apply.DatabaseApps(apply.ClusterDatabaseApps(cfg)...), apply.ResolveImages(imageDigests)}, options...)
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
	fs := flag.NewFlagSet("storage init", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	site := fs.String("site", "", "the site to provision, by the name it has in the configuration")
	destination := fs.String("ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	execute := fs.Bool("execute", false, "actually create what is missing")
	sudo := fs.Bool("sudo", true, "run remote commands through sudo, since /srv and /etc are not the deploy user's")
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

	transport := siteTransport(*site, declared, *destination, *sudo)
	r.Section(fmt.Sprintf("%s (%s)", *site, transport.Describe()))
	if err := claimHosts(r, cfg, *execute, map[string]registry.Runner{*site: transport}); err != nil {
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
	fs := flag.NewFlagSet("host prepare", flag.ExitOnError)
	reporter := commonFlags(fs)
	o := hostPrepareFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// dryRunFound is read when it is called: apply's check sets it around
	// the run.
	found := func(summary string, stop error) { dryRunFound(summary, stop) }
	return hostPrepare(prepareEnv{r: reporter(), found: found, stdin: os.Stdin, stdout: os.Stdout}, *o)
}

// hostPrepareOptions are host prepare's flags.
type hostPrepareOptions struct {
	config, site, ssh string
	execute, sudo     bool
}

// hostPrepareFlags registers host prepare's own flags on fs.
func hostPrepareFlags(fs *flag.FlagSet) *hostPrepareOptions {
	o := &hostPrepareOptions{}
	fs.StringVar(&o.config, "config", "paisans.yaml", "path to the deployment declaration")
	fs.StringVar(&o.site, "site", "", "the site to prepare, by the name it has in the configuration")
	fs.StringVar(&o.ssh, "ssh", "", "ssh destination, used verbatim in place of the site's ssh section (its user, host, port and keys are then ignored)")
	fs.BoolVar(&o.execute, "execute", false, "actually install and configure what is missing")
	fs.BoolVar(&o.sudo, "sudo", true, "run remote commands through sudo, since packages, the firewall and kernel modules are root's")
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
	// stdin and stdout are where the review asks for approval, as
	// runSiteRemove takes them. Under apply with no --site, which asks once
	// for every site, they are unused.
	stdin  io.Reader
	stdout io.Writer
}

// runHostPrepareIn is host prepare run with args through env, on none of the
// process's globals: its flags parse into its own variables, and nothing is
// routed. -v is accepted and changes nothing here; env.r is already as
// verbose as the run.
func runHostPrepareIn(env prepareEnv, args []string) error {
	fs := flag.NewFlagSet("host prepare", flag.ContinueOnError)
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
	if o.site == "" {
		return fmt.Errorf("host prepare: --site is required. A site at a time is deliberate, the same reason apply --site takes one")
	}

	cfg, err := config.Load(o.config)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, o.config, result)
	if result.Refused() {
		return refused(o.config, len(result.Refusals()), "")
	}
	declared, ok := cfg.Sites[o.site]
	if !ok {
		return fmt.Errorf("host prepare: %s declares no site %q. Declared sites are %s", o.config, o.site, strings.Join(cfg.SiteNames(), ", "))
	}

	transport := siteTransport(o.site, declared, o.ssh, o.sudo)
	if env.retries != nil {
		transport.RetryLog = env.retries
	}
	r.Section(fmt.Sprintf("%s (%s)", o.site, transport.Describe()))
	done := r.Step("check mesh subnet")
	err = checkMeshLive(cfg, o.site, transport)
	if err != nil {
		done.Fail(err)
		return err
	}
	// Only once it is true: a check that could not run found nothing.
	done.Detail("nothing on %s overlaps the mesh subnet", o.site)
	done.Done("")
	host, err := hostGate(r, cfg, o.site, transport)
	if err != nil {
		return err
	}
	// The review comes before the claim, which is the first change: an
	// operator who does not approve leaves the host exactly as it was.
	g, err := grant.For(cfg, nil, o.site)
	if err != nil {
		return err
	}
	review, approvedBy, err := stepReview(r, env.stdin, env.stdout, cfg, o.site, g, grant.PrepareScope, transport, o.execute, "paisans host prepare --site "+o.site+" --execute")
	if err != nil {
		return err
	}
	if err := claimHosts(r, cfg, o.execute, map[string]registry.Runner{o.site: transport}); err != nil {
		return err
	}
	var options []hostprep.Option
	if host.Shared() {
		options = append(options, hostprep.Shared())
	}
	plan, err := ui.Get(r, "read the host", func() (*hostprep.Plan, error) {
		return hostprep.Build(o.site, cfg, transport, options...)
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
			showReview(r, "review what paisans.yaml grants", review.review, false)
			pending = review.review.Pending()
		}
		refusal := hostprep.Refusal(plan)
		env.found(strings.Join(plan.Kinds(), ", "), refusal)
		switch {
		case refusal != nil:
			r.Result("Nothing changed. --execute would stop at the refusal above.")
		case len(plan.Steps) == 0 && pending == 0:
			r.Result("%s is prepared. Nothing to do.", o.site)
		case len(plan.Steps) == 0:
			r.Result("%s is prepared. Nothing changed; --execute asks you to approve the review above and records it.", o.site)
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
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	reporter := commonFlags(fs)
	for _, add := range extra {
		add(fs)
	}
	configPath := fs.String("config", "paisans.yaml", "path to the deployment declaration")
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
	return r, cfg, wants, provider, *execute, nil
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
// name is the site's name in paisans.yaml. The transport carries it so that
// a sudo password prompt names the site it is for; it plays no part in
// reaching the host.
func siteTransport(name string, site config.Site, override string, sudo bool) apply.SSHTransport {
	var t apply.SSHTransport
	if override != "" {
		t = apply.SSHTransport{Site: name, Destination: override, Sudo: sudo}
	} else {
		// validate has already refused a bad key, so problems are empty here.
		keys, _ := site.SSH.AuthorizedKeys()
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k.Line
		}
		t = apply.SSHTransport{Site: name, User: site.SSH.User, Host: site.SSHHost(), Port: site.SSH.PortOrDefault(), PublicKeys: lines, Sudo: sudo}
	}
	if sudo {
		t.Auth = sudoAuthFor(t.Describe())
	}
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
