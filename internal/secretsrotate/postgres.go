package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/failover"
	"github.com/paisans-software/paisans-stack/internal/patroni"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// A Postgres password is checked by Postgres itself, and the roles Spilo
// creates at bootstrap, like a pinned app's own Postgres, read their password
// from the environment only when the data directory is first initialised. So
// rendering the next value changes nothing Postgres checks: stage 2 sets it
// with ALTER ROLE, on the Patroni leader (the only member that takes a write,
// and the one every replica streams the catalogue from) or inside a pinned
// app's own Postgres container.
//
// The SQL travels on stdin with statement logging off (apply.NoStatementLog),
// and the login that checks the next value reads it from stdin into
// PGPASSWORD inside the container, so no password reaches a server log, an
// argv or `ps`.

// How long a Patroni that was started again has to run and stream, or a
// switchover to complete, and how often to look.
var (
	pgWait = 3 * time.Minute
	pgPoll = 3 * time.Second
	sleep  = time.Sleep
)

// clusterRoles maps a cluster secret to the role Spilo creates for it and
// the variable patroni.env hands it in (render/templates/patroni.env.tmpl,
// lines 23 to 30: PGUSER_SUPERUSER=postgres, PGUSER_STANDBY=standby,
// PGUSER_ADMIN=admin). A login is what checks their password.
var clusterRoles = map[string]struct{ role, env string }{
	"superuser": {"postgres", "PGPASSWORD_SUPERUSER"},
	"admin":     {"admin", "PGPASSWORD_ADMIN"},
	"standby":   {"standby", "PGPASSWORD_STANDBY"},
}

// ErrKept is a secret kept rather than rotated, because nothing checks it.
// A KeptError wraps it, with the path and the reason for the plan to list.
var ErrKept = errors.New("kept")

// KeptError is the refusal of a rotation whose secret is kept: Path is the
// secret, and Reason is why nothing checks it.
type KeptError struct{ Path, Reason string }

func (e *KeptError) Error() string { return e.Path + " is kept: " + e.Reason }
func (e *KeptError) Unwrap() error { return ErrKept }

// AdminNoLogin is why cluster.admin_password is kept on a cluster whose admin
// role cannot log in: no login checks the password, so a new value would
// change nothing, and the rotation could never pass its own gate.
const AdminNoLogin = "the cluster's admin role is NOLOGIN, so no login checks its password and there is nothing to rotate. Spilo creates it so at the Spilo 4 tags render pins, which run Patroni 4 and so create no bootstrap users (postgres-appliance/scripts/post_init.sh, `CREATE ROLE admin CREATEDB` then `ALTER ROLE admin WITH CREATEDB NOLOGIN`)"

// pgIdentifier is what is put into a command line or SQL as a role or a
// database name, the same as apply allows when it creates them.
var pgIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// clusterRole rotates cluster.<name>_password: one of the roles Spilo creates
// at bootstrap, whose password every data site's patroni.env carries.
type clusterRole struct {
	Generated
	name, role, env string
}

// ClusterRole is the rotation of cluster.<name>_password, for name superuser
// (the role postgres), admin or standby. Which admin a cluster has depends on
// the Spilo it was bootstrapped with, so it is read from the cluster: an
// admin that logs in is rotated like standby, and one that cannot is refused
// before anything changes, with a KeptError. Postgres takes the next value on the
// leader first; then every data site's Patroni is started again with it, the
// replicas one at a time and the leader last, after a switchover to a replica
// that already runs with it, so that no step is a failover.
func ClusterRole(name string) Rotator {
	r := clusterRole{Generated: Generated{SecretPath: "cluster." + name + "_password"}, name: name}
	if known, ok := clusterRoles[name]; ok {
		r.role, r.env = known.role, known.env
	}
	return r
}

func (r clusterRole) Cost() Cost { return Cost{Reconnects: true} }

func (r clusterRole) Precheck(e Env) error {
	if err := r.known(e); err != nil {
		return err
	}
	if len(e.Cfg.Cluster.Sites) == 0 {
		return fmt.Errorf("the deployment declares no data site, so no Postgres holds this role")
	}
	return nil
}

// known refuses a name the cluster does not render, and an admin role that
// cannot log in, which is kept.
func (r clusterRole) known(e Env) error {
	if r.role == "" {
		return fmt.Errorf("the cluster renders no role for %q, only superuser, admin and standby", r.name)
	}
	if r.name != "admin" {
		return nil
	}
	ok, err := canLogin(e, r.role)
	if err != nil {
		return err
	}
	if !ok {
		return &KeptError{Path: r.SecretPath, Reason: AdminNoLogin}
	}
	return nil
}

// canLogin reads whether role may log in, from pg_roles on the leader over
// the container's trusted socket (apply.Psql), so no password is involved. A
// role that does not exist logs in no more than a NOLOGIN one.
func canLogin(e Env, role string) (bool, error) {
	leader, t, err := leaderSite(e)
	if err != nil {
		return false, err
	}
	sql := fmt.Sprintf("SELECT rolcanlogin FROM pg_roles WHERE rolname = %s;\n", apply.QuoteLiteral(role))
	out, err := t.RunInput(apply.Psql(e.Cfg.Deployment())+" -At", sql)
	if err != nil {
		return false, fmt.Errorf("%s: reading whether %s logs in: %w: %s", leader, role, err, lastLine(out))
	}
	switch strings.TrimSpace(out) {
	case "t":
		return true, nil
	case "f", "":
		return false, nil
	}
	return false, fmt.Errorf("%s: reading whether %s logs in: unexpected answer %q", leader, role, lastLine(out))
}

func (r clusterRole) Checked(e Env, next string) error {
	if err := r.known(e); err != nil {
		return err
	}
	return alterOnLeader(e, r.role, next)
}

func (r clusterRole) Accepts(e Env, next string) (bool, error) {
	if err := r.known(e); err != nil {
		return false, err
	}
	return loginOnLeader(e, r.role, "postgres", next)
}

// Users is every data site's infra stack, the replicas in the order
// cluster.sites lists them and the leader last. Each is a step of its own
// rather than a scoped apply, which is a no-op on a site whose patroni.env a
// stopped run already wrote and so would leave its Patroni running with the
// old value, and which on the leader would be a failover.
func (r clusterRole) Users(e Env) []Use {
	leader := ""
	if c, _, err := readCluster(e); err == nil {
		if m, ok := c.Leader(); ok {
			leader = m.Name
		}
	}
	var replicas, last []Use
	for _, site := range e.Cfg.Cluster.Sites {
		u := Use{Site: site, Stacks: []string{"infra"}, Do: r.present(site)}
		if site == leader {
			last = append(last, u)
		} else {
			replicas = append(replicas, u)
		}
	}
	return append(replicas, last...)
}

// present brings site's Patroni to the next value, and is safe to run again:
//
//  1. A Patroni already running with the next value is left alone, and only
//     waited for.
//  2. A site that leads switches the leader to a streaming replica that runs
//     with the next value first, as `site remove` does (failover.
//     SwitchoverCommand), so that starting its Patroni again is a replica
//     restart. With no other member there is nothing to switch to, and the
//     one Patroni starts again in place.
//  3. The site's infra stack is applied with s, which writes patroni.env, and
//     its Patroni is started again with apply.RecreatePatroni when the
//     running container still has the old value, which is the rollout
//     `site add` uses for a replica's patroni.env (apply.ReplicaEnv).
//  4. It waits for the container to run with the next value and its own
//     Patroni to answer running, and streaming when another member leads.
func (r clusterRole) present(site string) func(Env, *config.Secrets) error {
	return func(e Env, s *config.Secrets) error {
		next, _ := s.Get(r.SecretPath)
		if next == "" {
			return fmt.Errorf("%s: the secrets handed to it hold no value for %s", site, r.SecretPath)
		}
		t, ok := e.Sites[site]
		if !ok {
			return fmt.Errorf("%s: no way to reach it", site)
		}
		d := e.Cfg.Deployment()
		has, err := runsWith(d, t, r.env, next)
		if err != nil {
			return fmt.Errorf("%s: %w", site, err)
		}
		if !has {
			c, _, err := readCluster(e)
			if err != nil {
				return err
			}
			if l, ok := c.Leader(); ok && l.Name == site && len(c.Members) > 1 {
				if err := r.switchFrom(e, c, site, next); err != nil {
					return err
				}
			}
			if err := e.Apply(site, s, []string{"infra"}); err != nil {
				return err
			}
			if has, err = runsWith(d, t, r.env, next); err != nil {
				return fmt.Errorf("%s: %w", site, err)
			}
			if !has {
				if out, err := t.Run(apply.RecreatePatroni(d)); err != nil {
					return fmt.Errorf("%s: starting Patroni again: %w: %s", site, err, lastLine(out))
				}
			}
		}
		return r.waitRunning(e, site, next)
	}
}

// switchFrom moves the leader off site, to a streaming member that already
// runs with next: the Sync Standby where the cluster keeps one, which holds
// every acknowledged write, as `site remove` switches to.
func (r clusterRole) switchFrom(e Env, c patroni.Cluster, site, next string) error {
	d := e.Cfg.Deployment()
	var tried []string
	candidate := ""
	for _, m := range c.Members {
		if m.Name == site || m.State != "streaming" || !slices.Contains(e.Cfg.Cluster.Sites, m.Name) {
			continue
		}
		if e.Cfg.Cluster.Synchronous && m.Role != "sync_standby" {
			continue
		}
		t, ok := e.Sites[m.Name]
		if !ok {
			continue
		}
		has, err := runsWith(d, t, r.env, next)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if has {
			candidate = m.Name
			break
		}
		tried = append(tried, m.Name)
	}
	if candidate == "" {
		what := "no member streams"
		if e.Cfg.Cluster.Synchronous {
			what = "no Sync Standby streams"
		}
		if len(tried) > 0 {
			what = strings.Join(tried, ", ") + " still runs with the old value"
		}
		return fmt.Errorf("%s leads and its Patroni was not started again, which would be a failover: %s, so there is no member to switch the leader to that runs with the next value. Run again once every replica is applied", site, what)
	}
	if out, err := e.Sites[site].Run(failover.SwitchoverCommand(d, site, candidate)); err != nil {
		return fmt.Errorf("%s: switchover to %s: %w: %s", site, candidate, err, lastLine(out))
	}
	return poll(fmt.Sprintf("the switchover from %s to %s", site, candidate), func() error {
		c, _, err := readCluster(e)
		if err != nil {
			return err
		}
		if l, ok := c.Leader(); !ok || l.Name != candidate {
			return fmt.Errorf("%s does not lead yet", candidate)
		}
		return nil
	})
}

// waitRunning waits for site's container to run with next and its own
// Patroni to answer through its REST API (apply.OwnStatus): running, and
// streaming when the cluster has another member to stream from. The member
// list in etcd is not enough, since the old process wrote it and it can say
// streaming until its key expires.
func (r clusterRole) waitRunning(e Env, site, next string) error {
	d := e.Cfg.Deployment()
	t := e.Sites[site]
	api := fmt.Sprintf("%s:%d", e.Cfg.Sites[site].Address, render.PatroniAPIPort)
	return poll(site+"'s Patroni running with the next value", func() error {
		has, err := runsWith(d, t, r.env, next)
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("its Patroni still runs with the old value")
		}
		out, err := t.Run(apply.OwnStatus(d, api))
		if err != nil {
			return fmt.Errorf("its Patroni's REST API does not answer yet: %s", lastLine(out))
		}
		fields := strings.Fields(out)
		if len(fields) == 0 || fields[0] != "running" {
			return fmt.Errorf("its Patroni answers %q, not running", strings.TrimSpace(out))
		}
		c, _, err := readCluster(e)
		if err != nil {
			return err
		}
		if l, ok := c.Leader(); ok && l.Name != site && (len(fields) < 2 || fields[1] != "streaming") {
			return fmt.Errorf("its Patroni answers %q, not streaming from %s", strings.TrimSpace(out), l.Name)
		}
		return nil
	})
}

// appDatabase rotates apps.<app>.database_password.
type appDatabase struct {
	Generated
	app string
}

// AppDatabase is the rotation of apps.<app>.database_password. A clustered
// app's role is altered on the Patroni leader and every apps site's stack is
// applied in turn; a pinned app's role is altered inside its own Postgres
// container, whose POSTGRES_PASSWORD is read only at initdb, and its one
// stack is applied.
func AppDatabase(app string) Rotator {
	return appDatabase{Generated: Generated{SecretPath: "apps." + app + ".database_password"}, app: app}
}

func (r appDatabase) Cost() Cost { return Cost{Reconnects: true} }

// ownPostgres reports whether the app runs its own Postgres, which is how
// render decides it (render/plan.go, planApp: OwnsDB is a pinned app, and
// its kind has a postgres service exactly when blueprints.UsesPostgres).
func (r appDatabase) ownPostgres(e Env) bool {
	app := e.Cfg.Apps[r.app]
	return app.Placement.Mode == config.PlacementPinned && blueprints.UsesPostgres(app.Blueprint)
}

func (r appDatabase) role() string { return render.DBIdentifier(r.app) }

func (r appDatabase) Precheck(e Env) error {
	app, ok := e.Cfg.Apps[r.app]
	if !ok {
		return fmt.Errorf("the configuration declares no app %q", r.app)
	}
	if !blueprints.UsesPostgres(app.Blueprint) {
		return fmt.Errorf("apps.%s is a %s, which keeps no data in Postgres, so no role checks this password", r.app, app.Blueprint)
	}
	if !pgIdentifier.MatchString(r.role()) {
		return fmt.Errorf("apps.%s: its database role %q is not one apply creates", r.app, r.role())
	}
	return nil
}

func (r appDatabase) Checked(e Env, next string) error {
	if !r.ownPostgres(e) {
		return alterOnLeader(e, r.role(), next)
	}
	site, t, err := r.pinnedSite(e)
	if err != nil {
		return err
	}
	role := r.role()
	if !pgIdentifier.MatchString(role) {
		return fmt.Errorf("apps.%s: its database role %q is not one apply creates", r.app, role)
	}
	command := fmt.Sprintf("%s exec -T postgres psql -X -q -U %s -d %s -v ON_ERROR_STOP=1", e.Cfg.Deployment().ComposeCmd(r.app), role, role)
	if out, err := t.RunInput(command, alterSQL(role, next)); err != nil {
		return fmt.Errorf("%s: ALTER ROLE %s in %s's own Postgres: %w: %s", site, role, r.app, err, redact(out, next))
	}
	return nil
}

// Accepts logs in to the pinned app's own Postgres by the compose service
// name, as the app does: the official image trusts loopback (its
// entrypoint runs initdb with no --auth, whose pg_hba trusts local and
// loopback connections, and appends `host all all all scram-sha-256`:
// docker-library/postgres, docker-entrypoint.sh, pg_setup_hba_conf), so only
// a connection over the stack's network is asked for a password.
func (r appDatabase) Accepts(e Env, next string) (bool, error) {
	role := r.role()
	if !r.ownPostgres(e) {
		return loginOnLeader(e, role, role, next)
	}
	site, t, err := r.pinnedSite(e)
	if err != nil {
		return false, err
	}
	ok, err := login(t, e.Cfg.Deployment().ComposeCmd(r.app)+" exec -T postgres", "postgres", role, role, next)
	if err != nil {
		return false, fmt.Errorf("%s: %w", site, err)
	}
	return ok, nil
}

// Users is every site running the app, as render places it: the one it is
// pinned to, or every apps site.
func (r appDatabase) Users(e Env) []Use {
	app := e.Cfg.Apps[r.app]
	if app.Placement.Mode == config.PlacementPinned {
		return []Use{{Site: app.Placement.Site, Stacks: []string{r.app}}}
	}
	var out []Use
	for _, site := range e.Cfg.AppsSites() {
		out = append(out, Use{Site: site, Stacks: []string{r.app}})
	}
	return out
}

func (r appDatabase) pinnedSite(e Env) (string, apply.Transport, error) {
	site := e.Cfg.Apps[r.app].Placement.Site
	t, ok := e.Sites[site]
	if !ok {
		return site, nil, fmt.Errorf("%s: no way to reach it", site)
	}
	return site, t, nil
}

// alterSQL sets role's password, with statement logging off first.
func alterSQL(role, password string) string {
	return apply.NoStatementLog + fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s;\n", apply.QuoteIdent(role), apply.QuoteLiteral(password))
}

// alterOnLeader sets role's password on the Patroni leader, over its
// container's own socket as the superuser (apply.Psql).
func alterOnLeader(e Env, role, next string) error {
	if !pgIdentifier.MatchString(role) {
		return fmt.Errorf("%q is not a role apply creates", role)
	}
	leader, t, err := leaderSite(e)
	if err != nil {
		return err
	}
	if out, err := t.RunInput(apply.Psql(e.Cfg.Deployment()), alterSQL(role, next)); err != nil {
		return fmt.Errorf("%s: ALTER ROLE %s on the leader: %w: %s", leader, role, err, redact(out, next))
	}
	return nil
}

// loginOnLeader reports whether role logs in to database with password on
// the Patroni leader.
func loginOnLeader(e Env, role, database, password string) (bool, error) {
	leader, t, err := leaderSite(e)
	if err != nil {
		return false, err
	}
	ok, err := login(t, patroni.Exec(e.Cfg.Deployment()), "127.0.0.1", role, database, password)
	if err != nil {
		return false, fmt.Errorf("%s: %w", leader, err)
	}
	return ok, nil
}

// login runs psql in the container exec reaches, as role over TCP to host,
// where pg_hba asks for a password: the local socket is trusted and would
// take any. In Spilo that is 127.0.0.1, which its pg_hba checks with md5
// (zalando/spilo, postgres-appliance/scripts/configure_spilo.py,
// `host all all 127.0.0.1/32 md5`). The password is read from stdin into PGPASSWORD inside the
// container, and -w makes a missing one a failure rather than a prompt. A
// refused password is false; anything else that stops the login is an
// error, since it says nothing about the password.
func login(t apply.Transport, exec, host, role, database, password string) (bool, error) {
	if !pgIdentifier.MatchString(role) || !pgIdentifier.MatchString(database) {
		return false, fmt.Errorf("%q or %q is not a name apply creates", role, database)
	}
	if strings.ContainsAny(password, "\n\x00") {
		return false, fmt.Errorf("the password holds a newline or a NUL byte, which no login can send")
	}
	command := fmt.Sprintf(`%s sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -X -w -A -t -h %s -U %s -d %s -c "SELECT 1"'`, exec, host, role, database)
	out, err := t.RunInput(command, password+"\n")
	if err == nil {
		if strings.TrimSpace(out) != "1" {
			return false, fmt.Errorf("a login as %s answered %q", role, lastLine(redact(out, password)))
		}
		return true, nil
	}
	if strings.Contains(out, "password authentication failed") {
		return false, nil
	}
	return false, fmt.Errorf("a login as %s did not get as far as its password: %w: %s", role, err, lastLine(redact(out, password)))
}

// runsWith reports whether the site's running Patroni container has next in
// variable env. The container hashes its own value, so the password is
// compared without crossing the ssh connection in either direction.
func runsWith(d deployment.Deployment, t apply.Transport, env, next string) (bool, error) {
	out, err := t.Run(fmt.Sprintf(`%s sh -c 'printf %%s "$%s" | sha256sum'`, patroni.Exec(d), env))
	if err != nil {
		return false, fmt.Errorf("reading what its Patroni runs with: %w: %s", err, lastLine(out))
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return false, fmt.Errorf("reading what its Patroni runs with: no answer")
	}
	h := sha256.Sum256([]byte(next))
	return fields[0] == hex.EncodeToString(h[:]), nil
}

// readCluster reads /cluster from the first data site whose Patroni answers
// with a running leader, and returns the site that answered.
func readCluster(e Env) (patroni.Cluster, string, error) {
	d := e.Cfg.Deployment()
	var errs []error
	for _, site := range e.Cfg.Cluster.Sites {
		t, ok := e.Sites[site]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: no way to reach it", site))
			continue
		}
		api := fmt.Sprintf("%s:%d", e.Cfg.Sites[site].Address, render.PatroniAPIPort)
		out, err := t.Run(patroni.ClusterCommand(d, api))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w: %s", site, err, lastLine(out)))
			continue
		}
		c, err := patroni.Parse(out)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", site, err))
			continue
		}
		if _, ok := c.Leader(); !ok {
			errs = append(errs, fmt.Errorf("%s: Patroni reports no running leader", site))
			continue
		}
		return c, site, nil
	}
	return patroni.Cluster{}, "", fmt.Errorf("no data site's Patroni names a running leader: %w", errors.Join(errs...))
}

// leaderSite is the site whose Patroni leads, and the way to reach it.
func leaderSite(e Env) (string, apply.Transport, error) {
	c, _, err := readCluster(e)
	if err != nil {
		return "", nil, err
	}
	l, _ := c.Leader()
	if !slices.Contains(e.Cfg.Cluster.Sites, l.Name) {
		return "", nil, fmt.Errorf("Patroni's leader is %q, which is not one of cluster.sites", l.Name)
	}
	t, ok := e.Sites[l.Name]
	if !ok {
		return "", nil, fmt.Errorf("%s leads, and there is no way to reach it", l.Name)
	}
	return l.Name, t, nil
}

// poll runs check until it passes or pgWait has gone by.
func poll(what string, check func() error) error {
	return pollWithin(what, pgWait, pgPoll, check)
}

// pollWithin runs check every interval until it passes or wait has gone by.
func pollWithin(what string, wait, interval time.Duration, check func() error) error {
	n := 1
	if interval > 0 && wait/interval > 0 {
		n = int(wait / interval)
	}
	var last error
	for i := 0; i < n; i++ {
		if last = check(); last == nil {
			return nil
		}
		if i < n-1 {
			sleep(interval)
		}
	}
	return fmt.Errorf("%s did not happen within %s: %w", what, wait, last)
}

// redact takes a password out of output: psql quotes a failing line back,
// and that line can be the one that carries it.
func redact(out, password string) string {
	if password == "" {
		return out
	}
	out = strings.ReplaceAll(out, apply.QuoteLiteral(password), "'[redacted]'")
	return strings.ReplaceAll(out, password, "[redacted]")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
