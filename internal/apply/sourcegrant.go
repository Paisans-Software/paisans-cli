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
	// App is the engagement app the role is for.
	App      string
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
// healthy, Mbin's among them. It runs only where the bootstrap ran the
// primary's script: a replica is read only, and the grant streams to it.
//
// A grant that finds Mbin's tables missing is owed, and the record of owed
// work keeps it, so the next apply of this site runs it even with nothing
// else to do. Without that, a leader's site applied before Mbin's first
// start, which is the order on a deployment whose leader holds no apps,
// would never grant: Mbin creating its tables changes no file of this site.
// A grant owed on a site that is now a replica stays owed until an apply of
// this site finds it leading.
func runSourceGrants(plan *Plan, t Transport) error {
	if plan.Bootstrap == nil {
		return nil
	}
	if !plan.primary {
		plan.grantsOwed = plan.owedGrants
		if len(plan.grantsOwed) > 0 {
			plan.say("the grant of %s is still owed, and runs on an apply of %s while its Patroni leads", strings.Join(plan.grantsOwed, ", "), plan.Site)
		}
		return nil
	}
	for _, r := range plan.Bootstrap.Readers {
		done := plan.step("grant " + r.Role)
		done.Detail("SELECT on the allowlisted columns of database %s, for %s", r.Database, r.App)
		out, err := t.RunInput(Psql(plan.Deployment), grantSQL(r))
		if err != nil {
			done.Fail(err)
			// The script carries no password, but psql quotes back what it
			// was sent, and redacting costs nothing.
			out = strings.ReplaceAll(out, QuoteLiteral(r.password), "'[redacted]'")
			out = strings.ReplaceAll(out, r.password, "[redacted]")
			return fmt.Errorf("%s: granting %s its reads on database %s failed. Every app stack is up, but %s cannot read Mbin until this succeeds. A table or column that does not exist means Mbin's schema changed from what the digest reads:\n%s",
				plan.Site, r.Role, r.Database, r.App, out)
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
