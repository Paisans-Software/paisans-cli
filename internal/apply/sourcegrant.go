package apply

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// mbinAllowlist is every table and column of Mbin's database the digest's
// reader is granted SELECT on, and nothing else is. Mbin keeps credentials
// there (sessions, remember me and OAuth tokens, and the password hash and
// TOTP secrets on "user"), so the grant names columns one by one and never a
// whole table that holds a secret.
//
// It must match, exactly, Allowlist in internal/mbin/allowlist.go of the
// paisans-engagement-service repository, which lists what the service's
// queries read. A query reading a column missing here fails at run time
// rather than widening the grant, and a column Mbin drops fails the grant at
// apply rather than going unnoticed.
var mbinAllowlist = map[string][]string{
	"entry":                 {"id", "user_id", "magazine_id", "slug", "title", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
	"entry_comment":         {"id", "user_id", "entry_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
	"post":                  {"id", "user_id", "magazine_id", "slug", "body", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
	"post_comment":          {"id", "user_id", "post_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
	"entry_vote":            {"user_id", "entry_id", "choice"},
	"post_vote":             {"user_id", "post_id", "choice"},
	"favourite":             {"user_id", "entry_id", "post_id"},
	"bookmark":              {"user_id", "entry_id", "post_id"},
	"notification":          {"user_id", "status", "entry_id", "entry_comment_id", "post_id", "post_comment_id"},
	"magazine":              {"id", "name", "title", "ap_id", "is_adult", "visibility", "marked_for_deletion_at", "ap_deleted_at"},
	"magazine_subscription": {"user_id", "magazine_id"},
	"magazine_block":        {"user_id", "magazine_id"},
	"magazine_ban":          {"user_id", "magazine_id", "expired_at"},
	"user_block":            {"blocker_id", "blocked_id"},
	"user":                  {"id", "username", "email", "is_verified", "is_banned", "is_deleted", "marked_for_deletion_at", "visibility", "type", "ap_id", "oauth_oidc_id", "last_active", "created_at"},
}

// Reader is an engagement app's read only role on its source's database.
// The bootstrap creates the role with the app roles; the grant comes later,
// from runSourceGrants, since the tables it names exist only once the source
// app has started.
type Reader struct {
	// App is the engagement app the role is for, and Source the Mbin app
	// whose database it reads.
	App      string
	Source   string
	Role     string
	Database string
	// password is never printed and never placed on a command line.
	password string
}

// mbinTablesPending is what the grant says when Mbin has not created its
// tables yet, and what runSourceGrants looks for to report it.
const mbinTablesPending = "has not created its tables yet"

// grantSQL is the grant for one reader, sent on stdin. It connects to the
// source's database, since a table grant names a table in the database the
// session is in.
//
// The grant waits for Mbin's tables rather than failing on them: on a first
// apply the reader is created before Mbin has run its migrations, and the
// next apply grants. Once `entry` exists, a table or column the list names
// and Mbin lacks is an error, which fails the apply loudly: a Mbin release
// that changed its schema needs the list changed on purpose.
//
// It grants and never revokes. What the reader keeps from PUBLIC is CONNECT
// and TEMP on the database and EXECUTE on functions in schema public, and
// none of those writes Mbin's tables: a temporary table lives in the
// session's own schema, and a function runs with its caller's rights unless
// it is SECURITY DEFINER, and Mbin's migrations create no function at all
// (read at v1.16.4). CONNECT is granted all the same, so the reader still
// connects after an operator revokes it from PUBLIC.
//
// Every statement is idempotent, so it runs on every apply. Identifiers are
// quoted here, by QuoteIdent, rather than by format('%I') in the block:
// they are static, and the SQL an operator reads in a log is then the SQL
// that ran.
func grantSQL(r Reader) string {
	role, database := QuoteIdent(r.Role), QuoteIdent(r.Database)
	var sql strings.Builder
	fmt.Fprintf(&sql, "\\connect %s\n", database)
	// \connect starts a new session, which has statement logging back on.
	sql.WriteString(NoStatementLog)
	sql.WriteString("DO $grant$\nBEGIN\n")
	sql.WriteString("  IF to_regclass('public.entry') IS NULL THEN\n")
	fmt.Fprintf(&sql, "    RAISE NOTICE %s;\n", QuoteLiteral(fmt.Sprintf("Mbin %s in database %s, so %s is granted nothing; the next apply grants it", mbinTablesPending, r.Database, r.Role)))
	sql.WriteString("    RETURN;\n  END IF;\n")
	fmt.Fprintf(&sql, "  GRANT CONNECT ON DATABASE %s TO %s;\n", database, role)
	fmt.Fprintf(&sql, "  GRANT USAGE ON SCHEMA public TO %s;\n", role)
	for _, table := range slices.Sorted(maps.Keys(mbinAllowlist)) {
		var columns []string
		for _, column := range mbinAllowlist[table] {
			columns = append(columns, QuoteIdent(column))
		}
		fmt.Fprintf(&sql, "  GRANT SELECT (%s) ON public.%s TO %s;\n", strings.Join(columns, ", "), QuoteIdent(table), role)
	}
	sql.WriteString("END\n$grant$;\n")
	return sql.String()
}

// runSourceGrants grants each reader its reads, once every app stack is
// healthy, Mbin's among them. It runs only on the site whose Patroni leads: a
// replica is read only, and the grant streams to it. A plan with no action
// asks Patroni here first, since it ran no bootstrap to find out.
//
// It runs on every apply of the leader's site, action or none, so a Mbin
// release that changed a column the allowlist names fails the next apply of
// that site loudly. A grant that finds Mbin's tables missing is owed, and the
// record of owed work keeps it until a grant succeeds.
//
// A grant owed on a site that is now a replica stays in its record: whether
// the site that leads has granted since is not known here. That site's next
// apply grants, and the note says so.
func runSourceGrants(plan *Plan, t Transport) error {
	if plan.Bootstrap == nil {
		return nil
	}
	if plan.grantsOnly && len(plan.Bootstrap.EtcdUnstarted) == 0 {
		if err := checkLeader(plan, t); err != nil {
			return err
		}
	}
	if !plan.primary {
		plan.grantsOwed = plannedGrants(plan.owedGrants, plan.Bootstrap.Readers)
		switch {
		case len(plan.grantsOwed) == 0:
		case plan.leader != "":
			plan.say("the grant of %s is owed here, but %s leads Patroni, and its next apply grants it", strings.Join(plan.grantsOwed, ", "), plan.leader)
		default:
			plan.say("the grant of %s is still owed, and runs on an apply of %s once its Patroni leads", strings.Join(plan.grantsOwed, ", "), plan.Site)
		}
		return nil
	}
	for _, r := range plan.Bootstrap.Readers {
		// A held app's reader may not exist yet: a pass with nothing to act
		// on runs no bootstrap, and the first pass on a site running Pocket
		// ID holds every other app back. The pass that releases it creates
		// the role before granting it.
		if plan.held[r.App] {
			if slices.Contains(plan.owedGrants, r.Role) {
				plan.grantsOwed = append(plan.grantsOwed, r.Role)
			}
			continue
		}
		done := plan.step("grant " + r.Role)
		done.Detail("SELECT on the allowlisted columns of database %s, for %s", r.Database, r.App)
		out, err := t.RunInput(Psql(plan.Deployment), grantSQL(r))
		if err != nil {
			done.Fail(err)
			// The script carries no password, so what psql quotes back of it
			// needs no redacting.
			return fmt.Errorf("%s: granting %s its reads on database %s failed. Every app stack is up, but %s cannot read Mbin until this succeeds. A table or column that does not exist means Mbin's schema changed from what the digest reads: until the toolkit's allowlist matches the new schema, remove the engagement app or hold Mbin on its previous release with apps.%s.images.app, then apply again:\n%s",
				plan.Site, r.Role, r.Database, r.App, r.Source, out)
		}
		if strings.Contains(out, mbinTablesPending) {
			done.Done("owed, waiting for Mbin's tables")
			plan.grantsOwed = append(plan.grantsOwed, r.Role)
			plan.say("Mbin has not created its tables in %s yet, so %s is granted nothing. The grant is owed: apply %s again once Mbin has started, and it runs then", r.Database, r.Role, plan.Site)
			continue
		}
		done.Done("")
	}
	return nil
}

// OwedGrants are the reader roles whose grant the record of owed work holds,
// which this apply runs if its site leads Patroni.
func (p *Plan) OwedGrants() []string { return p.owedGrants }

// checkLeader is a quiet apply's part of the bootstrap: whether this site's
// Patroni leads, and so whether it grants. There is nothing to create, so it
// is the wait for a primary and no more. A failure here leaves every app
// stack as it was, running, so it says that and not what a failed bootstrap
// says.
func checkLeader(plan *Plan, t Transport) error {
	done := plan.step("wait for Patroni primary")
	var roles []string
	for _, r := range plan.Bootstrap.Readers {
		roles = append(roles, r.Role)
	}
	roles = append(roles, plan.owedGrants...)
	slices.Sort(roles)
	roles = slices.Compact(roles)
	err := waitForPrimary(plan, t, fmt.Sprintf("app stacks are unchanged, and only the grant of %s did not run", strings.Join(roles, ", ")))
	if replica, ok := err.(errReplica); ok {
		done.Done("")
		plan.leader = replica.leader
		plan.say("Patroni here is a replica, and %s holds the leader and grants the readers their reads", replica.leader)
		return nil
	}
	finish(done, err)
	if err != nil {
		return err
	}
	plan.primary = true
	return nil
}

// plannedGrants is owed less any role no reader of this plan has: an owed
// grant for an engagement app that has since been removed has nothing left
// to grant.
func plannedGrants(owed []string, readers []Reader) []string {
	var out []string
	for _, role := range owed {
		if slices.ContainsFunc(readers, func(r Reader) bool { return r.Role == role }) {
			out = append(out, role)
		}
	}
	return out
}
