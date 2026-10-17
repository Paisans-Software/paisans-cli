package dns

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const comment = "paisans-f2a9: created by paisans dns init"

// mailOnly are the mail records a deployment publishes or checks, without
// the leftovers it only looks for.
func mailOnly(wants []Want) map[string]Want {
	out := map[string]Want{}
	for _, w := range wants {
		if (w.Type == "TXT" || w.Type == "MX") && !w.Leftover && !w.Guard {
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
// own behalf and every name's under it. Its DMARC policy covers every
// hostname, so no hostname gets a record of its own.
func TestNoMailLocksDownTheDomain(t *testing.T) {
	got := wantsOf(t, declared())
	want := map[string]string{
		"TXT example.org":              spfLockdown,
		"MX example.org":               nullMX,
		"TXT *._domainkey.example.org": dkimLockdown,
		"TXT _dmarc.example.org":       dmarcLockdown,
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
}

// A hostname that is itself a registrable domain is covered by no policy the
// deployment may write, so it says it sends no mail with SPF.
func TestRegistrableHostnameGetsSPF(t *testing.T) {
	cfg := declared()
	cfg.Apps["shop"] = config.App{Blueprint: config.Blueprint("writefreely"), Hostname: "example.net"}
	got := wantsOf(t, cfg)
	if w := got["TXT example.net"]; w.Content != spfLockdown || len(w.Sources) != 1 || w.Sources[0] != "apps.shop.hostname" {
		t.Errorf("the registrable hostname's SPF: %+v", w)
	}
	if _, ok := got["TXT _dmarc.example.net"]; ok {
		t.Errorf("a registrable hostname must get no DMARC policy")
	}
}

// A domain whose mail somebody else runs gets none of the domain records, and
// its DMARC policy is only checked; each hostname gets its own, which that
// policy covers where it rejects.
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
	if w := got["TXT _dmarc.talk.example.org"]; w.Content != dmarcLockdown || w.CoveredBy != "_dmarc.example.org" {
		t.Errorf("a hostname under a mail domain gets its own policy, covered by the domain's: %+v", w)
	}
	if _, ok := got["TXT talk.example.org"]; ok {
		t.Errorf("a hostname gets no SPF of its own")
	}

	for policy, want := range map[string]Action{
		"v=DMARC1; p=reject; rua=mailto:r@example.net": Covered,
		"v=DMARC1; p=none": Create,
	} {
		fake := newFake()
		fake.zones["example.org"] = "zone-1"
		fake.records["zone-1"] = []cfRecord{
			{Type: "MX", Name: "example.org", Content: "mx.example.net", Priority: ptr(10)},
			{Type: "TXT", Name: "_dmarc.example.org", Content: policy},
		}
		p := plan(t, fake.serve(t), cfg)
		if e, _ := entryAt(p, "_dmarc.talk.example.org"); e.Action != want {
			t.Errorf("under %q: got %s, want %s", policy, e.Action, want)
		}
	}
}

// A sender the toolkit configures makes its own domain a mail domain, so the
// toolkit never locks out its own mail.
func TestSMTPSenderIsAMailDomain(t *testing.T) {
	cfg := declared()
	cfg.SMTP = config.SMTP{FromAddress: "hello@example.org"}
	cfg.Apps["auth"] = config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org", SMTP: &config.SMTP{FromAddress: "noreply@Talk.Example.org"}}
	// An app that sends no mail has no say, even with a block (validate
	// refuses one, but this must not depend on it).
	cfg.Apps["blog"] = config.App{Blueprint: config.Blueprint("writefreely"), Hostname: "blog.example.org", SMTP: &config.SMTP{FromAddress: "x@blog.example.org"}}

	names := MailNames(cfg)
	if s := names["example.org"]; len(s) != 1 || s[0] != "smtp.from_address" {
		t.Errorf("the deployment's sender: %v", names)
	}
	if s := names["talk.example.org"]; len(s) != 1 || s[0] != "apps.auth.smtp.from_address" {
		t.Errorf("an app's sender: %v", names)
	}
	if _, ok := names["blog.example.org"]; ok {
		t.Errorf("a blueprint that sends no mail made its sender a mail domain: %v", names)
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
	if w := got["TXT _dmarc.blog.example.org"]; w.Content != dmarcLockdown || w.CoveredBy != "_dmarc.example.org" {
		t.Errorf("blog sends no mail, so it says so: %+v", w)
	}
	if w := got["TXT _dmarc.talk.example.org"]; !w.Advisory {
		t.Errorf("talk sends mail, so its DMARC policy is only checked: %+v", w)
	}
}

// Mbin sends mail, so an Mbin app's own sender is a mail name the same way.
func TestMbinSenderIsAMailDomain(t *testing.T) {
	cfg := declared()
	cfg.Apps["forum"] = config.App{Blueprint: config.Blueprint("mbin"), Hostname: "forum.example.org", SMTP: &config.SMTP{FromAddress: "noreply@forum.example.org"}}
	names := MailNames(cfg)
	if s := names["forum.example.org"]; len(s) != 1 || s[0] != "apps.forum.smtp.from_address" {
		t.Errorf("an mbin app's sender: %v", names)
	}
	if _, ok := wantsOf(t, cfg)["TXT forum.example.org"]; ok {
		t.Errorf("forum.example.org sends mail and must get no SPF -all")
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

// An advisory DMARC policy that is missing, at a domain that receives mail,
// is a note, never a create and
// never a refusal, and Execute writes nothing for it.
func TestAdvisoryDMARCWarnsAndWritesNothing(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{ID: "mx", Type: "MX", Name: "example.org", Content: "mx.example.net", Priority: new(int)}}
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

// A domain that sends mail and receives none, with no DMARC policy, gets the
// sender's policy written: reject, with relaxed alignment, so mail a relay
// signs for a name under the same organizational domain still passes. Once
// written it reads back present, is never a leftover, and prune keeps it.
func TestSendOnlyDomainGetsItsDMARCPolicyWritten(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	c := fake.serve(t)
	cfg := declared()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@example.org"}
	p := plan(t, c, cfg)
	var dmarc *Entry
	for i, e := range p.Creates() {
		if e.Name == "_dmarc.example.org" {
			dmarc = &p.Creates()[i]
		}
	}
	if dmarc == nil || dmarc.Content != dmarcSender || dmarc.Advisory {
		t.Fatalf("the send-only domain's DMARC policy should be created: %+v", p.Entries)
	}
	shown := &ui.Recorder{}
	p.Show(shown)
	if !shown.Has("pending", `create TXT _dmarc.example.org -> "v=DMARC1; p=reject; sp=reject;"`) {
		t.Errorf("plan lines:\n%s", shown.Lines())
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	again := plan(t, c, cfg)
	if len(again.Creates()) != 0 || len(again.Conflicts()) != 0 {
		t.Fatalf("second run should be all present: %+v", again.Entries)
	}
	for _, e := range again.Entries {
		if e.Name == "_dmarc.example.org" && e.Action != Present {
			t.Errorf("the written policy reads back %s: %s", e.Action, e.Detail)
		}
	}
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := BuildPrune(context.Background(), c, cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pruned.Removes() {
		if e.Name == "_dmarc.example.org" {
			t.Errorf("prune would delete the sender's DMARC policy")
		}
	}
}

// A send-only domain's existing policy is somebody's: a weak one is still a
// note, never replaced and never a refusal.
func TestSendOnlyDomainWithAWeakPolicyIsANote(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{ID: "dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=none"}}
	c := fake.serve(t)
	cfg := declared()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@example.org"}
	p := plan(t, c, cfg)
	for _, e := range p.Entries {
		if e.Name == "_dmarc.example.org" && e.Action != Warn {
			t.Errorf("a weak policy at a send-only domain: got %s, want %s", e.Action, Warn)
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
		// A hostname's SPF is wanted no longer: DMARC covers it.
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
	for _, id := range []string{"spf", "mx", "dkim", "dmarc", "talk"} {
		if !removes[id] {
			t.Errorf("%s should be removed: %+v", id, p.Entries)
		}
	}
	for _, id := range []string{"edited", "spf-at-dmarc", "real-mx"} {
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
	cfg.Apps["auth"] = config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org"}
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
	// way. The domain now has mail but no MX, so it sends and receives none,
	// and its sender's DMARC policy is dns init's to write.
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
	created := false
	for _, e := range again.Creates() {
		created = created || (e.Name == "_dmarc.example.org" && e.Content == dmarcSender)
	}
	if !created {
		t.Errorf("after prune, the send-only domain's DMARC policy should be created: %+v", again.Entries)
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
		cfg.Apps["auth"] = config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org"}
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
	cfg.Apps["auth"] = config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org"}
	plan(t, fake.serve(t), cfg)
	for _, name := range fake.lookups {
		if strings.HasSuffix(name, "example.net") {
			t.Errorf("looked up %s", name)
		}
	}
}

// staged is declared with community.domain a name below the registrable
// domain, as a staging deployment inside a community's zone is.
func staged() *config.Config {
	cfg := declared()
	cfg.Community.Domain = "staging.example.org"
	site := cfg.Sites["vm"]
	site.Endpoint = "vm.staging.example.org:51820"
	cfg.Sites["vm"] = site
	cfg.Apps = map[string]config.App{
		"talk": {Blueprint: config.Blueprint("mbin"), Hostname: "talk.staging.example.org"},
	}
	return cfg
}

// Every mail name under the domain has its DMARC policy checked, and one
// that receives no mail gets the sender's policy written, as the domain does.
func TestEveryMailNameUnderTheDomainGetsItsDMARCChecked(t *testing.T) {
	cfg := declared()
	cfg.Apps["auth"] = config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org", SMTP: &config.SMTP{FromAddress: "noreply@id.example.org"}}
	got := wantsOf(t, cfg)
	if w := got["TXT _dmarc.id.example.org"]; !w.Advisory || w.Sources[0] != "apps.auth.smtp.from_address" {
		t.Fatalf("the app sender's DMARC policy should be checked: %+v", w)
	}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	p := plan(t, fake.serve(t), cfg)
	created := false
	for _, e := range p.Creates() {
		created = created || (e.Name == "_dmarc.id.example.org" && e.Content == dmarcSender)
	}
	if !created {
		t.Errorf("a send-only app sender's DMARC policy should be created: %+v", p.Entries)
	}
}

// Below the registrable domain, a receiver with no policy at a hostname's own
// _dmarc uses the registrable domain's, not community.domain's, so each
// hostname gets its own. At the registrable domain, the domain's covers them.
func TestHostnamesBelowTheRegistrableDomainGetTheirOwnDMARC(t *testing.T) {
	got := wantsOf(t, staged())
	for _, name := range []string{"talk.staging.example.org", "talk-media.staging.example.org", "vm.staging.example.org"} {
		w, ok := got["TXT _dmarc."+name]
		if !ok || w.Content != dmarcLockdown || w.Advisory {
			t.Errorf("%s: got %+v, want its own %q", name, w, dmarcLockdown)
		}
	}
	if w := got["TXT _dmarc.staging.example.org"]; w.Content != dmarcLockdown || w.Advisory {
		t.Errorf("the domain's own policy: %+v", w)
	}
	for key := range wantsOf(t, declared()) {
		if strings.HasPrefix(key, "TXT _dmarc.") && key != "TXT _dmarc.example.org" {
			t.Errorf("at the registrable domain, %s is not needed", key)
		}
	}
}

// entryAt is the plan's entry for the TXT record at name, and whether there
// is one.
func entryAt(p *Plan, name string) (Entry, bool) {
	for _, e := range p.Entries {
		if e.Type == "TXT" && e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// stagedDMARC are the no-mail DMARC policies staged() wants below the
// registrable domain.
var stagedDMARC = []string{
	"_dmarc.staging.example.org",
	"_dmarc.talk.staging.example.org",
	"_dmarc.talk-media.staging.example.org",
	"_dmarc.vm.staging.example.org",
}

// The registrable domain above community.domain gets the lockdown policy
// when it has none and nothing under it has mail that policy would govern,
// and the hostnames below it then need no policy of their own.
func TestRegistrableDomainPolicyIsWrittenWhenNothingHasMail(t *testing.T) {
	w := wantsOf(t, staged())["TXT _dmarc.example.org"]
	if !w.Advisory || !w.Parent {
		t.Fatalf("the registrable domain's policy is a parent want: %+v", w)
	}
	for _, name := range stagedDMARC {
		if w := wantsOf(t, staged())["TXT "+name]; w.CoveredBy != "_dmarc.example.org" {
			t.Errorf("%s is not marked as covered by the registrable domain: %+v", name, w)
		}
	}

	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "null-mx", Type: "MX", Name: "example.org", Content: ".", Priority: new(int)},
		{ID: "code", Type: "TXT", Name: "example.org", Content: "google-site-verification=abc"},
		{ID: "www", Type: "A", Name: "www.example.org", Content: "198.51.100.7"},
	}
	c := fake.serve(t)
	p := plan(t, c, staged())
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Create || e.Content != dmarcParent {
		t.Fatalf("the registrable domain's policy: %+v", e)
	}
	// Until it reads back, nothing says the provider let it be written, so
	// the hostnames get their own in the same run.
	for _, name := range stagedDMARC {
		if e, _ := entryAt(p, name); e.Action != Create {
			t.Errorf("%s: got %s, want create", name, e.Action)
		}
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}

	// Once it is there, dns prune removes them, and they are covered.
	wants, err := Desired(staged())
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := BuildPrune(context.Background(), c, staged(), wants)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, e := range pruned.Removes() {
		removed = append(removed, e.Name)
	}
	want := slices.Clone(stagedDMARC)
	sort.Strings(removed)
	sort.Strings(want)
	if strings.Join(removed, " ") != strings.Join(want, " ") {
		t.Errorf("prune removes %v, want %v", removed, want)
	}
	if err := ExecutePrune(context.Background(), c, pruned, ui.Discard); err != nil {
		t.Fatal(err)
	}
	again := plan(t, c, staged())
	for _, name := range stagedDMARC {
		if e, _ := entryAt(again, name); e.Action != Covered {
			t.Errorf("%s after prune: got %s, want covered", name, e.Action)
		}
	}
}

// A name under the registrable domain that has mail, and no policy of its
// own, would be governed by the one written there, so none is written, and
// each hostname below keeps its own.
func TestRegistrableDomainWithMailIsOnlyChecked(t *testing.T) {
	dkim := "v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQC"
	cases := map[string][]cfRecord{
		"an MX at the domain": {
			{Type: "MX", Name: "example.org", Content: "mx.example.net", Priority: ptr(10)},
		},
		"an MX below it": {
			{Type: "MX", Name: "shop.example.org", Content: "mx.example.net", Priority: ptr(10)},
		},
		"a wildcard MX": {
			{Type: "MX", Name: "*.example.org", Content: "mx.example.net", Priority: ptr(10)},
		},
		"an SPF record naming senders": {
			{Type: "TXT", Name: "example.org", Content: `"v=spf1 include:_spf.example.net -all"`},
		},
		"a DKIM key": {
			{Type: "TXT", Name: "s1._domainkey.news.example.org", Content: dkim},
		},
		"a zone delegated elsewhere": {
			{Type: "NS", Name: "lab.example.org", Content: "ns1.example.net"},
		},
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake()
			fake.zones["example.org"] = "zone-1"
			fake.records["zone-1"] = records
			p := plan(t, fake.serve(t), staged())
			e, _ := entryAt(p, "_dmarc.example.org")
			if e.Action != Warn {
				t.Fatalf("the registrable domain's policy: got %s, want warn", e.Action)
			}
			if !strings.Contains(e.Detail, records[0].Name) {
				t.Errorf("the note does not name %s: %s", records[0].Name, e.Detail)
			}
			for _, name := range stagedDMARC {
				if e, _ := entryAt(p, name); e.Action != Create {
					t.Errorf("%s: got %s, want create", name, e.Action)
				}
			}
		})
	}
}

// A name with mail and a policy of its own is not governed by the
// registrable domain's, so it does not stop that policy being written.
func TestMailNameWithItsOwnPolicyLeavesTheRegistrableDomainFree(t *testing.T) {
	dkim := "v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQC"
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "s1._domainkey.news.example.org", Content: dkim},
		{Type: "TXT", Name: "_dmarc.news.example.org", Content: "v=DMARC1; p=none; rua=mailto:r@example.net"},
		{Type: "MX", Name: "shop.example.org", Content: "mx.example.net", Priority: ptr(10)},
		{Type: "TXT", Name: "_dmarc.shop.example.org", Content: "v=DMARC1; p=quarantine"},
	}
	p := plan(t, fake.serve(t), staged())
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Create {
		t.Errorf("the registrable domain's policy: got %s (%s), want create", e.Action, e.Detail)
	}

	// A send-only sender under community.domain gets its policy written in
	// the same run, so it does not stop the registrable domain's either.
	cfg := staged()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@send.staging.example.org"}
	fake = newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "brevo1._domainkey.send.staging.example.org", Content: dkim},
	}
	p = plan(t, fake.serve(t), cfg)
	if e, _ := entryAt(p, "_dmarc.send.staging.example.org"); e.Action != Create || e.Content != dmarcSender {
		t.Errorf("the sender's own policy: %+v", e)
	}
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Create {
		t.Errorf("the registrable domain's policy: got %s (%s), want create", e.Action, e.Detail)
	}

	// One outside community.domain is not dns init's to write, so with no
	// policy of its own it stops the registrable domain's.
	cfg.SMTP = config.SMTP{FromAddress: "noreply@send.example.org"}
	fake = newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "brevo1._domainkey.send.example.org", Content: dkim},
	}
	p = plan(t, fake.serve(t), cfg)
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Warn {
		t.Errorf("the registrable domain's policy: got %s, want warn", e.Action)
	}
}

// A policy already at the registrable domain is never replaced: one that
// rejects covers the hostnames below, a weaker one is a note and they keep
// their own. A registrable domain the provider does not hold is not read.
func TestRegistrableDomainPolicyIsNeverReplaced(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{ID: "dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=reject; sp=reject; rua=mailto:r@example.net"}}
	p := plan(t, fake.serve(t), staged())
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Present {
		t.Errorf("a strong policy there: got %s", e.Action)
	}
	for _, name := range stagedDMARC {
		if e, _ := entryAt(p, name); e.Action != Covered {
			t.Errorf("%s under a strong policy: got %s, want covered", name, e.Action)
		}
	}

	fake = newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{ID: "dmarc", Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=none"}}
	c := fake.serve(t)
	p = plan(t, c, staged())
	shown := &ui.Recorder{}
	p.Show(shown)
	if !shown.Has("note", "TXT _dmarc.example.org is not this toolkit's to write") {
		t.Errorf("no note for the registrable domain's weak policy:\n%s", shown.Lines())
	}
	for _, name := range stagedDMARC {
		if e, _ := entryAt(p, name); e.Action != Create {
			t.Errorf("%s under a weak policy: got %s, want create", name, e.Action)
		}
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	for _, rec := range fake.posts {
		if rec.Name == "_dmarc.example.org" {
			t.Fatalf("wrote over the registrable domain's DMARC policy: %+v", rec)
		}
	}

	fake = newFake()
	fake.zones["staging.example.org"] = "zone-2"
	p = plan(t, fake.serve(t), staged())
	if e, ok := entryAt(p, "_dmarc.example.org"); ok {
		t.Errorf("checked a registrable domain the provider does not hold: %+v", e)
	}
	for _, name := range stagedDMARC {
		if e, _ := entryAt(p, name); e.Action != Create {
			t.Errorf("%s with no registrable domain held: got %s, want create", name, e.Action)
		}
	}
}

// The registrable domain's policy this deployment wrote is a conflict once
// a sender the configuration names under it has no policy of its own: the
// toolkit's own mail would be rejected under it. The rest of the zone is not
// read again.
func TestOwnRegistrableDomainPolicyOverAConfiguredSenderIsAConflict(t *testing.T) {
	cfg := staged()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@news.example.org"}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "parent", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcParent, Comment: comment},
		{Type: "MX", Name: "shop.example.org", Content: "mx.example.net", Priority: ptr(10)},
	}
	c := fake.serve(t)
	p := plan(t, c, cfg)
	e, _ := entryAt(p, "_dmarc.example.org")
	if e.Action != Conflict || !strings.Contains(e.Detail, "news.example.org, a sender this configuration names") || strings.Contains(e.Detail, "shop") {
		t.Fatalf("got %s (%s), want a conflict naming the sender alone", e.Action, e.Detail)
	}
	shown := &ui.Recorder{}
	p.Show(shown)
	if i := shown.Index("refuse", "TXT _dmarc.example.org conflicts"); i < 0 || !strings.Contains(shown.Events[i].Extra, "delete by hand") {
		t.Errorf("the refusal should say to delete it by hand:\n%s", shown.Lines())
	}

	fake.records["zone-1"] = append(fake.records["zone-1"], cfRecord{Type: "TXT", Name: "_dmarc.news.example.org", Content: "v=DMARC1; p=none"})
	if e, _ := entryAt(plan(t, c, cfg), "_dmarc.example.org"); e.Action != Present {
		t.Errorf("with the sender's own policy: got %s (%s), want present", e.Action, e.Detail)
	}
}

// A weaker policy between a hostname and the registrable domain is the one a
// receiver walking up the tree reads, so the hostname is not covered.
func TestWeakerPolicyBetweenIsNotCovered(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=reject; sp=reject"},
		{Type: "TXT", Name: "_dmarc.staging.example.org", Content: "v=DMARC1; p=none"},
	}
	cfg := staged()
	cfg.Community.MailDomains = []string{"staging.example.org"}
	p := plan(t, fake.serve(t), cfg)
	if e, _ := entryAt(p, "_dmarc.talk.staging.example.org"); e.Action != Create {
		t.Errorf("got %s, want create", e.Action)
	}
}

// A hostname's own policy at a name the registrable domain's covers is a
// conflict all the same when it is weaker: a receiver reads it first.
func TestCoveredHostnameWithAWeakPolicyIsAConflict(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{Type: "TXT", Name: "_dmarc.talk.staging.example.org", Content: "v=DMARC1; p=none"}}
	p := plan(t, fake.serve(t), staged())
	if e, _ := entryAt(p, "_dmarc.talk.staging.example.org"); e.Action != Conflict {
		t.Errorf("got %s, want conflict", e.Action)
	}
}

// Once the registrable domain's policy rejects, prune removes the hostnames'
// own no-mail policies this deployment made, and never the registrable
// domain's own.
func TestPruneRemovesHostnamePoliciesTheRegistrableDomainCovers(t *testing.T) {
	cfg := staged()
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "parent", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "talk", Type: "TXT", Name: "_dmarc.talk.staging.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "domain", Type: "TXT", Name: "_dmarc.staging.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "spf", Type: "TXT", Name: "talk.staging.example.org", Content: spfLockdown, Comment: comment},
	}
	p, err := BuildPrune(context.Background(), fake.serve(t), cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	removes := map[string]bool{}
	for _, e := range p.Removes() {
		removes[e.ID] = true
	}
	if !removes["talk"] || !removes["domain"] || !removes["spf"] || removes["parent"] {
		t.Errorf("removes %v, want talk, domain and spf", removes)
	}
}

// A hostname's own DMARC policy is a no-mail record like its SPF: prune
// removes it once nothing wants it.
func TestPruneRemovesAHostnamesDMARCNothingWants(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "gone", Type: "TXT", Name: "_dmarc.old.staging.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "talk", Type: "TXT", Name: "_dmarc.talk.staging.example.org", Content: dmarcLockdown, Comment: comment},
	}
	c := fake.serve(t)
	cfg := staged()
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
	if !removes["gone"] || removes["talk"] {
		t.Errorf("removes %v, want gone only", removes)
	}
}

func ptr(n int) *int { return &n }

// Mail at the registrable domain itself is governed by the policy there, so
// the policy this deployment wrote is a conflict once the domain has mail.
func TestOwnRegistrableDomainPolicyWithMailAtTheDomainIsAConflict(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown, Comment: comment},
		{Type: "MX", Name: "example.org", Content: "mx.example.net", Priority: ptr(10)},
	}
	if e, _ := entryAt(plan(t, fake.serve(t), staged()), "_dmarc.example.org"); e.Action != Conflict || !strings.Contains(e.Detail, "an MX record at example.org") {
		t.Errorf("got %s (%s), want a conflict naming the domain's MX", e.Action, e.Detail)
	}
}

// A sender the configuration names under the registrable domain, outside
// community.domain, with no policy of its own, stops the policy being
// written there, even before its mail's DNS exists.
func TestConfiguredSenderStopsTheRegistrableDomainPolicy(t *testing.T) {
	cfg := staged()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@example.org"}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	p := plan(t, fake.serve(t), cfg)
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Warn || !strings.Contains(e.Detail, "example.org, a sender this configuration names") {
		t.Errorf("got %s (%s), want a warning naming the sender", e.Action, e.Detail)
	}
	for _, name := range stagedDMARC {
		if e, _ := entryAt(p, name); e.Action != Create {
			t.Errorf("%s: got %s, want create", name, e.Action)
		}
	}
}

// A hostname whose SPF record names a sender has mail nobody listed, which
// the policy covering it would reject, so the run stops.
func TestHostnameWithUndeclaredMailIsAConflict(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{Type: "TXT", Name: "talk.example.org", Content: "v=spf1 include:sendgrid.net -all"}}
	p := plan(t, fake.serve(t), declared())
	if e, _ := entryAt(p, "talk.example.org"); e.Action != Conflict || !strings.Contains(e.Detail, "community.mail_domains") {
		t.Errorf("got %s (%s), want a conflict naming community.mail_domains", e.Action, e.Detail)
	}
	// One that says it sends none is fine, and nothing is written there.
	fake = newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{Type: "TXT", Name: "talk.example.org", Content: spfLockdown}}
	if e, ok := entryAt(plan(t, fake.serve(t), declared()), "talk.example.org"); ok {
		t.Errorf("a no-mail SPF record at a hostname: %+v", e)
	}
}

// A hostname with mail under the registrable domain stops the policy there
// being written, although the plan would give it its own: written there,
// that one is covered and never created.
func TestCoveredHostnameWithMailStopsTheRegistrableDomainPolicy(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{{Type: "MX", Name: "talk.staging.example.org", Content: "mx.example.net", Priority: ptr(10)}}
	p := plan(t, fake.serve(t), staged())
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Warn {
		t.Errorf("the registrable domain's policy: got %s, want warn", e.Action)
	}
	if e, _ := entryAt(p, "_dmarc.talk.staging.example.org"); e.Action != Create {
		t.Errorf("the hostname's own policy: got %s, want create", e.Action)
	}
}

// Two policies at the registrable domain are none to a receiver, and a CNAME
// there delegates a policy this cannot read: either way it covers nothing,
// is a note, and stops nothing.
func TestRegistrableDomainPolicyThatCannotBeReadCoversNothing(t *testing.T) {
	cases := map[string][]cfRecord{
		"two policies": {
			{Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=reject; sp=reject"},
			{Type: "TXT", Name: "_dmarc.example.org", Content: "v=DMARC1; p=reject; sp=reject; rua=mailto:r@example.net"},
		},
		"a cname": {
			{Type: "CNAME", Name: "_dmarc.example.org", Content: "example-org._dmarc.vendor.example.net"},
		},
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake()
			fake.zones["example.org"] = "zone-1"
			fake.records["zone-1"] = records
			c := fake.serve(t)
			p := plan(t, c, staged())
			if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Warn {
				t.Errorf("the registrable domain's policy: got %s, want warn", e.Action)
			}
			for _, name := range stagedDMARC {
				if e, _ := entryAt(p, name); e.Action != Create {
					t.Errorf("%s: got %s, want create", name, e.Action)
				}
			}
			if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
				t.Fatal(err)
			}
			wants, err := Desired(staged())
			if err != nil {
				t.Fatal(err)
			}
			pruned, err := BuildPrune(context.Background(), c, staged(), wants)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(pruned.Removes()); n != 0 {
				t.Errorf("prune removes %d covered policies: %+v", n, pruned.Removes())
			}
		})
	}
}

// site remove reads no registrable domain the provider does not hold.
func TestSiteRemovalBelowAnUnheldRegistrableDomain(t *testing.T) {
	fake := newFake()
	fake.zones["staging.example.org"] = "zone-2"
	fake.records["zone-2"] = []cfRecord{{ID: "a", Type: "A", Name: "home-a.staging.example.org", Content: "198.51.100.20", Comment: comment}}
	before := staged()
	before.Sites["home-a"] = config.Site{Roles: []config.Role{config.RoleData}, Address: "10.44.0.1", Endpoint: "home-a.staging.example.org:51820", PublicAddress: "198.51.100.20"}
	after := staged()
	delete(after.Sites, "home-a")
	p, err := BuildSiteRemoval(context.Background(), fake.serve(t), before, "home-a", after)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Removes()) != 1 || p.Removes()[0].ID != "a" {
		t.Fatalf("want the address removed: %+v", p.Entries)
	}
}

// The registrable domain's policy is created after every policy its being
// written rested on.
func TestRegistrableDomainPolicyIsCreatedLast(t *testing.T) {
	cfg := staged()
	cfg.SMTP = config.SMTP{FromAddress: "noreply@send.staging.example.org"}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	c := fake.serve(t)
	p := plan(t, c, cfg)
	if e, _ := entryAt(p, "_dmarc.example.org"); e.Action != Create {
		t.Fatalf("the registrable domain's policy: got %s", e.Action)
	}
	if err := Execute(context.Background(), c, p, ui.Discard); err != nil {
		t.Fatal(err)
	}
	if last := fake.posts[len(fake.posts)-1]; last.Name != "_dmarc.example.org" {
		t.Errorf("created last: %s, want _dmarc.example.org", last.Name)
	}
}

// A covering policy this prune deletes covers nothing, so the policies it
// covered are kept.
func TestPruneKeepsPoliciesWhoseCoverItDeletes(t *testing.T) {
	cfg := declared()
	cfg.Community.MailDomains = []string{"example.org"}
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{ID: "domain", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcLockdown, Comment: comment},
		{ID: "talk", Type: "TXT", Name: "_dmarc.talk.example.org", Content: dmarcLockdown, Comment: comment},
	}
	p, err := BuildPrune(context.Background(), fake.serve(t), cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	removes := map[string]bool{}
	for _, e := range p.Removes() {
		removes[e.ID] = true
	}
	if !removes["domain"] || removes["talk"] {
		t.Errorf("removes %v, want domain only", removes)
	}
}

// The registrable domain's policy a former configuration wrote is outside
// every zone this one names, and --name vouches for it.
func TestPruneRemovesAVouchedRegistrableDomainPolicy(t *testing.T) {
	cfg := declared()
	cfg.Community.Domain = "example.net"
	site := cfg.Sites["vm"]
	site.Endpoint = "vm.example.net:51820"
	cfg.Sites["vm"] = site
	cfg.Apps = map[string]config.App{"talk": {Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.net"}}
	wants, err := Desired(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.zones["example.net"] = "zone-2"
	fake.records["zone-1"] = []cfRecord{{ID: "parent", Type: "TXT", Name: "_dmarc.example.org", Content: dmarcParent, Comment: comment}}
	c := fake.serve(t)
	p, err := BuildPrune(context.Background(), c, cfg, wants)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Removes()) != 0 {
		t.Fatalf("removed an unvouched record outside the domain: %+v", p.Removes())
	}
	p, err = BuildPrune(context.Background(), c, cfg, wants, "_dmarc.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Removes()) != 1 || p.Removes()[0].ID != "parent" {
		t.Errorf("want the vouched policy removed: %+v", p.Entries)
	}
}

// A name that is a CNAME has its target's mail, so a target with mail stops
// the registrable domain's policy.
func TestCNAMEToAHostWithMailStopsTheRegistrableDomainPolicy(t *testing.T) {
	was := lookupMail
	t.Cleanup(func() { lookupMail = was })
	lookupMail = func(_ context.Context, name string) bool { return name == "news.example.org" }
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "CNAME", Name: "news.example.org", Content: "esp.example.net"},
		{Type: "CNAME", Name: "www.example.org", Content: "host.example.net"},
	}
	e, _ := entryAt(plan(t, fake.serve(t), staged()), "_dmarc.example.org")
	if e.Action != Warn || !strings.Contains(e.Detail, "news.example.org") || strings.Contains(e.Detail, "www.example.org") {
		t.Errorf("got %s (%s), want a warning naming news.example.org alone", e.Action, e.Detail)
	}
}

// A hostname's SPF check refuses two SPF records, and says what it checked
// rather than what it would write.
func TestHostnameSPFCheck(t *testing.T) {
	fake := newFake()
	fake.zones["example.org"] = "zone-1"
	fake.records["zone-1"] = []cfRecord{
		{Type: "TXT", Name: "talk.example.org", Content: spfLockdown},
		{Type: "TXT", Name: "talk.example.org", Content: spfLockdown},
		{Type: "TXT", Name: "blog.example.org", Content: "v=spf1 include:mailgun.org ~all"},
	}
	p := plan(t, fake.serve(t), declared())
	if e, _ := entryAt(p, "talk.example.org"); e.Action != Conflict || !strings.Contains(e.Detail, "2 SPF records") {
		t.Errorf("two SPF records: got %s (%s)", e.Action, e.Detail)
	}
	shown := &ui.Recorder{}
	p.Show(shown)
	i := shown.Index("refuse", "TXT blog.example.org conflicts")
	if i < 0 || !strings.Contains(shown.Events[i].Extra, "checked: blog.example.org holds no SPF record that lets a server send") || strings.Contains(shown.Events[i].Extra, "wanted:") {
		t.Errorf("the refusal should say what was checked:\n%s", shown.Lines())
	}
}

// A hostname with no registrable domain under the public suffix list is
// covered by no DMARC policy, so it says it sends no mail with SPF.
func TestHostnameWithNoRegistrableDomainGetsSPF(t *testing.T) {
	cfg := declared()
	cfg.Apps["pages"] = config.App{Blueprint: config.Blueprint("writefreely"), Hostname: "github.io"}
	if w := wantsOf(t, cfg)["TXT github.io"]; w.Content != spfLockdown || w.Guard {
		t.Errorf("got %+v, want SPF written", w)
	}
}
