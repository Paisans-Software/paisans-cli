package apply_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// A host with no pending record owes nothing: every apply that ran on it
// finished.
func TestAHostWithNoPendingRecordOwesNothing(t *testing.T) {
	o, err := apply.ReadOwed(newHost(), apply.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !o.None() {
		t.Fatalf("a host with no record owes %+v", o)
	}
}

// The pending record an apply that stopped part way leaves names what the
// host is still owed, and doctor and deployment log say so from it.
func TestOwedNamesWhatAStoppedApplyLeft(t *testing.T) {
	host := newHost()
	host.files["/srv/paisans/f2a9/.paisans-pending.json"] = `{"version":1,"actions":[{"stack":"chat","recreate":true},{"stack":"matrix"},{"stack":"chat"}],"gateway_reload":true,"grants":["talk_reader"]}`
	o, err := apply.ReadOwed(host, apply.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	if o.None() || !slices.Equal(o.Stacks, []string{"chat", "matrix"}) || !o.Gateway || !slices.Equal(o.Grants, []string{"talk_reader"}) {
		t.Fatalf("owed %+v, want chat and matrix, the gateway and talk_reader", o)
	}
	if got, want := o.String(), "chat, matrix, the gateway's checks and 1 database grant"; got != want {
		t.Errorf("owed reads %q, want %q", got, want)
	}
}

func TestAnUnreadablePendingRecordIsAnError(t *testing.T) {
	host := newHost()
	host.files["/srv/paisans/f2a9/.paisans-pending.json"] = `{`
	if _, err := apply.ReadOwed(host, apply.Fixture); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("an unreadable record gives %v", err)
	}
}
