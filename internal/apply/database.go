package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Database is one clustered app's role and database, owned by that role.
type Database struct {
	App  string
	Role string
	Name string
	// password is never printed and never placed on a command line.
	password string
}

// Bootstrap is the per app database work a site with a Patroni member does,
// after the infrastructure stack and before any app stack.
type Bootstrap struct {
	// Patroni is this site's Patroni REST API, host:port, on the mesh.
	Patroni   string
	Databases []Database
	// ClusterSites is cluster.sites. A leader is one of these or it is not a
	// member this deployment declared.
	ClusterSites []string
	// EtcdUnstarted names the other founding etcd members not running yet,
	// set only when this site's own member is being founded. While it is not
	// empty no primary can appear, so apply stops after the infrastructure
	// stack instead of waiting out primaryWait. See WitnessFirstRefusal.
	EtcdUnstarted []string
}

// identifier is what this package will put into SQL as a role or database
// name. It is narrower than Postgres allows on purpose: a name that needs
// quoting to be safe is refused rather than quoted, so the SQL an operator
// reads in a log is the SQL that ran.
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedRoles are the roles the rendered Patroni defines for itself
// (patroni.env.tmpl). An app with one of these names would have its password
// set on the cluster's own superuser, admin or replication role.
var reservedRoles = map[string]bool{"postgres": true, "admin": true, "standby": true}

// Databases returns what site must create, or nil when it creates nothing:
// the site holds no Patroni member, or no clustered app uses Postgres.
//
// Only a site in cluster.sites runs Patroni, so only such a site can reach a
// primary through its own container. The work is idempotent and runs on
// whichever of them is the leader when it is applied; a replica leaves it to
// the leader's site, and starts its apps only once its own catalogue holds
// what the leader created.
func Databases(cfg *config.Config, secrets *config.Secrets, site string) (*Bootstrap, error) {
	inCluster := false
	for _, name := range cfg.Cluster.Sites {
		if name == site {
			inCluster = true
		}
	}
	if !inCluster {
		return nil, nil
	}
	out := &Bootstrap{
		Patroni:      fmt.Sprintf("%s:%d", cfg.Sites[site].Address, render.PatroniAPIPort),
		ClusterSites: append([]string(nil), cfg.Cluster.Sites...),
	}
	for _, name := range cfg.AppNames() {
		app := cfg.Apps[name]
		if app.Placement.Mode != config.PlacementCluster || !blueprints.UsesPostgres(app.Blueprint) {
			continue
		}
		id := render.DBIdentifier(name)
		if !identifier.MatchString(id) || len(id) > 63 {
			return nil, fmt.Errorf("apps.%s: its database role would be named %q, and a role is created only from lowercase letters, digits and underscores, starting with a letter, at most 63 characters. Rename the app", name, id)
		}
		if reservedRoles[id] || strings.HasPrefix(id, "pg_") {
			return nil, fmt.Errorf("apps.%s: its database role would be %q, which is a role the cluster itself uses or reserves. Setting this app's password on it would hand the app that role. Rename the app", name, id)
		}
		password, _ := secrets.Apps[name]["database_password"].(string)
		if password == "" {
			return nil, fmt.Errorf("secrets apps.%s.database_password: required to create the app's database role. Run `"+progname.Name()+" init` to generate it", name)
		}
		if strings.ContainsRune(password, 0) {
			return nil, fmt.Errorf("secrets apps.%s.database_password: contains a NUL byte, which Postgres cannot store", name)
		}
		out.Databases = append(out.Databases, Database{App: name, Role: id, Name: id, password: password})
	}
	if len(out.Databases) == 0 {
		return nil, nil
	}
	return out, nil
}

// WithDatabases attaches the bootstrap to a plan that acts on any stack. An
// apply with nothing to act on stays one that runs nothing, which is what
// makes re-running apply a habit rather than a risk. A rotated password
// changes the app's rendered environment, so it always arrives with an action.
func (p *Plan) WithDatabases(b *Bootstrap) {
	if b == nil || len(p.Actions) == 0 {
		return
	}
	p.Bootstrap = b
}

// How long to wait for a primary, and how often to ask. The first start of a
// fresh Spilo runs initdb before Patroni takes the leader key, which on a
// small host is tens of seconds; three minutes leaves room for a slow disk
// without leaving an operator watching a frozen terminal.
var (
	primaryWait = 180 * time.Second
	primaryPoll = 3 * time.Second
	sleep       = time.Sleep
)

// patroniCompose is how every Patroni command reaches the container: the
// infrastructure stack's compose file, which is where apply put it.
func patroniCompose(d deployment.Deployment) string {
	return d.ComposeCmd(infraStack) + " exec -T patroni"
}

// errReplica means this site's Patroni is not the leader, and names who is.
type errReplica struct{ leader string }

func (e errReplica) Error() string { return "replica of " + e.leader }

// waitForPrimary polls Patroni's /cluster until a member holds the leader key
// and is running, then reports whether that member is this site.
//
// A leader is this site, another of cluster.sites, or an error. Only the
// second is a replica: Patroni's member name is pinned to the site name, so a
// leader named anything else means that pin did not hold (on the first real
// host the member was named after the machine), and treating it as "someone
// else's job" skipped the databases and started every app with none. The
// error is immediate rather than polled out: a member's name does not change
// while it runs.
//
// /cluster rather than /primary because it answers two questions at once.
// /primary says only "not me" with a 503, which is the same answer a node
// still running initdb gives; /cluster names the leader, so a replica can be
// told apart from a cluster that has none yet (Patroni v4.1.0,
// docs/rest_api.rst, "GET /cluster"). curl runs inside the Spilo container,
// which installs it (zalando/spilo, postgres-appliance/build_scripts/
// prepare.sh), so the host needs nothing beyond Docker.
func waitForPrimary(plan *Plan, t Transport) error {
	command := fmt.Sprintf("%s curl -s --max-time 5 http://%s/cluster", patroniCompose(plan.Deployment), plan.Bootstrap.Patroni)
	infra := plan.Deployment.ComposeCmd(infraStack)
	attempts := int(primaryWait / primaryPoll)
	if attempts < 1 {
		attempts = 1
	}
	var last string
	for i := 0; i < attempts; i++ {
		out, err := t.Run(command)
		if err == nil {
			leader := patroniLeader(out)
			switch {
			case leader == plan.Site:
				return nil
			case leader != "" && contains(plan.Bootstrap.ClusterSites, leader):
				return errReplica{leader: leader}
			case leader != "":
				return fmt.Errorf(
					"%s: Patroni's leader is %q, which is not one of cluster.sites (%s), so no app database was created and no app stack was started. Each Patroni member's name must equal its site's name; read PATRONI_NAME in %s and `%s logs patroni` on the leader's host",
					plan.Site, leader, strings.Join(plan.Bootstrap.ClusterSites, ", "), plan.Deployment.Path(infraStack, "patroni.env"), infra)
			}
			last = strings.TrimSpace(out)
		} else {
			last = err.Error()
		}
		if i < attempts-1 {
			sleep(primaryPoll)
		}
	}
	return fmt.Errorf(
		"%s: no Patroni primary after %s, so no app database was created and no app stack was started. Last answer from %s:\n%s\nRead `%s logs patroni` on the host; \"waiting on etcd\" on a new deployment means a member of etcd.members has not been applied yet. Then apply again: it resumes here",
		plan.Site, primaryWait, plan.Bootstrap.Patroni, last, infra)
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// patroniLeader returns the running leader's name from a /cluster document,
// empty when there is none yet or the document is not one.
func patroniLeader(document string) string {
	var cluster struct {
		Members []struct {
			Name  string `json:"name"`
			Role  string `json:"role"`
			State string `json:"state"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(document), &cluster); err != nil {
		return ""
	}
	for _, member := range cluster.Members {
		if member.Role == "leader" && member.State == "running" {
			return member.Name
		}
	}
	return ""
}

// Psql is how the SQL reaches Postgres: over the container's own unix socket,
// as the superuser the rendered patroni.env names. Spilo's pg_hba trusts
// every local socket connection (zalando/spilo, postgres-appliance/scripts/
// configure_spilo.py, `local all all trust`), so no password is needed here
// and none is passed. -X skips any psqlrc, and ON_ERROR_STOP makes the first
// failure the exit status rather than a line in the output.
func Psql(d deployment.Deployment) string {
	return patroniCompose(d) + " psql -X -q -U postgres -d postgres -v ON_ERROR_STOP=1"
}

// NoStatementLog switches statement logging off for the session it starts.
// Every script that sets a password begins with it: Spilo ships
// log_statement = 'ddl' (configure_spilo.py), and CREATE ROLE and ALTER ROLE
// are DDL, so without it every password would be written to the server log in
// plaintext. log_min_error_statement covers the same text on a failure.
const NoStatementLog = "SET log_statement = 'none';\n" +
	"SET log_min_duration_statement = -1;\n" +
	"SET log_min_error_statement = 'panic';\n"

// bootstrapSQL is the whole script for one site, sent on stdin.
//
// Each step is idempotent, so a re-run converges rather than failing:
//
//   - the role is created only when missing
//   - its password is set every time, so rotating apps.<name>.database_password
//     and applying is the whole rotation
//   - the database is created only when missing, owned by the role
//
// CREATE DATABASE cannot run inside a transaction block, and so not inside a
// DO block either; `\gexec` runs a generated statement as its own top level
// command, which is the documented way to make it conditional.
//
// Statement logging is switched off for this session first, by
// NoStatementLog.
func bootstrapSQL(b *Bootstrap) string {
	var sql strings.Builder
	sql.WriteString(NoStatementLog)
	for _, db := range b.Databases {
		role, name := QuoteIdent(db.Role), QuoteLiteral(db.Role)
		fmt.Fprintf(&sql, "-- %s\n", db.App)
		fmt.Fprintf(&sql, "SELECT 'CREATE ROLE %s LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s)\\gexec\n", role, name)
		fmt.Fprintf(&sql, "ALTER ROLE %s WITH LOGIN PASSWORD %s;\n", role, QuoteLiteral(db.password))
		fmt.Fprintf(&sql, "SELECT 'CREATE DATABASE %s OWNER %s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = %s)\\gexec\n",
			QuoteIdent(db.Name), role, QuoteLiteral(db.Name))
	}
	return sql.String()
}

// QuoteIdent and QuoteLiteral are belt and braces: identifiers are already
// restricted to [a-z0-9_], but a password is any string, and the doubling
// rule is the whole of SQL literal quoting with standard_conforming_strings
// on, which is the default since PostgreSQL 9.1.
func QuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func QuoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ErrFoundingWait is the founding stop: this site's etcd is up and another
// founding member's is not, so its apply ends before the databases, and
// resumes when applied again once that member runs.
var ErrFoundingWait = errors.New("waiting on")

// ErrDatabasesWait is the replica's stop: Patroni here is a replica, and an
// app database or its role is not there yet, so its apply ends before any app
// stack, and resumes when applied again once the leader's site has created
// them.
var ErrDatabasesWait = errors.New("waiting for databases from")

// FoundingWaitError is the founding stop's cause, which is ErrFoundingWait:
// Site's etcd is up and Unstarted, the other founding members, have not
// started theirs.
type FoundingWaitError struct {
	Site      string
	Unstarted []string
}

func (e *FoundingWaitError) Error() string {
	return fmt.Sprintf("%s: etcd is %v %s", e.Site, ErrFoundingWait, strings.Join(e.Unstarted, ", "))
}

func (e *FoundingWaitError) Unwrap() error { return ErrFoundingWait }

// DatabasesWaitError is the replica's stop's cause, which is
// ErrDatabasesWait: Site waits for Leader, the Patroni leader's site, to
// create the app databases Missing.
type DatabasesWaitError struct {
	Site, Leader string
	Missing      []string
}

func (e *DatabasesWaitError) Error() string {
	return fmt.Sprintf("%s: %v %s: %s", e.Site, ErrDatabasesWait, e.Leader, strings.Join(e.Missing, ", "))
}

func (e *DatabasesWaitError) Unwrap() error { return ErrDatabasesWait }

// runBootstrap waits for a primary and creates what is missing. A replica
// creates nothing and stops unless every app database already exists;
// anything else that goes wrong stops the apply before any app stack is
// started.
func runBootstrap(plan *Plan, t Transport) error {
	if waiting := plan.Bootstrap.EtcdUnstarted; len(waiting) > 0 {
		return &ui.Problem{
			Hint: fmt.Sprintf("%s is waiting on %s to start etcd", plan.Site, strings.Join(waiting, ", ")),
			Explain: fmt.Sprintf(
				"The infrastructure stack is up. A new etcd cluster settles its version only once every founding member runs, and Patroni takes no leader key before that, so no app database was created and no app stack was started. Apply %s, then apply this site again: it resumes here.",
				strings.Join(waiting, ", then ")),
			Cause: &FoundingWaitError{Site: plan.Site, Unstarted: waiting},
		}
	}
	done := plan.step("wait for Patroni primary")
	err := waitForPrimary(plan, t)
	if _, replica := err.(errReplica); replica {
		done.Done("")
	} else {
		finish(done, err)
	}
	if err != nil {
		if replica, ok := err.(errReplica); ok {
			plan.leaderMoved(replica.leader)
			return replicaDatabases(plan, t, replica.leader)
		}
		return err
	}
	// Nothing to create means no step to report: a title naming no database
	// would only be noise.
	if len(plan.Bootstrap.Databases) == 0 {
		return nil
	}
	// The title stays short however many databases there are, since the
	// result column is fixed; each database is a detail of the step.
	title := fmt.Sprintf("bootstrap %d databases", len(plan.Bootstrap.Databases))
	if len(plan.Bootstrap.Databases) == 1 {
		title = "bootstrap database " + plan.Bootstrap.Databases[0].Name
	}
	done = plan.step(title)
	for _, db := range plan.Bootstrap.Databases {
		done.Detail("role %s owns database %s, for %s", db.Role, db.Name, db.App)
	}
	out, err := t.RunInput(Psql(plan.Deployment), bootstrapSQL(plan.Bootstrap))
	finish(done, err)
	if err != nil {
		// psql quotes the failing line back for a syntax error, and that line
		// can be the one carrying a password.
		for _, db := range plan.Bootstrap.Databases {
			out = strings.ReplaceAll(out, QuoteLiteral(db.password), "'[redacted]'")
			out = strings.ReplaceAll(out, db.password, "[redacted]")
		}
		return fmt.Errorf("%s: creating app roles and databases failed, so no app stack was started:\n%s", plan.Site, out)
	}
	for _, db := range plan.Bootstrap.Databases {
		plan.say("ensured role and database %s for %s", db.Name, db.App)
	}
	return nil
}

// replicaDatabases is a replica's part of the bootstrap: it creates nothing,
// since a replica is read only, and lets the apply go on to the app stacks
// only once every app database and its role is in its own catalogue, which
// streams the leader's. An app started before then fails to authenticate as
// a role nobody created and is restarted in a loop by Docker. A replica's
// Postgres that has only just started may not answer yet, so the read is
// polled like the wait for a primary.
func replicaDatabases(plan *Plan, t Transport, leader string) error {
	plan.say("Patroni here is a replica, and %s holds the leader and creates app roles and databases", leader)
	if len(plan.Bootstrap.Databases) == 0 {
		return nil
	}
	done := plan.step("check replica databases")
	attempts := int(primaryWait / primaryPoll)
	if attempts < 1 {
		attempts = 1
	}
	var out string
	var err error
	for i := 0; i < attempts; i++ {
		out, err = t.RunInput(Psql(plan.Deployment)+" -At", replicaSQL(plan.Bootstrap))
		if err == nil {
			break
		}
		if i < attempts-1 {
			sleep(primaryPoll)
		}
	}
	if err != nil {
		finish(done, err)
		return fmt.Errorf(
			"%s: Postgres here did not answer a read of its databases after %s, so no app stack was started:\n%s\nRead `%s logs patroni` on the host, then apply again: it resumes here",
			plan.Site, primaryWait, strings.TrimSpace(out), plan.Deployment.ComposeCmd(infraStack))
	}
	present := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, db := range plan.Bootstrap.Databases {
		if present[db.Name] {
			done.Detail("database %s and role %s exist, for %s", db.Name, db.Role, db.App)
		} else {
			missing = append(missing, db.Name)
		}
	}
	if len(missing) == 0 {
		done.Done("")
		return nil
	}
	done.Done("missing " + strings.Join(missing, ", "))
	return &ui.Problem{
		Hint: fmt.Sprintf("%s is waiting on %s to create databases %s", plan.Site, leader, strings.Join(missing, ", ")),
		Explain: fmt.Sprintf(
			"The infrastructure stack is up. Patroni here is a replica, and the primary, on %s, creates the app roles and databases, so no app stack was started. Apply %s, then apply this site again: it resumes here.",
			leader, leader),
		Cause: &DatabasesWaitError{Site: plan.Site, Leader: leader, Missing: missing},
	}
}

// replicaSQL lists, one per line, each app database that exists and whose
// role does too. It names databases and roles and nothing else: no password
// is read or sent.
func replicaSQL(b *Bootstrap) string {
	var want []string
	for _, db := range b.Databases {
		want = append(want, fmt.Sprintf("(%s, %s)", QuoteLiteral(db.Role), QuoteLiteral(db.Name)))
	}
	return "SELECT want.name FROM (VALUES " + strings.Join(want, ", ") + ") AS want(role, name)" +
		" WHERE EXISTS (SELECT FROM pg_database WHERE datname = want.name)" +
		" AND EXISTS (SELECT FROM pg_roles WHERE rolname = want.role);\n"
}
