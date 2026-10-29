package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// psHost answers `ps` with fixed output.
type psHost struct{ out string }

func (h psHost) Run(string) (string, error)              { return h.out, nil }
func (h psHost) RunInput(string, string) (string, error) { return h.out, nil }
func (h psHost) ReadFile(string) (string, bool, error)   { return "", false, nil }
func (h psHost) WriteFile(string, string, uint32) error  { return nil }
func (h psHost) Describe() string                        { return "fake" }

func TestStackHealthyIsOneLook(t *testing.T) {
	if err := apply.StackHealthy(apply.Fixture, "talk", psHost{`{"Service":"web","State":"running","Health":"healthy"}`}); err != nil {
		t.Fatalf("healthy stack: %v", err)
	}
	err := apply.StackHealthy(apply.Fixture, "talk", psHost{`{"Service":"web","State":"running","Health":"starting"}` + "\n" + `{"Service":"worker","State":"exited","ExitCode":1}`})
	if err == nil || !strings.Contains(err.Error(), "worker (exited, exit code 1)") {
		t.Fatalf("got %v", err)
	}
	if err := apply.StackHealthy(apply.Fixture, "talk", psHost{""}); err == nil || !strings.Contains(err.Error(), "no containers") {
		t.Fatalf("an empty stack is not healthy: %v", err)
	}
}

// policyHost answers `ps` with fixed output and `docker inspect` of a
// container's restart policy from policies, by ID.
type policyHost struct {
	ps       string
	policies map[string]string
}

func (h policyHost) Run(cmd string) (string, error) {
	if strings.HasPrefix(cmd, "docker inspect ") {
		fields := strings.Fields(cmd)
		return h.policies[fields[len(fields)-1]] + "\n", nil
	}
	return h.ps, nil
}
func (h policyHost) RunInput(cmd, _ string) (string, error) { return h.Run(cmd) }
func (h policyHost) ReadFile(string) (string, bool, error)  { return "", false, nil }
func (h policyHost) WriteFile(string, string, uint32) error { return nil }
func (h policyHost) Describe() string                       { return "fake" }

// A one shot, restart "no", that exited 0 is done; one that exited 1 failed;
// and exited 0 under any other policy is a service that stopped. A running
// service without a healthcheck, such as the beszel hub's setup asleep after
// its work, passes as it always has.
func TestAFinishedOneShotIsDone(t *testing.T) {
	const hub = `{"ID":"aaaaaaaaaaaa","Service":"app","State":"running","Health":"healthy"}` + "\n" +
		`{"ID":"cccccccccccc","Service":"setup","State":"running","Health":""}` + "\n"
	policies := map[string]string{"bbbbbbbbbbbb": "no", "dddddddddddd": "unless-stopped", "eeeeeeeeeeee": "on-failure", "ffffffffffff": "always"}
	for _, c := range []struct {
		name, admin string
		ok          bool
	}{
		{"restart no, exit 0", `{"ID":"bbbbbbbbbbbb","Service":"admin","State":"exited","ExitCode":0}`, true},
		{"restart no, exit 1", `{"ID":"bbbbbbbbbbbb","Service":"admin","State":"exited","ExitCode":1}`, false},
		{"unless-stopped, exit 0", `{"ID":"dddddddddddd","Service":"admin","State":"exited","ExitCode":0}`, false},
		{"on-failure, exit 0", `{"ID":"eeeeeeeeeeee","Service":"admin","State":"exited","ExitCode":0}`, false},
		{"always, exit 0", `{"ID":"ffffffffffff","Service":"admin","State":"exited","ExitCode":0}`, false},
		{"policy unreadable, exit 0", `{"ID":"","Service":"admin","State":"exited","ExitCode":0}`, false},
	} {
		err := apply.StackHealthy(apply.Fixture, "stats", policyHost{hub + c.admin, policies})
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), "admin (exited")) {
			t.Errorf("%s: got %v, want admin named as failed", c.name, err)
		}
	}
	if err := apply.StackHealthy(apply.Fixture, "stats", policyHost{hub, policies}); err != nil {
		t.Errorf("a running service without a healthcheck: %v", err)
	}
}
