package oidcclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/pocketid"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

const testKey = "not-a-real-api-key-0003"

// fakeIDP is Pocket ID in memory: clients with secret prefixes, groups and
// users, reached through the curl configs internal/pocketid writes.
type fakeIDP struct {
	clients   []*pocketid.OIDCClient
	secrets   map[string][]pocketid.ClientSecret
	groups    []pocketid.Group
	users     []*pocketid.User
	commands  []string
	stdins    []string
	mutations []string
	// sent is every secret value Pocket ID was given, in order.
	sent []string
	// updates is every client update's body, in order.
	updates []string
	// nextSecret numbers the secrets Pocket ID is sent, for their IDs.
	nextSecret int
	// onMutation runs before a mutation is served, so a test can see what
	// had already happened at that moment.
	onMutation func(method, path string)
}

func newFake() *fakeIDP {
	return &fakeIDP{secrets: map[string][]pocketid.ClientSecret{}}
}

func (f *fakeIDP) Describe() string { return "home-a.local" }

func (f *fakeIDP) RunInput(command, stdin string) (string, error) {
	f.commands = append(f.commands, command)
	f.stdins = append(f.stdins, stdin)
	method, target, body := readCurlConfig(stdin)
	u, _ := url.Parse(target)
	if method != "GET" {
		f.mutations = append(f.mutations, method+" "+u.Path)
		if f.onMutation != nil {
			f.onMutation(method, u.Path)
		}
	}
	status, out := f.serve(method, u, body)
	return fmt.Sprintf("%s\npaisans-http-status:%d", out, status), nil
}

func page[T any](data []T) string {
	raw, _ := json.Marshal(map[string]any{"data": data, "pagination": map[string]int{"totalPages": 1}})
	return string(raw)
}

func marshal(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func (f *fakeIDP) client(id string) *pocketid.OIDCClient {
	for _, c := range f.clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (f *fakeIDP) serve(method string, u *url.URL, body string) (int, string) {
	path := u.Path
	search := u.Query().Get("search")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case method == "GET" && path == "/api/oidc/clients":
		var out []pocketid.OIDCClient
		for _, c := range f.clients {
			if strings.Contains(c.Name, search) {
				out = append(out, *c)
			}
		}
		return 200, page(out)
	case method == "GET" && len(parts) == 4 && parts[2] == "clients":
		// As Pocket ID does: 404 for an ID it does not hold.
		if c := f.client(parts[3]); c != nil {
			return 200, marshal(c)
		}
		return 404, `{"error":"Record not found"}`
	case method == "GET" && len(parts) == 5 && parts[4] == "secrets":
		return 200, marshal(f.secrets[parts[3]])
	case method == "POST" && path == "/api/oidc/clients":
		var n pocketid.NewOIDCClient
		_ = json.Unmarshal([]byte(body), &n)
		c := &pocketid.OIDCClient{ID: fmt.Sprintf("c-%d", len(f.clients)+1), Name: n.Name, CallbackURLs: n.CallbackURLs, PkceEnabled: n.PkceEnabled, IsGroupRestricted: n.IsGroupRestricted, IsPublic: n.IsPublic, SkipConsent: n.SkipConsent}
		if n.LaunchURL != "" {
			c.LaunchURL = &n.LaunchURL
		}
		f.clients = append(f.clients, c)
		return 201, marshal(c)
	case method == "PUT" && len(parts) == 4 && parts[2] == "clients":
		// As Pocket ID does: every field is overwritten with what is sent,
		// and an unrestricted client loses its allowed groups.
		f.updates = append(f.updates, body)
		var in struct {
			Name              string   `json:"name"`
			CallbackURLs      []string `json:"callbackURLs"`
			IsPublic          bool     `json:"isPublic"`
			PkceEnabled       bool     `json:"pkceEnabled"`
			IsGroupRestricted bool     `json:"isGroupRestricted"`
			LaunchURL         *string  `json:"launchURL"`
			SkipConsent       bool     `json:"skipConsent"`
		}
		_ = json.Unmarshal([]byte(body), &in)
		c := f.client(parts[3])
		c.Name, c.CallbackURLs, c.IsPublic, c.PkceEnabled, c.IsGroupRestricted, c.LaunchURL = in.Name, in.CallbackURLs, in.IsPublic, in.PkceEnabled, in.IsGroupRestricted, in.LaunchURL
		c.SkipConsent = in.SkipConsent
		if !in.IsGroupRestricted {
			c.AllowedUserGroups = nil
		}
		return 200, marshal(c)
	case method == "POST" && len(parts) == 5 && parts[4] == "secrets":
		var in struct{ Secret string }
		_ = json.Unmarshal([]byte(body), &in)
		f.sent = append(f.sent, in.Secret)
		f.nextSecret++
		f.secrets[parts[3]] = append(f.secrets[parts[3]], pocketid.ClientSecret{ID: fmt.Sprintf("s-%d", f.nextSecret), Prefix: in.Secret[:4], IsActive: true})
		return 201, marshal(map[string]string{"secret": in.Secret})
	case method == "DELETE" && len(parts) == 6 && parts[4] == "secrets":
		held := f.secrets[parts[3]]
		for i, s := range held {
			if s.ID == parts[5] {
				f.secrets[parts[3]] = append(held[:i:i], held[i+1:]...)
				return 204, ""
			}
		}
		return 404, `{"error":"Client secret not found"}`
	case method == "PUT" && len(parts) == 5 && parts[4] == "allowed-user-groups":
		var in struct {
			UserGroupIds []string `json:"userGroupIds"`
		}
		_ = json.Unmarshal([]byte(body), &in)
		c := f.client(parts[3])
		c.AllowedUserGroups = nil
		for _, id := range in.UserGroupIds {
			for _, g := range f.groups {
				if g.ID == id {
					c.AllowedUserGroups = append(c.AllowedUserGroups, g)
				}
			}
		}
		return 200, marshal(c)
	case method == "GET" && path == "/api/user-groups":
		var out []pocketid.Group
		for _, g := range f.groups {
			if strings.Contains(g.Name, search) {
				out = append(out, g)
			}
		}
		return 200, page(out)
	case method == "POST" && path == "/api/user-groups":
		var g pocketid.Group
		_ = json.Unmarshal([]byte(body), &g)
		g.ID = fmt.Sprintf("g-%d", len(f.groups)+1)
		f.groups = append(f.groups, g)
		return 201, marshal(g)
	case method == "GET" && path == "/api/users":
		var out []pocketid.User
		for _, u := range f.users {
			if strings.Contains(u.Username, search) {
				out = append(out, *u)
			}
		}
		return 200, page(out)
	case method == "PUT" && len(parts) == 4 && parts[3] == "user-groups":
		var in struct {
			UserGroupIds []string `json:"userGroupIds"`
		}
		_ = json.Unmarshal([]byte(body), &in)
		for _, u := range f.users {
			if u.ID == parts[2] {
				u.UserGroups = nil
				for _, id := range in.UserGroupIds {
					for _, g := range f.groups {
						if g.ID == id {
							u.UserGroups = append(u.UserGroups, g)
						}
					}
				}
			}
		}
		return 200, `{}`
	}
	return 404, `{"error":"no route ` + method + " " + path + `"}`
}

func readCurlConfig(cfg string) (method, target, body string) {
	for _, line := range strings.Split(cfg, "\n") {
		option, quoted, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		value := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(strings.Trim(quoted, `"`))
		switch option {
		case "url":
			target = value
		case "request":
			method = value
		case "data-raw":
			body = value
		}
	}
	return method, target, body
}

// memRecorder is the secrets file, in memory.
type memRecorder struct {
	rec  Recorded
	fail bool
	// calls counts writes.
	calls int
}

func (m *memRecorder) Record(id, secret string) error {
	m.calls++
	if m.fail {
		return errors.New("disk full")
	}
	m.rec = Recorded{ClientID: id, ClientSecret: secret}
	return nil
}

func api(f *fakeIDP) *pocketid.Client {
	return &pocketid.Client{Transport: f, BaseURL: "http://10.44.0.1:1411", APIKey: testKey}
}

// bare is the launch URL the toolkit set before blueprints named a path, and
// connect is Mbin's default now.
const (
	bare    = "https://talk.example.org"
	connect = "https://talk.example.org/oauth/oidc/connect"
)

func talk() Desired {
	return Desired{App: "talk", Name: "talk", CallbackURL: "https://talk.example.org/oauth/oidc/verify", LaunchURL: connect, ToolkitLaunchURLs: []string{bare, connect}, PKCE: true, AdminGroup: "admins"}
}

func secretSource(values ...string) func() (string, error) {
	return func() (string, error) {
		v := values[0]
		values = values[1:]
		return v, nil
	}
}

func plan(t *testing.T, f *fakeIDP, d Desired, rec Recorded) *Plan {
	t.Helper()
	s, err := Probe(api(f), d, rec.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(d, rec, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func kinds(p *Plan) []StepKind {
	var out []StepKind
	for _, s := range p.Steps {
		out = append(out, s.Kind)
	}
	return out
}

// From nothing, the plan creates the group, the client and the secret, shows
// the client body exactly, and probing changed nothing.
func TestAFreshPlanShowsEverythingAndChangesNothing(t *testing.T) {
	f := newFake()
	p := plan(t, f, talk(), Recorded{})
	want := []StepKind{CreateGroup, CreateClient, AddSecret}
	if fmt.Sprint(kinds(p)) != fmt.Sprint(want) {
		t.Fatalf("planned %v, want %v", kinds(p), want)
	}
	if !strings.Contains(p.Steps[1].Detail, `{"name":"talk","callbackURLs":["https://talk.example.org/oauth/oidc/verify"],"isPublic":false,"pkceEnabled":true,"isGroupRestricted":false,"launchURL":"https://talk.example.org/oauth/oidc/connect","skipConsent":false}`) {
		t.Errorf("the client line does not show the body: %s", p.Steps[1].Detail)
	}
	if len(f.mutations) != 0 {
		t.Errorf("a probe mutated: %v", f.mutations)
	}
	var titles []string
	for _, s := range p.Steps {
		titles = append(titles, s.Title)
	}
	if want := "[create group admins create OIDC client talk create client secret for talk]"; fmt.Sprint(titles) != want {
		t.Errorf("titles %v, want %s", titles, want)
	}
}

// Execute reports each mutation as a step titled as the plan titles it, with
// what it sends as the step's detail, and the record of the secret as a
// detail of the secret's step.
func TestExecuteReportsAStepPerMutation(t *testing.T) {
	f := newFake()
	p := plan(t, f, talk(), Recorded{})
	r := &ui.Recorder{Verbose_: true}
	p.Report = r
	if err := Execute(p, api(f), &memRecorder{}, secretSource("reported-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	if i := r.Index("done", "create OIDC client talk"); i < 0 || r.Events[i].Extra != "created" {
		t.Errorf("the client step did not end created:\n%s", r.Lines())
	}
	if !r.Has("done", "create client secret for talk") || !r.Has("detail", "recorded oidc_clients.talk.client_id and oidc_clients.talk.client_secret") {
		t.Errorf("the secret step or its record is missing:\n%s", r.Lines())
	}
	if !r.Has("detail", "POST /api/oidc/clients") {
		t.Errorf("the request is not a detail:\n%s", r.Lines())
	}
	if strings.Contains(r.Lines(), "reported-secret-not-real-0001") {
		t.Error("the secret was reported")
	}
}

// A mutation that fails marks its step failed, and the error returned still
// names the step.
func TestExecuteMarksTheFailedStep(t *testing.T) {
	f := newFake()
	p := plan(t, f, talk(), Recorded{})
	r := &ui.Recorder{}
	p.Report = r
	err := Execute(p, api(f), &memRecorder{fail: true}, secretSource("failed-secret-not-real-0001"))
	if err == nil {
		t.Fatal("a failed record was not an error")
	}
	if !r.Has("fail", "create client secret for talk") {
		t.Errorf("the failed step is not marked:\n%s", r.Lines())
	}
}

// The secret is recorded before Pocket ID is sent it, never appears in a
// command line, and a fresh plan afterwards is empty.
func TestExecuteRecordsFirstAndConverges(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	f.onMutation = func(method, path string) {
		if strings.HasSuffix(path, "/secrets") && rec.rec.ClientSecret == "" {
			t.Error("Pocket ID was sent a secret before it was recorded")
		}
	}
	secret := "generated-client-secret-not-real-01"
	p := plan(t, f, talk(), Recorded{})
	if err := Execute(p, api(f), rec, secretSource(secret)); err != nil {
		t.Fatal(err)
	}
	if rec.rec != (Recorded{ClientID: "c-1", ClientSecret: secret}) {
		t.Errorf("recorded %+v", rec.rec)
	}
	if len(f.sent) != 1 || f.sent[0] != secret {
		t.Errorf("Pocket ID holds %v", f.sent)
	}
	for _, c := range f.commands {
		if c != pocketid.CurlCommand {
			t.Errorf("ran %q", c)
		}
	}
	for _, s := range p.Steps {
		if strings.Contains(s.Detail, secret) {
			t.Error("a plan line carries the secret")
		}
	}
	again := plan(t, f, talk(), rec.rec)
	if len(again.Steps) != 0 {
		t.Errorf("a second run plans %v", kinds(again))
	}
}

func TestRotateAddsASecretAndKeepsTheOldOne(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("first-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	d := talk()
	d.RotateSecret = true
	p := plan(t, f, d, rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{AddSecret}) || !strings.Contains(p.Steps[0].Detail, "stay valid") {
		t.Fatalf("rotation planned %v: %v", kinds(p), p.Steps)
	}
	if err := Execute(p, api(f), rec, secretSource("second-secret-not-real-0002")); err != nil {
		t.Fatal(err)
	}
	if rec.rec.ClientSecret != "second-secret-not-real-0002" || len(f.secrets["c-1"]) != 2 {
		t.Errorf("after rotation: recorded %q, Pocket ID holds %d", rec.rec.ClientSecret, len(f.secrets["c-1"]))
	}
}

// A run that recorded its secret and stopped before sending it resumes by
// sending the recorded one, not by minting another.
func TestAnInterruptedRunSendsTheRecordedSecret(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("first-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	f.secrets["c-1"] = nil // as if the POST never landed
	p := plan(t, f, talk(), rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{SendRecordedSecret}) {
		t.Fatalf("planned %v", kinds(p))
	}
	if err := Execute(p, api(f), rec, func() (string, error) { return "", errors.New("must not generate") }); err != nil {
		t.Fatal(err)
	}
	if f.sent[len(f.sent)-1] != "first-secret-not-real-0001" {
		t.Error("the recorded secret was not the one sent")
	}
}

// Credentials recorded for a client Pocket ID does not have are replaced: the
// client is created as from nothing, the new credentials are recorded over the
// stale ones, and the plan says which recorded client it replaces.
func TestRecordedCredentialsForAMissingClientAreReplaced(t *testing.T) {
	f := newFake()
	stale := Recorded{ClientID: "stale-client-id-not-real", ClientSecret: "stale-secret-not-real-0001"}
	p := plan(t, f, talk(), stale)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{CreateGroup, CreateClient, AddSecret}) {
		t.Fatalf("planned %v", kinds(p))
	}
	if p.Replaces != stale.ClientID {
		t.Errorf("Replaces is %q", p.Replaces)
	}
	if !strings.Contains(p.Steps[1].Detail, stale.ClientID) {
		t.Errorf("the create step does not name the client it replaces: %s", p.Steps[1].Detail)
	}
	for _, s := range p.Steps {
		if strings.Contains(s.Title+s.Detail, stale.ClientSecret) {
			t.Error("a plan line carries the recorded secret")
		}
	}
	rec := &memRecorder{rec: stale}
	if err := Execute(p, api(f), rec, secretSource("replacing-secret-not-real-01")); err != nil {
		t.Fatal(err)
	}
	if rec.rec != (Recorded{ClientID: "c-1", ClientSecret: "replacing-secret-not-real-01"}) {
		t.Errorf("recorded %q", rec.rec.ClientID)
	}
	if again := plan(t, f, talk(), rec.rec); len(again.Steps) != 0 || again.Replaces != "" {
		t.Errorf("a second run plans %v, replacing %q", kinds(again), again.Replaces)
	}
}

// If the secret cannot be recorded, Pocket ID is sent nothing.
func TestARecordFailureSendsNothing(t *testing.T) {
	f := newFake()
	err := Execute(plan(t, f, talk(), Recorded{}), api(f), &memRecorder{fail: true}, secretSource("never-sent-secret-not-real-01"))
	if err == nil || !strings.Contains(err.Error(), "sent nothing") {
		t.Fatalf("got %v", err)
	}
	if len(f.sent) != 0 {
		t.Error("a secret reached Pocket ID that is recorded nowhere")
	}
	if strings.Contains(err.Error(), "never-sent-secret-not-real-01") {
		t.Error("the error carries the secret")
	}
}

func TestAnExistingClientThatDiffersIsRefused(t *testing.T) {
	f := newFake()
	f.clients = []*pocketid.OIDCClient{{ID: "c-9", Name: "talk", CallbackURLs: []string{"https://talk.example.org/oauth/callback"}}}
	s, err := Probe(api(f), talk(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(talk(), Recorded{}, s)
	if err == nil || !strings.Contains(err.Error(), "do not include https://talk.example.org/oauth/oidc/verify") || !strings.Contains(err.Error(), "PKCE is off") {
		t.Fatalf("got %v", err)
	}
}

// A member group restricts the client to the member and admin groups.
func TestAMemberGroupRestrictsTheClient(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	p := plan(t, f, d, Recorded{})
	want := []StepKind{CreateGroup, CreateGroup, CreateClient, AllowGroups, AddSecret}
	if fmt.Sprint(kinds(p)) != fmt.Sprint(want) {
		t.Fatalf("planned %v", kinds(p))
	}
	rec := &memRecorder{}
	if err := Execute(p, api(f), rec, secretSource("member-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	c := f.clients[0]
	if !c.IsGroupRestricted || len(c.AllowedUserGroups) != 2 {
		t.Errorf("client %+v", c)
	}
}

// A client made before the launch URL was set, restricted and holding its
// allowed groups, plans only the launch URL. The update sends everything else
// back as it was, so nothing else about the client changes.
func TestAClientWithoutALaunchURLGetsOne(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	rec := &memRecorder{}
	if err := Execute(plan(t, f, d, Recorded{}), api(f), rec, secretSource("launch-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	c := f.clients[0]
	c.LaunchURL = nil
	before := *c
	p := plan(t, f, d, rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{SetLaunchURL}) {
		t.Fatalf("planned %v", kinds(p))
	}
	want := `set launch URL for client talk: PUT /api/oidc/clients/c-1 with launchURL "https://talk.example.org/oauth/oidc/connect" and every other field sent back as it is now`
	if p.Steps[0].Detail != want {
		t.Errorf("line %q", p.Steps[0].Detail)
	}
	f.mutations = nil
	if err := Execute(p, api(f), rec, func() (string, error) { return "", errors.New("must not generate") }); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.mutations) != "[PUT /api/oidc/clients/c-1]" {
		t.Errorf("mutations %v", f.mutations)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(f.updates[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["launchURL"] != connect || sent["isGroupRestricted"] != true || sent["pkceEnabled"] != true || sent["isPublic"] != false || sent["name"] != "talk" {
		t.Errorf("sent %v", sent)
	}
	if _, ok := sent["logoUrl"]; ok {
		t.Error("a logo URL was sent")
	}
	if strings.Contains(f.updates[0], "secrets") || strings.Contains(f.updates[0], "launch-secret") {
		t.Errorf("the update carries secrets: %s", f.updates[0])
	}
	after := *f.clients[0]
	if fmt.Sprint(after.CallbackURLs) != fmt.Sprint(before.CallbackURLs) || after.PkceEnabled != before.PkceEnabled ||
		after.IsPublic != before.IsPublic || !after.IsGroupRestricted || len(after.AllowedUserGroups) != 2 {
		t.Errorf("the update changed more than the launch URL: before %+v, after %+v", before, after)
	}
	if len(f.secrets["c-1"]) != 1 {
		t.Errorf("secrets %v", f.secrets["c-1"])
	}
	for _, cmd := range f.commands {
		if cmd != pocketid.CurlCommand || strings.Contains(cmd, testKey) {
			t.Errorf("ran %q", cmd)
		}
	}
	if again := plan(t, f, d, rec.rec); len(again.Steps) != 0 {
		t.Errorf("a second run plans %v", kinds(again))
	}
}

// A launch URL set to something else is left alone and reported.
func TestACustomisedLaunchURLIsLeftAlone(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("custom-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	custom := "https://talk.example.org/magazines"
	f.clients[0].LaunchURL = &custom
	p := plan(t, f, talk(), rec.rec)
	if len(p.Steps) != 0 {
		t.Fatalf("planned %v", kinds(p))
	}
	if !strings.Contains(strings.Join(p.Present, "\n"), "launch URL https://talk.example.org/magazines, not https://talk.example.org/oauth/oidc/connect; left as it is") {
		t.Errorf("present %v", p.Present)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warned with no sso_dashboard_link set: %v", p.Warnings)
	}
}

// A client holding the bare host, which is what this toolkit sent before
// Mbin's tile pointed at the connect route, is moved to the connect route,
// and a second run plans nothing.
func TestAClientOnTheOldDefaultMovesToTheConnectRoute(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("bare-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	old := bare
	f.clients[0].LaunchURL = &old
	p := plan(t, f, talk(), rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{SetLaunchURL}) {
		t.Fatalf("planned %v", kinds(p))
	}
	if !strings.Contains(p.Steps[0].Detail, `with launchURL "https://talk.example.org/oauth/oidc/connect"`) {
		t.Errorf("line %q", p.Steps[0].Detail)
	}
	if err := Execute(p, api(f), rec, func() (string, error) { return "", errors.New("must not generate") }); err != nil {
		t.Fatal(err)
	}
	if got := *f.clients[0].LaunchURL; got != connect {
		t.Errorf("launch URL is %s", got)
	}
	again := plan(t, f, talk(), rec.rec)
	if len(again.Steps) != 0 {
		t.Errorf("a second run plans %v", kinds(again))
	}
	if !strings.Contains(strings.Join(again.Present, "\n"), "client talk launch URL "+connect) {
		t.Errorf("present %v", again.Present)
	}
	// A finding is marked done where it is shown, so it does not say so.
	for _, line := range again.Present {
		if strings.HasPrefix(line, "present ") {
			t.Errorf("a finding begins with its label: %q", line)
		}
	}
}

// An operator's sso_dashboard_link moves a client off a toolkit default, but
// a value the toolkit did not set is still left alone, with a warning naming
// both values, because the configuration and the client now disagree.
func TestAChosenLinkNeverOverwritesACustomValue(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("chosen-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	d := talk()
	d.LaunchURL, d.LaunchURLChosen = "https://talk.example.org/magazines", true
	p := plan(t, f, d, rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{SetLaunchURL}) {
		t.Fatalf("from the toolkit's default, planned %v", kinds(p))
	}

	custom := "https://talk.example.org/m/meta"
	f.clients[0].LaunchURL = &custom
	p = plan(t, f, d, rec.rec)
	if len(p.Steps) != 0 {
		t.Fatalf("over a custom value, planned %v", kinds(p))
	}
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings %v", p.Warnings)
	}
	for _, want := range []string{"sso_dashboard_link", "https://talk.example.org/magazines", custom, "will not overwrite"} {
		if !strings.Contains(p.Warnings[0], want) {
			t.Errorf("warning lacks %q: %s", want, p.Warnings[0])
		}
	}
}

// A client that allows more than the app needs is refused, not narrowed: an
// extra callback lets another site complete the app's sign in, and an extra
// allowed group admits people the member group was meant to keep out.
func TestAnOverPermissiveClientIsRefused(t *testing.T) {
	f := newFake()
	f.groups = []pocketid.Group{{ID: "g-1", Name: "members"}, {ID: "g-2", Name: "admins"}, {ID: "g-3", Name: "provisional"}}
	f.clients = []*pocketid.OIDCClient{{ID: "c-9", Name: "talk", PkceEnabled: true, IsGroupRestricted: true,
		CallbackURLs:      []string{"https://talk.example.org/oauth/oidc/verify", "https://elsewhere.example.org/oauth/oidc/verify"},
		AllowedUserGroups: []pocketid.Group{f.groups[0], f.groups[1], f.groups[2]}}}
	d := talk()
	d.MemberGroup = "members"
	s, err := Probe(api(f), d, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(d, Recorded{}, s)
	if err == nil {
		t.Fatal("an over-permissive client was accepted")
	}
	for _, want := range []string{"https://elsewhere.example.org/oauth/oidc/verify", "provisional", "more than"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

// Pocket ID refuses every request without a code challenge once a client has
// PKCE on, so a client with PKCE on is wrong for an app that never sends one.
func TestPKCEOnIsRefusedForAnAppThatSendsNoChallenge(t *testing.T) {
	f := newFake()
	f.clients = []*pocketid.OIDCClient{{ID: "c-9", Name: "docs", PkceEnabled: true, CallbackURLs: []string{"https://docs.example.org/auth/oidc.callback"}}}
	d := Desired{App: "docs", Name: "docs", CallbackURL: "https://docs.example.org/auth/oidc.callback", PKCE: false}
	s, err := Probe(api(f), d, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(d, Recorded{}, s)
	if err == nil || !strings.Contains(err.Error(), "PKCE is on") {
		t.Fatalf("got %v", err)
	}
}

// The groups sent on allow are exactly the app's, so a client's allowed
// groups never carry anything the configuration does not name.
func TestAllowSendsExactlyTheDesiredGroups(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	rec := &memRecorder{}
	if err := Execute(plan(t, f, d, Recorded{}), api(f), rec, secretSource("exact-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range f.clients[0].AllowedUserGroups {
		names = append(names, g.Name)
	}
	if fmt.Sprint(names) != "[members admins]" {
		t.Errorf("allowed %v", names)
	}
}

// rotated is a client given a first secret and then a second by a rotation,
// with the second recorded: Pocket ID holds both, and both are valid.
func rotated(t *testing.T) (*fakeIDP, *memRecorder) {
	t.Helper()
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("first-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	d := talk()
	d.RotateSecret = true
	if err := Execute(plan(t, f, d, rec.rec), api(f), rec, secretSource("second-secret-not-real-0002")); err != nil {
		t.Fatal(err)
	}
	if len(f.secrets["c-1"]) != 2 {
		t.Fatalf("Pocket ID holds %d secrets after the rotation", len(f.secrets["c-1"]))
	}
	return f, rec
}

// After a rotation, Retire deletes every secret but the recorded one, so the
// value the app presented before stops being valid.
func TestRetireDeletesEverySecretButTheRecordedOne(t *testing.T) {
	f, rec := rotated(t)
	f.mutations = nil
	if err := Retire(api(f), "talk", rec.rec, nil); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.mutations); got != "[DELETE /api/oidc/clients/c-1/secrets/s-1]" {
		t.Errorf("mutations %s", got)
	}
	held := f.secrets["c-1"]
	if len(held) != 1 || held[0].Prefix != "seco" {
		t.Errorf("Pocket ID holds %+v", held)
	}
	for _, in := range f.stdins {
		if strings.Contains(in, "DELETE") && (strings.Contains(in, "first-secret-not-real-0001") || strings.Contains(in, "second-secret-not-real-0002")) {
			t.Errorf("a secret's value reached a request: %s", in)
		}
	}
	// Run again, it finds nothing to delete.
	f.mutations = nil
	if err := Retire(api(f), "talk", rec.rec, nil); err != nil || len(f.mutations) != 0 {
		t.Errorf("a second retire: %v, %v", err, f.mutations)
	}
}

// A recorded secret Pocket ID does not hold as active means the app may still
// present an older one: nothing is deleted.
func TestRetireDeletesNothingWhenTheRecordedSecretIsNotLive(t *testing.T) {
	f, _ := rotated(t)
	f.mutations = nil
	err := Retire(api(f), "talk", Recorded{ClientID: "c-1", ClientSecret: "third-secret-not-real-0003"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Nothing was deleted") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "third-secret-not-real-0003") {
		t.Errorf("the error carries the secret: %v", err)
	}
	if len(f.mutations) != 0 || len(f.secrets["c-1"]) != 2 {
		t.Errorf("deleted %v", f.mutations)
	}
}

// Pocket ID keeps four characters of a secret. Two active secrets sharing
// them cannot be told apart, so neither is deleted and the error says so,
// after the others are.
func TestRetireKeepsSecretsThatShareTheRecordedPrefix(t *testing.T) {
	f, rec := rotated(t)
	f.secrets["c-1"] = append(f.secrets["c-1"], pocketid.ClientSecret{ID: "s-9", Prefix: "seco", IsActive: true})
	f.mutations = nil
	err := Retire(api(f), "talk", rec.rec, nil)
	if err == nil || !strings.Contains(err.Error(), "s-2, s-9") {
		t.Fatalf("got %v", err)
	}
	if got := fmt.Sprint(f.mutations); got != "[DELETE /api/oidc/clients/c-1/secrets/s-1]" {
		t.Errorf("mutations %s", got)
	}
}

// Credentials recorded without a client ID name no client to retire from.
func TestRetireRefusesWithoutARecordedClient(t *testing.T) {
	f, _ := rotated(t)
	f.mutations = nil
	if err := Retire(api(f), "talk", Recorded{ClientSecret: "second-secret-not-real-0002"}, nil); err == nil || len(f.mutations) != 0 {
		t.Fatalf("got %v, %v", err, f.mutations)
	}
}

// Each deletion is a step of its own, so the operator sees what was removed.
func TestRetireReportsEachDeletion(t *testing.T) {
	f, rec := rotated(t)
	r := &ui.Recorder{Verbose_: true}
	if err := Retire(api(f), "talk", rec.rec, r); err != nil {
		t.Fatal(err)
	}
	if !r.Has("done", "delete old client secret s-1 of talk") {
		t.Errorf("reported:\n%s", r.Lines())
	}
}

// A client is created under the desired name, which need not be the app's:
// the gate's carries the community's.
func TestTheClientIsCreatedUnderItsName(t *testing.T) {
	f := newFake()
	d := talk()
	d.Name = "Example Community"
	p := plan(t, f, d, Recorded{})
	if !strings.Contains(p.Steps[1].Detail, `"name":"Example Community"`) {
		t.Errorf("create line %s", p.Steps[1].Detail)
	}
	if err := Execute(p, api(f), &memRecorder{}, secretSource("named-secret-not-real-00001")); err != nil {
		t.Fatal(err)
	}
	if f.clients[0].Name != "Example Community" {
		t.Errorf("client named %q", f.clients[0].Name)
	}
}

// The recorded client under another name than the desired one, as after the
// community is renamed, is renamed in place: same ID, same secret, every
// other field sent back as it was, and a second run plans nothing.
func TestTheRecordedClientIsRenamed(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	rec := &memRecorder{}
	if err := Execute(plan(t, f, d, Recorded{}), api(f), rec, secretSource("rename-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	d.Name = "Example Community"
	p := plan(t, f, d, rec.rec)
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{SetName}) {
		t.Fatalf("planned %v", kinds(p))
	}
	want := `rename client talk to "Example Community": PUT /api/oidc/clients/c-1 with name "Example Community" and every other field sent back as it is now`
	if p.Steps[0].Detail != want {
		t.Errorf("line %q", p.Steps[0].Detail)
	}
	f.mutations = nil
	if err := Execute(p, api(f), rec, func() (string, error) { return "", errors.New("must not generate") }); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.mutations) != "[PUT /api/oidc/clients/c-1]" {
		t.Errorf("mutations %v", f.mutations)
	}
	c := f.clients[0]
	if c.Name != "Example Community" || c.LaunchURL == nil || *c.LaunchURL != connect || !c.IsGroupRestricted || len(c.AllowedUserGroups) != 2 || !c.PkceEnabled {
		t.Errorf("client after the rename %+v", c)
	}
	if rec.rec.ClientID != "c-1" || len(f.secrets["c-1"]) != 1 {
		t.Errorf("recorded %q, secrets %v", rec.rec.ClientID, f.secrets["c-1"])
	}
	if again := plan(t, f, d, rec.rec); len(again.Steps) != 0 {
		t.Errorf("a second run plans %v", kinds(again))
	}
}

// Without a recorded ID, a client under another name is not the app's: the
// plan creates one under the desired name rather than renaming somebody
// else's.
func TestAnUnrecordedClientUnderAnotherNameIsNotRenamed(t *testing.T) {
	f := newFake()
	f.clients = []*pocketid.OIDCClient{{ID: "c-9", Name: "talk"}}
	d := talk()
	d.Name = "Example Community"
	p := plan(t, f, d, Recorded{})
	if fmt.Sprint(kinds(p)) != fmt.Sprint([]StepKind{CreateGroup, CreateClient, AddSecret}) {
		t.Fatalf("planned %v", kinds(p))
	}
}

// Extra groups are created when missing, and never join the client's
// allowed groups, which stay the member and admin groups.
func TestExtraGroupsAreCreatedButNotAllowed(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	d.ExtraGroups = []string{"moderators"}
	p := plan(t, f, d, Recorded{})
	var extra []string
	for _, s := range p.Steps {
		if s.Kind == CreateGroup && s.Extra {
			extra = append(extra, s.Group)
		}
	}
	if fmt.Sprint(extra) != "[moderators]" {
		t.Fatalf("extra group steps %v", extra)
	}
	rec := &memRecorder{}
	if err := Execute(p, api(f), rec, secretSource("extra-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	for _, g := range f.clients[0].AllowedUserGroups {
		allowed = append(allowed, g.Name)
	}
	if fmt.Sprint(allowed) != "[members admins]" {
		t.Errorf("allowed %v", allowed)
	}
}

// An extra group Pocket ID already has plans nothing.
func TestAnExistingExtraGroupIsPresent(t *testing.T) {
	f := newFake()
	d := talk()
	d.MemberGroup = "members"
	d.ExtraGroups = []string{"moderators"}
	if err := Execute(plan(t, f, d, Recorded{}), api(f), &memRecorder{}, secretSource("extra-secret-not-real-0002")); err != nil {
		t.Fatal(err)
	}
	for _, s := range plan(t, f, d, Recorded{}).Steps {
		if s.Kind == CreateGroup {
			t.Errorf("planned %s again", s.Group)
		}
	}
}

// A new client is created with consent skipped when the app asks for no
// consent screen.
func TestANewClientSkipsConsent(t *testing.T) {
	f := newFake()
	d := talk()
	d.SkipConsent = true
	if err := Execute(plan(t, f, d, Recorded{}), api(f), &memRecorder{}, secretSource("consent-secret-not-real-0001")); err != nil {
		t.Fatal(err)
	}
	if !f.clients[0].SkipConsent {
		t.Errorf("client %+v", f.clients[0])
	}
}

// An existing client whose consent setting differs is corrected, and nothing
// else about it changes.
func TestAnExistingClientsConsentIsCorrected(t *testing.T) {
	f := newFake()
	rec := &memRecorder{}
	if err := Execute(plan(t, f, talk(), Recorded{}), api(f), rec, secretSource("consent-secret-not-real-0002")); err != nil {
		t.Fatal(err)
	}
	before := *f.clients[0]
	d := talk()
	d.SkipConsent = true
	p := plan(t, f, d, rec.rec)
	var ks []StepKind
	for _, s := range p.Steps {
		ks = append(ks, s.Kind)
	}
	if fmt.Sprint(ks) != fmt.Sprint([]StepKind{SetSkipConsent}) {
		t.Fatalf("planned %v", ks)
	}
	if err := Execute(p, api(f), &memRecorder{}, secretSource("unused-not-real-0003")); err != nil {
		t.Fatal(err)
	}
	if !f.clients[0].SkipConsent {
		t.Error("consent not corrected")
	}
	after := *f.clients[0]
	after.SkipConsent = before.SkipConsent
	if a, b := marshal(after), marshal(before); a != b {
		t.Errorf("something else changed: %s, was %s", a, b)
	}
}
