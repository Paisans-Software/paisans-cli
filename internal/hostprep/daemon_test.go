package hostprep_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

// The user's daemon.json: the userland proxy off, Docker's pools less the
// one overlapping the mesh, and the data root when admit set one.
func TestDaemonJSON(t *testing.T) {
	got, err := hostprep.DaemonJSON("172.18.0.0/24", "/srv/data/paisans-f2a9")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UserlandProxy *bool `json:"userland-proxy"`
		Pools         []struct {
			Base string `json:"base"`
			Size int    `json:"size"`
		} `json:"default-address-pools"`
		DataRoot string `json:"data-root"`
	}
	if err := json.Unmarshal([]byte(got), &d); err != nil {
		t.Fatal(err)
	}
	if d.UserlandProxy == nil || *d.UserlandProxy || d.DataRoot != "/srv/data/paisans-f2a9" {
		t.Errorf("got %s", got)
	}
	for _, p := range d.Pools {
		if p.Base == "172.18.0.0/16" || p.Size != 24 {
			t.Errorf("pool %+v", p)
		}
	}
	if !strings.HasSuffix(got, "}\n") {
		t.Errorf("no final newline: %q", got)
	}
}
