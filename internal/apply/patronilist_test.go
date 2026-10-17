package apply_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/patronitest"
)

// patronictlHost answers `patronictl list` with a fixed output.
type patronictlHost struct {
	*fakeHost
	answer string
}

func (h *patronictlHost) Run(command string) (string, error) {
	if strings.Contains(command, "patronictl") && strings.Contains(command, "list -f json") {
		return h.answer, nil
	}
	return h.fakeHost.Run(command)
}

const patroniList = `[{"Cluster": "f2a9", "Member": "home-a", "Host": "10.44.0.1", "Role": "Leader", "State": "running", "TL": 3}, {"Cluster": "f2a9", "Member": "home-b", "Host": "10.44.0.2", "Role": "Sync Standby", "State": "streaming", "TL": 3, "Receive LSN": "0/5000148", "Receive Lag": 0, "Replay LSN": "0/5000148", "Replay Lag": 0}]`

// patronictl logs to stderr when it cannot reach one of etcd's endpoints,
// before or after its answer, and the transport returns both together.
func TestPatronictlListIsReadPastItsLogLines(t *testing.T) {
	for name, out := range map[string]string{
		"alone":      patroniList + "\n",
		"log before": patronitest.Before + patroniList + "\n",
		"log after":  patroniList + "\n" + patronitest.After,
		"log around": patronitest.Wrap(patroniList),
		"two before": patronitest.Before + patronitest.After + patroniList + "\n",
	} {
		got, err := apply.ListMembers(apply.Fixture, &patronictlHost{fakeHost: newHost(), answer: out})
		if err != nil || strings.Join(got, ",") != "home-a Leader running,home-b Sync Standby streaming" {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
}

// Output with no whole list is never read as members.
func TestPatronictlListWithoutAWholeListRefuses(t *testing.T) {
	for name, out := range map[string]string{
		"logs only": patronitest.Before + patronitest.After,
		"empty":     "",
		"cut off":   patronitest.Before + patroniList[:len(patroniList)/2] + "\n" + patronitest.After,
		"an object": `{"Member": "home-a"}` + "\n",
	} {
		if got, err := apply.ListMembers(apply.Fixture, &patronictlHost{fakeHost: newHost(), answer: out}); err == nil {
			t.Errorf("%s read as %v", name, got)
		}
	}
}

// show-config's YAML is read past patronictl's log lines, and an answer with
// no settings in it is never read as every setting off.
func TestPatroniConfig(t *testing.T) {
	live, err := apply.PatroniConfig(patronitest.Wrap("loop_wait: 10\nsynchronous_mode: true\n"))
	if err != nil || live["synchronous_mode"] != true || live["loop_wait"] != 10 {
		t.Errorf("got %v, %v", live, err)
	}
	for _, out := range []string{"", patronitest.Before + patronitest.After} {
		if live, err := apply.PatroniConfig(out); err == nil {
			t.Errorf("%q read as %v", out, live)
		}
	}
}
