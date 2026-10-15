package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The dry-run plan marks each step pending, in the order Execute takes them;
// the reasons are details.
func TestListPlanMarksStepsPendingInOrderWithReasonsAsDetail(t *testing.T) {
	plan := &apply.Plan{
		Site: "home-a", Transport: "ubuntu@192.0.2.10",
		Changes:       []apply.Change{{Path: "/srv/paisans/f2a9/infra/compose.yaml", Kind: apply.Create}, {Path: "/srv/paisans/f2a9/talk/.env", Kind: apply.Update}},
		Actions:       []apply.Action{{Stack: "infra", Recreate: true, Reason: "an environment or compose file changed"}},
		GatewayReload: true,
	}
	rec := &ui.Recorder{Verbose_: true}
	listPlan(rec, plan)
	order := []int{rec.Index("pending", "write 2 files"), rec.Index("pending", "recreate infra"), rec.Index("pending", "reload gateway")}
	for i := 1; i < len(order); i++ {
		if order[i-1] < 0 || order[i] < order[i-1] {
			t.Fatalf("steps missing or out of order:\n%s", rec.Lines())
		}
	}
	if !rec.Has("detail", "an environment or compose file changed") {
		t.Errorf("reason not a detail:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "/srv/paisans/f2a9/talk/.env") {
		t.Errorf("file paths not details:\n%s", rec.Lines())
	}
}

// The sweep is the plan's last step, with each image it would remove.
func TestListPlanEndsWithTheSweep(t *testing.T) {
	plan := &apply.Plan{
		Site: "home-a", Transport: "ubuntu@192.0.2.10",
		Actions:       []apply.Action{{Stack: "infra", Recreate: true}},
		GatewayReload: true,
		Sweep:         []apply.Prune{{Ref: "ghcr.io/example-org/mbin:v1.9.0", ID: "sha256:0123456789abcdef"}},
	}
	rec := &ui.Recorder{Verbose_: true}
	listPlan(rec, plan)
	sweep := rec.Index("pending", "sweep old images")
	if sweep < 0 || sweep < rec.Index("pending", "reload gateway") || sweep < rec.Index("pending", "recreate infra") {
		t.Fatalf("the sweep is missing or not last:\n%s", rec.Lines())
	}
	if !rec.Has("detail", "ghcr.io/example-org/mbin:v1.9.0") {
		t.Errorf("the swept image is not named:\n%s", rec.Lines())
	}
}

// A file edited on the host is a refusal in the plan itself, so a dry run
// says at once that nothing would be applied.
func TestListPlanRefusesConflicts(t *testing.T) {
	plan := &apply.Plan{
		Site: "home-a", Transport: "ubuntu@192.0.2.10",
		Changes: []apply.Change{{Path: "/srv/paisans/f2a9/talk/.env", Kind: apply.Conflict}},
	}
	rec := &ui.Recorder{}
	listPlan(rec, plan)
	i := rec.Index("refuse", "1 file was edited on the host")
	if i < 0 {
		t.Fatalf("no refusal for the conflict:\n%s", rec.Lines())
	}
	if !strings.Contains(rec.Events[i].Extra, "/srv/paisans/f2a9/talk/.env") {
		t.Errorf("the refusal does not name the file by default:\n%s", rec.Lines())
	}
}

// A note the operator has to act on is a warning on a dry run and on
// --execute alike, by default, where --execute lists no plan; a note that is
// only information is a detail. The same note found again by a later pass is
// said once.
func TestPlanNotesShowByDefaultOnDryRunAndExecute(t *testing.T) {
	plan := &apply.Plan{Notes: []apply.Note{
		{Hint: "infra network differs from its compose file: run infra down, then apply --recreate infra", Text: "the infrastructure stack's network paisans-f2a9-infra_default does not match"},
		{Text: "gone was owed an action by a stopped apply"},
	}}
	for _, execute := range []bool{false, true} {
		var b strings.Builder
		r := ui.NewPlain(&b, false)
		seen := map[string]bool{}
		presentPlan(r, plan, execute, seen)
		presentPlan(r, plan, execute, seen)
		out := b.String()
		if strings.Count(out, "  WARN infra network differs from its compose file: run infra down, then apply --recreate infra\n") != 1 {
			t.Errorf("execute=%v: the note to act on is not one warning: %q", execute, out)
		}
		if strings.Contains(out, "gone was owed") {
			t.Errorf("execute=%v: an informational note shows by default: %q", execute, out)
		}
	}
	rec := &ui.Recorder{Verbose_: true}
	presentPlan(rec, plan, true, map[string]bool{})
	if !rec.Has("detail", "gone was owed") {
		t.Errorf("the informational note is not a detail:\n%s", rec.Lines())
	}
}

// Piped, each step of the plan carries the writer's own word and no padded
// column.
func TestListPlanPlainMarksStepsTodo(t *testing.T) {
	plan := &apply.Plan{
		Site: "home-a", Transport: "ubuntu@192.0.2.10",
		Changes:       []apply.Change{{Path: "/srv/paisans/f2a9/infra/compose.yaml", Kind: apply.Create}, {Path: "/srv/paisans/f2a9/talk/.env", Kind: apply.Update}},
		Actions:       []apply.Action{{Stack: "infra", Recreate: true, Reason: "an environment or compose file changed"}},
		GatewayReload: true,
	}
	var b strings.Builder
	listPlan(ui.NewPlain(&b, true), plan)
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"  todo write 2 files\n",
		"      create /srv/paisans/f2a9/infra/compose.yaml\n",
		"  todo recreate infra\n",
		"      an environment or compose file changed\n",
		"  todo wait for infra\n",
		"  todo reload gateway\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "  -    ") {
		t.Errorf("a step is a padded item:\n%s", out)
	}
}

// A dry run names each image whose build moves, with the versions and the
// digests it goes between, at every verbosity; an image that stays is not
// listed.
func TestListPlanNamesEachMovedImage(t *testing.T) {
	const (
		old = "sha256:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b"
		new = "sha256:3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d"
		tag = "ghcr.io/example-org/mbin:v1-fork"
	)
	plan := &apply.Plan{
		Site: "home-a", Transport: "ubuntu@192.0.2.10",
		Pins: []apply.ImagePin{
			{Stack: "talk", App: "talk", Services: []string{"app"}, Image: tag, Reference: tag + "@" + new, Deployed: tag + "@" + old, Source: "registry", Version: "1.16.0-fork", DeployedVersion: "1.15.0-fork"},
			{Stack: "talk", App: "talk", Services: []string{"valkey"}, Image: "docker.io/valkey/valkey:9.1.2-trixie", Reference: "docker.io/valkey/valkey:9.1.2-trixie@" + old, Deployed: "docker.io/valkey/valkey:9.1.2-trixie@" + old, Source: "registry"},
			{Stack: "talk", App: "talk", Services: []string{"rabbitmq"}, Image: "docker.io/library/rabbitmq:3.13.7-management-alpine", Reference: "docker.io/library/rabbitmq:3.13.7-management-alpine@" + new, Deployed: "docker.io/library/rabbitmq:3.13.7-management-alpine", Source: "registry"},
		},
	}
	rec := &ui.Recorder{}
	listPlan(rec, plan)
	want := "talk  app  " + tag + "  1.15.0-fork (sha256:1a2b3c4d5e6f) -> 1.16.0-fork (sha256:3c4d5e6f7a8b)"
	if !rec.Has("pending", want) {
		t.Errorf("the moved image is not listed as %q:\n%s", want, rec.Lines())
	}
	// A tag the host runs bare is pinned, and says so rather than claiming a
	// new build.
	pinned := "talk  rabbitmq  docker.io/library/rabbitmq:3.13.7-management-alpine  pins docker.io/library/rabbitmq:3.13.7-management-alpine to sha256:3c4d5e6f7a8b"
	if !rec.Has("pending", pinned) {
		t.Errorf("the first pin is not listed as %q:\n%s", pinned, rec.Lines())
	}
	if strings.Contains(rec.Lines(), "valkey") {
		t.Errorf("an image that stays is listed:\n%s", rec.Lines())
	}
}

// apply without --site plans every site, so its steps hold no clustered app
// on its other sites' build; a quiet reporter alone is not such a run.
func TestOnlyARunOverEverySiteSkipsTheOtherSitesBuilds(t *testing.T) {
	if planningEverySite {
		t.Fatal("set outside a run")
	}
	restore := planEverySite()
	if !planningEverySite {
		t.Error("not set inside a run")
	}
	restore()
	if planningEverySite {
		t.Error("left set after the run")
	}
}
