package main

import (
	"flag"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
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
	"site":        "List, admit, prepare, add, remove, evict and log in to sites.",
	"mesh":        "Replace sites' WireGuard keys.",
	"gateway":     "Move the gateway role to another site.",
	"storage":     "Provision object storage, join sites to it, replace keys.",
	"failover":    "Switch the database primary to another site and back.",
	"dns":         "Create the DNS records the configuration implies, or prune them.",
	"deployment":  "Apply, list, check out, fetch, publish, pull and recover.",
	"operator":    "Add an operator, sign the configuration, or give a sign-in link.",
	"secrets":     "List, set, prune and rotate secrets.",
	"app":         "Take a removed app off hosts.",
	"oidc":        "Create an app's sign-in client at Pocket ID.",
	"oidc client": "Create an app's sign-in client at Pocket ID.",
	"web-server":  "Show or check the web server in front of a monitor's app.",
	"catalog":     "Add, move, remove and list the catalogs of blueprints.",
	"blueprint":   "List, show and lint blueprints.",
}

var helpSections = []helpSection{
	{"Most of the time", []string{"deployment"}},
	{"Start a deployment", []string{"create", "validate", "init", "render"}},
	{"Sites", []string{"site", "mesh", "gateway", "storage", "prune"}},
	{"Check", []string{"doctor", "images", "preflight", "failover", "web-server", "version"}},
	{"Configuration and secrets", []string{"operator", "secrets", "dns"}},
	{"Apps", []string{"app", "oidc", "catalog", "blueprint"}},
}

var helpCommands = []helpCommand{
	{
		name:    "catalog add",
		summary: "Pin a catalog of blueprints at its newest release.",
		usage: []string{
			"paisans catalog add <owner>/<name> [--tag <tag>] [--config " + config.FileName + "] [--id <id>]",
			"paisans catalog add [<name>] <url> [--tag <tag>] [--config " + config.FileName + "] [--id <id>]",
		},
		detail: "A catalog's repository is named paisans-<name>. <owner>/<name> alone is https://github.com/<owner>/paisans-<name>, and a URL alone names the catalog <owner>/<name>, the prefix left out; give a name before the URL for another.\n\n" +
			"Read the catalog at <url>, pin its newest release, the highest vMAJOR.MINOR.PATCH tag with pre-releases skipped (or --tag), and the commit it names in " + config.FileName + " as catalogs.<name>, and list its blueprints, which apps then name as <name>/<blueprint>. Edits only " + config.FileName + ". " + draftTopic,
	},
	{
		name:    "catalog update",
		summary: "Move catalogs' pins to their newest releases.",
		usage:   []string{"paisans catalog update [<name>] [--tag <tag>] [--config " + config.FileName + "] [--id <id>]"},
		detail: "With no catalog named, first name the core catalog release every command uses and each newer one skipped. For each declared catalog, or the one named: read its newest tag (or --tag), and when it names another commit, say what it changes for each app using one of its blueprints (images, port, sign-in, the gate's openings, storage, secrets) and move the pin. " +
			"Refuses a move that would leave an app without its blueprint. Edits only " + config.FileName + ". " + draftTopic,
	},
	{
		name:    "catalog remove",
		summary: "Remove a catalog no app uses.",
		usage:   []string{"paisans catalog remove <name> [--config " + config.FileName + "] [--id <id>]"},
		detail:  "Remove catalogs.<name> from " + config.FileName + ". Refused while an app names one of its blueprints. " + draftTopic,
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
		detail: "For a new deployment: ask, at a terminal, about the community, its operators, its machines, its apps and its mail, and write a commented " + config.FileName + " once validate refuses nothing in it. Refuses a file that exists. " +
			"Asks for no secret and reaches no host.",
	},
	{
		name:    "init",
		summary: "Give the configuration an id, a mesh subnet and its secrets.",
		usage:   []string{"paisans init [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--id <id>] [--yes]"},
		detail: "Give the configuration an id if it has none; check each site's admission and list every site not admitted, with why, refusing when a site does not answer, unless the deployment is already on a site; " +
			"at a terminal, without --yes, ask for each site not admitted whether to admit it now from this machine, and on a yes run site admit --site's own flow, which asks for your login on the host and each grant, shows the plan and asks before it runs; " +
			"otherwise skip the site, whose site admit checks the subnet as root, printing the site admit and site request lines that admit it. --yes never admits a host: admitting stays its owner's step; " +
			"until the deployment is on any site, reach every admitted site and keep mesh.subnet only if it overlaps nothing there, else, with no site admitted, roll a random /24 that overlaps nothing and move each site's address into it; " +
			"then generate the secrets it needs, filling in only what is missing, and say what is still owed from elsewhere. " + draftTopic,
	},
	{
		name:    "render",
		summary: "Write each site's artifacts to a local directory.",
		usage:   []string{"paisans render [--config " + config.FileName + "] [--secrets secrets.enc.yaml] --out ./out"},
		detail:  "Validate, then write per site artifacts to a local directory.",
	},
	{
		name:    "deployment apply",
		summary: "Take every site from " + config.FileName + " to a running stack.",
		usage: []string{
			"paisans deployment apply [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                         [--min-free <size>] [--keep-images] [--require-all] [--break-lease] [--execute] [--yes]",
			"paisans deployment apply --site <name> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                         [--ssh <destination>] [--only <stack>]... [--overwrite <path>]...\n" +
				"                         [--recreate <stack>]... [--hold-apps] [--min-free <size>] [--keep-images]\n" +
				"                         [--require-all] [--break-lease] [--execute] [--yes]",
		},
		detail: "Without --site: take every site from " + config.FileName + " to a running stack, running init, site prepare, apply, site add, storage and dns init in the order the deployment needs. " +
			"With --site: compare that site's rendered artifacts with what is on its host and show what would change.\n\n" +
			"Changes nothing on a host without --execute; without --site, the dry run may add missing generated secrets to the local secrets file, and says so when it does.\n\n" +
			"Both, and site prepare, review the keys and images " + config.FileName + " grants against what each host last applied; --execute asks for the word approve at a terminal when anything is added or changed, and asks nothing when nothing is. Before it, apply shows how the configuration differs from the sites' newest version: " + config.FileName + " as a diff, the secrets by key only. " +
			"A draft drafted from an older version than the sites' newest is merged forward first, as deployment pull merges it, and with --execute the merge is written into the draft and the apply stops, deploying nothing: review the merged draft, settle any conflict it lists, and run apply again. Versions after the draft's base that only sign its files again, as an attestation by operator register does, leave nothing to merge, and the draft deploys on the sites' newest. A conflict keeps the draft's value and is listed only at the merge; on the next apply it shows only in the review's difference. " +
			"The review flags each change that puts back the value a path or secret key had before the newest revision that changed it, naming that revision and its signer, and never refuses one. " +
			"A draft with no usable \"drafted from\" line is taken as drafted from the system copy's version, which the review names, and is warned about when it was last saved before the version that brought the sites' newest files reached this workstation. " +
			"A deploy makes the deployed files the system copy and rewrites the draft's \"drafted from\" line to name the version it published.\n\n" +
			"--yes approves the review entry by entry, and types create and rollback when apply asks for them. " + yesTopic + "\n\n" +
			"Before anything changes, every app it starts logs in to its mail server from its site, sending nothing; one that does not log in refuses the apply.\n\n" +
			leaseTopic + "\n\n" +
			"Every run, --site included, reaches every site's host to classify it, and runs on the sites it has: a site whose host has not admitted the deployment is left out, a joined site that does not answer is kept with its own steps skipped, and the run ends degraded. --require-all: exit 1 when the run is degraded: a site pending or down.\n\n" +
			"--hold-apps: start none of the site's app stacks, a pinned one included, and apply its mesh, its infrastructure and its Caddy. Without --site, apply runs it on each gateway outside etcd.members before the data sites are founded, so their apps reach Pocket ID through the gateway as they start.",
	},
	{
		name:    "site list",
		summary: "List every site by name, with its roles, mesh address and SSH destination.",
		usage:   []string{"paisans site list [--config " + config.FileName + "]"},
		detail: "List every site " + config.FileName + " declares, by the name --site and site remove take, with its roles, its mesh address, the SSH destination every command that logs in reaches its host at, defaults filled in, and its public_address and public_address6. " +
			"The table is on stdout. Reads only the local file; reaches no host and changes nothing.",
	},
	{
		name:    "site request",
		summary: "Write the request a host's owner admits a site from.",
		usage:   []string{"paisans site request --site <name> [--config " + config.FileName + "] > <name>.admit"},
		detail: "Write to stdout the request the host's owner admits the site from: the deployment's id, the site's user, SSH and WireGuard ports, mesh address and subnet, whether it has an endpoint, the grants its roles need, the apps handed to the owner's web server, and each operator's public keys. " +
			"Print its digest, sixteen characters as xxxx-xxxx-xxxx-xxxx, to stderr, for the owner to compare over a channel you both trust, since the request carries the keys that will log in to their machine. " +
			"Reaches no host; holds nothing secret.",
	},
	{
		name:    "site admit",
		summary: "Admit a deployment to this host, as its owner.",
		usage: []string{
			"sudo paisans site admit --request <file> [--watchdog] [--public-ports] [--data-dir <path>]\n                        [--bridge-netfilter] [--mesh-lan] [--enable-ufw] [--execute]\n                        [--digest <digest>] [--yes]",
			"paisans site admit --site <name> [--ssh <user>|<user@host[:port] or ssh alias>] [--config " + config.FileName + "] [the same grants]\n                   [--execute] [--yes]",
		},
		detail: "The one root step for a deployment on a host, run by the host's owner: the deployment's own unprivileged user with its own rootless Docker, the WireGuard helper and its sudoers line, the firewall, the egress table, the grants given and the admission record. " +
			"ufw must be installed. When it is not active, --enable-ufw, or a yes at a terminal, has admit allow every port sshd listens on, arm a timer that turns ufw off again in 10 minutes, enable ufw, and stop the timer once a new SSH login gets through: with --site, admit's own login as the user; with --request, one you open and confirm. Without it admit refuses, and prints the commands that enable ufw by hand, every allow before the enable. Admit never enables ufw while it holds a deny or reject rule of yours, or with IPV6 not yes, as ufw reads /etc/default/ufw and /etc/ufw/ufw.conf, while sshd listens on IPv6, and then asks nothing. " +
			"--watchdog and --public-ports are the owner's to give: at a terminal, admit asks for each one the site's roles need and the flags leave out; elsewhere a data site without --watchdog, or a gateway or Caddy monitor without --public-ports, stops before the host is reached. " +
			"With --site, --ssh may be your login alone, for the site's declared ssh host and port, or an alias of your ssh config for that host, alias:port or a word your config maps to the declared host and port, and is asked for at a terminal when left out. " +
			"--execute with --request asks for the request's digest at a terminal, or takes it as --digest, compared the same way; with --site it ends by logging in as the user with your own key.\n\n" +
			"--yes grants each grant the site's roles need and answers the ufw question with --site. It never answers the digest: --yes --request --execute needs --digest. With --request it refuses where admit would enable ufw, since only a person can open the new SSH session that shows SSH still works; --site proves it with admit's own login. With --site it needs --ssh. " + yesTopic + "\n\n" +
			"Never run by an agent unattended.",
	},
	{
		name:    "site prepare",
		summary: "Keep the deployment user's keys, configuration store and daemon settings on an admitted host.",
		usage:   []string{"paisans site prepare --site <name> [--config " + config.FileName + "] [--ssh <destination>]\n                     [--execute] [--yes]", "paisans site prepare --all [--config " + config.FileName + "] [--execute] [--yes]"},
		detail: "Run as the deployment's user on a host whose owner has admitted the deployment: check the admission record against " + config.FileName + ", refusing on any difference, read the watchdog site admit set up, and keep the user's daemon.json, its configuration store with the units that serve it, its authorized keys from ssh.keys, and its shell: DOCKER_HOST set in ~/.bashrc and ~/deployment linked to the deployment's directory. " +
			"Installs nothing and writes nothing that is root's. Writes nothing without --execute. " +
			"--all prepares every site, one at a time in the configuration's order, each with its own review, and stops at the first that fails.",
	},
	{
		name:    "site add",
		summary: "Join a new data site to the running cluster.",
		usage:   []string{"paisans site add <site> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                 [--execute] [--yes]"},
		detail: "Join a new data site to the running cluster in eight gated stages: preflight, mesh, etcd (learners, then promoted), the Patroni replica, synchronous mode, HAProxy, each existing replica's patroni.env, one replica restart at a time, and each monitor site's uptime stack, so it checks the new site.\n\n" +
			"Reads every site and plans only what differs, so a re-run resumes. Writes nothing without --execute. --execute asks for approve when the review adds or changes an image. " + yesTopic,
	},
	{
		name:    "site remove",
		summary: "Take a site out of the running deployment, reaching its host or not.",
		usage: []string{
			"paisans site remove <site> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                    [--delete-data] [--force --ssh <user@host[:port] or ssh alias>] [--execute] [--yes]",
			"paisans site remove <site> --host-gone [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n" +
				"                    [--execute] [--yes]",
			"paisans site remove --force --ssh <user@host[:port] or ssh alias> --id <id or token>\n" +
				"                    [--delete-data] [--execute] [--yes]",
		},
		detail: "Take a site out of the running deployment in gated stages: its data out (the Patroni leader switched to the Sync Standby, its Garage node out of the layout and the objects moved), out of the cluster (its etcd member, and the mesh, HAProxy and gateway routes on every remaining site, and each replica's patroni.env, one at a time; one data site out of two and a witness takes the witness out of etcd too), its host cleaned of everything provably this deployment's (a gateway's Caddy kept, reduced to the host owner's sites in its caddy.d, /srv/paisans/<token>/caddy.d, while there are any), the A and AAAA records dns init made for its public address deleted at the DNS provider, and its entry out of " + config.FileName + ".\n\n" +
			"Refuses the only gateway, data or apps site, a site an app is pinned to, and an unhealthy cluster. --host-gone never reaches the host, as below; --delete-data deletes its data after the site's name is typed at a terminal. Writes nothing without --execute.\n\n" +
			"--force --ssh <host> --id <id or token>, with no " + config.FileName + ": clean that host of the deployment the login's own admission record names, as --force does, from that record alone, logging in as the deployment's user. DNS is not modified. --execute asks for the site's name at a terminal.\n\n" +
			"The host stage runs as the deployment's user and ends with the sudo paisans site evict line the host's owner runs to remove the rest.\n\n" +
			"--host-gone is for a site whose host is not reached: dead, or still running but no longer the deployment's to reach. It never reaches that host, and cuts the site off in this order: the mesh first on every remaining site, then its DNS records, Patroni's own failover, its etcd member, its Garage node skipped as dead, then the rest as remove does. Refuses --force, --ssh, --id and --delete-data, which reach a host. " +
			"Generates the secrets of a replacement declared in the same edit, which apply then brings up. --execute asks for the site's name, and for revoked, lose writes or lose objects when they apply.\n\n" +
			yesTopic,
	},
	{
		name:    "site evict",
		summary: "Remove a deployment from this host, as its owner.",
		usage: []string{
			"sudo paisans site evict --request <file> [--data-dir <path>] [--execute] [--yes]",
			"sudo paisans site evict --deployment <id> --user <name> [--data-dir <path>] [--execute] [--yes]",
			"paisans site evict --site <name> [--ssh <user>|<user@host[:port] or ssh alias>] [--config " + config.FileName + "] [--data-dir <path>]\n                   [--execute] [--yes]",
		},
		detail: "Undo site admit for one deployment: its mesh, its user with its home and data, the helper, the egress table, its firewall rules, the record and the registry entry. " +
			"Host-wide files and sysctls go only when no other deployment is admitted. Evict never disables ufw, which stays as it is with its paisans: ssh allows when admit enabled it. " +
			"--data-dir, the one admit was given, is needed only when the host holds no admission record.\n\n" +
			"Run it as root on the host with --request or --deployment and --user, or from a workstation with --site, which reads the site from " + config.FileName + ". With --site, --ssh may be your login alone, for the site's declared ssh host and port, or an alias of your ssh config for that host, alias:port or a word your config maps to the declared host and port, and is asked for at a terminal when left out. " +
			"After site remove has taken the site out of " + config.FileName + ", use the --deployment and --user line it printed.\n\n" +
			"--execute asks for the user's name at a terminal; --yes types it, and with --site needs --ssh. " + yesTopic + "\n\n" +
			"Never run by an agent unattended.",
	},
	{
		name:    "site deployments",
		summary: "List every deployment on a host.",
		usage:   []string{"paisans site deployments --ssh <user@host[:port] or ssh alias> [--sudo=false]"},
		detail: "List every admission record on the host --ssh names (the deployment, its user, the site and roles it was admitted as, the grants and when), every deployment in its registry (id, token, domain, site, roles and root), and any deployment directory no registry entry names. " +
			"--sudo=false lists admission records only, which are readable without sudo. Needs no " + config.FileName + "; changes nothing.\n\n" + sshAliasTopic,
	},
	{
		name:    "site ssh",
		summary: "Log in to a site's host as the deployment's user.",
		usage:   []string{"paisans site ssh <site> [--config " + config.FileName + "] [-- command...]"},
		detail: "Open your own ssh session on the site's host, logged in as every command logs in there: the site's ssh.user, or the deployment's own user when it names none, at its ssh host and port, offering the keys its ssh.keys lists from your agent. " +
			"A plain ssh to the host reaches your own account; this one reaches the deployment's, where site prepare sets DOCKER_HOST and links ~/deployment.\n\n" +
			"Words after -- run as a command in place of a shell, as for ssh, and its exit status is site ssh's. " +
			"Reads " + config.FileName + " without validating it; changes nothing itself.",
	},
	{
		name:    "mesh rekey",
		summary: "Replace sites' WireGuard keys, one site at a time.",
		usage:   []string{"paisans mesh rekey <site>... | --all [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                   [--id <id>] [--execute]"},
		detail: "Replace sites' WireGuard keys, one site at a time: a next key in the secrets, every peer's mesh file and then the site's own synced with it, a handshake under it from every peer, etcd and Patroni healthy, then the key promoted. " +
			"A stopped run resumes. Writes nothing without --execute. " + draftTopic,
	},
	{
		name:    "gateway move",
		summary: "Move the gateway role to the site " + config.FileName + " gives it.",
		usage:   []string{"paisans gateway move [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--execute] [--yes]"},
		detail: "Move the gateway role to the site " + config.FileName + " gives it, from the site that still serves as the gateway, in gated stages: carry the deployment record, check the new gateway answers for every hostname through its own address, add its address records beside the old one's, delete the old one's (only after that check, and only records dns init made for it), wait out their TTL, then stop the old gateway's Caddy.\n\n" +
			"Resumes after a fix. Writes nothing without --execute. --yes is handed to the hand-off's apply, which may ask for confirmations of its own. " + yesTopic,
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
		usage:   []string{"paisans storage rotate-key --app <name> [--config " + config.FileName + "]\n                           [--secrets secrets.enc.yaml] [--id <id>] [--execute]"},
		detail: "Replace one app's S3 key, in gated stages: the new pair into the secrets (the old one kept beside it), the new key imported and granted, the app applied alone on every site running it, a probe written, read and deleted with the new key, and only then the old key deleted from Garage and the secrets.\n\n" +
			"Resumes from the secrets file. Writes nothing without --execute. " + draftTopic,
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
		usage:   []string{"paisans doctor [--config " + config.FileName + "] [--site <name>]... [--id <id>] [--execute]"},
		detail: "Reach every site, or the ones --site names, and report what is stuck: unreachable sites, etcd quorum and version, the Patroni leader and why a replica will not promote, containers that are down, Pocket ID's active instance, and clocks. " +
			"Prints how to recover. Reaches hosts and changes none; exits 1 on any FAIL. With --execute it removes the config: keys that only repeat what gating renders, and records the ssh.user of a site it logs in to as the deployment's own user, both in " + config.FileName + ". " + draftTopic + " --id applies only with --execute.",
	},
	{
		name:    "images",
		summary: "List every image each site runs and what the next apply runs.",
		usage:   []string{"paisans images [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n               [--check]"},
		detail: "List, for every site, each image its stacks run: where its reference comes from (the core catalog's foundation, the blueprint, or the configuration key that declares it), the build the site runs, and the build the next apply would run. " +
			"An override names what it replaced, and says when that is a newer release. " +
			"Reads every site over ssh and the registries; changes nothing. An image a registry cannot resolve shows unknown under newest. A site it cannot reach is listed as unknown and the command exits 1. --check exits 3 when a build would move or an override lags.",
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
			"Creates only what is missing, updates the address of an A or AAAA record it made for this deployment, never deletes, and refuses if any other record conflicts. Writes nothing without --execute.",
	},
	{
		name:    "dns prune",
		summary: "Delete the records dns init made that are no longer wanted.",
		usage:   []string{"paisans dns prune [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--name <fqdn>]... [--execute]"},
		detail: "Delete the records dns init created that the configuration no longer implies: only one carrying init's comment, at a name under community.domain or one the configuration names, and no longer wanted; an A or AAAA pointing at a site's public address, or a TXT or MX holding exactly a no-mail content init writes.\n\n" +
			"Lists every other record init marked, with why it is kept, and lists the zone again to confirm each delete. Writes nothing without --execute.",
	},
	{
		name:    "deployment publish",
		summary: "Sign the draft and push it to every site.",
		usage:   []string{"paisans deployment publish [--config " + config.FileName + "] [--merge] [--break-lease] [--execute] [--yes]"},
		detail: "Sign the draft's " + config.FileName + ", secrets.enc.yaml and .sops.yaml as a version on the one it was drafted from and push it to every site; --merge builds on every head of a fork. " +
			"A draft drafted from an older version than the sites' newest is refused, naming deployment pull, which merges it forward, unless the versions after it only sign its files again, as an attestation does. With --execute the published files become the system copy, and the draft's \"drafted from\" line names the new version.\n\n" +
			leaseTopic + configTrust,
	},
	{
		name:    "deployment pull",
		summary: "Bring the system copy and the draft to the sites' newest version.",
		usage:   []string{"paisans deployment pull [--config " + config.FileName + "] [--id <id>] [--root <sha>] [--key <path.pub>] [--execute] [--yes]"},
		detail: "Move the system copy to the sites' newest version and merge that version into the draft, from the version the draft was drafted from: its \"drafted from\" line names it, or, without a usable line, the system copy's version is taken. " +
			"paisans-stack.yaml and .sops.yaml merge path by path, a list being one value, with the draft's key order, blank lines and comments kept, but for a comment only the sites' newest changed, and a file with a YAML anchor, an alias or a merge key (<<) is refused; the secrets merge key by key, decrypted with this workstation's age key and encrypted again to the merged .sops.yaml's recipients. " +
			"A path both sides changed differently is a conflict: the draft keeps its own value there, and the conflict is listed, a secret by its key only. The merged draft's \"drafted from\" line names the sites' newest version; review it and deploy it with deployment apply --execute. " +
			"When the versions from the draft's base to the sites' newest only sign the base's files again, as an attestation by operator register does, the draft is up to date: only its \"drafted from\" line is rewritten, to the sites' newest. " +
			"In a folder with no draft, start one from the sites' newest version, of the only deployment on this workstation or the one --id names. Writes nothing without --execute." +
			configTrust + "\n\n" + keyTrust,
	},
	{
		name:    "deployment log",
		summary: "List every version, its signer, the sites holding it, and what each site applied.",
		usage:   []string{"paisans deployment log [--config " + config.FileName + "] [--root <sha>] [--key <path.pub>]"},
		detail:  "List every version of the configuration the sites keep, its signer, and the sites holding it, then the revision each site last finished applying, whether that is the head, and what a stopped apply still owes it: apply publishes a version before it changes any host, so a version every site holds is not by itself what runs." + configTrust + "\n\n" + keyTrust,
	},
	{
		name:    "deployment recover",
		summary: "Make the system copy from the sites and start a draft of it.",
		usage:   []string{"paisans deployment recover --from <user@host[:port] or ssh alias>... [--root <sha>] [--key <path.pub>] [--id <id>]\n                       [--config " + config.FileName + "] [--execute] [--yes]"},
		detail: "For a deployment this workstation holds no system copy of, gather from the --from hosts and every site the trusted heads name, and with --execute make the system copy the head's, with its root and key, and start a draft of it in the directory of --config, which must hold none of the three files. " +
			"A deployment whose system copy here has a version is refused, naming deployment checkout and deployment pull, unless --root names another root than the one it pinned, or the copy pinned none, as after the deployment was made again under its id; a system copy with no version, or one --root replaces, is moved aside whole into replaced/ of the data directory, never removed. A fork is refused, naming the merge with deployment publish --merge an operator who holds the files makes, and nothing is written. " +
			"The system copy is listed by deployment list." +
			configTrust + "\n\n" + keyTrust + "\n\n" + sshAliasTopic,
	},
	{
		name:    "deployment list",
		summary: "List the system copies on this workstation.",
		usage:   []string{"paisans deployment list"},
		detail: "List every deployment's system copy on this workstation: its id, domain, revision, version, whether it is verified, where it came from and when. A verified copy is a version the sites trust, not one every site runs: deployment log says what each site applied. " +
			"A system copy is the one copy of a deployment this workstation trusts: the files of the version it last deployed or synced, the signed versions gathered from the sites, and the root, signing key and newest revision trust is read from. " +
			"The first deploy of a new deployment, deployment recover --execute, the join of operator register and operator register --root --execute make one, and deployment fetch fills one with no version from a host; a deploy or deployment pull moves it to a newer version, and an attestation by operator register moves its head to the attestation. Each records the version it moved to, which this list shows. " +
			"System copies are kept under deployments/ in ~/Library/Application Support/Paisans on macOS, and in $XDG_DATA_HOME/paisans (~/.local/share/paisans) elsewhere, or in $" + localstore.DirVariable + " when set; locks/ there holds the lock a command takes while it writes one, and replaced/ the copies a recovery moved aside. " +
			"Work on a deployment in a draft: deployment checkout starts one. Reads nothing else, and changes nothing.",
	},
	{
		name:    "deployment checkout",
		summary: "Start a draft from a deployment's system copy.",
		usage:   []string{"paisans deployment checkout [--id <id>] [--head <sha>] [--config " + config.FileName + "] [--execute]"},
		detail: "Write the three files of a deployment's system copy on this workstation (see deployment list) into the folder of --config, which holds no draft: " + config.FileName + ", secrets.enc.yaml and .sops.yaml, with a first line in " + config.FileName + " naming the version they were drafted from. " +
			"The deployment is the only one on this workstation, or the one --id names. The system copy is judged from its own anchor first; an unverified or forked one is refused, which deployment recover --root <sha> pins instead. " +
			"--head <sha> writes that trusted head of the system copy's store in place of its own version, the files a hand merge for deployment publish --merge starts from. " +
			"Never overwrites a file, writes nothing to the system copy, and reaches no host. The system copy may be behind the sites; deployment pull brings the draft up to date. Writes nothing without --execute.",
	},
	{
		name:    "deployment fetch",
		summary: "Fill a system copy of every deployment a host holds.",
		usage:   []string{"paisans deployment fetch --ssh <user@host[:port] or ssh alias>"},
		detail: "Read, through sudo, the registry of the host --ssh names and, for each deployment in it, every version of its configuration the host keeps, and record them in the deployment's system copy (see deployment list). " +
			"A deployment with a system copy here is judged from the key that signed it, as deployment pull judges. A system copy with a version keeps its files, head and record, since only a deploy or deployment pull moves it, and the run names deployment pull when the host holds a newer one; one with no version takes the newest trusted version as its own. A deployment with no system copy here has nothing to trust from, so its newest versions are set aside in its system copy's incoming versions, never as its own files, and listed as unverified, " +
			"for deployment recover --root <sha> to pin once the hash is checked with another operator. Needs no " + config.FileName + "; changes nothing on the host.\n\n" + sshAliasTopic,
	},
	{
		name:    "operator add",
		summary: "Declare one more operator in " + config.FileName + ".",
		usage: []string{
			"paisans operator add [<username>] [--name <first last>] [--email <address>]\n                     [--key <path.pub or key line>]... [--site <name>]... [--all-sites]\n                     [--config " + config.FileName + "] [--id <id>]",
			"paisans operator add --request <file> [--site <name>]... [--all-sites] [--config " + config.FileName + "]\n                     [--id <id>]",
		},
		detail: "Write the operator's entry under operators, after the last one, and name them in the ssh.keys of each site --site gives, or of every site with --all-sites, leaving every other line of the file as it was. " +
			"At a terminal it asks, with create's questions, for whatever the flags did not give or give in a form the file would refuse, and which sites the operator may log in to. Without a terminal it asks nothing: the username, --name, --email, at least one --key and the sites must all be given, or nothing is written. " +
			"A --key is a .pub file, every line of which but blank lines and comments is a key, or one key line itself. " +
			"Writes the file only when validate refuses nothing the addition introduced. Reaches no host: the next deployment apply --execute gives the operator their sites, publishes the change and makes their Pocket ID account. Their age key, to read the secrets, is added to .sops.yaml by hand, with sops updatekeys; operator invite and operator add --request do that for a new operator.\n\n" +
			"With --request, accept the signed request a new operator's operator register --invite wrote, on any operator's workstation: the request must be signed by the key its invite names, and the invite by a key an operator of this deployment lists, for this deployment and the root this workstation trusts. " +
			"It declares the operator with the request's username, name, email and key, asking only for the sites, adds their age key to .sops.yaml and re-encrypts secrets.enc.yaml to every recipient there, its contents unchanged, decrypting with your own age key. An operator declared already keeps their sites, so --site and --all-sites are refused for them. " +
			"Refuses a .sops.yaml whose creation rule uses key_groups or has no age field, which is left to you, and a secrets file whose sops metadata sets what the toolkit never writes, such as encrypted_regex or keys other than age, which sops updatekeys re-encrypts by hand. Every check is made before the first file is written, and a write that fails puts back the files written before it. The username, --name, --email and --key are refused beside --request. " +
			"Accepting the same request again adds the age key or re-encrypts if either is missing, and otherwise changes nothing.\n\n" + draftTopic,
	},
	{
		name:    "operator age-key",
		summary: "Show your age public key for a deployment, making the key first if there is none.",
		usage:   []string{"paisans operator age-key [--id <deployment id>] [--config " + config.FileName + "]\n                       [--age-private-key-file <path>]"},
		detail: "Print your age public key for the deployment, on a line of its own at the end of the output. The id comes from --id, or from " + config.FileName + " beside it. " +
			"The key is the first of these that is present: the one key a SOPS_AGE_* variable holds, the keystore item for the deployment id, the file --age-private-key-file names when it exists, and otherwise a new key, kept in that file when one is named and in the keystore when not. " +
			"A key file, made or read, comes with the export SOPS_AGE_KEY_FILE line that points sops at it; a directory is refused. Never prints the private key, and never replaces a keystore item or a key file. Reaches no host and changes nothing in the configuration.",
	},
	{
		name:    "operator invite",
		summary: "Write a signed invite for a new operator to stdout.",
		usage:   []string{"paisans operator invite <username> --key <path.pub or key line> [--config " + config.FileName + "]"},
		detail: "Write an invite for the operator <username> will be, signed through your ssh-agent with the signing key the system copy records. " +
			"The invite names the deployment, its domain, the root of the history this workstation trusts, the new operator's key without its comment, and each site's ssh host, port and user. It holds nothing secret.\n\n" +
			"Refuses a username or key an operator already has, a workstation with no recorded signing key or whose ssh-agent does not hold it (run operator register), and one that does not know its root, from the system copy's pinned root or a single root among the versions it has gathered (run deployment pull). The root and the inviter come from the system copy, whatever the draft holds. Reaches no host and changes no file.",
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
		name:    "operator grants",
		summary: "List the site admit flags each deployment on a host was admitted with.",
		usage:   []string{"paisans operator grants --ssh <user@host[:port] or ssh alias>"},
		detail: "Read each admission record on the host --ssh names, an alias of your ssh config or user@host[:port], and list each site's grants and flags, yes or no (--watchdog, --public-ports, --data-dir, --bridge-netfilter, --mesh-lan, --enable-ufw) and its web server, then the site admit command that admitted it. " +
			"Admit takes the rest of a site's admission from the configuration, so add --ssh and anything new, such as --swapfile, and read the dry run before --execute. Needs no " + config.FileName + " and no sudo, since the records are readable by any user; changes nothing.",
	},
	{
		name:    "operator register",
		summary: "Sign the sites' newest version with your own key.",
		usage: []string{
			"paisans operator register [--config " + config.FileName + "] [--key <path.pub>] [--root <sha>]\n                          [--id <deployment id>] [--execute] [--yes]",
			"paisans operator register --invite <file> --inviter-key <path.pub or key line>\n                          [--name <first last>] [--email <address>] [--age-private-key-file <path>]\n                          [--config " + config.FileName + "] > <file>.request",
		},
		detail: "Sign an attestation of the sites' newest version with your own key, so trust starts from your signature: the key in your ssh-agent listed under your entry in operators, or the one --key names. " +
			"Attests the version the system copy holds, whatever the draft holds, or, for a deployment with no system copy here and a root pinned with --root, the one the pin leads to, making the system copy from it. " +
			"Records the key in the system copy as its signing key, which deployment publish signs with from then on.\n\n" +
			"With --invite, answer the signed invite an operator wrote with operator invite, in any directory, since nothing there is read or written: check it against --inviter-key, which must be the key that signed it, and that the ssh-agent holds the key it names; find or make your age key for its deployment, from a SOPS_AGE_* variable, the keystore item, --age-private-key-file, or a new keystore item, in that order; ask for your full name and email at a terminal or take --name and --email; and write the signed request to stdout. " +
			"The invite and the inviter's key wait as the deployment's pending invite in invites/<deployment id>/ of the workstation's data directory, where deployment list's saved deployments live, and the first answer binds the age key it named to the deployment in age/<deployment id>.recipient there. Every later answer for the deployment searches all the sources for the bound key and uses the one that holds it, and is refused, with no key made, when none does; removing the binding file starts over with another key. " +
			"Reaches no host and takes no --key, --root, --id or --execute. Refuses an invite to a deployment whose pending invite is a different one, naming the file to remove to abandon it, before anything is made. A key kept in a file comes with the export SOPS_AGE_KEY_FILE line that points sops at it, and the request is followed on stderr by what comes next.\n\n" +
			"Run without --invite in a directory without the files, with an invite pending, it joins: the one invite pending, or the one --id names when several are. --key or --root without --id registers instead. With several pending and no --id it refuses and lists their directories; removing one abandons that invite. " +
			"The join makes the system copy from the invite's root and sites, and starts a draft of it in the directory, once an operator has accepted the request (operator add --request) and applied it (deployment apply --execute), and refuses before that with advice to ask the inviter. It signs your attestation with the invite's key and removes the pending invite once the system copy is made. A deployment whose system copy is here already is refused. It then says whether the secrets open with your age key and, when they do not, whether the fix is this workstation's or the acceptance's. Writing needs --execute; without it, the join says what it would write.\n\n" +
			"Changes only the sites' configuration stores, and nothing without --execute. It goes ahead on whichever sites answer, and with --execute it asks for the word rollback at a terminal when the newest version is older than one seen here before; --yes types it. " + yesTopic,
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
			"paisans secrets set <dotted.key> [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--id <id>] < value",
			"paisans secrets set [--config " + config.FileName + "] [--secrets secrets.enc.yaml] [--id <id>]",
		},
		detail: "Read one value from stdin and write it into the encrypted secrets file, printing only its name. " +
			"For credentials issued elsewhere, so they never touch an echoing terminal or an editor.\n\n" +
			"With a key, at a terminal, ask for its value with echo off, saying whether it is already set; an empty answer writes nothing.\n\n" +
			"With no key, at a terminal, ask for each secret still owed in turn, saying why it is owed, and read each with echo off. " +
			"Each answer is written before the next is asked; an empty one skips the key, which stays owed. A sign-in client is not asked for, since apply creates it.\n\n" +
			"An SMTP password is tried before it is written: from this machine, it logs in to the mail server and sends one test message to every operator, so no site need be deployed yet. One the server refuses is not written; a server this machine cannot reach is warned about, and apply tries the password from each site.\n\n" + draftTopic,
	},
	{
		name:    "secrets prune",
		summary: "Remove the secrets of what " + config.FileName + " no longer declares.",
		usage:   []string{"paisans secrets prune [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                      [--without-record] [--id <id>] [--execute] [--yes]"},
		detail: "Remove the secrets that name something " + config.FileName + " no longer declares: a removed site's keys, a removed app's passwords and sign-in client, a Pocket ID group nothing names. Lists them by key, never by value.\n\n" +
			"--without-record trusts " + config.FileName + " alone, when no gateway's deployment record can be read, and --execute then asks for the word prune at a terminal; --yes types it. " + yesTopic + "\n\n" +
			"Only reads the gateways' deployment records; its --execute writes the draft's secrets file, and records the ssh.user of a gateway it logs in to as the deployment's own user. " + draftTopic,
	},
	{
		name:    "secrets rotate",
		summary: "Replace secrets, one at a time, in gated stages.",
		usage:   []string{"paisans secrets rotate <path>... | --all [--restart] [--replaced <path>]...\n                       [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                       [--id <id>] [--execute] [--yes]"},
		detail: "Replace secrets, named by their paths in the secrets file (apps.<app>.<unit> for a unit of secrets an app's blueprint groups), or every one in scope with --all, one at a time in gated stages: a next value in the secrets file, the side that checks it, each site that uses it healthy before the next, then the value promoted.\n\n" +
			"A pasted secret (the DNS token or an SMTP password) whose value every host using it runs, read line by line, is asked for at a terminal before any host is reached: replace it at its issuer when asked and paste the new one. One already set anew with secrets set is handed on from the file. A DNS token is tried against the zone before any host gets it; an SMTP password logs in to the mail server from this machine (a server it cannot reach is a warning) and then from every site that will be handed it, none being handed it until all accept, and a pasted one sends the operators one test message. --replaced <path> lists one already replaced and applied, which every host runs, instead. Lists the secrets it keeps and why.\n\n" +
			"--all refuses a file that lacks a secret init generates. A stopped --all resumes with what it left, and says so; --all --restart starts the full list again.\n\n" +
			"Refuses while a mesh rekey is unfinished, a site does not answer, or etcd or Patroni is unhealthy; apply and every other command that renders refuse while a rotation is unfinished. " +
			"A run that signs members out asks for sign out at a terminal, before any host is reached; --yes types it, and never pastes a secret. " + yesTopic + " A stopped run resumes. Writes nothing without --execute. " + draftTopic,
	},
	{
		name:    "app remove",
		summary: "Take an app that has left " + config.FileName + " off every host.",
		usage:   []string{"paisans app remove <app> [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                   [--delete-data] [--execute] [--yes]"},
		detail: "Take an app that has left " + config.FileName + " off every host, once a whole apply on each site has marked its files left over: its containers and networks, its rendered files (one edited on the host is kept), its stack directory when nothing else is in it, and its Pocket ID client when the secrets file records that client's ID.\n\n" +
			"--delete-data also deletes its database and role, its Garage bucket and keys, its named volumes and its data directories, after the app's name is typed at a terminal, or by --yes. " + yesTopic + " " +
			"Leaves its secrets and DNS records, and says so. Writes nothing without --execute.",
	},
	{
		name:    "oidc client create",
		summary: "Create an app's client at the deployment's Pocket ID.",
		usage:   []string{"paisans oidc client create --app <name> [--rotate-secret]\n                           [--config " + config.FileName + "] [--secrets secrets.enc.yaml]\n                           [--site <name>] [--ssh <destination>] [--id <id>] [--execute]"},
		detail: "Create an app's client at the deployment's Pocket ID, with the groups the app reads, and record its ID and secret in the secrets file. The secret is never printed. " +
			"Writes nothing without --execute. Supports every blueprint that signs in through Pocket ID. " + draftTopic,
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

// yesTopic is what every command that takes --yes says of it.
// sshAliasTopic is how a destination flag reads a Host alias of the
// operator's ssh config.
const sshAliasTopic = "A destination is user@host[:port], or a Host alias of your ssh config: alias, user@alias, alias:port or user@alias:port, a user or port given overriding the config's. " +
	"The alias is resolved with ssh -G, so Match, Include and wildcards apply as they do for ssh, and ssh is given the alias, so its ProxyJump, IdentityFile and other options apply to the connection. " +
	"Messages show it as alias -> user@host:port. A destination with no user takes the one ssh -G reports: its config's User, or your local login where none applies, as ssh does."

const yesTopic = "--yes answers each confirmation as typing it would, with no terminal, and prints approved by --yes: and what it approved, a line for each. It needs PAISANS_ALLOW_YES=1 in the environment, so a command line copied alone never approves anything."

// draftTopic is how a command that writes the deployment's files finds
// them.
const draftTopic = "It writes the draft in the folder of --config, and in a folder with no draft first starts one there from the system copy, of the only deployment on this workstation or the one --id names. The system copy is unchanged until the draft is deployed."

// configTrust is what every config subcommand shares, after its own
// sentence.
const configTrust = "\n\n" +
	"The signed copies of " + config.FileName + ", secrets.enc.yaml and .sops.yaml every site keeps. Each config command gathers every site's versions first and trusts them from your own signing key or a root pinned with --root.\n\n" +
	"Publish, pull, recover, apply and operator register go ahead on whichever sites answer. With --execute, publish, pull, recover, apply and operator register ask for rollback when the newest version is older than one seen here before, and log only warns. " +
	"--yes types rollback for each command but log, which asks nothing. " + yesTopic + " " +
	"With --execute each site first syncs with its peers; without it, and in log, no site is changed and only the system copy's store of versions here is written."

// keyTrust is how pull, log and recover start trust from --key on a
// workstation that records no signing key.
const keyTrust = "With no signing key recorded in the system copy, name your own operator key with --key <path.pub>. It anchors only on versions it signed itself, under one root, that the trust rules trust from that root: every head must be signed by it and list it under an operator, and every trusted version must declare the deployment's id. " +
	"A newer version by another operator, or the key's versions under two roots, need --root <sha>, the full hash deployment log prints on a root's line or another operator's system copy pins, since a key alone cannot tell this deployment's history from one it signed before a redeploy under the same id. " +
	"For the same reason, when every site that answers serves an older incarnation, a recovery with --key can land on a genuine version the key signed there: recover --key prints the full hash of the root it pins, to confirm with another operator before applying. " +
	"A pinned root anchors alone, and --key is then ignored, with a note. With a root pinned, a gather whose sites serve none of its history is refused: the pin is out of date, and you recover from the current root into an empty directory, or a site that answered holds a history it should not. " +
	"pull and recover with --execute record the named key as the system copy's signing key when your ssh-agent holds it (load it with ssh-add) and an operator of the head lists it; log records nothing. With neither --key nor a root, they refuse and say so."

// executeTopic is `paisans help execute`: which commands reach a host, and
// what --execute lets each of them change.
func executeTopic() string {
	return `site prepare, site deployments, apply, prune, site add, site remove, mesh rekey,
storage init, storage add, storage rotate-key, app admin create, app remove,
oidc client create, secrets prune, secrets rotate, preflight, failover test,
doctor, init, config, operator register, site admit and site evict
are the only commands that reach a host. Each reads it to plan, and changes it
only with --execute; config and operator register check no admission record,
and their --execute only starts each site's configuration sync and, for publish
and register, adds a version to its store; site deployments and preflight have
no --execute and never change it; init has none and changes a host only by a
site admit you approve at its terminal; doctor's --execute and secrets prune's
change no host either, editing only the local files, and secrets prune only
reads the gateways' deployment records. site admit and site evict reach a host
as its owner, as root on it or through your own sudo. site admit claims the host
in /var/lib/paisans/registry.json as a step of its plan, refusing if another
deployment there holds this one's token, WireGuard interface or listen port, or
a mesh subnet overlapping this one's, and site evict takes the claim away as its
last. Every other command that writes to a host first reads the host's admission
record for this deployment and refuses, with nothing changed, a host that has
none, or whose record does not match the site: another site name, ssh user, SSH
or WireGuard port, mesh subnet, forwarding, a grant its roles need, or, for
an external web server, the owner's web server record. It names the two lines that fix it, ` + progname.Cmd() + ` site request --site <site> >
<site>.admit for the operator, and sudo ` + progname.Default + ` site admit --request
<site>.admit for the host's owner. apply and site prepare also refuse when
anything else on the host overlaps the mesh subnet. dns init and dns prune reach
no host, only the DNS provider's API, and change it only with --execute; site
request reaches no host and takes no --execute, writing only to stdout and
stderr; site remove also deletes the removed site's records there. web-server
show and web-server check change nothing: check looks at a monitor's public
hostname as any visitor could, and with managed_by: external both read the
monitor's web server over ssh, show always and check when a check fails.
Everything else writes files locally and stops.

Every command takes -v or --verbose. By default each step is one line; with it,
the reasons, values, request bodies and command output behind each step show
too. A failure always says what went wrong and what to do; with -v, the
underlying error shows under it too.
`
}

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
	fmt.Fprint(w, progname.Name()+" installs and runs a community stack from "+config.FileName+".\n\n")
	fmt.Fprint(w, "Usage: "+progname.Cmd()+" <command> [flags]\n")
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
		"Run '"+progname.Cmd()+" help <command>' for a command's details, and '"+progname.Cmd()+" help\n"+
		"execute' for which commands reach a host and what --execute changes.\n")
}

// printGroupHelp writes a group's page: its subcommands, one line each.
func printGroupHelp(w io.Writer, group string) {
	subs := subcommands(group)
	fmt.Fprintf(w, "%s\n\nUsage: %s %s <command> [flags]\n\nCommands:\n", groupSummary[group], progname.Cmd(), group)
	width := 0
	for _, c := range subs {
		width = max(width, len(strings.TrimPrefix(c.name, group+" ")))
	}
	for _, c := range subs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, strings.TrimPrefix(c.name, group+" "), c.summary)
	}
	fmt.Fprintf(w, "\nRun '%s %s <command> --help' for a command's usage, details and flags.\n", progname.Cmd(), group)
}

// printCommandHelp writes one command's page: its usage, what it does, and,
// given its flag set, its flags.
func printCommandHelp(w io.Writer, c *helpCommand, fs *flag.FlagSet) {
	fmt.Fprint(w, "Usage:\n")
	for _, u := range c.usage {
		fmt.Fprintf(w, "  %s\n", invokedUsage(u))
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

// invokedUsage is a usage line from helpCommands, which names the command
// paisans, with the command named as it was invoked and its continuation
// lines moved by the difference, so they stay under its flags. A line run
// with sudo is the host's owner's, on the host, and keeps paisans.
func invokedUsage(u string) string {
	if strings.HasPrefix(u, "sudo ") {
		return u
	}
	u = strings.Replace(u, progname.Default, progname.Cmd(), 1)
	return strings.ReplaceAll(u, "\n"+strings.Repeat(" ", len(progname.Default)), "\n"+strings.Repeat(" ", len(progname.Cmd())))
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
		fmt.Fprintf(fs.Output(), "Run '%s %s --help' for its usage and flags.\n", progname.Cmd(), name)
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
			return func(w io.Writer) { fmt.Fprint(w, executeTopic()) }, true, nil
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
