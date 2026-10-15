package main

import (
	"flag"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/config"
	"io"
	"os"
	"slices"
	"strings"
)

// Help comes in three levels, each a step further into one command, so that
// the first thing an operator reads fits on a screen:
//
//	paisans help                    every command, one line each
//	paisans help site               site's subcommands, one line each
//	paisans help site add           its usage, what it does, and its flags
//
// `paisans site help`, `paisans site --help` and `paisans site add --help`
// reach the same pages. The text lives in helpCommands below, one entry per
// command that runs; a group (site, host, secrets) is the entries whose name
// starts with it.

// helpCommand is one runnable command's help: its name as typed after paisans,
// the line that lists it, its usage lines and what it does.
type helpCommand struct {
	name    string
	summary string
	usage   []string
	detail  string
}

// helpSection is a heading on the first page and the commands listed under it,
// by the first word of their names.
type helpSection struct {
	title string
	names []string
}

// groupSummary is the line the first page gives a command with
// subcommands. A command without any is listed by its own summary.
var groupSummary = map[string]string{
	"host":        "Prepare a host, or list what is on it.",
	"site":        "Add a site to the running deployment, or remove one.",
	"mesh":        "Replace sites' WireGuard keys.",
	"gateway":     "Move the gateway role to another site.",
	"storage":     "Provision object storage, join sites to it, replace keys.",
	"failover":    "Switch the database primary to another site and back.",
	"dns":         "Create the DNS records the configuration implies, or prune them.",
	"config":      "Publish, pull, list and recover the sites' signed configuration.",
	"operator":    "Sign the sites' configuration, or give an operator a sign-in link.",
	"secrets":     "List, set, prune and rotate secrets.",
	"app":         "Take a removed app off hosts.",
	"oidc":        "Create an app's sign-in client at Pocket ID.",
	"oidc client": "Create an app's sign-in client at Pocket ID.",
	"web-server":  "Show or check the web server in front of a monitor's app.",
	"catalog":     "Add, move, remove and list the catalogs of blueprints.",
	"blueprint":   "List, show and lint blueprints.",
}

var helpSections = []helpSection{
	{"Most of the time", []string{"apply"}},
	{"Start a deployment", []string{"create", "validate", "init", "render"}},
	{"Hosts and sites", []string{"host", "site", "mesh", "gateway", "storage", "prune"}},
	{"Check", []string{"doctor", "images", "preflight", "failover", "web-server", "version"}},
	{"Configuration and secrets", []string{"config", "operator", "secrets", "dns"}},
	{"Apps", []string{"app", "oidc", "catalog", "blueprint"}},
}

var helpCommands = []helpCommand{
	{
		name:    "catalog add",
		summary: "Pin a catalog of blueprints at its newest release.",
		usage: []string{
			"paisans catalog add <owner>/<name> [--tag <tag>] [--config " + config.FileName + "]",
			"paisans catalog add [<name>] <url> [--tag <tag>] [--config " + config.FileName + "]",
		},
		detail: "A catalog's repository is named paisans-<name>. <owner>/<name> alone is https://github.com/<owner>/paisans-<name>, and a URL alone names the catalog <owner>/<name>, the prefix left out; give a name before the URL for another.\n\n" +
			"Read the catalog at <url>, pin its newest release, the highest vMAJOR.MINOR.PATCH tag with pre-releases skipped (or --tag), and the commit it names in " + config.FileName + " as catalogs.<name>, and list its blueprints, which apps then name as <name>/<blueprint>. Edits only " + config.FileName + ".",
	},
	{
		name:    "catalog update",
		summary: "Move catalogs' pins to their newest releases.",
		usage:   []string{"paisans catalog update [<name>] [--tag <tag>] [--config " + config.FileName + "]"},
		detail: "For each declared catalog, or the one named: read its newest tag (or --tag), and when it names another commit, say what it changes for each app using one of its blueprints (images, port, sign-in, the gate's openings, storage, secrets) and move the pin. " +
			"Refuses a move that would leave an app without its blueprint. Edits only " + config.FileName + ".",
	},
	{
		name:    "catalog remove",
		summary: "Remove a catalog no app uses.",
		usage:   []string{"paisans catalog remove <name> [--config " + config.FileName + "]"},
		detail:  "Remove catalogs.<name> from " + config.FileName + ". Refused while an app names one of its blueprints.",
	},
	{
		name:    "catalog list",
		summary: "List the catalogs and their pins.",
		usage:   []string{"paisans catalog list [--config " + config.FileName + "]"},
		detail:  "List the core catalog, with the release it reads, its newest one this toolkit can read or the one pinned here, and every declared catalog with its URL, tag, commit and whether its tags are checked.",
	},
	{
		name:    "blueprint list",
		summary: "List every blueprint an app can name.",
		usage:   []string{"paisans blueprint list [--config " + config.FileName + "]"},
		detail:  "List the reserved and core blueprints built into the toolkit, and every blueprint in each declared catalog, as an app names it.",
	},
	{
		name:    "blueprint show",
		summary: "Print a blueprint's blueprint.yaml.",
		usage:   []string{"paisans blueprint show <name|catalog/name> [--config " + config.FileName + "]"},
		detail:  "Print the blueprint.yaml of a built in blueprint, or of one in a declared catalog at its pinned commit.",
	},
	{
		name:    "blueprint lint",
		summary: "Check blueprint directories as a catalog author writes them.",
		usage:   []string{"paisans blueprint lint <dir>..."},
		detail:  "Check each directory: its blueprint.yaml is one this toolkit reads, every other file is a template, and compose.yaml.tmpl and caddy.snippet.tmpl are there. Needs no " + config.FileName + ".",
	},
	{
		name:    "validate",
		summary: "Report every problem in " + config.FileName + ".",
		usage:   []string{"paisans validate [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--offline]"},
		detail: "Load the configuration and report every problem found.\n\n" +
			"Then try every app's mail settings: each logs in to its mail server from a site running it, over ssh, and nothing is sent. A failure is a warning. " +
			"A site that cannot be reached, or a secrets file that cannot be read, skips the check with a warning; --offline checks the file alone and reaches no site.",
	},
	{
		name:    "create",
		summary: "Write a new " + config.FileName + " by answering questions.",
		usage:   []string{"paisans create [--config " + config.FileName + "]"},
		detail: "For a new deployment: ask, at a terminal, about the community, its operators, its machines, its apps and its mail, and write a commented " + config.FileName + " that validate refuses nothing in, once you have read it and said yes. Refuses a file that exists. " +
			"Asks for no secret and reaches no host.",
	},
	{
		name:    "init",
		summary: "Give the configuration an id, a mesh subnet and its secrets.",
		usage:   []string{"paisans init [--config " + config.FileName + "] [--secrets secrets.enc.yaml]"},
		detail: "Give the configuration an id if it has none; until the deployment is on any site, reach every site and keep mesh.subnet only if it overlaps nothing there, else roll a random /24 that overlaps nothing and move each site's address into it; " +
			"then generate the secrets it needs, filling in only what is missing, and say what is still owed from elsewhere.",
	},
	{
		name:    "render",
		summary: "Write each site's artifacts to a local directory.",
		usage:   []string{"paisans render [--config " + config.FileName + "] [--secrets secrets.enc.yaml] --out ./out"},
		detail:  "Validate, then write per site artifacts to a local directory.",
	},
	{
		name:    "apply",
		summary: "Take every site from " + config.FileName + " to a running stack.",
		usage: []string{
			"paisans apply [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"              [--min-free <size>] [--keep-images] [--execute]",
			"paisans apply --site <name> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"              [--ssh <destination>] [--only <stack>]... [--overwrite <path>]...\n" +
				"              [--recreate <stack>]... [--min-free <size>] [--keep-images] [--execute]",
		},
		detail: "Without --site: take every site from " + config.FileName + " to a running stack, running init, host prepare, apply, site add, storage and dns init in the order the deployment needs. " +
			"With --site: compare that site's rendered artifacts with what is on its host and show what would change.\n\n" +
			"Changes nothing on a host without --execute; without --site, the dry run may add missing generated secrets to the local secrets file, and says so when it does.\n\n" +
			"Both, and host prepare, review the keys and images " + config.FileName + " grants against what each host last applied; --execute asks for the word approve at a terminal when anything is added or changed, and asks nothing when nothing is.\n\n" +
			"Before anything changes, every app it starts logs in to its mail server from its site, sending nothing; one that does not log in refuses the apply.",
	},
	{
		name:    "host prepare",
		summary: "Take a blank host to the state apply assumes.",
		usage:   []string{"paisans host prepare --site <name> [--config " + config.FileName + "] [--ssh <destination>]\n                     [--execute]", "paisans host prepare --all [--config " + config.FileName + "] [--execute]"},
		detail: "Take a blank host to the state apply assumes: Docker, the WireGuard tools, a firewall, and a watchdog on a data site. " +
			"Installs only what is missing. Writes nothing without --execute. " +
			"--all prepares every site, one at a time in the configuration's order, each with its own review, and stops at the first that fails.",
	},
	{
		name:    "host deployments",
		summary: "List every deployment on a host.",
		usage:   []string{"paisans host deployments --ssh <user@host[:port]> [--sudo=false]"},
		detail: "List every deployment in the registry of the host --ssh names (id, token, domain, site, roles and root, and a Caddy kept for /srv/caddy.d), and any deployment directory or labelled compose project no registry entry names. " +
			"Needs no " + config.FileName + "; changes nothing.",
	},
	{
		name:    "site add",
		summary: "Join a new data site to the running cluster.",
		usage:   []string{"paisans site add <site> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                 [--execute]"},
		detail: "Join a new data site to the running cluster in eight gated stages: preflight, mesh, etcd (learners, then promoted), the Patroni replica, synchronous mode, HAProxy, each existing replica's patroni.env, one replica restart at a time, and each monitor site's uptime stack, so it checks the new site.\n\n" +
			"Reads every site and plans only what differs, so a re-run resumes. Writes nothing without --execute.",
	},
	{
		name:    "site remove",
		summary: "Take a site out of the running deployment.",
		usage: []string{
			"paisans site remove <site> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                    [--host-gone] [--delete-data] [--force --ssh <user@host[:port]>] [--execute]",
			"paisans site remove <site> --evict [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                    [--execute]",
			"paisans site remove --force --ssh <user@host[:port]> --id <id or token>\n" +
				"                    [--delete-data] [--execute]",
		},
		detail: "Take a site out of the running deployment in gated stages: its data out (the Patroni leader switched to the Sync Standby, its Garage node out of the layout and the objects moved), out of the cluster (its etcd member, and the mesh, HAProxy and gateway routes on every remaining site, and each replica's patroni.env, one at a time; one data site out of two and a witness takes the witness out of etcd too), its host cleaned of everything provably this deployment's (a gateway's Caddy kept, reduced to the host owner's sites in /srv/caddy.d, while there are any), the A and AAAA records dns init made for its public address deleted at the DNS provider, and its entry out of " + config.FileName + ".\n\n" +
			"Refuses the only gateway, data or apps site, a site an app is pinned to, and an unhealthy cluster. --host-gone skips the host; --delete-data deletes its data after the site's name is typed at a terminal. Writes nothing without --execute.\n\n" +
			"--force --ssh <host> --id <id or token>, with no " + config.FileName + ": clean that host of the deployment its registry names, as --force does, from the registry entry alone. DNS is not modified. --execute asks for the site's name at a terminal.\n\n" +
			"--evict: take out a site whose host still runs and that nobody else can reach, never reaching that host: the mesh cut first on every remaining site, then its DNS records, Patroni's own failover, its etcd member, its Garage node skipped as dead, then the rest as remove does. " +
			"Generates the secrets of a replacement declared in the same edit, which apply then brings up. --execute asks for the site's name, and for revoked, lose writes or lose objects when they apply.",
	},
	{
		name:    "mesh rekey",
		summary: "Replace sites' WireGuard keys, one site at a time.",
		usage:   []string{"paisans mesh rekey <site>... | --all [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                   [--execute]"},
		detail: "Replace sites' WireGuard keys, one site at a time: a next key in the secrets, every peer's mesh file and then the site's own synced with it, a handshake under it from every peer, etcd and Patroni healthy, then the key promoted. " +
			"A stopped run resumes. Writes nothing without --execute.",
	},
	{
		name:    "gateway move",
		summary: "Move the gateway role to the site " + config.FileName + " gives it.",
		usage:   []string{"paisans gateway move [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--execute]"},
		detail: "Move the gateway role to the site " + config.FileName + " gives it, from the site that still serves as the gateway, in gated stages: carry the deployment record, check the new gateway answers for every hostname through its own address, add its address records beside the old one's, delete the old one's (only after that check, and only records dns init made for it), wait out their TTL, then stop the old gateway's Caddy.\n\n" +
			"Resumes after a fix. Writes nothing without --execute.",
	},
	{
		name:    "storage init",
		summary: "Provision object storage on a site.",
		usage:   []string{"paisans storage init --site <name> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                     [--ssh <destination>] [--execute]"},
		detail: "Provision object storage on a site: each app's key and bucket, and the layout when it is the only Garage site. " +
			"Creates only what is missing, and writes nothing without --execute.",
	},
	{
		name:    "storage add",
		summary: "Join every Garage site into one cluster.",
		usage:   []string{"paisans storage add [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                    [--change-replication] [--wait <duration>] [--stop-test] [--execute]"},
		detail: "Join every site in storage.garage.sites into one Garage cluster, in gated stages: connect, layout, sync, provision, media routes, a smoke test; and, with --change-replication, Garage's reset for another replication factor.\n\n" +
			"Exits at a stage waiting on Garage (status 75) and resumes on the next run. Creates only what is missing, and writes nothing without --execute.",
	},
	{
		name:    "storage rotate-key",
		summary: "Replace one app's S3 key.",
		usage:   []string{"paisans storage rotate-key --app <name> [--config " + config.FileName + "]\n                           [--secrets secrets.enc.yaml] [--execute]"},
		detail: "Replace one app's S3 key, in gated stages: the new pair into the secrets (the old one kept beside it), the new key imported and granted, the app applied alone on every site running it, a probe written, read and deleted with the new key, and only then the old key deleted from Garage and the secrets.\n\n" +
			"Resumes from the secrets file. Writes nothing without --execute.",
	},
	{
		name:    "prune",
		summary: "List and remove one site's dangling Docker volumes.",
		usage:   []string{"paisans prune --site <name> [--config " + config.FileName + "] [--ssh <destination>] [--execute]"},
		detail: "List one site's dangling Docker volumes, with size and top level entries, and say which carry this deployment's label. " +
			"Removes those with --execute; every other volume is kept.",
	},
	{
		name:    "preflight",
		summary: "Run the read only checks site add runs first.",
		usage:   []string{"paisans preflight --site <new site> [--config " + config.FileName + "]"},
		detail:  "The read only checks site add runs first, for the site being added and every site already running. Changes nothing.",
	},
	{
		name:    "failover test",
		summary: "Switch the Patroni primary to another data site and back.",
		usage:   []string{"paisans failover test [--config " + config.FileName + "] [--execute]"},
		detail: "Switch the Patroni primary to another data site and back, checking the cluster and every app before and after each switch. " +
			"Interrupts writes briefly, twice. Changes nothing without --execute.",
	},
	{
		name:    "doctor",
		summary: "Report what is stuck on every site, and how to recover.",
		usage:   []string{"paisans doctor [--config " + config.FileName + "] [--site <name>]... [--execute]"},
		detail: "Reach every site, or the ones --site names, and report what is stuck: unreachable sites, etcd quorum and version, the Patroni leader and why a replica will not promote, containers that are down, Pocket ID's active instance, and clocks. " +
			"Prints how to recover. Reaches hosts and changes none; exits 1 on any FAIL. With --execute it removes the config: keys that only repeat what gating renders, and records the ssh.user of a site it logs in to as the deployment's own user, both in " + config.FileName + ".",
	},
	{
		name:    "images",
		summary: "List every image each site runs and what the next apply runs.",
		usage:   []string{"paisans images [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n               [--check]"},
		detail: "List, for every site, each image its stacks run: where its reference comes from (the core catalog's foundation, the blueprint, or the configuration key that declares it), the build the site runs, and the build the next apply would run. " +
			"An override names what it replaced, and says when that is a newer release. " +
			"Reads every site over ssh and the registries; changes nothing. A site it cannot reach is listed as unknown and the command exits 1. --check exits 3 when a build would move or an override lags.",
	},
	{
		name:    "version",
		summary: "Print this binary's version.",
		usage:   []string{"paisans version", "paisans --version"},
		detail: "Print the toolkit version the binary was built from and, in parentheses, its commit, with -dirty when the tree had uncommitted changes: paisans 0.1.0 (519299d5cb00). A build with no commit recorded prints the version alone. " +
			"Blueprints' requires_toolkit is checked against the version.",
	},
	{
		name:    "dns init",
		summary: "Create the public DNS records the configuration implies.",
		usage:   []string{"paisans dns init [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--execute]"},
		detail: "Create the public DNS records the configuration implies, at the provider named by acme.provider: addresses, and records saying every name without mail (community.mail_domains, smtp senders) sends and receives none.\n\n" +
			"Creates only what is missing, never updates or deletes, and refuses if any record conflicts. Writes nothing without --execute.",
	},
	{
		name:    "dns prune",
		summary: "Delete the records dns init made that are no longer wanted.",
		usage:   []string{"paisans dns prune [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--name <fqdn>]... [--execute]"},
		detail: "Delete the records dns init created that the configuration no longer implies: only one carrying init's comment, at a name under community.domain or one the configuration names, and no longer wanted; an A or AAAA pointing at a site's public address, or a TXT or MX holding exactly a no-mail content init writes.\n\n" +
			"Lists every other record init marked, with why it is kept, and lists the zone again to confirm each delete. Writes nothing without --execute.",
	},
	{
		name:    "config publish",
		summary: "Sign the local files and push them to every site.",
		usage:   []string{"paisans config publish [--config " + config.FileName + "] [--merge] [--execute]"},
		detail: "Sign the local " + config.FileName + ", secrets.enc.yaml and .sops.yaml as a version on the sites' newest and push it to every site; --merge builds on every head of a fork." +
			configTrust,
	},
	{
		name:    "config pull",
		summary: "Move the local files to the sites' newest version.",
		usage:   []string{"paisans config pull [--config " + config.FileName + "] [--root <sha>] [--execute]"},
		detail: "Move the local files to the sites' newest version, or, over local edits, show the difference and put its files in .paisans/incoming/." +
			configTrust,
	},
	{
		name:    "config log",
		summary: "List every version, its signer, and the sites holding it.",
		usage:   []string{"paisans config log [--config " + config.FileName + "] [--root <sha>]"},
		detail:  "List every version of the configuration the sites keep, its signer, and the sites holding it." + configTrust,
	},
	{
		name:    "config recover",
		summary: "Write the newest configuration onto an empty directory.",
		usage:   []string{"paisans config recover --from <user@host[:port]>... [--root <sha>] [--id <id>]\n                       [--config " + config.FileName + "] [--execute]"},
		detail: "Onto an empty directory, gather from the --from hosts and every site the trusted heads name, and write the head's files, or each head's in .paisans/incoming/ with a fork." +
			configTrust,
	},
	{
		name:    "operator magic-link",
		summary: "Give a declared operator a one-time sign-in link.",
		usage:   []string{"paisans operator magic-link <operator> [--app <pocket-id app>]\n                       [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                       [--site <name>] [--ssh <destination>] [--execute]"},
		detail: "Issue an operator under operators a one-time link that signs them in to Pocket ID once, to register a passkey with: after a first link expired unused, or a passkey was lost. " +
			"Printed once on stdout, and emailed by Pocket ID as well when it emails apply's links. " +
			"Changes nothing else: apply makes the account and its groups, and an operator with no account yet is pointed there. Writes nothing without --execute.",
	},
	{
		name:    "operator register",
		summary: "Sign the sites' newest version with your own key.",
		usage:   []string{"paisans operator register [--config " + config.FileName + "] [--key <path.pub>] [--root <sha>] [--execute]"},
		detail: "Sign an attestation of the sites' newest version with your own key, so trust starts from your signature: the key in your ssh-agent listed under your entry in operators, or the one --key names. " +
			"Signs only a version equal to your local files, or, with no files and a root pinned, the one the pin leads to, and then writes its files. " +
			"Records the key in .paisans/signing-key, which config publish signs with from then on.\n\n" +
			"Changes only the sites' configuration stores, and nothing without --execute. With --execute it asks for the word partial at a terminal when no more than half the sites answered directly, and for rollback when the newest version is older than one seen here before.",
	},
	{
		name:    "secrets list",
		summary: "List every secret's path, whether it is set, and how rotate takes it.",
		usage:   []string{"paisans secrets list [--config " + config.FileName + "] [--secrets secrets.enc.yaml]"},
		detail: "List every secret " + config.FileName + " needs, and every other one the secrets file holds for something it still declares, by its path in the secrets file: set or unset; generated (by init), pasted (with secrets set) or captured (by apply); and the path secrets rotate takes for it, a unit's for a secret rotated with its unit, kept for one it keeps. " +
			"Prints names alone, never a value or any part of one.\n\n" +
			"The table is on stdout, the report on stderr. Reads only the local files.",
	},
	{
		name:    "secrets set",
		summary: "Write one value from stdin into the secrets file, or ask for each owed one.",
		usage: []string{
			"paisans secrets set <dotted.key> [--config " + config.FileName + "] [--secrets secrets.enc.yaml] < value",
			"paisans secrets set [--config " + config.FileName + "] [--secrets secrets.enc.yaml]",
		},
		detail: "Read one value from stdin and write it into the encrypted secrets file, printing only its name. " +
			"For credentials issued elsewhere, so they never touch an echoing terminal or an editor.\n\n" +
			"With a key, at a terminal, ask for its value with echo off, saying whether it is already set; an empty answer writes nothing.\n\n" +
			"With no key, at a terminal, ask for each secret still owed in turn, saying why it is owed, and read each with echo off. " +
			"Each answer is written before the next is asked; an empty one skips the key, which stays owed. A sign-in client is not asked for, since apply creates it.\n\n" +
			"An SMTP password is tried before it is written: from a site running an app that uses it, over ssh, it logs in to the mail server and sends one test message to every operator. One that fails is not written.",
	},
	{
		name:    "secrets prune",
		summary: "Remove the secrets of what " + config.FileName + " no longer declares.",
		usage:   []string{"paisans secrets prune [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--execute]"},
		detail: "Remove the secrets that name something " + config.FileName + " no longer declares: a removed site's keys, a removed app's passwords and sign-in client, a Pocket ID group nothing names. Lists them by key, never by value.\n\n" +
			"Only reads the gateways' deployment records; its --execute writes the local secrets file, and records the ssh.user of a gateway it logs in to as the deployment's own user.",
	},
	{
		name:    "secrets rotate",
		summary: "Replace secrets, one at a time, in gated stages.",
		usage:   []string{"paisans secrets rotate <path>... | --all [--restart] [--replaced <path>]...\n                       [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                       [--execute]"},
		detail: "Replace secrets, named by their paths in the secrets file (apps.<app>.<unit> for a unit of secrets an app's blueprint groups), or every one in scope with --all, one at a time in gated stages: a next value in the secrets file, the side that checks it, each site that uses it healthy before the next, then the value promoted.\n\n" +
			"A pasted secret (the DNS token or an SMTP password) whose value every host using it runs, read line by line, is asked for at a terminal before any host is claimed: replace it at its issuer when asked and paste the new one. One already set anew with secrets set is handed on from the file. A DNS token is tried against the zone before any host gets it; an SMTP password logs in to the mail server from a site, and a pasted one sends the operators one test message. --replaced <path> lists one already replaced and applied, which every host runs, instead. Lists the secrets it keeps and why.\n\n" +
			"--all refuses a file that lacks a secret init generates. A stopped --all resumes with what it left, and says so; --all --restart starts the full list again.\n\n" +
			"Refuses while a mesh rekey is unfinished, a site does not answer, or etcd or Patroni is unhealthy; apply and every other command that renders refuse while a rotation is unfinished. " +
			"A run that signs members out asks for sign out at a terminal, before any host is claimed. A stopped run resumes. Writes nothing without --execute.",
	},
	{
		name:    "app remove",
		summary: "Take an app that has left " + config.FileName + " off every host.",
		usage:   []string{"paisans app remove <app> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                   [--delete-data] [--execute]"},
		detail: "Take an app that has left " + config.FileName + " off every host, once a whole apply on each site has marked its files left over: its containers and networks, its rendered files (one edited on the host is kept), its stack directory when nothing else is in it, and its Pocket ID client when the secrets file records that client's ID.\n\n" +
			"--delete-data also deletes its database and role, its Garage bucket and keys, its named volumes and its data directories, after the app's name is typed at a terminal. " +
			"Leaves its secrets and DNS records, and says so. Writes nothing without --execute.",
	},
	{
		name:    "oidc client create",
		summary: "Create an app's client at the deployment's Pocket ID.",
		usage:   []string{"paisans oidc client create --app <name> [--rotate-secret]\n                           [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                           [--site <name>] [--ssh <destination>] [--execute]"},
		detail: "Create an app's client at the deployment's Pocket ID, with the groups the app reads, and record its ID and secret in the secrets file. The secret is never printed. " +
			"Writes nothing without --execute. Supports every blueprint that signs in through Pocket ID.",
	},
	{
		name:    "web-server show",
		summary: "Print what the web server in front of a monitor's app must do.",
		usage:   []string{"paisans web-server show --app <name> [--config " + config.FileName + "]"},
		detail: "For an app pinned to a monitor site, print what the web server in front of it must do (terminate TLS for its hostname, pass Host, set X-Forwarded-For and X-Forwarded-Proto), filled in for the web server it finds on the monitor over ssh, read only, or for Caddy, nginx and Apache when it finds none.\n\n" +
			"Changes nothing. With managed_by: external it reads the monitor's web server over ssh.",
	},
	{
		name:    "web-server check",
		summary: "Check, from here, that a monitor's app is served correctly.",
		usage:   []string{"paisans web-server check --app <name> [--config " + config.FileName + "]"},
		detail: "From this machine, check that the hostname resolves to the monitor, that /healthz answers with a valid certificate, that sign in redirects with an https callback, and that the published port is closed from outside. " +
			"Reads only; exits 1 on any FAIL. With the toolkit's own Caddy in front (managed_by: paisans), only the first two apply.\n\n" +
			"It looks at the monitor's public hostname as any visitor could; with managed_by: external it also reads the monitor's web server over ssh when a check fails.",
	},
}

// configTrust is what every config subcommand shares, after its own
// sentence.
const configTrust = "\n\n" +
	"The signed copies of " + config.FileName + ", secrets.enc.yaml and .sops.yaml every site keeps. Each config command gathers every site's versions first and trusts them from your own signing key or a root pinned with --root.\n\n" +
	"With --execute, pull, recover, a publish of the first version, apply and operator register ask for the word partial at a terminal when no more than half the sites answered directly; publish, pull, recover, apply and operator register ask for rollback when the newest version is older than one seen here before, and log only warns. " +
	"With --execute each site first syncs with its peers; without it, and in log, no site is changed and only .paisans/ here is written."

// executeTopic is `paisans help execute`: which commands reach a host, and
// what --execute lets each of them change.
const executeTopic = `host prepare, host deployments, apply, prune, site add, site remove, mesh rekey,
storage init, storage add, storage rotate-key, app admin create, app remove,
oidc client create, secrets prune, secrets rotate, preflight, failover test,
doctor, init, config and operator register are the only commands that reach a
host. Each reads it to plan, and changes it only with --execute; config and
operator register claim nothing, and their --execute only starts each site's
configuration sync and, for publish and register, adds a version to its store;
host deployments, preflight and init have no --execute and never change it;
doctor's --execute and secrets prune's change no host either, editing only the
local files, and secrets prune only reads the gateways' deployment records. With --execute, a command first claims each
host it writes to in /var/lib/paisans/registry.json, and refuses if another
deployment there holds this one's token, WireGuard interface or listen port, or
a mesh subnet overlapping this one's. apply and host prepare also refuse when
anything else on the host overlaps the mesh subnet. dns init and dns prune reach
no host, only the DNS provider's API, and change it only with --execute; site
remove also deletes the removed site's records there. web-server show and
web-server check change nothing: check looks at a monitor's public hostname as
any visitor could, and with managed_by: external both read the monitor's web
server over ssh, show always and check when a check fails. Everything else
writes files locally and stops.

Every command takes -v or --verbose. By default each step is one line; with it,
the reasons, values, request bodies and command output behind each step show
too. A failure always says what went wrong and what to do; with -v, the
underlying error shows under it too.
`

// helpWidth is the column help text wraps at.
const helpWidth = 80

// findCommand returns the command named name, or nil.
func findCommand(name string) *helpCommand {
	for i := range helpCommands {
		if helpCommands[i].name == name {
			return &helpCommands[i]
		}
	}
	return nil
}

// subcommands returns the commands under group, in the order commands lists
// them, or nil when group has none.
func subcommands(group string) []helpCommand {
	var subs []helpCommand
	for _, c := range helpCommands {
		if strings.HasPrefix(c.name, group+" ") {
			subs = append(subs, c)
		}
	}
	return subs
}

// isGroup reports whether name is a command that takes a subcommand.
func isGroup(name string) bool { return len(subcommands(name)) > 0 }

// printTopHelp writes the first page: every command, one line each.
func printTopHelp(w io.Writer) {
	fmt.Fprint(w, "paisans installs and runs a community stack from "+config.FileName+".\n\n")
	fmt.Fprint(w, "Usage: paisans <command> [flags]\n")
	for _, s := range helpSections {
		fmt.Fprintf(w, "\n%s:\n", s.title)
		for _, name := range s.names {
			summary := groupSummary[name]
			if c := findCommand(name); c != nil {
				summary = c.summary
			}
			fmt.Fprintf(w, "  %-12s%s\n", name, summary)
		}
	}
	fmt.Fprint(w, "\nA command that changes a host changes it only with --execute; without it, it\n"+
		"shows what it would do. Every command takes -v or --verbose.\n\n"+
		"Run 'paisans help <command>' for a command's details, and 'paisans help\n"+
		"execute' for which commands reach a host and what --execute changes.\n")
}

// printGroupHelp writes a group's page: its subcommands, one line each.
func printGroupHelp(w io.Writer, group string) {
	subs := subcommands(group)
	fmt.Fprintf(w, "%s\n\nUsage: paisans %s <command> [flags]\n\nCommands:\n", groupSummary[group], group)
	width := 0
	for _, c := range subs {
		width = max(width, len(strings.TrimPrefix(c.name, group+" ")))
	}
	for _, c := range subs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, strings.TrimPrefix(c.name, group+" "), c.summary)
	}
	fmt.Fprintf(w, "\nRun 'paisans %s <command> --help' for a command's usage, details and flags.\n", group)
}

// printCommandHelp writes one command's page: its usage, what it does, and,
// given its flag set, its flags.
func printCommandHelp(w io.Writer, c *helpCommand, fs *flag.FlagSet) {
	fmt.Fprint(w, "Usage:\n")
	for _, u := range c.usage {
		fmt.Fprintf(w, "  %s\n", u)
	}
	for _, p := range strings.Split(c.detail, "\n\n") {
		fmt.Fprintf(w, "\n%s\n", wrap(p, helpWidth))
	}
	if fs == nil {
		return
	}
	fmt.Fprint(w, "\nFlags:\n")
	out := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(out)
}

// wrap breaks text into lines of at most width columns, at spaces.
func wrap(text string, width int) string {
	var b strings.Builder
	line := 0
	for _, word := range strings.Fields(text) {
		switch {
		case line == 0:
		case line+1+len(word) > width:
			b.WriteByte('\n')
			line = 0
		default:
			b.WriteByte(' ')
			line++
		}
		b.WriteString(word)
		line += len(word)
	}
	return b.String()
}

// helpAsked is whether the command line asked for help, so that a flag set's
// usage prints the command's whole page rather than a pointer to it.
var helpAsked bool

// isHelpWord reports whether arg asks for help.
func isHelpWord(arg string) bool {
	return slices.Contains([]string{"help", "-h", "-help", "--help"}, arg)
}

// newFlagSet is flag.NewFlagSet for a command named in commands. Asked for
// help, its usage is the command's page on stdout; given a flag it does not
// know, a line saying where that page is, after the flag package's own error.
func newFlagSet(name string, handling flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet(name, handling)
	fs.Usage = func() {
		if c := findCommand(name); helpAsked && c != nil {
			printCommandHelp(os.Stdout, c, fs)
			return
		}
		fmt.Fprintf(fs.Output(), "Run 'paisans %s --help' for its usage and flags.\n", name)
	}
	return fs
}

// resolveHelp reads a command line, without the program's name, for a
// request for help. It returns the page to print in place of running
// anything, and whether it was asked for: a bare paisans or a group with no
// subcommand gets its page too, as the usage error it is. Otherwise it
// returns the command line to run. When that asks for help, it is cut to the
// command's name and --help, so the command's own flag set prints its page
// and nothing else on the line can make the command run: `paisans help site
// add`, `paisans site add help` and `paisans site add x --help` all become
// `site add --help`.
func resolveHelp(args []string) (page func(io.Writer), asked bool, run []string) {
	if len(args) == 0 {
		return printTopHelp, false, nil
	}
	if isHelpWord(args[0]) {
		switch topic := args[1:]; {
		case len(topic) == 0:
			return printTopHelp, true, nil
		case len(topic) == 1 && topic[0] == "execute":
			return func(w io.Writer) { fmt.Fprint(w, executeTopic) }, true, nil
		default:
			args = append(slices.Clone(topic), "--help")
		}
	}
	// The command's name is as many words as name a group, and then one:
	// app, app remove.
	name := args[0]
	for at := 1; isGroup(name); at++ {
		group := name
		if len(args) == at {
			return func(w io.Writer) { printGroupHelp(w, group) }, false, nil
		}
		if isHelpWord(args[at]) {
			return func(w io.Writer) { printGroupHelp(w, group) }, true, nil
		}
		name += " " + args[at]
	}
	if findCommand(name) == nil {
		return nil, false, args
	}
	at := len(strings.Fields(name))
	for i, a := range args[at:] {
		if a == "--" {
			break
		}
		if isHelpWord(a) && (a != "help" || i == 0) {
			helpAsked = true
			return nil, true, append(slices.Clone(args[:at]), "--help")
		}
	}
	return nil, false, args
}
