package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const referHomeB = "Run paisans doctor --site home-b, which says what the deployment is without while it is gone."

// A site ssh cannot reach stops apply, and the stop refers the operator to
// doctor for that site, once, after what ssh said.
func TestAnApplyStoppedByAnUnreachableSiteRefersToDoctor(t *testing.T) {
	down := &ui.Problem{Hint: "ubuntu@home-b cannot be reached over ssh", Explain: "Check that the host is up.", Cause: &apply.UnreachableError{Site: "home-b", Err: apply.ErrUnreachable}}
	fakeConverge(t, map[string]error{"deployment apply --site vm": fmt.Errorf("reading home-b's compose files: %w", down)})
	err := converge(t, "--execute")
	_, explain := ui.Describe(err)
	if n := strings.Count(explain, referHomeB); n != 1 {
		t.Errorf("the referral appears %d times, want once:\n%s", n, explain)
	}
	if i, j := strings.Index(explain, "Check that the host is up."), strings.Index(explain, referHomeB); i < 0 || j < i {
		t.Errorf("the referral is not after ssh's advice:\n%s", explain)
	}
	if !errors.Is(err, apply.ErrUnreachable) {
		t.Error("the referral hid the cause")
	}
}

// Any other failure keeps today's text, and so does an ssh failure that
// names no site.
func TestAnApplyStoppedByAnythingElseKeepsItsText(t *testing.T) {
	for _, stepErr := range []error{
		errors.New("vm: boom"),
		&ui.Problem{Hint: "ubuntu@203.0.113.10 cannot be reached over ssh", Explain: "Check that the host is up.", Cause: apply.ErrUnreachable},
	} {
		fakeConverge(t, map[string]error{"deployment apply --site vm": stepErr})
		err := converge(t, "--execute")
		if strings.Contains(err.Error(), "paisans doctor") {
			t.Errorf("%v: referred to doctor:\n%v", stepErr, err)
		}
	}
}

func TestInitRefersAnUnreachableSiteToDoctor(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].down = true
	path, _ := initWorld(t, nil, sites, []byte{212, 37})
	_, explain := ui.Describe(runInitQuietly(t, path))
	if n := strings.Count(explain, referHomeB); n != 1 {
		t.Errorf("the referral appears %d times, want once:\n%s", n, explain)
	}
	if strings.Contains(explain, "--site home-a") {
		t.Errorf("a site that answered is referred:\n%s", explain)
	}
}

// A site that answered ssh but whose manifest could not be read is not
// doctor's reach finding, and gets no referral.
func TestInitDoesNotReferASiteThatAnswered(t *testing.T) {
	sites := fakeSites()
	sites["home-b"].broken = errors.New("cat: Input/output error")
	path, _ := initWorld(t, nil, sites, []byte{212, 37})
	err := runInitQuietly(t, path)
	if err == nil {
		t.Fatal("init read an unreadable manifest")
	}
	if strings.Contains(err.Error(), "paisans doctor") {
		t.Errorf("referred to doctor:\n%v", err)
	}
}

// gateway move calls the provider without the token check dns init makes,
// so its refusal is a bare HTTP 403 until the referral words it as the check
// does and points to doctor.
func TestAProviderRefusalInAGatewayMoveIsWordedAsTheTokenCheck(t *testing.T) {
	raw := fmt.Errorf("gateway move: %w", errors.New("cloudflare: GET /zones/abc/dns_records: HTTP 403: [9109] Cannot use the access token from location: 203.0.113.7"))
	err := referProvider(raw)
	_, explain := ui.Describe(err)
	want := "cloudflare refuses the token from this workstation (203.0.113.7). Add the address to the token's Client IP Address Filtering, or remove the filter. Run paisans doctor, which checks the token from every place that uses it."
	if !strings.Contains(explain, want) {
		t.Errorf("explanation lacks the referral:\n%s", explain)
	}
	if !errors.Is(err, errors.Unwrap(raw)) {
		t.Error("the referral hid the cause")
	}
	if again := referProvider(err); strings.Count(again.Error(), "Run paisans doctor") != 1 {
		t.Errorf("referred twice:\n%v", again)
	}
	other := errors.New("cloudflare: HTTP 500: upstream")
	if got := referProvider(other); got != other {
		t.Errorf("an unrecognised provider error changed: %v", got)
	}
}
