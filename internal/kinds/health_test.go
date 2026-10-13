package kinds_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Every kind has a health route recorded, so a new kind cannot be seeded with
// a guessed one: it fails here until somebody establishes it.
func TestEveryKindHasAHealthRoute(t *testing.T) {
	for _, kind := range kinds.Builtin() {
		h, ok := kinds.HealthFor(kind)
		if !ok {
			t.Errorf("%s has no health route recorded", kind)
			continue
		}
		if !strings.HasPrefix(h.Path, "/") {
			t.Errorf("%s: path %q is not absolute", kind, h.Path)
		}
		for _, code := range strings.Split(h.Expect, ",") {
			if len(code) != 3 {
				t.Errorf("%s: %q is not a comma list of exact codes; the fork has no ranges", kind, h.Expect)
			}
		}
	}
}

func TestPocketIDAnswersNoContent(t *testing.T) {
	if h, _ := kinds.HealthFor(config.KindPocketID); h.Expect != "204" {
		t.Fatalf("pocket-id expects %q", h.Expect)
	}
}

func TestOnlyTheseKindsSendMail(t *testing.T) {
	for _, kind := range kinds.Builtin() {
		if got, want := kinds.SendsMail(kind), kind == config.KindUptime || kind == config.KindPocketID || kind == config.KindMbin || kind == config.KindInvite; got != want {
			t.Errorf("%s: SendsMail = %v", kind, got)
		}
	}
}

// Both forks carry a status-only /healthz, which answers 200 when
// healthy on a private instance too.
func TestMbinAndWriteFreelyHaveADedicatedHealthRoute(t *testing.T) {
	for _, kind := range []config.Kind{config.KindMbin, config.KindWriteFreely} {
		if h, _ := kinds.HealthFor(kind); h.Path != "/healthz" || h.Expect != "200" {
			t.Errorf("%s health is %+v, want /healthz 200", kind, h)
		}
	}
}
