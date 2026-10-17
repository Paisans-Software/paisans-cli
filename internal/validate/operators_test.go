package validate_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// operatorsWarning is the operators-fewer-than-two warning's message, failing
// the test unless exactly one fired.
func operatorsWarning(t *testing.T, r validate.Result) string {
	t.Helper()
	var out []string
	for _, f := range r.Warnings() {
		if f.Rule == "operators-fewer-than-two" {
			out = append(out, f.Message)
		}
	}
	if len(out) != 1 {
		t.Fatalf("operators-fewer-than-two warned %d times, want once: %v", len(out), r.Findings)
	}
	return out[0]
}

// One declared operator is a warning that says the operators heartbeat fails,
// since the reconciler fails it while one operator is declared.
func TestOneOperatorWarnsOfTheHeartbeat(t *testing.T) {
	msg := operatorsWarning(t, validate.Check(load(t, "operators-fewer-than-two")))
	if !strings.Contains(msg, "only alice") || !strings.Contains(msg, "operators heartbeat fails") {
		t.Errorf("message = %q, want it to name alice and the failing heartbeat", msg)
	}
}

// Two declared operators of whom one reaches every Pocket ID site warn too,
// but without the heartbeat: the reconciler counts the operators declared,
// so its heartbeat stays healthy.
func TestOneReachingOperatorOfTwoWarnsWithoutTheHeartbeat(t *testing.T) {
	cfg := load(t, "valid")
	home := cfg.Sites["home-b"]
	home.SSH.Operators = slices.DeleteFunc(slices.Clone(home.SSH.Operators), func(id string) bool { return id == "bob" })
	cfg.Sites["home-b"] = home
	msg := operatorsWarning(t, validate.Check(cfg))
	if !strings.Contains(msg, "only alice") || strings.Contains(msg, "heartbeat") {
		t.Errorf("message = %q, want it to name alice and not the heartbeat", msg)
	}
}

// A deployment with no Pocket ID app is not held to the rule at all.
func TestOperatorsWithoutPocketID(t *testing.T) {
	cfg := load(t, "operators-none")
	for name, app := range cfg.Apps {
		if app.Blueprint == config.BlueprintPocketID {
			delete(cfg.Apps, name)
		}
	}
	r := validate.Check(cfg)
	if r.Has("operators-none") || r.Has("operators-fewer-than-two") {
		t.Errorf("held a deployment with no Pocket ID app to the operators rule: %v", r.Findings)
	}
}

// Without an uptime app nothing receives the operators heartbeat, so the
// warning does not mention it.
func TestOneOperatorWithoutAMonitorLeavesOutTheHeartbeat(t *testing.T) {
	cfg := load(t, "operators-fewer-than-two")
	for name, app := range cfg.Apps {
		if app.Blueprint == config.BlueprintUptime {
			delete(cfg.Apps, name)
		}
	}
	if msg := operatorsWarning(t, validate.Check(cfg)); strings.Contains(msg, "heartbeat") {
		t.Errorf("message = %q, want no heartbeat without a monitor", msg)
	}
}

// No operator declared at all is refused, as none reaching the sites is.
func TestNoOperatorDeclaredIsRefused(t *testing.T) {
	cfg := load(t, "valid")
	cfg.Operators = nil
	for name, site := range cfg.Sites {
		site.SSH.Operators = nil
		cfg.Sites[name] = site
	}
	if got := findings(validate.Check(cfg), "operators-none"); len(got) != 1 || got[0] != "REFUSE operators" {
		t.Errorf("operators-none = %v, want one refusal of operators", got)
	}
}
