package render_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// withSharedDelegation is the fixture with the homeserver's delegation on the
// named app's own hostname, rendered.
func withSharedDelegation(t *testing.T, owner string) (string, map[string]string) {
	t.Helper()
	cfg := fixture(t)
	chat := cfg.Apps["chat"]
	chat.Hostnames = map[string]string{"wellknown": cfg.Apps[owner].Hostname}
	cfg.Apps["chat"] = chat
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the shared delegation is refused: %v", refusals)
	}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatalf("building the plan: %v", err)
	}
	return cfg.Apps[owner].Hostname, planFiles(plan)
}

// The account app on the apex and the homeserver's user identifiers ending in
// it: one host block for the name, the delegation documents routed to the
// homeserver's snippet, and everything else to the account app's.
func TestADelegationSharesAnotherAppsHostBlock(t *testing.T) {
	cfg := fixture(t)
	account := cfg.Apps["account"]
	account.Hostname = "example.org"
	cfg.Apps["account"] = account
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("the account app on the homeserver's delegation hostname is refused: %v", refusals)
	}
	plan, err := render.Build(cfg, fixtureSecrets(t))
	if err != nil {
		t.Fatalf("building the plan: %v", err)
	}
	files := planFiles(plan)

	caddyfile := files["vm/srv/paisans/f2a9/infra/caddy/etc/Caddyfile"]
	if n := strings.Count(caddyfile, "\nexample.org {"); n != 1 {
		t.Fatalf("example.org has %d host blocks; Caddy refuses more than one:\n%s", n, caddyfile)
	}
	block := hostBlock(t, caddyfile, "example.org")
	shared := strings.Index(block, "handle /.well-known/matrix/* {\n\t\timport /etc/caddy/snippets/chat-wellknown.caddy\n\t}")
	owner := strings.Index(block, "handle {\n\t\timport /etc/caddy/snippets/account.caddy\n\t}")
	if shared < 0 || owner < 0 {
		t.Fatalf("the host block does not route both apps:\n%s", block)
	}
	if shared > owner {
		t.Errorf("the delegation is written after the account app's catch all:\n%s", block)
	}
	if _, ok := files["vm/srv/paisans/f2a9/infra/caddy/etc/snippets/chat-wellknown.caddy"]; !ok {
		t.Error("the delegation's snippet was not rendered")
	}
}

// On a gated hostname the delegation is the first handle in the block, ahead
// of the gate and its bypasses. Caddy sorts a handle with one path ahead of a
// bare one, but keeps one with a named matcher of several conditions in the
// order written, so being first is what puts it ahead of those too. A
// federating server fetching delegation will not follow a redirect to a
// passkey prompt.
func TestADelegationOnAGatedHostnameIsAheadOfTheGate(t *testing.T) {
	hostname, files := withSharedDelegation(t, "talk")
	block := hostBlock(t, files["vm/srv/paisans/f2a9/infra/caddy/etc/Caddyfile"], hostname)
	first := strings.Index(block, "\thandle ")
	if first < 0 || !strings.HasPrefix(block[first:], "\thandle /.well-known/matrix/* {") {
		t.Errorf("the delegation is not the first handle on the gated hostname:\n%s", block)
	}
	if !strings.Contains(block, "import gate\n") {
		t.Errorf("the gated hostname lost its gate:\n%s", block)
	}
}
