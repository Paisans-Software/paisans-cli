package blueprints_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// Every kind has a health route recorded, so a new kind cannot be seeded with
// a guessed one: it fails here until somebody establishes it.
func TestEveryBlueprintHasAHealthRoute(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		h, ok := blueprints.HealthFor(blueprint)
		if !ok {
			t.Errorf("%s has no health route recorded", blueprint)
			continue
		}
		if !strings.HasPrefix(h.Path, "/") {
			t.Errorf("%s: path %q is not absolute", blueprint, h.Path)
		}
		for _, code := range strings.Split(h.Expect, ",") {
			if len(code) != 3 {
				t.Errorf("%s: %q is not a comma list of exact codes; the fork has no ranges", blueprint, h.Expect)
			}
		}
	}
}

func TestPocketIDAnswersNoContent(t *testing.T) {
	if h, _ := blueprints.HealthFor(config.BlueprintPocketID); h.Expect != "204" {
		t.Fatalf("pocket-id expects %q", h.Expect)
	}
}

func TestOnlyTheseBlueprintsSendMail(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		if got, want := blueprints.SendsMail(blueprint), blueprint == config.BlueprintUptime || blueprint == config.BlueprintPocketID || blueprint == config.Blueprint("mbin") || blueprint == config.BlueprintInvite; got != want {
			t.Errorf("%s: SendsMail = %v", blueprint, got)
		}
	}
}

// Both forks carry a status-only /healthz, which answers 200 when
// healthy on a private instance too.
func TestMbinAndWriteFreelyHaveADedicatedHealthRoute(t *testing.T) {
	for _, blueprint := range []config.Blueprint{config.Blueprint("mbin"), config.Blueprint("writefreely")} {
		if h, _ := blueprints.HealthFor(blueprint); h.Path != "/healthz" || h.Expect != "200" {
			t.Errorf("%s health is %+v, want /healthz 200", blueprint, h)
		}
	}
}
