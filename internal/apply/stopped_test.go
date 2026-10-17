package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// actionFor returns the plan's action for stack, and whether it has one.
func actionFor(p *apply.Plan, stack string) (apply.Action, bool) {
	for _, a := range p.Actions {
		if a.Stack == stack {
			return a, true
		}
	}
	return apply.Action{}, false
}

// A host whose files all match the render, with a stack's containers removed
// or stopped, is not converged. An operator who removed every container and
// image to free disk space, then applied, got "Deployment complete" over a
// site with its apps down, because nothing but a changed file planned an
// action.
func TestAStoppedStackIsStarted(t *testing.T) {
	host := applied(t, "home-a")
	host.stopped = map[string]string{
		"talk/app":     "absent",
		"infra/garage": "exited",
	}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Writes()) != 0 {
		t.Errorf("an unchanged render would rewrite %d file(s)", len(p.Writes()))
	}

	talk, ok := actionFor(p, "talk")
	if !ok {
		t.Fatalf("a stack with no container plans nothing: %v", p.Actions)
	}
	if !talk.Recreate || talk.Force || talk.Down {
		t.Errorf("talk's action is %+v, want a plain up -d", talk)
	}
	if !strings.Contains(talk.Reason, "no container for app") {
		t.Errorf("talk's reason does not say what is missing: %q", talk.Reason)
	}

	// `up -d` of the whole project, never a restart narrowed to garage, which
	// does not start a removed container, and never forced, which would
	// replace Patroni on the primary to start Garage beside it.
	infra, ok := actionFor(p, "infra")
	if !ok {
		t.Fatalf("a stopped infrastructure service plans nothing: %v", p.Actions)
	}
	if !infra.Recreate || infra.Force || len(infra.Services) > 0 {
		t.Errorf("infra's action is %+v, want a plain up -d", infra)
	}
	if !strings.Contains(infra.Reason, "garage stopped") {
		t.Errorf("infra's reason does not say what is stopped: %q", infra.Reason)
	}

	for _, a := range p.Actions {
		if a.Stack != "talk" && a.Stack != "infra" {
			t.Errorf("%s runs, and is planned %q", a.Stack, a.Reason)
		}
	}

	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	for _, stack := range []string{"infra", "talk"} {
		if !host.ran("/srv/paisans/f2a9/" + stack + "/compose.yaml up -d") {
			t.Errorf("%s was not started", stack)
		}
	}
}

// A restarting or paused container is not stopped: `up -d` would leave it as
// it is. The health gate is what explains a restart loop, and a pause is
// somebody's.
func TestARestartingOrPausedContainerPlansNothing(t *testing.T) {
	host := applied(t, "home-a")
	host.stopped = map[string]string{
		"talk/app": "restarting",
		"wiki/app": "paused",
	}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 {
		t.Errorf("planned %v", p.Actions)
	}
}

// A held stack stays held, stopped or not: the pass that releases it starts
// it. Pocket ID's first pass holds every other app this way.
func TestAHeldStoppedStackIsNotStarted(t *testing.T) {
	host := applied(t, "home-a")
	host.stopped = map[string]string{"talk/app": "absent", "wiki/app": "absent"}
	p, err := apply.Build("home-a", plan(t), acmeModule(t), host, apply.Except("talk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := actionFor(p, "talk"); ok {
		t.Error("a held stack was planned")
	}
	if _, ok := actionFor(p, "wiki"); !ok {
		t.Errorf("a stack not held was not: %v", p.Actions)
	}
}
