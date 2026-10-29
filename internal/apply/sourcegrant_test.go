package apply_test

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The toolkit's copy of the allowlist is the grant, and the service's copy is
// what its queries read. Written out here rather than compared at run time,
// since the two live in different repositories: a change to either has to be
// made to this table as well, on purpose.
func TestAllowlistMatchesService(t *testing.T) {
	want := map[string][]string{
		"entry":                 {"id", "user_id", "magazine_id", "slug", "title", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
		"entry_comment":         {"id", "user_id", "entry_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
		"post":                  {"id", "user_id", "magazine_id", "slug", "body", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
		"post_comment":          {"id", "user_id", "post_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
		"entry_vote":            {"user_id", "entry_id", "choice", "created_at"},
		"post_vote":             {"user_id", "post_id", "choice", "created_at"},
		"favourite":             {"user_id", "entry_id", "post_id"},
		"bookmark":              {"user_id", "entry_id", "post_id"},
		"notification":          {"user_id", "status", "entry_id", "entry_comment_id", "post_id", "post_comment_id"},
		"magazine":              {"id", "name", "title", "ap_id", "is_adult", "visibility", "marked_for_deletion_at", "ap_deleted_at"},
		"magazine_subscription": {"user_id", "magazine_id"},
		"magazine_block":        {"user_id", "magazine_id"},
		"magazine_ban":          {"user_id", "magazine_id", "expired_at"},
		"user_block":            {"blocker_id", "blocked_id"},
		"user":                  {"id", "username", "email", "is_verified", "is_banned", "is_deleted", "marked_for_deletion_at", "visibility", "type", "ap_id", "oauth_oidc_id", "last_active", "created_at", "application_status"},
	}
	got := apply.MbinAllowlist
	if a, b := slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)); !slices.Equal(a, b) {
		t.Fatalf("tables %v, want %v", a, b)
	}
	for table, columns := range want {
		if a, b := slices.Sorted(slices.Values(got[table])), slices.Sorted(slices.Values(columns)); !slices.Equal(a, b) {
			t.Errorf("%s: columns %v, want %v", table, a, b)
		}
	}
}

// A role that can read a session, a token or a password hash can log in as a
// member. None of them is ever in the grant.
func TestAllowlistExcludesCredentials(t *testing.T) {
	for _, table := range []string{
		"sessions", "rememberme_token", "oauth2_access_token", "oauth2_refresh_token",
		"oauth2_authorization_code", "oauth2_client", "oauth2_user_consent",
		"reset_password_request", "user_push_subscription", "site", "settings",
		"messenger_messages", "message", "message_thread", "user_note", "ap_activity", "activity",
	} {
		if _, ok := apply.MbinAllowlist[table]; ok {
			t.Errorf("the grant names table %s", table)
		}
	}
	oauth := regexp.MustCompile(`^oauth_.*_id$`)
	for table, columns := range apply.MbinAllowlist {
		for _, column := range columns {
			switch {
			case slices.Contains([]string{"password", "totp_secret", "totp_backup_codes", "private_key", "old_private_key", "ip"}, column):
				t.Errorf("the grant names %s.%s", table, column)
			case oauth.MatchString(column) && column != "oauth_oidc_id":
				t.Errorf("the grant names %s.%s, an identity at another provider", table, column)
			}
		}
	}
}

func fixtureReaders(t *testing.T) *apply.Bootstrap {
	t.Helper()
	cfg, secrets := fixture(t)
	b, err := apply.Databases(cfg, secrets, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Readers) != 1 {
		t.Fatalf("the fixture's digest has %d readers, want 1: %+v", len(b.Readers), b.Readers)
	}
	return b
}

// The bootstrap creates the role, with read only transactions by default as
// defence in depth (the role can turn that off; the boundary is the SELECT
// only column grants). It grants nothing: Mbin's tables do not exist before
// its first start.
func TestBootstrapCreatesReadOnlyReader(t *testing.T) {
	b := fixtureReaders(t)
	if r := b.Readers[0]; r.Role != "digest_reader" || r.Database != "talk" {
		t.Errorf("reader %s on %s, want digest_reader on talk", r.Role, r.Database)
	}
	sql := apply.BootstrapSQL(b)
	for _, want := range []string{
		`SELECT 'CREATE ROLE "digest_reader" LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'digest_reader')\gexec`,
		`ALTER ROLE "digest_reader" WITH LOGIN PASSWORD 'fixture-not-a-secret-digest-reader';`,
		`ALTER ROLE "digest_reader" SET default_transaction_read_only = on;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the script is missing\n  %s\nin\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "GRANT") {
		t.Errorf("the bootstrap grants, before Mbin has created its tables:\n%s", sql)
	}
	if strings.Contains(sql, `CREATE DATABASE "digest_reader"`) {
		t.Error("the reader is given a database of its own")
	}
}

// A deployment whose Mbin is pinned has validation refusing it, but a
// bootstrap reached without validation must still not create a reader for a
// database the cluster does not hold.
func TestNoReaderForAPinnedMbin(t *testing.T) {
	cfg, secrets := fixture(t)
	talk := cfg.Apps["talk"]
	talk.Placement = config.Placement{Mode: config.PlacementPinned, Site: "home-a"}
	cfg.Apps["talk"] = talk
	b, err := apply.Databases(cfg, secrets, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Readers) != 0 {
		t.Errorf("a reader was planned on a pinned Mbin: %+v", b.Readers)
	}
}

// The grant waits for Mbin's tables rather than failing on them: on a first
// apply the reader exists before Mbin has run its migrations.
func TestGrantSQLWaitsForMbinTables(t *testing.T) {
	sql := apply.GrantSQL(fixtureReaders(t).Readers[0])
	connect := strings.Index(sql, "\\connect \"talk\"\n")
	guard := strings.Index(sql, "IF to_regclass('public.entry') IS NULL THEN")
	notice := strings.Index(sql, "RAISE NOTICE")
	grant := strings.Index(sql, "GRANT")
	if connect < 0 || guard < 0 || notice < 0 || grant < 0 {
		t.Fatalf("missing a part: connect %d, guard %d, notice %d, grant %d in\n%s", connect, guard, notice, grant, sql)
	}
	if !(connect < guard && guard < notice && notice < grant) {
		t.Errorf("out of order: connect %d, guard %d, notice %d, grant %d in\n%s", connect, guard, notice, grant, sql)
	}
	if strings.Contains(sql, "REVOKE") {
		t.Errorf("the grant revokes:\n%s", sql)
	}
}

// SELECT is granted column by column, so a column the list leaves out is one
// the role cannot read, and "user" is quoted, since it is a reserved word.
func TestGrantSQLColumnGrants(t *testing.T) {
	sql := apply.GrantSQL(fixtureReaders(t).Readers[0])
	for _, want := range []string{
		`GRANT CONNECT ON DATABASE "talk" TO "digest_reader";`,
		`GRANT USAGE ON SCHEMA public TO "digest_reader";`,
		`GRANT SELECT ("id", "username", "email", "is_verified", "is_banned", "is_deleted", "marked_for_deletion_at", "visibility", "type", "ap_id", "oauth_oidc_id", "last_active", "created_at") ON public."user" TO "digest_reader";`,
		`GRANT SELECT ("user_id", "magazine_id") ON public."magazine_subscription" TO "digest_reader";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the grant is missing\n  %s\nin\n%s", want, sql)
		}
	}
	if got, want := strings.Count(sql, "GRANT SELECT"), len(apply.MbinAllowlist); got != want {
		t.Errorf("%d SELECT grants, want one per table, %d", got, want)
	}
	if regexp.MustCompile(`GRANT SELECT ON|ALL TABLES`).MatchString(sql) {
		t.Errorf("a table is granted whole:\n%s", sql)
	}
	if strings.Contains(sql, "fixture-not-a-secret") {
		t.Error("the grant carries a password it has no use for")
	}
}

// orderedHost records each script sent on stdin among the commands, so a
// test can place it in the order the apply ran things. grantFails refuses
// the grant script alone.
type orderedHost struct {
	*fakeHost
	grantFails bool
	// tablesMissing answers the grant as Postgres does before Mbin's first
	// start: the guard's notice, and success.
	tablesMissing bool
}

func (h *orderedHost) RunInput(command, stdin string) (string, error) {
	h.commands = append(h.commands, "stdin: "+stdin)
	if h.grantFails && strings.Contains(stdin, `\connect`) {
		h.inputs = append(h.inputs, stdin)
		return "ERROR: column \"is_verified\" does not exist\n" + stdin, fmt.Errorf("exit status 3")
	}
	if h.tablesMissing && strings.Contains(stdin, `\connect`) {
		h.inputs = append(h.inputs, stdin)
		return "NOTICE:  Mbin has not created its tables yet in database talk, so digest_reader is granted nothing; the next apply grants it\n", nil
	}
	return h.fakeHost.RunInput(command, stdin)
}

func (h *orderedHost) WriteFile(path, content string, mode uint32) error {
	h.commands = append(h.commands, "write: "+path)
	return h.fakeHost.WriteFile(path, content, mode)
}

// The grant runs once every app stack is healthy, Mbin's among them, since
// only Mbin's first start creates the tables it names; and before the
// manifest, so an apply whose grant failed is not recorded as done.
func TestGrantsRunAfterAppStacks(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	p := bootstrapPlan(t, host.fakeHost)
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	grant := host.indexOf(`stdin: \connect "talk"`)
	if grant < 0 {
		t.Fatalf("no grant ran: %v", host.commands)
	}
	for _, action := range p.Actions {
		switch i := host.indexOf("/srv/paisans/f2a9/" + action.Stack + "/compose.yaml up -d"); {
		case i < 0:
			t.Errorf("%s never started", action.Stack)
		case i > grant:
			t.Errorf("%s was started after the grant", action.Stack)
		}
	}
	manifest := -1
	for i, command := range host.commands {
		if command == "write: "+p.Deployment.Manifest() {
			manifest = i
		}
	}
	if manifest < grant {
		t.Errorf("the final manifest was not written after the grant: manifest %d, grant %d", manifest, grant)
	}
	if _, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; owed {
		t.Error("an apply whose grant succeeded still owes something")
	}
	for _, command := range host.fakeHost.commands {
		if strings.Contains(command, "fixture-not-a-secret-digest-reader") && !strings.HasPrefix(command, "stdin: ") {
			t.Errorf("the reader's password is on a command line: %s", command)
		}
	}
}

// A grant that fails, a column Mbin renamed, stops the apply loudly rather
// than widening the grant, and leaves the apply owing its last stack so the
// next apply comes back to it.
func TestAFailedGrantFailsTheApply(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), grantFails: true}
	p := bootstrapPlan(t, host.fakeHost)
	err := apply.Execute(p, host)
	if err == nil {
		t.Fatal("a failed grant passed")
	}
	for _, want := range []string{"is_verified", "remove the engagement app", "apps.talk.images.app", "then apply again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
	}
	if _, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; !owed {
		t.Error("a failed grant leaves the apply owing nothing, so the next apply does not come back to it")
	}
}

// A reader is named for its app, and a name the cluster reserves would have
// the reader's password set on a role Postgres or Patroni owns.
func TestAReaderCannotTakeAReservedRole(t *testing.T) {
	cfg, secrets := fixture(t)
	if _, err := apply.SourceReaders(cfg, secrets, "pg_digest", "talk"); err == nil || !strings.Contains(err.Error(), "reserves") {
		t.Errorf("a reader named pg_digest_reader was planned: %v", err)
	}
}

// Without an engagement app there is nothing to grant and no psql to run for
// it.
func TestNoGrantWithoutAReader(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	p := bootstrapPlan(t, host.fakeHost)
	p.Bootstrap.Readers = nil
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if host.ran(`\connect`) {
		t.Error("a grant ran with no reader planned")
	}
}

// A replica grants nothing: it is read only, and the grant streams to it
// from the leader. It waits for the reader the leader creates, as it does
// for the app roles.
func TestAReplicaGrantsNothingAndWaitsForTheReader(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "talk", "wiki"}
	err := apply.Execute(bootstrapPlan(t, host.fakeHost), host)
	if err == nil || !strings.Contains(err.Error(), "digest_reader") {
		t.Fatalf("a replica without the reader went on: %v", err)
	}
	if host.ran(`\connect`) {
		t.Error("a replica ran the grant")
	}

	host = &orderedHost{fakeHost: newHost()}
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "digest_reader", "talk", "wiki"}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	if host.ran(`\connect`) {
		t.Error("a replica ran the grant")
	}
}

// A grant that finds no Mbin tables yet is owed, not done: the next apply of
// the site runs it even with nothing else to do, which is the only way it
// runs where the leader's site applied before Mbin's first start. Once it
// succeeds nothing is owed, and an apply with nothing to do runs nothing.
func TestAGrantWaitingForMbinsTablesIsOwed(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), tablesMissing: true}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	record, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]
	if !owed || !strings.Contains(record, "digest_reader") {
		t.Fatalf("a grant that waited for Mbin's tables is not owed: %q", record)
	}

	host.tablesMissing = false
	host.commands = nil
	again := bootstrapPlan(t, host.fakeHost)
	if len(again.Actions) != 0 {
		t.Fatalf("the second apply has actions, so it does not test a quiet apply: %+v", again.Actions)
	}
	if again.Bootstrap == nil {
		t.Fatal("an apply owing a grant attaches no bootstrap, so the grant never runs")
	}
	if err := apply.Execute(again, host); err != nil {
		t.Fatal(err)
	}
	if !host.ran(`stdin: \connect "talk"`) {
		t.Fatalf("the owed grant did not run: %v", host.commands)
	}
	if _, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; owed {
		t.Error("a grant that succeeded is still owed")
	}

	host.commands = nil
	third := bootstrapPlan(t, host.fakeHost)
	if len(third.Actions) != 0 || len(third.OwedGrants()) != 0 {
		t.Errorf("an apply owing nothing still plans work: owed %v, actions %+v", third.OwedGrants(), third.Actions)
	}
}

// A Mbin upgrade changes no file on a leader's site that holds no apps, so a
// grant that ran only with an action would never meet Mbin's new schema.
// Every apply of the leading site grants again, with the leader check and the
// grant alone: the roles and databases already exist.
func TestAQuietApplyOfTheLeaderGrants(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	host.commands, host.inputs = nil, nil
	again := bootstrapPlan(t, host.fakeHost)
	if len(again.Actions) != 0 {
		t.Fatalf("the second apply has actions, so it does not test a quiet apply: %+v", again.Actions)
	}
	if again.Bootstrap == nil {
		t.Fatal("a quiet apply of a deployment with a reader attaches no bootstrap, so the grant never reruns")
	}
	said := &ui.Recorder{}
	again.Report = said
	if err := apply.Execute(again, host); err != nil {
		t.Fatal(err)
	}
	if !host.ran(`stdin: \connect "talk"`) {
		t.Fatalf("a quiet apply of the leader did not grant: %v", host.commands)
	}
	if host.created() {
		t.Errorf("a quiet apply ran the bootstrap: %v", host.inputs)
	}
	for _, step := range []string{"wait for Patroni primary", "grant digest_reader"} {
		if !said.Has("step", step) {
			t.Errorf("no %q step:\n%s", step, said.Lines())
		}
	}
	if _, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; owed {
		t.Error("a quiet apply whose grant succeeded owes something")
	}
}

// A quiet apply of a replica that owes no grant asks who leads and runs no
// SQL: the leader's own apply grants.
func TestAQuietApplyOfAReplicaGrantsNothing(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "digest_reader", "talk", "wiki"}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	host.commands, host.inputs = nil, nil
	again := bootstrapPlan(t, host.fakeHost)
	if len(again.Actions) != 0 {
		t.Fatalf("the second apply has actions, so it does not test a quiet apply: %+v", again.Actions)
	}
	if err := apply.Execute(again, host); err != nil {
		t.Fatal(err)
	}
	if len(host.inputs) != 0 {
		t.Errorf("a quiet apply of a replica ran SQL: %v", host.inputs)
	}
	if _, owed := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; owed {
		t.Error("a quiet apply of a replica owes something")
	}
}

// A site that owed a grant and is now a replica keeps the record, since the
// grant is not known to have run, and says the site that does lead grants it
// on its next apply.
func TestAReplicaOwingAGrantNamesTheLeader(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), tablesMissing: true}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	host.tablesMissing = false
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "digest_reader", "talk", "wiki"}
	p := bootstrapPlan(t, host.fakeHost)
	said := &ui.Recorder{}
	p.Report = said
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if !said.Has("detail", "home-b leads Patroni, and its next apply grants it") {
		t.Errorf("the owed grant's note does not name the leader:\n%s", said.Lines())
	}
}

// A replica keeps an owed grant only for a reader the deployment still
// plans: once the engagement app is gone there is nothing left to grant.
func TestAReplicaDropsAGrantForAReaderNoLongerPlanned(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), tablesMissing: true}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	host.tablesMissing = false
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "digest_reader", "talk", "wiki"}
	p := bootstrapPlan(t, host.fakeHost)
	p.Bootstrap.Readers = nil
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	if record := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; strings.Contains(record, "digest_reader") {
		t.Errorf("a replica kept a grant for a reader no longer planned: %q", record)
	}
}

// A grant still waiting stays owed through an apply that also waits.
func TestAGrantStillWaitingStaysOwed(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), tablesMissing: true}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	if record := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; !strings.Contains(record, "digest_reader") {
		t.Errorf("a grant that waited twice is no longer owed: %q", record)
	}
}

// A site owing a grant that has become a replica grants nothing, since a
// replica is read only, and keeps owing it.
func TestAReplicaKeepsAnOwedGrant(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost(), tablesMissing: true}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	host.tablesMissing = false
	host.commands = nil
	host.leader = "home-b"
	host.replicaHas = []string{"account", "auth", "digest", "digest_reader", "talk", "wiki"}
	if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
		t.Fatal(err)
	}
	if host.ran(`\connect`) {
		t.Error("a replica ran the grant")
	}
	if record := host.files["/srv/paisans/f2a9/.paisans-pending.json"]; !strings.Contains(record, "digest_reader") {
		t.Errorf("a replica dropped an owed grant: %q", record)
	}
}

// A quiet apply that cannot tell who leads fails, since the grant did not
// run, and says what it left alone: every app stack is as it was, so the
// bootstrap's "no app stack was started" would be untrue here.
func TestAQuietApplyThatFindsNoLeaderSaysTheAppsAreUntouched(t *testing.T) {
	for _, c := range []struct {
		name, leader, want string
	}{
		{"no primary", "", "no Patroni primary after"},
		{"a leader outside cluster.sites", "stranger", `Patroni's leader is "stranger"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			noWait(t)
			host := &orderedHost{fakeHost: newHost()}
			if err := apply.Execute(bootstrapPlan(t, host.fakeHost), host); err != nil {
				t.Fatal(err)
			}
			host.commands, host.inputs = nil, nil
			host.leader = c.leader
			again := bootstrapPlan(t, host.fakeHost)
			if len(again.Actions) != 0 {
				t.Fatalf("the second apply has actions, so it does not test a quiet apply: %+v", again.Actions)
			}
			err := apply.Execute(again, host)
			if err == nil {
				t.Fatal("a quiet apply that found no leader passed")
			}
			for _, want := range []string{c.want, "app stacks are unchanged", "only the grant of digest_reader did not run", "logs patroni", "then apply again"} {
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
					t.Errorf("the failure does not say %q:\n%v", want, err)
				}
			}
			if strings.Contains(err.Error(), "no app stack was started") || strings.Contains(err.Error(), "no app database was created") {
				t.Errorf("the failure says the bootstrap's consequence, which is untrue here:\n%v", err)
			}
			if host.ran(`\connect`) {
				t.Error("the grant ran with no leader known")
			}
		})
	}
}

// heldBootstrapPlan is bootstrapPlan holding the named app stacks back.
func heldBootstrapPlan(t *testing.T, host *fakeHost, held ...string) *apply.Plan {
	t.Helper()
	cfg, secrets := fixture(t)
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Except(held...))
	if err != nil {
		t.Fatal(err)
	}
	b, err := apply.Databases(cfg, secrets, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	p.WithDatabases(b)
	return p
}

// A site that runs Pocket ID applies first with every other app held back.
// When the digest is new, that pass has nothing to act on, so it runs no
// bootstrap and its reader's role does not exist yet. The held digest's
// grant waits for the pass that starts it, which creates the role first.
func TestAHeldReaderIsNotGranted(t *testing.T) {
	noWait(t)
	host := &orderedHost{fakeHost: newHost()}
	if err := apply.Execute(heldBootstrapPlan(t, host.fakeHost, "digest"), host); err != nil {
		t.Fatal(err)
	}
	host.commands, host.inputs = nil, nil
	first := heldBootstrapPlan(t, host.fakeHost, "digest")
	if len(first.Actions) != 0 {
		t.Fatalf("the held pass has actions, so it does not test a quiet one: %+v", first.Actions)
	}
	if err := apply.Execute(first, host); err != nil {
		t.Fatal(err)
	}
	if host.ran(`stdin: \connect "talk"`) {
		t.Errorf("a pass holding the digest back granted its reader: %v", host.commands)
	}

	host.commands, host.inputs = nil, nil
	final := bootstrapPlan(t, host.fakeHost)
	if _, ok := actionOn(final, "digest"); !ok {
		t.Fatalf("the pass releasing the digest does not start it: %+v", final.Actions)
	}
	if err := apply.Execute(final, host); err != nil {
		t.Fatal(err)
	}
	create, grant := host.indexOf(`CREATE ROLE "digest_reader"`), host.indexOf(`stdin: \connect "talk"`)
	switch {
	case create < 0:
		t.Errorf("the pass releasing the digest did not create its reader: %v", host.commands)
	case grant < 0:
		t.Errorf("the pass releasing the digest did not grant its reader: %v", host.commands)
	case grant < create:
		t.Errorf("the reader was granted before it was created: create %d, grant %d", create, grant)
	}
}
