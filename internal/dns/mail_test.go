package dns

import (
	"context"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const comment = "paisans-f2a9: created by paisans dns init"

// mailOnly are the mail records a deployment publishes or checks, without
// the leftovers it only looks for.
func mailOnly(wants []Want) map[string]Want {
	out := map[string]Want{}
	for _, w := range wants {
		if (w.Type == "TXT" || w.Type == "MX") && !w.Leftover {
			out[w.Type+" "+w.Name] = w
		}
	}
	return out
}

func wantsOf(t *testing.T, cfg *config.Config) map[string]Want {
	t.Helper()
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return mailOnly(wants)
}

// With no mail anywhere, the domain says it sends and receives none, on its
// own behalf and every name's under it, and every name it publishes says so
// for itself.
func TestNoMailLocksDownTheDomainAndEveryHostname(t *testing.T) {
	got := wantsOf(t, declared())
	want := map[string]string{
		"TXT example.org":              spfLockdown,
		"MX example.org":               nullMX,
		"TXT *._domainkey.example.org": dkimLockdown,
		"TXT _dmarc.example.org":       dmarcLockdown,
		"TXT talk.example.org":         spfLockdown,
		"TXT talk-media.example.org":   spfLockdown,
		"TXT docs.example.org":         spfLockdown,
		"TXT docs-media.example.org":   spfLockdown,
		"TXT vm.example.org":           spfLockdown,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mail records, want %d: %+v", len(got), len(want), got)
	}
	for key, content := range want {
		w, ok := got[key]
		if !ok || w.Content != content || w.Advisory {
			t.Errorf("%s: got %+v, want %q", key, w, content)
		}
	}
	if got["MX example.org"].Priority != 0 {
		t.Errorf("a null MX has preference 0")
	}
	if s := got["TXT talk.example.org"].Sources; len(s) != 1 || s[0] != "apps.talk.hostname" {
		t.Errorf("a hostname's SPF names the key that publishes the hostname: %v", s)
	}
}

// A domain whose mail somebody else runs gets none of the domain records, and
// its DMARC policy is only checked; its hostnames still say they send none.
func TestMailDomainLeavesTheDomainAlone(t *testing.T) {
	cfg := declared()
	cfg.Community.MailDomains = []string{"Example.org."}
	got := wantsOf(t, cfg)
	for _, key := range []string{"TXT example.org", "MX example.org", "TXT *._domainkey.example.org"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s is the mail provider's, but was wanted", key)
		}
	}
	dmarc, ok := got["TXT _dmarc.example.org"]
	if !ok || !dmarc.Advisory || dmarc.Sources[0] != "community.mail_domains" {
		t.Errorf("the domain's DMARC policy should be checked, not written: %+v", dmarc)
	}
	if _, ok := got["TXT talk.example.org"]; !ok {
		t.Errorf("a hostname under a mail domain still says it sends no mail")
	}
}

// A sender the toolkit configures makes its own domain a mail domain, so the
// toolkit never locks out its own mail.
func TestSMTPSenderIsAMailDomain(t *testing.T) {
	cfg := declared()
	cfg.SMTP = config.SMTP{FromAddress: "hello@example.org"}
	cfg.Apps["auth"] = config.App{Kind: config.KindPocketID, Hostname: "id.example.org", SMTP: &config.SMTP{FromAddress: "noreply@Talk.Example.org"}}
	// An app that sends no mail has no say, even with a block (validate
	// refuses one, but this must not depend on it).
	cfg.Apps["docs"] = config.App{Kind: config.KindOutline, Hostname: "docs.example.org", SMTP: &config.SMTP{FromAddress: "x@docs.example.org"}}

	names := MailNames(cfg)
	if s := names["example.org"]; len(s) != 1 || s[0] != "smtp.from_address" {
		t.Errorf("the deployment's sender: %v", names)
	}
	if s := names["talk.example.org"]; len(s) != 1 || s[0] != "apps.auth.smtp.from_address" {
		t.Errorf("an app's sender: %v", names)
	}
	if _, ok := names["docs.example.org"]; ok {
		t.Errorf("a kind that sends no mail made its sender a mail domain: %v", names)
	}

	got := wantsOf(t, cfg)
	if _, ok := got["TXT talk.example.org"]; ok {
		t.Errorf("talk.example.org sends mail and must get no SPF -all")
	}
	if _, ok := got["TXT example.org"]; ok {
		t.Errorf("example.org sends mail and must get no SPF -all")
	}
	if !got["TXT _dmarc.example.org"].Advisory {
		t.Errorf("the sending domain's DMARC is advisory")
	}
	if _, ok := got["TXT docs.example.org"]; !ok {
		t.Errorf("docs sends no mail, so it says so")
	}
}

func TestMailClassification(t *testing.T) {
	spf := Want{Type: "TXT", Name: "talk.example.org", Content: spfLockdown}
	mx := Want{Type: "MX", Name: "example.org", Content: nullMX}
	dkim := Want{Type: "TXT", Name: "*._domainkey.example.org", Content: dkimLockdown}
	dmarc := Want{Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown}
	advisory := dmarc
	advisory.Advisory = true
	advisory.Sources = []string{"community.mail_domains"}

	txt := func(content string) Record { return Record{Type: "TXT", Content: content} }
	cases := []struct {
		name    string
		want    Want
		records []Record
		action  Action
		detail  string
	}{
		{"spf missing", spf, nil, Create, ""},
		{"spf beside unrelated text", spf, []Record{txt("google-site-verification=abc"), {Type: "A", Content: "203.0.113.10"}}, Create, ""},
		{"spf present quoted", spf, []Record{txt(`"v=spf1  -all"`)}, Present, ""},
		{"spf present split", spf, []Record{txt(`"v=spf1 " "-all"`)}, Present, ""},
		{"spf that sends", spf, []Record{txt("v=spf1 include:_spf.example.net ~all")}, Conflict, "add it to community.mail_domains"},
		{"two spf", spf, []Record{txt("v=spf1 -all"), txt("v=spf1 -all")}, Conflict, "more than one"},
		{"cname", spf, []Record{{Type: "CNAME", Content: "elsewhere.example.net"}}, Conflict, "CNAME"},
		{"mx missing", mx, []Record{txt("v=spf1 -all")}, Create, ""},
		{"null mx present", mx, []Record{{Type: "MX", Content: ".", Priority: 0}}, Present, ""},
		{"real mx", mx, []Record{{Type: "MX", Content: "mx1.example.net", Priority: 10}}, Conflict, "receives mail"},
		{"dkim empty", dkim, []Record{txt("v=DKIM1; p=")}, Present, ""},
		{"dkim key", dkim, []Record{txt("v=DKIM1; k=rsa; p=MIGfMA0")}, Conflict, "signs mail"},
		{"dmarc with reports", dmarc, []Record{txt("v=DMARC1; p=reject; rua=mailto:r@example.net")}, Present, ""},
		{"dmarc beside other text", dmarc, []Record{txt("not a policy")}, Create, ""},
		{"dmarc weak", dmarc, []Record{txt("v=DMARC1; p=none")}, Conflict, "weaker"},
		{"dmarc weak subdomains", dmarc, []Record{txt("v=DMARC1; p=reject; sp=none")}, Conflict, "weaker"},
		{"dmarc partial", dmarc, []Record{txt("v=DMARC1; p=reject; pct=50")}, Conflict, "weaker"},
		{"advisory missing", advisory, nil, Warn, "there is none"},
		{"advisory weak", advisory, []Record{txt("v=DMARC1; p=quarantine")}, Warn, "weaker"},
		{"advisory strong", advisory, []Record{txt("v=DMARC1; p=reject; sp=reject; rua=mailto:r@example.net")}, Present, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, detail := classify(tc.want, tc.records, nil)
			if action != tc.action || !strings.Contains(detail, tc.detail) {
				t.Errorf("got %s %q, want %s containing %q", action, detail, tc.action, tc.detail)
			}
		})
	}
}

// The no-mail records are created at the provider like any other, the null
// MX with its preference of 0 sent rather than omitted, and they read back.
// The zone walk never asks whether a record's name is a zone.
func TestMailRecordsAreCreatedAndReadBack(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	c := fake.serve(t)
	p := plan(t, c, declared())
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	var mx *cfRecord
	for i, rec := range fake.posts {
		if rec.Type == "MX" {
			mx = &fake.posts[i]
		}
	}
	if mx == nil || mx.Priority == nil || *mx.Priority != 0 || mx.Content != "." || mx.Name != "example.org" {
		t.Fatalf("the null MX: %+v", mx)
	}
	for _, name := range fake.lookups {
		if strings.HasPrefix(name, "*") || strings.HasPrefix(name, "_") {
			t.Errorf("asked whether %s is a zone", name)
		}
	}
	again := plan(t, c, declared())
	if len(again.Creates()) != 0 || len(again.Conflicts()) != 0 {
		t.Fatalf("second run should be all present: %+v", again.Entries)
	}
}

// An advisory DMARC policy that is missing is a note, never a create and
// never a refusal, and Execute writes nothing for it.
func TestAdvisoryDMARCWarnsAndWritesNothing(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	c := fake.serve(t)
	cfg := declared()
	cfg.Community.MailDomains = []string{"example.org"}
	p := plan(t, c, cfg)
	if len(p.Conflicts()) != 0 {
		t.Fatalf("an advisory record is never a conflict: %+v", p.Conflicts())
	}
	for _, e := range p.Creates() {
		if e.Name == "_dmarc.example.org" {
			t.Fatalf("an advisory record is never created")
		}
	}
	shown := &ui.Recorder{}
	p.Show(shown)
	if !shown.Has("note", "TXT _dmarc.example.org is not this toolkit's to write") {
		t.Errorf("no note for the missing policy:\n%s", shown.Lines())
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	for _, rec := range fake.posts {
		if rec.Name == "_dmarc.example.org" {
			t.Fatalf("wrote the mail domain's DMARC policy: %+v", rec)
		}
	}
}

// When the domain is given mail of its own, the no-mail records dns init made
// there are prune's to remove, and nothing else at those names is.
func TestPruneRemovesNoMailRecordsAMailDomainNoLongerWants(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "spf", Type: "TXT", Name: "example.org", Content: `"v=spf1 -all"`, Comment: comment},
		{ID: "mx", Type: "MX", Name: "example.org", Content: ".", Priority: new(int), Comment: comment},
		{ID: "dkim", Type: "TXT", Name: "*._domainkey.example.org", Content: dkimLockdown, Comment: comment},
		{ID: "dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown, Comment: comment},
		// Still wanted: a hostname says it sends no mail whatever the
		// domain does.
		{ID: "talk", Type: "TXT", Name: "talk.example.org", Content: spfLockdown, Comment: comment},
		// The comment, but content dns init never writes.
		{ID: "edited", Type: "TXT", Name: "example.org", Content: "v=spf1 include:_spf.example.net -all", Comment: comment},
		{ID: "spf-at-dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: spfLockdown, Comment: comment},
		{ID: "real-mx", Type: "MX", Name: "example.org", Content: "mx.example.net", Comment: comment},
	}
	c := fake.serve(t)
	cfg := declared()
	cfg.Community.MailDomains = []string{"example.org"}
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPrune(context.Background(), c, cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	removes := map[string]bool{}
	for _, e := range p.Removes() {
		removes[e.ID] = true
	}
	for _, id := range []string{"spf", "mx", "dkim", "dmarc"} {
		if !removes[id] {
			t.Errorf("%s should be removed: %+v", id, p.Entries)
		}
	}
	for _, id := range []string{"talk", "edited", "spf-at-dmarc", "real-mx"} {
		if removes[id] {
			t.Errorf("%s should be kept", id)
		}
	}
	shown := &ui.Recorder{Verbose_: true}
	p.Show(shown)
	if !shown.Has("pending", `delete TXT example.org -> "v=spf1 -all"`) || !shown.Has("pending", "delete MX example.org -> 0 .") {
		t.Errorf("plan lines:\n%s", shown.Lines())
	}
}

// site remove's DNS stage removes addresses only.
func TestSiteRemovalLeavesNoMailRecords(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "a", Type: "A", Name: "home-a.example.org", Content: "198.51.100.20", Comment: comment},
		{ID: "spf", Type: "TXT", Name: "home-a.example.org", Content: spfLockdown, Comment: comment},
	}
	c := fake.serve(t)
	before := declared()
	before.Sites["home-a"] = config.Site{Roles: []config.Role{config.RoleData}, Address: "10.44.0.1", Endpoint: "home-a.example.org:51820", PublicAddress: "198.51.100.20"}
	after := declared()
	delete(after.Sites, "home-a")
	p, err := BuildSiteRemoval(context.Background(), c, before, "home-a", after)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Removes()) != 1 || p.Removes()[0].ID != "a" {
		t.Fatalf("want only the address removed: %+v", p.Entries)
	}
}

// zoneWithNoMailRecords is a zone holding every no-mail record dns init
// writes at the domain, plus one at talk, each with the comment given.
func zoneWithNoMailRecords(comment string) *fakeCloudflare {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "spf", Type: "TXT", Name: "example.org", Content: `"v=spf1 -all"`, Comment: comment},
		{ID: "mx", Type: "MX", Name: "example.org", Content: ".", Priority: new(int), Comment: comment},
		{ID: "dkim", Type: "TXT", Name: "*._domainkey.example.org", Content: dkimLockdown, Comment: comment},
		{ID: "dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "talk-spf", Type: "TXT", Name: "talk.example.org", Content: spfLockdown, Comment: comment},
	}
	return fake
}

// A no-mail record dns init made at a name that now has mail is a conflict,
// so dns init, and apply after it, stops until dns prune has removed it.
func TestNoMailRecordLeftAtAMailNameIsAConflict(t *testing.T) {
	fake := zoneWithNoMailRecords(comment)
	c := fake.serve(t)
	cfg := declared()
	cfg.Community.MailDomains = []string{"example.org"}
	cfg.SMTP = config.SMTP{FromAddress: "hello@talk.example.org"}
	cfg.Apps["auth"] = config.App{Kind: config.KindPocketID, Hostname: "id.example.org"}
	p := plan(t, c, cfg)

	got := map[string]Entry{}
	for _, e := range p.Conflicts() {
		got[e.Type+" "+e.Name] = e
	}
	for _, key := range []string{"TXT example.org", "MX example.org", "TXT *._domainkey.example.org", "TXT _dmarc.example.org", "TXT talk.example.org"} {
		e, ok := got[key]
		if !ok {
			t.Errorf("%s: no conflict for the no-mail record left there; conflicts: %+v", key, p.Conflicts())
			continue
		}
		if !strings.Contains(e.Detail, "paisans dns prune --execute") || !strings.Contains(e.Detail, "has mail of its own") {
			t.Errorf("%s: the conflict must say to prune: %q", key, e.Detail)
		}
	}
	if len(got) != 5 {
		t.Errorf("want 5 conflicts, got %+v", p.Conflicts())
	}
	shown := &ui.Recorder{}
	p.Show(shown)
	if i := shown.Index("refuse", "TXT example.org conflicts"); i < 0 || !strings.Contains(shown.Events[i].Extra, `remove with dns prune: example.org -> "v=spf1 -all" (zone example.org)`) {
		t.Errorf("the refusal should say what prune removes:\n%s", shown.Lines())
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err == nil || len(fake.posts) != 0 {
		t.Fatalf("execute must refuse and write nothing: err %v, %d post(s)", err, len(fake.posts))
	}

	// dns prune removes exactly those, and dns init then has nothing in its
	// way: a warning for the DMARC policy that is now the mail's to set.
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := BuildPrune(context.Background(), c, cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecutePrune(context.Background(), c, pruned, ui.Discard); err != nil {
		t.Fatal(err)
	}
	again := plan(t, c, cfg)
	if n := len(again.Conflicts()); n != 0 {
		t.Fatalf("after prune: %d conflict(s): %+v", n, again.Conflicts())
	}
	warned := false
	for _, e := range again.Entries {
		warned = warned || (e.Action == Warn && e.Name == "_dmarc.example.org")
	}
	if !warned {
		t.Errorf("after prune, the missing DMARC policy should be a warning")
	}
}

// Only this deployment's own no-mail records are its to flag. One with no
// comment, or another deployment's, is somebody's choice.
func TestNoMailRecordNotOursAtAMailNameIsLeftAlone(t *testing.T) {
	for _, other := range []string{"", "paisans-0b7d: created by paisans dns init"} {
		fake := zoneWithNoMailRecords(other)
		cfg := declared()
		cfg.Community.MailDomains = []string{"example.org"}
		cfg.SMTP = config.SMTP{FromAddress: "hello@talk.example.org"}
		cfg.Apps["auth"] = config.App{Kind: config.KindPocketID, Hostname: "id.example.org"}
		p := plan(t, fake.serve(t), cfg)
		if n := len(p.Conflicts()); n != 0 {
			t.Errorf("comment %q: %d conflict(s): %+v", other, n, p.Conflicts())
		}
	}
}

// A sender outside the deployment's zones is a mail name too, but dns init
// never wrote there, so it is not looked up at all.
func TestMailNameOutsideTheDomainIsNotRead(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	cfg := declared()
	cfg.SMTP = config.SMTP{FromAddress: "community@mail.example.net"}
	cfg.Apps["auth"] = config.App{Kind: config.KindPocketID, Hostname: "id.example.org"}
	plan(t, fake.serve(t), cfg)
	for _, name := range fake.lookups {
		if strings.HasSuffix(name, "example.net") {
			t.Errorf("looked up %s", name)
		}
	}
}
