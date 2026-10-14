package secretsrotate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/blueprints/coretest"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// brokerBlueprint declares a broker password changed inside the running app,
// and a keypair unit, as the core catalog's mbin blueprint does.
const brokerBlueprint = `apiVersion: paisans.blueprint/v1
name: broker
services:
  - {name: app, image: "example/app:1"}
  - {name: rabbitmq, image: "example/rabbitmq:1"}
database: {engine: none}
port: 8000
config: {format: env, file: .env}
health: {path: /status, status: [200]}
settings:
  queue: {type: enum, values: [postgres, rabbitmq], default: postgres}
secrets:
  generate:
    - name: rabbitmq_password
      rotation_cost: restart
      rotate:
        service: rabbitmq
        when: "has(app.settings.queue) && app.settings.queue == 'rabbitmq'"
        kept: "the app runs its queue in Postgres and starts no RabbitMQ, so no account has this password"
        change: ["sh", "-c", 'IFS= read -r p; exec rabbitmqctl change_password mbin "$p"']
        check: ["sh", "-c", 'IFS= read -r p; exec rabbitmqctl authenticate_user mbin "$p"']
        refused: "Failed to authenticate"
    - {name: oauth_passphrase, group: oauth, rotation_cost: signs-out}
    - {name: oauth_private_key, format: rsa-keypair, passphrase: oauth_passphrase, group: oauth}
    - {name: oauth_public_key, format: rsa-public, of: oauth_private_key, group: oauth}
    - {name: data_key, format: hex32, never_rotate: "it encrypts the app's data"}
`

// declaredWorld is mqWorld with its apps on a catalog blueprint that
// declares their rotations, rather than on Mbin.
func declaredWorld(t *testing.T) *mqWorld {
	t.Helper()
	coretest.Cache(t)
	repo := filepath.Join(t.TempDir(), "paisans-example")
	for name, content := range map[string]string{
		"catalog.yaml":                        "apiVersion: paisans.catalog/v1\nname: example\n",
		"blueprints/broker/blueprint.yaml":    brokerBlueprint,
		"blueprints/broker/compose.yaml.tmpl": "services: {}\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "catalog")
	w := newMQWorld(t)
	w.cfg.Catalogs = map[string]config.Catalog{"ex": {URL: repo, Commit: git("rev-parse", "HEAD")}}
	for _, name := range []string{"forum", "plain"} {
		app := w.cfg.Apps[name]
		app.Blueprint = "ex/broker"
		w.cfg.Apps[name] = app
	}
	if problems := config.ResolveBlueprints(w.cfg); len(problems) > 0 {
		t.Fatal(problems)
	}
	return w
}

// A secret its blueprint changes inside the running app is changed in the
// service it names on every site, with the value on stdin and never in the
// command line, and is accepted once the check passes.
func TestAnInAppRotationRunsTheBlueprintsCommands(t *testing.T) {
	w := declaredWorld(t)
	e := w.env()
	r := InApp(w.cfg, "forum", "rabbitmq_password")
	if c := r.Cost(); c.SignsOut || c.Downtime != "the forum stack, for one restart on each site in turn" {
		t.Errorf("cost %+v", c)
	}
	if err := r.(Prechecker).Precheck(e); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.Accepts(e, "mq-new"); err != nil || ok {
		t.Fatalf("accepts before: %v %v", ok, err)
	}
	if err := r.Checked(e, "mq-new"); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.Accepts(e, "mq-new"); err != nil || !ok {
		t.Fatalf("accepts after: %v %v", ok, err)
	}
	for _, c := range w.cmds {
		if strings.Contains(c.command, "mq-") {
			t.Fatalf("a value in a command line: %s", c.command)
		}
		if !strings.Contains(c.command, "/forum/compose.yaml exec -T 'rabbitmq' 'sh' '-c'") {
			t.Fatalf("not run in the forum's rabbitmq: %s", c.command)
		}
	}
	if err := r.Checked(e, "-x"); err == nil {
		t.Error("a value with a leading dash was sent")
	}
	w.down["home-b"] = true
	if ok, err := r.Accepts(e, "mq-new"); err == nil || ok {
		t.Errorf("an unreachable service is %v %v, not an error", ok, err)
	}

	// An app whose condition does not hold keeps the secret, with why.
	err := InApp(w.cfg, "plain", "rabbitmq_password").(Prechecker).Precheck(e)
	var kept *KeptError
	if !errors.As(err, &kept) || !strings.Contains(kept.Reason, "starts no RabbitMQ") {
		t.Errorf("precheck %v, want kept", err)
	}
}

// A unit's next value holds a new value for every member, its keypair
// encrypted under the new passphrase, and a unit whose keypair does not open
// is refused at promotion.
func TestASecretUnitRotatesTogether(t *testing.T) {
	w := declaredWorld(t)
	priv, pub, err := secretsgen.ReplacementKeypair("pass-old")
	if err != nil {
		t.Fatal(err)
	}
	w.store.s.Apps["forum"]["oauth_passphrase"] = "pass-old"
	w.store.s.Apps["forum"]["oauth_private_key"] = priv
	w.store.s.Apps["forum"]["oauth_public_key"] = pub
	r := AppGroup(w.cfg, "forum", "oauth")
	if r.Path() != "apps.forum.oauth" || !r.Cost().SignsOut {
		t.Fatalf("path %s, cost %+v", r.Path(), r.Cost())
	}
	next, err := r.Next(w.env(), w.store.s)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Promote(w.store.s, next); err != nil {
		t.Fatal(err)
	}
	pass, _ := w.store.s.Get("apps.forum.oauth_passphrase")
	key, _ := w.store.s.Get("apps.forum.oauth_private_key")
	if pass == "pass-old" || key == priv {
		t.Fatal("the unit was not replaced")
	}
	if _, err := secretsgen.OpenPrivateKey(key, pass); err != nil {
		t.Errorf("the new key does not open with the new passphrase: %v", err)
	}
	var values map[string]string
	if err := yaml.Unmarshal([]byte(next), &values); err != nil {
		t.Fatal(err)
	}
	values["oauth_passphrase"] = "another"
	broken, _ := yaml.Marshal(values)
	if err := r.Promote(w.store.s, string(broken)); err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Errorf("a unit whose key does not open: %v", err)
	}
}

// A secret its blueprint says is never rotated is refused a replacement, with
// the blueprint's reason, and a keypair is refused on its own.
func TestADeclaredSecretIsReplacedOnlyAsItsBlueprintAllows(t *testing.T) {
	w := declaredWorld(t)
	w.store.s.Apps["forum"]["data_key"] = strings.Repeat("a", 64)
	w.store.s.Apps["forum"]["oauth_private_key"] = "x"
	if _, err := secretsgen.ReplacementIn(w.cfg, "apps.forum.data_key", w.store.s); err == nil || !strings.Contains(err.Error(), "it encrypts the app's data") {
		t.Errorf("never rotated: %v", err)
	}
	if _, err := secretsgen.ReplacementIn(w.cfg, "apps.forum.oauth_private_key", w.store.s); err == nil || !strings.Contains(err.Error(), "apps.forum.oauth") {
		t.Errorf("a keypair alone: %v", err)
	}
}
