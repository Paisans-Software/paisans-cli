package appadmin

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/pocketid"
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

// A declared admin with no account is created, given a 24 hour link, and
// emailed one of Pocket ID's own.
func TestDeclaredAdminIsCreatedLinkedAndEmailed(t *testing.T) {
	f := &fakePocketID{}
	plan, err := Build(config.KindPocketID, f, declaredRequest())
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

// A declared admin who renamed themselves is found by their address, and no
// second account is made.
func TestDeclaredAdminIsFoundByEmail(t *testing.T) {
	f := &fakePocketID{users: []pocketid.User{{ID: "u-7", Username: "fern-renamed",
		Email: strptr("founder@example.org"), EmailVerified: true, IsAdmin: true}}}
	plan, err := Build(config.KindPocketID, f, declaredRequest())
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
	plan, err := Build(config.KindPocketID, f, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0] != ActionCreate {
		t.Errorf("actions %v, want a create", plan.Actions)
	}
}

// A declared admin who has registered a passkey has taken the account up:
// no link, no email.
func TestDeclaredAdminWithAPasskeyGetsNothing(t *testing.T) {
	f := &fakePocketID{users: []pocketid.User{{ID: "u-1", Username: "founder",
		Email: strptr("founder@example.org"), EmailVerified: true, IsAdmin: true}},
		passkeys: map[string]bool{"u-1": true}}
	plan, err := Build(config.KindPocketID, f, declaredRequest())
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
		plan, err := Build(config.KindPocketID, f, req)
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
	plan, err := Build(config.KindPocketID, &fakePocketID{}, declaredRequest())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Titles(), "; ")
	if got != "create administrator founder; issue login link for founder; email login link to founder" {
		t.Errorf("titles %q", got)
	}
}
