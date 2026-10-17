package storageadd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/storageadd"
)

// drift is every file h's manifest records with a checksum other than the
// file's on the host: what the next apply takes for a hand edit once the
// render moves on.
func (h *host) drift(t *testing.T) []string {
	t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files["/srv/paisans/f2a9/.paisans-manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range m.Files {
		content, ok := h.files["/"+f.Path]
		sum := sha256.Sum256([]byte(content))
		if ok && hex.EncodeToString(sum[:]) != f.SHA256 {
			out = append(out, f.Path)
		}
	}
	return out
}

// A reset join on a deployment whose gateway is a Garage node writes the
// gateway's garage.toml in the reset and its media routes after the join,
// and the gateway's manifest records both as written.
func TestAResetJoinRecordsWhatItWritesOnTheGateway(t *testing.T) {
	cfg, secrets := fixture(t)
	cfg.Storage.Garage.Consistency = "dangerous"
	a := cfg.Sites["home-a"]
	a.Roles = append(a.Roles, config.RoleGateway)
	a.Endpoint = "home-a.example.org:51820"
	cfg.Sites["home-a"] = a
	vm := cfg.Sites["vm"]
	vm.Roles = []config.Role{config.RoleWitness}
	cfg.Sites["vm"] = vm
	w := newWorld(t, cfg, secrets)
	w.provisioned(1, "home-a")
	w.deployGarage("home-b", 2)
	// The media routes as an earlier configuration rendered them, so the
	// join has them to write.
	for path, content := range w.hosts["home-a"].files {
		if strings.HasSuffix(path, "-media.caddy") {
			w.hosts["home-a"].files[path] = content + "# before the join\n"
		}
	}
	w.hosts["home-a"].recordManifest()

	p := w.build(storageadd.Options{ChangeReplication: true, Wait: 30_000_000_000})
	if err := storageadd.Execute(p); err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.SiteNames() {
		if d := w.hosts[name].drift(t); len(d) > 0 {
			t.Errorf("%s's manifest does not record what is on the host for %v", name, d)
		}
	}
}
