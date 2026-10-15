package appadmin

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// declaredRequest is a request as apply makes one for an admin the
// configuration declares.
func declaredRequest() Request {
	req := pidRequest()
	req.Declared = true
	req.LinkTTL = 24 * time.Hour
	req.EmailLink = true
	return req
}

func strptr(s string) *string { return &s }

// A declared operator with no account is created, given a 24 hour link, and
// emailed one of Pocket ID's own.
func TestDeclaredAdminIsCreatedLinkedAndEmailed(t *testing.T) {
	f := &fakePocketID{}
	plan, err := Build(config.BlueprintPocketID, f, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Action{ActionCreate, ActionLoginLink, ActionEmailLink}; !reflect.DeepEqual(plan.Actions, want) {
		t.Fatalf("actions %v, want %v", plan.Actions, want)
	}
	out, err := Execute(plan, f)
	if err != nil {
		t.Fatal(err)
	}
	if out.LoginLink != "https://id.example.org/lc/tok-not-real-0001" || out.ExpiresIn != 24*time.Hour {
		t.Errorf("outcome %+v", out)
	}
	if !out.Emailed || !reflect.DeepEqual(f.emailed, []string{"u-1"}) {
		t.Errorf("emailed %v, outcome %+v", f.emailed, out)
	}
	if !reflect.DeepEqual(f.ttls, []string{"24h0m0s", "24h0m0s"}) {
		t.Errorf("ttls %v", f.ttls)
	}
}

// A declared operator who renamed themselves is found by their address, and no
// second account is made.
func TestDeclaredAdminIsFoundByEmail(t *testing.T) {
	f := &fakePocketID{users: []pocketid.User{{ID: "u-7", Username: "fern-renamed",
		Email: strptr("founder@example.org"), EmailVerified: true, IsAdmin: true}}}
	plan, err := Build(config.BlueprintPocketID, f, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Action{ActionLoginLink, ActionEmailLink}; !reflect.DeepEqual(plan.Actions, want) {
		t.Fatalf("actions %v, want %v", plan.Actions, want)
	}
	if _, err := Execute(plan, f); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.mutations {
		if m == "POST /api/users" {
			t.Error("created a second account")
		}
	}
	if !reflect.DeepEqual(f.emailed, []string{"u-7"}) {
		t.Errorf("emailed %v", f.emailed)
	}
}

// An address that only contains the declared one is someone else's.
func TestDeclaredAdminEmailMatchIsWhole(t *testing.T) {
	f := &fakePocketID{users: []pocketid.User{{ID: "u-7", Username: "other",
		Email: strptr("cofounder@example.org"), EmailVerified: true, IsAdmin: true}}}
	plan, err := Build(config.BlueprintPocketID, f, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0] != ActionCreate {
		t.Errorf("actions %v, want a create", plan.Actions)
	}
}

// A declared operator who has registered a passkey has taken the account up:
// no link, no email.
func TestDeclaredAdminWithAPasskeyGetsNothing(t *testing.T) {
	f := &fakePocketID{users: []pocketid.User{{ID: "u-1", Username: "founder",
		Email: strptr("founder@example.org"), EmailVerified: true, IsAdmin: true}},
		passkeys: map[string]bool{"u-1": true}}
	plan, err := Build(config.BlueprintPocketID, f, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 0 || !plan.State.Passkey {
		t.Errorf("actions %v, state %+v", plan.Actions, plan.State)
	}
}

// One without a passkey gets a new link on every run.
func TestDeclaredAdminWithoutAPasskeyGetsANewLinkEachRun(t *testing.T) {
	f := &fakePocketID{}
	req := declaredRequest()
	req.EmailLink = false
	for run := 1; run <= 2; run++ {
		plan, err := Build(config.BlueprintPocketID, f, req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Execute(plan, f); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens != 2 || len(f.emailed) != 0 {
		t.Errorf("tokens %d, emailed %v", f.tokens, f.emailed)
	}
}

// The dry run's titles say what will happen and carry no credential.
func TestDeclaredAdminTitles(t *testing.T) {
	plan, err := Build(config.BlueprintPocketID, &fakePocketID{}, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Titles(), "; ")
	if got != "create administrator founder; issue login link for founder; email login link to founder" {
		t.Errorf("titles %q", got)
	}
}

// A member request, an operator under management: paisans, makes an account
// that is not a Pocket ID administrator, and its title says so.
func TestMemberRequestCreatesNoAdministrator(t *testing.T) {
	f := &fakePocketID{}
	req := declaredRequest()
	req.Member = true
	plan, err := Build(config.BlueprintPocketID, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Titles()[0]; got != "create user founder" {
		t.Errorf("title %q, want create user founder", got)
	}
	if _, err := Execute(plan, f); err != nil {
		t.Fatal(err)
	}
	if len(f.users) != 1 || f.users[0].IsAdmin {
		t.Errorf("users %+v, want one that is not an administrator", f.users)
	}
	if !strings.Contains(f.bodies[0], `"isAdmin":false`) {
		t.Errorf("create body %s, want isAdmin false", f.bodies[0])
	}
}

// A member request never grants the flag and never clears one set by hand:
// that flag is the operator reconciler's to report.
func TestMemberRequestLeavesTheAdministratorFlag(t *testing.T) {
	for _, admin := range []bool{false, true} {
		f := &fakePocketID{users: []pocketid.User{{ID: "u-1", Username: "founder",
			Email: strptr("founder@example.org"), EmailVerified: true, IsAdmin: admin}},
			passkeys: map[string]bool{"u-1": true}}
		req := declaredRequest()
		req.Member = true
		req.AdminGroups = nil
		req.MemberGroups = nil
		plan, err := Build(config.BlueprintPocketID, f, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) != 0 {
			t.Errorf("isAdmin %t: actions %v, want none", admin, plan.Actions)
		}
	}
}
