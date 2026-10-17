package grant_test

import (
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/grant"
)

// An entry approved on a host and not applied yet is not asked about again:
// a run that stopped after the approval, part way through its steps, does
// not ask for the same entries when it runs again. An entry approved as
// something else is still asked about.
func TestAnEntryApprovedEarlierIsNotAskedAgain(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{
		{Stack: "blog", Service: "app", Reference: "writefreely:1"},
		{Stack: "talk", Service: "app", Reference: "mbin:2"},
	}}
	scope := grant.ApplyScope()
	first := grant.Compare("home-a", g, grant.Record{}, scope)
	if first.Pending() != 2 {
		t.Fatalf("%+v", first)
	}
	rec := grant.Record{}.Approve(first.Changes[:1], time.Unix(100, 0), "alice")
	again := grant.Compare("home-a", g, rec, scope)
	if again.Pending() != 1 || again.Changes[0].Subject != "image talk/app" {
		t.Errorf("pending %+v", again.Changes)
	}
	if len(again.Approved) != 1 || again.Approved[0].Subject != "image blog/app" {
		t.Errorf("approved earlier %+v", again.Approved)
	}
	g.Images[0].Reference = "writefreely:3"
	moved := grant.Compare("home-a", g, rec, scope)
	if moved.Pending() != 2 || len(moved.Approved) != 0 {
		t.Errorf("an approval of another reference passed: %+v", moved)
	}
}

// Approving again replaces an entry's approval rather than adding a second,
// and a removal is never recorded, since it needs no approval.
func TestApproveKeepsOneApprovalPerEntry(t *testing.T) {
	a := grant.Change{Op: grant.Added, Subject: "image talk/app", To: "mbin:1"}
	b := grant.Change{Op: grant.Changed, Subject: "image talk/app", To: "mbin:2"}
	gone := grant.Change{Op: grant.Removed, Subject: "image blog/app"}
	rec := grant.Record{}.Approve([]grant.Change{a, gone}, time.Unix(100, 0), "alice").Approve([]grant.Change{b}, time.Unix(200, 0), "bob")
	if rec.Approved == nil || len(rec.Approved.Entries) != 1 {
		t.Fatalf("%+v", rec.Approved)
	}
	if e := rec.Approved.Entries[0]; e.Subject != "image talk/app" || e.To != "mbin:2" || e.ApprovedBy != "bob" || e.ApprovedAt != "1970-01-01T00:03:20Z" {
		t.Errorf("%+v", e)
	}
	if same := rec.Approve([]grant.Change{b}, time.Unix(300, 0), "bob"); grant.Encode(same) != grant.Encode(rec) {
		t.Errorf("approving the same entry again changed the record:\n%s", grant.Encode(same))
	}
}

// Applying what was approved moves it from the approvals to the part it
// applied, with who approved it, and leaves approvals outside the scope the
// command applied for the command that applies them.
func TestUpdateTakesTheApprovalsItApplies(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{
		{Stack: "blog", Service: "app", Reference: "writefreely:1"},
		{Stack: "talk", Service: "app", Reference: "mbin:2"},
	}}
	review := grant.Compare("home-a", g, grant.Record{}, grant.ApplyScope())
	rec := grant.Record{}.Approve(review.Changes, time.Unix(100, 0), "alice")
	blog := grant.Update(rec, g, grant.ApplyScope("blog"), time.Unix(200, 0), "")
	if blog.Images == nil || blog.Images.ApprovedBy != "alice" {
		t.Errorf("who approved was not carried to the part: %+v", blog.Images)
	}
	if blog.Approved == nil || len(blog.Approved.Entries) != 1 || blog.Approved.Entries[0].Subject != "image talk/app" {
		t.Errorf("approvals left %+v", blog.Approved)
	}
	all := grant.Update(blog, g, grant.ApplyScope(), time.Unix(300, 0), "")
	if all.Approved != nil {
		t.Errorf("approvals left after everything applied: %+v", all.Approved)
	}
}

// A record holding approvals is read and written as version 3.
func TestParseReadsApprovals(t *testing.T) {
	rec := grant.Record{}.Approve([]grant.Change{{Op: grant.Added, Subject: "key SHA256:a", To: "x"}}, time.Unix(0, 0), "")
	got, err := grant.Parse(grant.Encode(rec))
	if err != nil || got.Approved == nil || len(got.Approved.Entries) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.Contains(grant.Encode(rec), `"version": 3`) {
		t.Errorf("not written as version 3: %s", grant.Encode(rec))
	}
}

// A scope naming services records only those images: the approval of
// another service in the same stack is kept, and so is its recorded image.
func TestAScopeOfServicesKeepsTheRestOfItsStack(t *testing.T) {
	full := grant.Grant{Images: []grant.Image{
		{Stack: "infra", Service: "etcd", Reference: "etcd:3"},
		{Stack: "infra", Service: "haproxy", Reference: "haproxy:9"},
	}}
	rec := grant.Record{}.Approve(grant.Compare("home-b", full, grant.Record{}, grant.ApplyScope()).Changes, time.Unix(100, 0), "alice")
	etcd := grant.Grant{Images: full.Images[:1]}
	scope := grant.Scope{Images: true, Stacks: []string{"infra"}, Services: []string{"infra/etcd"}}
	if c := grant.Compare("home-b", etcd, rec, scope); c.Pending() != 0 || len(c.Changes) != 0 || len(c.Approved) != 1 {
		t.Fatalf("the narrowed review %+v", c)
	}
	rec = grant.Update(rec, etcd, scope, time.Unix(200, 0), "")
	if rec.Approved == nil || len(rec.Approved.Entries) != 1 || rec.Approved.Entries[0].Subject != "image infra/haproxy" {
		t.Errorf("approvals kept %+v", rec.Approved)
	}
	if after := grant.Compare("home-b", full, rec, grant.ApplyScope()); after.Pending() != 0 {
		t.Errorf("haproxy asked again: %+v", after.Changes)
	}
}
