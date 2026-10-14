package secretsrotate

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
)

// mqWorld is two apps sites, each with its own RabbitMQ holding the password
// it accepts, an Mbin on rabbitmq and one on the default queue.
type mqWorld struct {
	t      *testing.T
	cfg    *config.Config
	store  *memStore
	broker map[string]string // site -> password the broker accepts
	cmds   []pgCmd
	log    []string
	down   map[string]bool
}

func newMQWorld(t *testing.T) *mqWorld {
	cfg := &config.Config{
		ID: "3f2a6c1e-8b4d-4e2a-9c1f-7d5e6b8a9c0d",
		Sites: map[string]config.Site{
			"home-a": {Roles: []config.Role{config.RoleApps}, Address: "10.0.0.1"},
			"home-b": {Roles: []config.Role{config.RoleApps}, Address: "10.0.0.2"},
		},
		Apps: map[string]config.App{
			"forum": {Blueprint: config.Blueprint("mbin"), Placement: config.Placement{Mode: config.PlacementCluster}, Settings: map[string]any{"queue": "rabbitmq"}},
			"plain": {Blueprint: config.Blueprint("mbin"), Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
			"gate":  {Blueprint: config.BlueprintOAuth2Proxy, Placement: config.Placement{Mode: config.PlacementPinned, Site: "home-a"}},
		},
	}
	s := &config.Secrets{Apps: map[string]map[string]any{"forum": {"rabbitmq_password": "mq-old"}, "plain": {"rabbitmq_password": "mq-plain"}}}
	return &mqWorld{t: t, cfg: cfg, store: &memStore{s: s}, broker: map[string]string{"home-a": "mq-old", "home-b": "mq-old"}, down: map[string]bool{}}
}

type mqHost struct {
	w    *mqWorld
	site string
}

func (h *mqHost) Run(c string) (string, error)           { return "", errors.New("unexpected Run: " + c) }
func (h *mqHost) ReadFile(string) (string, bool, error)  { return "", false, nil }
func (h *mqHost) WriteFile(string, string, uint32) error { return nil }
func (h *mqHost) Describe() string                       { return h.site }
func (h *mqHost) RunInput(command, stdin string) (string, error) {
	w := h.w
	w.cmds = append(w.cmds, pgCmd{h.site, command, stdin})
	if w.down[h.site] {
		return "Error: unable to perform an operation on node 'rabbit@rabbitmq'", errors.New("exit 69")
	}
	p := strings.TrimSuffix(stdin, "\n")
	switch {
	case strings.Contains(command, "rabbitmqctl change_password mbin"):
		w.broker[h.site] = p
		w.log = append(w.log, "change "+h.site)
		return "Changing password for user \"mbin\" ...\n", nil
	case strings.Contains(command, "rabbitmqctl authenticate_user mbin"):
		if w.broker[h.site] != p {
			return "Authenticating user \"mbin\" ...\nError:\nFailed to authenticate user \"mbin\" to node rabbit@rabbitmq\n", errors.New("exit 70")
		}
		return "Success\n", nil
	}
	return "", fmt.Errorf("unexpected command %s", command)
}

func (w *mqWorld) env() Env {
	sites := map[string]apply.Transport{}
	for name := range w.cfg.Sites {
		sites[name] = &mqHost{w, name}
	}
	return Env{Cfg: w.cfg, Store: w.store, Sites: sites}
}

func TestInAppRabbitMQUsersAndCost(t *testing.T) {
	w := newMQWorld(t)
	r := InApp(w.cfg, "forum", "rabbitmq_password")
	if r.Path() != "apps.forum.rabbitmq_password" {
		t.Fatalf("path %q", r.Path())
	}
	if got := usesOf(w.env(), r); !slices.Equal(got, []string{"home-a:forum", "home-b:forum"}) {
		t.Fatalf("users %v", got)
	}
	if c := r.Cost(); c.SignsOut || c.Downtime != "the forum stack, for one restart on each site in turn" {
		t.Fatalf("cost %+v", c)
	}
}

func TestInAppRabbitMQChangesEveryBrokerWithThePasswordOnStdin(t *testing.T) {
	w := newMQWorld(t)
	e := w.env()
	r := InApp(w.cfg, "forum", "rabbitmq_password")
	if ok, err := r.Accepts(e, "mq-new"); err != nil || ok {
		t.Fatalf("accepts before: %v %v", ok, err)
	}
	if err := r.Checked(e, "mq-new"); err != nil {
		t.Fatal(err)
	}
	if err := r.Checked(e, "mq-new"); err != nil {
		t.Fatalf("again: %v", err)
	}
	for site, p := range w.broker {
		if p != "mq-new" {
			t.Fatalf("%s accepts %q", site, p)
		}
	}
	if ok, err := r.Accepts(e, "mq-new"); err != nil || !ok {
		t.Fatalf("accepts after: %v %v", ok, err)
	}
	if ok, _ := r.Accepts(e, "mq-old"); ok {
		t.Fatal("the old password is still accepted")
	}
	for _, c := range w.cmds {
		if strings.Contains(c.command, "mq-") {
			t.Fatalf("a password in a command line: %s", c.command)
		}
		if !strings.Contains(c.command, "/forum/compose.yaml exec -T 'rabbitmq' 'sh' '-c'") {
			t.Fatalf("not run in the forum's rabbitmq: %s", c.command)
		}
	}
}

func TestInAppRabbitMQAcceptsReportsAnUnreachableBrokerAsAnError(t *testing.T) {
	w := newMQWorld(t)
	w.down["home-b"] = true
	ok, err := InApp(w.cfg, "forum", "rabbitmq_password").Accepts(w.env(), "mq-old")
	if err == nil || ok {
		t.Fatalf("a broker that did not answer is %v %v, not a refused password", ok, err)
	}
	if err := InApp(w.cfg, "forum", "rabbitmq_password").Checked(w.env(), "mq-new"); err == nil {
		t.Fatal("a broker that did not answer took the password")
	}
}

func TestInAppRabbitMQRefusesAPasswordItCannotSend(t *testing.T) {
	w := newMQWorld(t)
	if err := InApp(w.cfg, "forum", "rabbitmq_password").Checked(w.env(), "a\nb"); err == nil {
		t.Fatal("a newline password was sent")
	}
	if len(w.cmds) != 0 {
		t.Fatalf("commands ran: %v", w.cmds)
	}
}

// Review Focus 3: an Mbin on the default queue has no RabbitMQ user.
func TestInAppRabbitMQOnPostgresQueueIsKeptAndNothingIsWritten(t *testing.T) {
	w := newMQWorld(t)
	err := InApp(w.cfg, "plain", "rabbitmq_password").(Prechecker).Precheck(w.env())
	var kept *KeptError
	if !errors.As(err, &kept) || !errors.Is(err, ErrKept) {
		t.Fatalf("precheck %v, want a KeptError", err)
	}
	if kept.Path != "apps.plain.rabbitmq_password" || !strings.Contains(kept.Reason, "RabbitMQ") {
		t.Fatalf("kept %+v", kept)
	}
	if len(w.cmds) != 0 {
		t.Fatalf("commands ran: %v", w.cmds)
	}
	if err := w.run(InApp(w.cfg, "plain", "rabbitmq_password")); err != nil {
		t.Fatal(err)
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations %v", w.store.s.Rotations)
	}
}

func TestInAppRabbitMQPrecheckRefusesTheWrongApp(t *testing.T) {
	w := newMQWorld(t)
	for _, app := range []string{"gate", "missing"} {
		err := InApp(w.cfg, app, "rabbitmq_password").(Prechecker).Precheck(w.env())
		if err == nil || errors.Is(err, ErrKept) {
			t.Fatalf("%s: %v", app, err)
		}
	}
	if err := InApp(w.cfg, "forum", "rabbitmq_password").(Prechecker).Precheck(w.env()); err != nil {
		t.Fatal(err)
	}
}

func (w *mqWorld) run(r Rotator) error {
	e := w.env()
	e.Apply = func(site string, s *config.Secrets, stacks []string) error {
		v, _ := s.Get("apps.forum.rabbitmq_password")
		w.log = append(w.log, "apply "+site+" "+v)
		return nil
	}
	e.Healthy = func(site string, stacks []string) error { w.log = append(w.log, "healthy "+site); return nil }
	run, err := Plan(e, []Rotator{r})
	if err != nil {
		return err
	}
	return run.Execute(e, signOut)
}

func TestInAppRabbitMQRotatesBrokersBeforeTheStackIsHandedTheValue(t *testing.T) {
	w := newMQWorld(t)
	if err := w.run(InApp(w.cfg, "forum", "rabbitmq_password")); err != nil {
		t.Fatal(err)
	}
	next, _ := w.store.s.Get("apps.forum.rabbitmq_password")
	if next == "mq-old" || next == "" {
		t.Fatalf("file holds %q", next)
	}
	want := []string{"change home-a", "change home-b", "apply home-a " + next, "healthy home-a", "apply home-b " + next, "healthy home-b"}
	if !slices.Equal(w.log, want) {
		t.Fatalf("log\n%v\nwant\n%v", w.log, want)
	}
}

func TestInAppRabbitMQNextNeverBeginsWithADash(t *testing.T) {
	w := newMQWorld(t)
	draws := []string{"-dashed-one", "--dashed-two", "-dashed-three", "clean-value-1"}
	old := inAppGenerate
	defer func() { inAppGenerate = old }()
	inAppGenerate = func(*config.Config, string, *config.Secrets) (string, error) {
		v := draws[0]
		draws = draws[1:]
		return v, nil
	}
	got, err := InApp(w.cfg, "forum", "rabbitmq_password").Next(w.env(), w.store.s)
	if err != nil || got != "clean-value-1" {
		t.Fatalf("next %q %v", got, err)
	}
}

func TestInAppRabbitMQRefusesALeadingDashWithNoCommand(t *testing.T) {
	w := newMQWorld(t)
	r := InApp(w.cfg, "forum", "rabbitmq_password")
	if err := r.Checked(w.env(), "-secret"); err == nil || !strings.Contains(err.Error(), "dash") {
		t.Fatalf("checked: %v", err)
	}
	if _, err := r.Accepts(w.env(), "-secret"); err == nil || !strings.Contains(err.Error(), "dash") {
		t.Fatalf("accepts: %v", err)
	}
	if len(w.cmds) != 0 {
		t.Fatalf("commands ran: %v", w.cmds)
	}
}

func TestInAppRabbitMQSendsSpacesAndQuotesUnchanged(t *testing.T) {
	w := newMQWorld(t)
	pw := `zq spaced'quoted"dollar$value`
	if err := InApp(w.cfg, "forum", "rabbitmq_password").Checked(w.env(), pw); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.cmds {
		if c.stdin != pw+"\n" || strings.Contains(c.command, "spaced") {
			t.Fatalf("stdin %q command %q", c.stdin, c.command)
		}
	}
}

func TestInAppRabbitMQResumesWithOneBrokerAlreadyOnTheNextValue(t *testing.T) {
	w := newMQWorld(t)
	w.store.s.Rotations = map[string]string{"apps.forum.rabbitmq_password": "mq-next"}
	w.broker["home-a"] = "mq-next"
	if err := w.run(InApp(w.cfg, "forum", "rabbitmq_password")); err != nil {
		t.Fatal(err)
	}
	for site, p := range w.broker {
		if p != "mq-next" {
			t.Fatalf("%s accepts %q", site, p)
		}
	}
	if v, _ := w.store.s.Get("apps.forum.rabbitmq_password"); v != "mq-next" {
		t.Fatalf("file holds %q", v)
	}
	if len(w.store.s.Rotations) != 0 {
		t.Fatalf("rotations %v", w.store.s.Rotations)
	}
}

// A pending password that begins with a dash cannot be sent to the broker,
// and the refusal says how to remove it.
func TestInAppRabbitMQLeadingDashNamesTheWayOut(t *testing.T) {
	w := newMQWorld(t)
	_, err := InApp(w.cfg, "forum", "rabbitmq_password").Accepts(w.env(), "-starts-with-a-dash")
	if err == nil || !strings.Contains(err.Error(), `"A rotation that cannot finish"`) {
		t.Fatalf("err = %v", err)
	}
}
