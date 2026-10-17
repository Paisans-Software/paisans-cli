package siteadd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/siteadd"
)

// drift is every file h's manifest records with a checksum other than the
// file's on the host: what the next apply takes for a hand edit once the
// render moves on.
func (w *world) drift(h *host) []string {
	w.t.Helper()
	var m render.Manifest
	if err := json.Unmarshal([]byte(h.files[w.cfg.Deployment().Manifest()]), &m); err != nil {
		w.t.Fatal(err)
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

// Every file a join writes on an existing site is in that site's manifest
// as written: the mesh file stage 2 writes on a site whose HAProxy stage 6
// writes as well among them.
func TestAJoinRecordsWhatItWrites(t *testing.T) {
	defer siteadd.SetFast()()
	w := newWorld(t)
	if err := siteadd.Execute(build(t, w)); err != nil {
		t.Fatal(err)
	}
	for _, name := range w.cfg.SiteNames() {
		if d := w.drift(w.hosts[name]); len(d) > 0 {
			t.Errorf("%s's manifest does not record what is on the host for %v", name, d)
		}
	}
}
