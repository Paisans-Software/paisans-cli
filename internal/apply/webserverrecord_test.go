package apply_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// The files written for the operator's web server are recorded in the
// manifest beside everything else, and an apply that rewrites the manifest
// keeps them: they are this deployment's until the command that removes
// them says otherwise.
func TestApplyKeepsTheWebServerRecord(t *testing.T) {
	host := gatewayAt(t, buildA)
	d := plan(t).Deployment
	entry := render.ManifestWebServer{App: "status", Path: "/opt/caddy/paisans.d/paisans-f2a9-status.caddy", SHA256: "ab", Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}
	if err := apply.WriteWebServerRecord(host, d, []render.ManifestWebServer{entry}); err != nil {
		t.Fatal(err)
	}
	var before render.Manifest
	json.Unmarshal([]byte(host.files[manifestFile]), &before)
	if len(before.Files) == 0 || before.Caddy == nil {
		t.Fatalf("the rest of the manifest was lost: %+v", before)
	}

	p, err := apply.Build("vm", plan(t), acmeModule(t), host, apply.ResolveImages(&registry{digest: buildB}))
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(p, host); err != nil {
		t.Fatal(err)
	}
	got, err := apply.ReadWebServerRecord(host, d)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []render.ManifestWebServer{entry}) {
		t.Errorf("after apply the record is %+v", got)
	}
	if manifestCaddy(t, host).Reference == before.Caddy.Reference {
		t.Errorf("the manifest was not rewritten, so this proves nothing")
	}

	if err := apply.WriteWebServerRecord(host, d, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := apply.ReadWebServerRecord(host, d); got != nil {
		t.Errorf("emptied record reads %+v", got)
	}
}
