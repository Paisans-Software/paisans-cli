package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// pgWorld is a fake Patroni cluster on three data sites, with one clustered
// app and one pinned app that runs its own Postgres. It holds what Postgres
// accepts for each role, what each site's patroni.env says on disk and what
// its running Patroni was started with, and every command run, with its
// stdin.
type pgWorld struct {
	t      *testing.T
	cfg    *config.Config
	store  *memStore
	leader string
	// sync is the member Patroni reports as the Sync Standby.
	sync string
	// roles is the password the cluster accepts for each role.
	roles map[string]string
	// pinned is the password each pinned app's own Postgres accepts.
	pinned map[string]string
	// file and running are each site's PGPASSWORD_* values, as its
	// patroni.env has them and as its running Patroni was started with.
	file, running map[string]map[string]string
	// apps is the database password each site's app stack presents, keyed
	// site/app.
	apps map[string]string
	// adminLogin is an admin role that may log in, as Spilo 3 bootstraps it;
	// false is the NOLOGIN admin Spilo 4's post_init.sh creates.
	adminLogin bool
	// loginErr is an answer to a login that is not about the password.
	loginErr error
	cmds     []pgCmd
	log      []string
}

type pgCmd struct{ site, command, stdin string }

type memStore struct{ s *config.Secrets }

func (m *memStore) Load() (*config.Secrets, error) { return roundTrip(m.s), nil }
func (m *memStore) Save(s *config.Secrets) error   { m.s = roundTrip(s); return nil }

func roundTrip(s *config.Secrets) *config.Secrets {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var out config.Secrets
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return &out
}

var pgEnvVars = map[string]string{
	"superuser_password": "PGPASSWORD_SUPERUSER",
	"admin_password":     "PGPASSWORD_ADMIN",
	"standby_password":   "PGPASSWORD_STANDBY",
}

func newPGWorld(t *testing.T) *pgWorld {
	t.Helper()
	oldSleep, oldWait, oldPoll := sleep, pgWait, pgPoll
	sleep, pgWait, pgPoll = func(time.Duration) {}, 3*time.Second, time.Second
	t.Cleanup(func() { sleep, pgWait, pgPoll = oldSleep, oldWait, oldPoll })

	both := []config.Role{config.RoleData, config.RoleApps}
	cfg := &config.Config{
		ID: "3f2a6c1e-8b4d-4e2a-9c1f-7d5e6b8a9c0d",
		Sites: map[string]config.Site{
			"home-a": {Roles: both, Address: "10.0.0.1"},
			"home-b": {Roles: both, Address: "10.0.0.2"},
			"home-c": {Roles: []config.Role{config.RoleData}, Address: "10.0.0.3"},
		},
		Cluster: config.Cluster{Sites: []string{"home-a", "home-b", "home-c"}, Synchronous: true},
		Apps: map[string]config.App{
			"forum": {Kind: config.KindMbin, Placement: config.Placement{Mode: config.PlacementCluster}},
			"notes": {Kind: config.KindOutline, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-b"}},
			"proxy": {Kind: config.KindOAuth2Proxy, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
		},
	}
	s := &config.Secrets{}
	s.Cluster.SuperuserPassword = "super-old"
	s.Cluster.AdminPassword = "admin-old"
	s.Cluster.StandbyPassword = "standby-old"
	s.Apps = map[string]map[string]any{
		"forum": {"database_password": "forum-old"},
		"notes": {"database_password": "notes-old"},
	}
	w := &pgWorld{
		t: t, cfg: cfg, store: &memStore{s: s},
		leader: "home-b", sync: "home-c",
		roles:  map[string]string{"postgres": "super-old", "admin": "admin-old", "standby": "standby-old", "forum": "forum-old"},
		pinned: map[string]string{"notes": "notes-old"},
		file:   map[string]map[string]string{}, running: map[string]map[string]string{},
		apps: map[string]string{"home-a/forum": "forum-old", "home-b/forum": "forum-old", "home-b/notes": "notes-old"},
	}
	for _, site := range cfg.Cluster.Sites {
		w.file[site] = map[string]string{"PGPASSWORD_SUPERUSER": "super-old", "PGPASSWORD_ADMIN": "admin-old", "PGPASSWORD_STANDBY": "standby-old"}
		w.running[site] = map[string]string{"PGPASSWORD_SUPERUSER": "super-old", "PGPASSWORD_ADMIN": "admin-old", "PGPASSWORD_STANDBY": "standby-old"}
	}
	return w
}

// env is the Env a rotation runs with. Apply writes what s renders: for
// infra, each site's patroni.env, and compose up -d starts Patroni again
// when that file changed; for an app stack, the database password it
// presents.
func (w *pgWorld) env() Env {
	sites := map[string]apply.Transport{}
	for name := range w.cfg.Sites {
		sites[name] = &pgHost{w: w, site: name}
	}
	return Env{
		Cfg:   w.cfg,
		Store: w.store,
		Sites: sites,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			for _, stack := range stacks {
				if stack == "infra" {
					changed := false
					for key, v := range pgEnvVars {
						value, _ := s.Get("cluster." + key)
						if w.file[site][v] != value {
							w.file[site][v] = value
							changed = true
						}
					}
					if changed {
						w.log = append(w.log, "apply "+site+" infra")
						if site == w.leader {
							w.log = append(w.log, "FAILOVER: apply recreated the leader's Patroni on "+site)
						}
						w.running[site] = maps(w.file[site])
					}
					continue
				}
				value, _ := s.Get("apps." + stack + ".database_password")
				if w.apps[site+"/"+stack] != value {
					w.apps[site+"/"+stack] = value
					w.log = append(w.log, "apply "+site+" "+stack)
				}
			}
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			w.log = append(w.log, "healthy "+site+" "+strings.Join(stacks, ","))
			return nil
		},
		Now: time.Now,
	}
}

func maps(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type pgHost struct {
	w    *pgWorld
	site string
}

func (h *pgHost) Describe() string { return h.site }
func (h *pgHost) ReadFile(string) (string, bool, error) {
	return "", false, errors.New("not read here")
}
func (h *pgHost) WriteFile(string, string, uint32) error { return errors.New("not written here") }

var (
	envVarRef  = regexp.MustCompile(`"\$(PGPASSWORD_[A-Z]+)"`)
	candidate  = regexp.MustCompile(`--candidate (\S+)`)
	loginUser  = regexp.MustCompile(` -U (\w+)`)
	alterRole  = regexp.MustCompile(`ALTER ROLE "(\w+)" WITH PASSWORD '((?:[^']|'')*)';`)
	composeApp = regexp.MustCompile(`/([\w-]+)/compose\.yaml exec -T postgres`)
)

func (h *pgHost) Run(command string) (string, error) {
	w := h.w
	w.cmds = append(w.cmds, pgCmd{site: h.site, command: command})
	switch {
	case strings.Contains(command, "/cluster"):
		var members []map[string]any
		for _, name := range w.cfg.Cluster.Sites {
			m := map[string]any{"name": name, "role": "replica", "state": "streaming", "lag": 0}
			if name == w.leader {
				m = map[string]any{"name": name, "role": "leader", "state": "running"}
			} else if name == w.sync {
				m["role"] = "sync_standby"
			}
			members = append(members, m)
		}
		out, _ := json.Marshal(map[string]any{"members": members})
		return string(out), nil
	case strings.Contains(command, "sha256sum"):
		m := envVarRef.FindStringSubmatch(command)
		if m == nil {
			w.t.Fatalf("unexpected env read: %s", command)
		}
		return sum(w.running[h.site][m[1]]) + "  -\n", nil
	case strings.Contains(command, "--force-recreate patroni"):
		w.running[h.site] = maps(w.file[h.site])
		w.log = append(w.log, "recreate "+h.site)
		if h.site == w.leader {
			w.log = append(w.log, "FAILOVER: recreated the leader's Patroni on "+h.site)
		}
		return "", nil
	case strings.Contains(command, "switchover"):
		m := candidate.FindStringSubmatch(command)
		w.log = append(w.log, fmt.Sprintf("switchover %s to %s", w.leader, m[1]))
		w.sync = w.leader
		w.leader = m[1]
		return "", nil
	case strings.Contains(command, "python3 -c"):
		if h.site == w.leader {
			return "running \n", nil
		}
		return "running streaming\n", nil
	}
	w.t.Fatalf("unexpected command on %s: %s", h.site, command)
	return "", nil
}

func (h *pgHost) RunInput(command, stdin string) (string, error) {
	w := h.w
	w.cmds = append(w.cmds, pgCmd{site: h.site, command: command, stdin: stdin})
	pinnedApp := ""
	if strings.Contains(command, "exec -T postgres") {
		m := composeApp.FindStringSubmatch(command)
		pinnedApp = m[1]
	}
	switch {
	case strings.Contains(command, "read -r PGPASSWORD"):
		if w.loginErr != nil {
			return "psql: error: connection refused", w.loginErr
		}
		role := loginUser.FindStringSubmatch(command)[1]
		if role == "admin" && pinnedApp == "" && !w.adminLogin {
			// Spilo's post_init.sh creates admin NOLOGIN under Patroni 4.
			return `psql: error: FATAL:  role "admin" is not permitted to log in`, errors.New("exit status 2")
		}
		want := w.roles[role]
		if pinnedApp != "" {
			want = w.pinned[pinnedApp]
		}
		if strings.TrimSuffix(stdin, "\n") != want {
			return fmt.Sprintf("psql: error: FATAL:  password authentication failed for user %q", role), errors.New("exit status 2")
		}
		return "1\n", nil
	case strings.Contains(command, "psql") && strings.Contains(stdin, "rolcanlogin"):
		if strings.Contains(command, "read -r") || !strings.Contains(stdin, "'admin'") {
			w.t.Fatalf("unexpected role read: %s / %s", command, stdin)
		}
		if w.adminLogin {
			return "t\n", nil
		}
		return "f\n", nil
	case strings.Contains(command, "psql"):
		if !strings.HasPrefix(stdin, apply.NoStatementLog) {
			w.t.Errorf("SQL that sets a password does not switch statement logging off first:\n%s", stdin)
		}
		m := alterRole.FindStringSubmatch(stdin)
		if m == nil {
			w.t.Fatalf("unexpected SQL: %s", stdin)
		}
		password := strings.ReplaceAll(m[2], "''", "'")
		if pinnedApp != "" {
			w.pinned[pinnedApp] = password
			w.log = append(w.log, "alter "+pinnedApp+"'s own role on "+h.site)
			return "", nil
		}
		if h.site != w.leader {
			return "ERROR: cannot execute ALTER ROLE in a read-only transaction", errors.New("exit status 3")
		}
		w.roles[m[1]] = password
		w.log = append(w.log, "alter role "+m[1]+" on "+h.site)
		return "", nil
	}
	w.t.Fatalf("unexpected command with stdin on %s: %s", h.site, command)
	return "", nil
}

// noSecretOnArgv fails when a value appears in any command string.
func (w *pgWorld) noSecretOnArgv(values ...string) {
	w.t.Helper()
	for _, c := range w.cmds {
		for _, v := range values {
			if v != "" && strings.Contains(c.command, v) {
				w.t.Errorf("a password is on a command line on %s: %s", c.site, c.command)
			}
		}
	}
}

func (w *pgWorld) rotate(r Rotator) error {
	w.t.Helper()
	e := w.env()
	run, err := Plan(e, []Rotator{r})
	if err != nil {
		return err
	}
	return run.Execute(e, nil)
}

func indexOf(log []string, entry string) int { return slices.Index(log, entry) }

func TestClusterRolePasswordOnlyOnStdin(t *testing.T) {
	w := newPGWorld(t)
	if err := w.rotate(ClusterRole("standby")); err != nil {
		t.Fatal(err)
	}
	next := w.store.s.Cluster.StandbyPassword
	if next == "standby-old" || next == "" {
		t.Fatalf("not promoted: %q", next)
	}
	if w.roles["standby"] != next {
		t.Fatalf("the cluster accepts %q for standby, want the promoted value", w.roles["standby"])
	}
	w.noSecretOnArgv(next, "standby-old")
	var sentInSQL, sentToLogin bool
	for _, c := range w.cmds {
		if strings.Contains(c.stdin, "ALTER ROLE") && strings.Contains(c.stdin, next) {
			sentInSQL = true
		}
		if strings.Contains(c.command, "read -r PGPASSWORD") && strings.TrimSpace(c.stdin) == next {
			sentToLogin = true
			if !strings.Contains(c.command, "-h 127.0.0.1") {
				t.Errorf("the login is not forced over TCP, where a password is asked for: %s", c.command)
			}
		}
	}
	if !sentInSQL || !sentToLogin {
		t.Fatalf("the next value was not sent on stdin to ALTER ROLE (%v) and to the login check (%v)", sentInSQL, sentToLogin)
	}
}

func TestClusterRoleNames(t *testing.T) {
	for role, want := range map[string]struct{ path, pgRole string }{
		"superuser": {"cluster.superuser_password", "postgres"},
		"standby":   {"cluster.standby_password", "standby"},
	} {
		t.Run(role, func(t *testing.T) {
			w := newPGWorld(t)
			r := ClusterRole(role)
			if r.Path() != want.path {
				t.Fatalf("path %q, want %q", r.Path(), want.path)
			}
			if !r.Cost().Reconnects {
				t.Fatal("cost does not say users reconnect")
			}
			if err := r.Checked(w.env(), "fresh"); err != nil {
				t.Fatal(err)
			}
			if w.roles[want.pgRole] != "fresh" {
				t.Fatalf("role %s was not altered: %v", want.pgRole, w.roles)
			}
		})
	}
	w := newPGWorld(t)
	if err := ClusterRole("replicator").(Prechecker).Precheck(w.env()); err == nil {
		t.Fatal("a role the cluster does not render was not refused")
	}
}

func TestClusterRoleLeaderFromCluster(t *testing.T) {
	w := newPGWorld(t)
	w.leader, w.sync = "home-a", "home-b"
	r := ClusterRole("superuser")
	var order []string
	for _, u := range r.Users(w.env()) {
		order = append(order, u.Site)
		if !slices.Equal(u.Stacks, []string{"infra"}) {
			t.Fatalf("%s: stacks %v, want infra", u.Site, u.Stacks)
		}
		if u.Do == nil {
			t.Fatalf("%s: no step of its own", u.Site)
		}
	}
	if !slices.Equal(order, []string{"home-b", "home-c", "home-a"}) {
		t.Fatalf("users %v, want the replicas then the leader home-a", order)
	}
	if err := r.Checked(w.env(), "fresh"); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.cmds {
		if strings.Contains(c.stdin, "ALTER ROLE") && c.site != "home-a" {
			t.Fatalf("ALTER ROLE ran on %s, not the leader", c.site)
		}
	}
}

func TestClusterRoleReplicasFirstLeaderAfterSwitchover(t *testing.T) {
	w := newPGWorld(t)
	if err := w.rotate(ClusterRole("superuser")); err != nil {
		t.Fatal(err)
	}
	next := w.store.s.Cluster.SuperuserPassword
	for _, entry := range w.log {
		if strings.HasPrefix(entry, "FAILOVER") {
			t.Fatalf("%s\nlog: %v", entry, w.log)
		}
	}
	a, c := indexOf(w.log, "apply home-a infra"), indexOf(w.log, "apply home-c infra")
	sw, b := indexOf(w.log, "switchover home-b to home-c"), indexOf(w.log, "apply home-b infra")
	if a < 0 || c < 0 || sw < 0 || b < 0 || !(a < c && c < sw && sw < b) {
		t.Fatalf("want home-a, home-c, a switchover from home-b to the sync standby, then home-b; log: %v", w.log)
	}
	if indexOf(w.log, "healthy home-b infra") < b {
		t.Fatalf("no health gate after the old leader: %v", w.log)
	}
	for _, site := range w.cfg.Cluster.Sites {
		if got := w.running[site]["PGPASSWORD_SUPERUSER"]; got != next {
			t.Fatalf("%s runs with %q, want the next value", site, got)
		}
	}
	w.noSecretOnArgv(next)
}

func TestClusterRoleLeaderStepIsSafeToRerun(t *testing.T) {
	w := newPGWorld(t)
	if err := w.rotate(ClusterRole("standby")); err != nil {
		t.Fatal(err)
	}
	s := roundTrip(w.store.s)
	w.log = nil
	r := ClusterRole("standby")
	for _, u := range r.Users(w.env()) {
		if err := u.Do(w.env(), s); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.log) != 0 {
		t.Fatalf("a re-run changed something: %v", w.log)
	}
}

func TestClusterRoleRecreatesWhereApplyChangedNothing(t *testing.T) {
	// A run that wrote a replica's patroni.env and stopped before its Patroni
	// started again: the scoped apply has nothing to write, and the running
	// Patroni still has the old value.
	w := newPGWorld(t)
	w.file["home-a"]["PGPASSWORD_SUPERUSER"] = "the-next"
	s := roundTrip(w.store.s)
	s.Cluster.SuperuserPassword = "the-next"
	u := ClusterRole("superuser").Users(w.env())[0]
	if u.Site != "home-a" {
		t.Fatalf("first user %s", u.Site)
	}
	if err := u.Do(w.env(), s); err != nil {
		t.Fatal(err)
	}
	if w.running["home-a"]["PGPASSWORD_SUPERUSER"] != "the-next" || indexOf(w.log, "recreate home-a") < 0 {
		t.Fatalf("home-a's Patroni was not started again with the next value: %v", w.log)
	}
}

func TestClusterRoleNoSwitchoverWithoutAReplicaThatPresentsIt(t *testing.T) {
	w := newPGWorld(t)
	s := roundTrip(w.store.s)
	s.Cluster.StandbyPassword = "the-next"
	users := ClusterRole("standby").Users(w.env())
	leader := users[len(users)-1]
	if leader.Site != "home-b" {
		t.Fatalf("last user %s, want the leader", leader.Site)
	}
	err := leader.Do(w.env(), s)
	if err == nil {
		t.Fatal("the leader was switched over to a replica that does not present the next value")
	}
	if len(w.log) != 0 {
		t.Fatalf("something changed: %v", w.log)
	}
	w.noSecretOnArgv("the-next")
}

func TestAppDatabaseClustered(t *testing.T) {
	w := newPGWorld(t)
	r := AppDatabase("forum")
	if r.Path() != "apps.forum.database_password" || !r.Cost().Reconnects {
		t.Fatalf("path %q cost %+v", r.Path(), r.Cost())
	}
	var users []string
	for _, u := range r.Users(w.env()) {
		users = append(users, u.Site+":"+strings.Join(u.Stacks, ","))
	}
	if !slices.Equal(users, []string{"home-a:forum", "home-b:forum"}) {
		t.Fatalf("users %v, want every apps site's forum", users)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	next, _ := w.store.s.Get("apps.forum.database_password")
	if w.roles["forum"] != next || next == "forum-old" {
		t.Fatalf("the cluster accepts %q for forum, the file holds %q", w.roles["forum"], next)
	}
	if w.apps["home-a/forum"] != next || w.apps["home-b/forum"] != next {
		t.Fatalf("apps present %v", w.apps)
	}
	if indexOf(w.log, "alter role forum on home-b") < 0 {
		t.Fatalf("not altered on the leader: %v", w.log)
	}
	w.noSecretOnArgv(next)
}

func TestAppDatabasePinnedAltersItsOwnContainer(t *testing.T) {
	w := newPGWorld(t)
	r := AppDatabase("notes")
	users := r.Users(w.env())
	if len(users) != 1 || users[0].Site != "home-b" || !slices.Equal(users[0].Stacks, []string{"notes"}) {
		t.Fatalf("users %+v, want notes on home-b", users)
	}
	if err := w.rotate(r); err != nil {
		t.Fatal(err)
	}
	next, _ := w.store.s.Get("apps.notes.database_password")
	if w.pinned["notes"] != next || next == "notes-old" {
		t.Fatalf("notes' own Postgres accepts %q, the file holds %q", w.pinned["notes"], next)
	}
	if w.roles["notes"] != "" {
		t.Fatal("the cluster was given a role for a pinned app")
	}
	want := w.cfg.Deployment().ComposeCmd("notes") + " exec -T postgres psql"
	found := false
	for _, c := range w.cmds {
		if strings.Contains(c.stdin, "ALTER ROLE") {
			if !strings.HasPrefix(c.command, want) || c.site != "home-b" || !strings.Contains(c.command, "-U notes") {
				t.Fatalf("ALTER ROLE ran as %q on %s, want inside notes' postgres on home-b", c.command, c.site)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no ALTER ROLE ran")
	}
	w.noSecretOnArgv(next)
}

func TestAppDatabaseRefusesAnAppWithoutPostgres(t *testing.T) {
	w := newPGWorld(t)
	for _, app := range []string{"proxy", "missing"} {
		if err := AppDatabase(app).(Prechecker).Precheck(w.env()); err == nil {
			t.Fatalf("%s: not refused", app)
		}
	}
	if err := AppDatabase("forum").(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
}

// Review Focus 1: a run stopped after stage 2, with Postgres already on the
// next value and the apps still on the old one, finishes with that value.
func TestResumeAfterCheckedFinishesWithTheSameValue(t *testing.T) {
	w := newPGWorld(t)
	w.store.s.Rotations = map[string]string{"apps.forum.database_password": "forum-next"}
	w.roles["forum"] = "forum-next"
	if err := w.rotate(AppDatabase("forum")); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.cmds {
		if strings.Contains(c.stdin, "ALTER ROLE") {
			t.Fatalf("ALTER ROLE ran again on a resumed rotation: %s", c.command)
		}
	}
	if got, _ := w.store.s.Get("apps.forum.database_password"); got != "forum-next" {
		t.Fatalf("promoted %q, want the value the stopped run left", got)
	}
	if w.apps["home-a/forum"] != "forum-next" || w.apps["home-b/forum"] != "forum-next" {
		t.Fatalf("apps present %v", w.apps)
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}

func TestAcceptsTellsAWrongPasswordFromAnUnanswerablePostgres(t *testing.T) {
	w := newPGWorld(t)
	r := ClusterRole("standby")
	if ok, err := r.Accepts(w.env(), "not-it"); ok || err != nil {
		t.Fatalf("a wrong password: %v, %v", ok, err)
	}
	if ok, err := r.Accepts(w.env(), "standby-old"); !ok || err != nil {
		t.Fatalf("the accepted password: %v, %v", ok, err)
	}
	w.loginErr = errors.New("exit status 2")
	if _, err := r.Accepts(w.env(), "standby-old"); err == nil {
		t.Fatal("a Postgres that does not answer read as one that refuses the password")
	}
}

// On a cluster whose admin is NOLOGIN, as Spilo 4 creates it, no login
// checks cluster.admin_password: its precheck refuses as kept, and the plan
// lists it as kept, before anything changes.
func TestClusterRoleAdminNoLoginIsKept(t *testing.T) {
	w := newPGWorld(t)
	err := ClusterRole("admin").(Prechecker).Precheck(w.env())
	var kept *KeptError
	if !errors.Is(err, ErrKept) || !errors.As(err, &kept) || kept.Path != "cluster.admin_password" || !strings.Contains(kept.Reason, "NOLOGIN") {
		t.Fatalf("admin was not refused as kept: %v", err)
	}
	run, err := Plan(w.env(), []Rotator{ClusterRole("admin")})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Items) != 0 || len(run.Kept) != 1 || run.Kept[0].Path != "cluster.admin_password" {
		t.Fatalf("items %v, kept %v", run.Items, run.Kept)
	}
	if err := run.Execute(w.env(), nil); err != nil {
		t.Fatal(err)
	}
	if len(w.store.s.Rotations) != 0 || w.store.s.Cluster.AdminPassword != "admin-old" {
		t.Fatalf("the secrets file changed: rotations %v, admin %q", w.store.s.Rotations, w.store.s.Cluster.AdminPassword)
	}
	if w.roles["admin"] != "admin-old" || len(w.log) != 0 {
		t.Fatalf("something changed: role %q, log %v", w.roles["admin"], w.log)
	}
	for _, c := range w.cmds {
		if strings.Contains(c.stdin, "ALTER ROLE") {
			t.Fatalf("ALTER ROLE ran: %s", c.command)
		}
	}
	if _, err := ClusterRole("admin").Accepts(w.env(), "admin-old"); err == nil {
		t.Fatal("Accepts on admin did not refuse")
	}
}

// On a cluster whose admin logs in, as Spilo 3 bootstraps it, the password is
// a live credential and is rotated like standby's.
func TestClusterRoleAdminThatLogsInIsRotated(t *testing.T) {
	w := newPGWorld(t)
	w.adminLogin = true
	if err := w.rotate(ClusterRole("admin")); err != nil {
		t.Fatal(err)
	}
	next := w.store.s.Cluster.AdminPassword
	if next == "admin-old" || w.roles["admin"] != next || len(w.store.s.Rotations) != 0 {
		t.Fatalf("not rotated: file %q, cluster %q, rotations %v", next, w.roles["admin"], w.store.s.Rotations)
	}
	for _, site := range w.cfg.Cluster.Sites {
		if w.running[site]["PGPASSWORD_ADMIN"] != next {
			t.Fatalf("%s runs with the old admin password", site)
		}
	}
	w.noSecretOnArgv(next)
}

// TestClusterRolesInOneRunFollowTheLeader runs superuser then standby in one
// Execute. The first moves the leader with its switchover, so the second must
// order its users by the leader as it stands when its stage 3 starts, not as
// it stood when the run was planned, or it would try to switch the leader
// away from a site whose replicas still run the old value.
func TestClusterRolesInOneRunFollowTheLeader(t *testing.T) {
	w := newPGWorld(t)
	e := w.env()
	run, err := Plan(e, []Rotator{ClusterRole("superuser"), ClusterRole("standby")})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(e, nil); err != nil {
		t.Fatal(err)
	}
	var switchovers []string
	for _, entry := range w.log {
		if strings.HasPrefix(entry, "switchover") {
			switchovers = append(switchovers, entry)
		}
	}
	// One switchover per role, each away from the leader as it then stood.
	if !slices.Equal(switchovers, []string{"switchover home-b to home-c", "switchover home-c to home-b"}) {
		t.Fatalf("switchovers %v, want home-b to home-c then home-c to home-b", switchovers)
	}
	for _, entry := range w.log {
		if strings.HasPrefix(entry, "FAILOVER") {
			t.Fatalf("a step was a failover: %v", w.log)
		}
	}
	if w.store.s.Cluster.SuperuserPassword == "super-old" || w.store.s.Cluster.StandbyPassword == "standby-old" {
		t.Fatalf("not both promoted: %+v", w.store.s.Cluster)
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations left: %v", w.store.s.Rotations)
	}
}
