// Package oidcclient creates an app's client at the deployment's Pocket ID,
// with the groups the app reads, and records the client's credentials in the
// secrets file.
//
// Every step is a Pocket ID client, group or user mutation, and those need a
// human's approval each time. So the shape is the one `storage init`
// uses: probe read only, plan one printed line per
// mutation, showing what it sends, and change nothing until the operator
// re-runs with --execute. The printed plan is what is approved.
//
// The client secret is never printed and never returned by Pocket ID to be
// captured. It is generated on the workstation, written into the secrets file
// first, and only then sent to Pocket ID as a caller supplied value
// (dto/oidc_dto.go:77-83 at tag v2.14.0). The order matters: an interruption
// after the write leaves a recorded secret Pocket ID does not hold, which the
// next run sees by its prefix and sends; the other order would leave a live
// secret at Pocket ID that exists nowhere else.
package oidcclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Desired is the client an app needs, from its blueprint and its configuration.
type Desired struct {
	// App is the app's name, the client's key under oidc_clients in the
	// secrets file.
	App string
	// Name is the client's name at Pocket ID (config.Config.ClientName): what
	// a person signing in is shown, and how the client is found.
	Name        string
	CallbackURL string
	// LaunchURL is what the app's tile on Pocket ID's dashboard opens, and a
	// client without one is not listed there. Empty plans nothing for it.
	LaunchURL string
	// ToolkitLaunchURLs are the launch URLs this toolkit has set by default
	// (blueprints.ToolkitLaunchURLs). An existing client holding one of them,
	// or none, is moved to LaunchURL. Any other value is the operator's and is
	// never overwritten.
	ToolkitLaunchURLs []string
	// LaunchURLChosen is whether the operator set LaunchURL through the app's
	// sso_dashboard_link, rather than taking the blueprint's default. A custom
	// value that differs from a chosen one is warned about, since the
	// operator evidently wants the tile to go somewhere it does not.
	LaunchURLChosen bool
	PKCE            bool
	// SkipConsent is whether the client skips Pocket ID's consent screen.
	// It is on unless the app's sso_consent setting is true.
	SkipConsent bool
	// AdminGroup is the group whose members the app makes administrators, and
	// MemberGroup the group a member must be in to sign in at all. Each is
	// empty when the app's configuration names none. A member group also
	// restricts the client at Pocket ID to that group and the admin group, so
	// a refused member is stopped before the app sees them.
	AdminGroup  string
	MemberGroup string
	// ExtraGroups are further groups the app reads from the groups claim.
	// Each is found or created, and none is allowed on the client.
	ExtraGroups []string
	// RotateSecret adds a new secret even when the recorded one is live.
	RotateSecret bool
}

// Recorded is what the secrets file holds for this client now.
type Recorded struct {
	ClientID     string
	ClientSecret string
}

// Recorder writes the client's credentials into the secrets file. It is
// called before Pocket ID is sent a secret.
type Recorder interface {
	Record(clientID, clientSecret string) error
}

// State is what a probe found. It changes nothing to find it.
type State struct {
	Client  *pocketid.OIDCClient
	Secrets []pocketid.ClientSecret
	Groups  map[string]*pocketid.Group
}

// Probe reads everything the plan depends on. The client is the one with the
// desired name or, when Pocket ID has none, the one with recordedID, the
// client ID the secrets file records: a client this toolkit made under a name
// the configuration has since changed, such as the gate's after the community
// is renamed. Build plans the rename.
func Probe(ctx context.Context, c *pocketid.Client, d Desired, recordedID string) (State, error) {
	var s State
	var err error
	if s.Client, err = c.FindOIDCClient(ctx, d.Name); err != nil {
		return s, fmt.Errorf("looking up client %s: %w", d.Name, err)
	}
	if s.Client == nil && recordedID != "" {
		if s.Client, err = c.OIDCClientByID(ctx, recordedID); err != nil {
			return s, fmt.Errorf("looking up client %s by its recorded ID: %w", d.App, err)
		}
	}
	if s.Client != nil {
		if s.Secrets, err = c.ClientSecrets(ctx, s.Client.ID); err != nil {
			return s, fmt.Errorf("listing client %s's secrets: %w", d.App, err)
		}
	}
	s.Groups = map[string]*pocketid.Group{}
	for _, name := range append(d.groups(), d.ExtraGroups...) {
		if s.Groups[name], err = c.FindGroup(ctx, name); err != nil {
			return s, fmt.Errorf("looking up group %s: %w", name, err)
		}
	}
	return s, nil
}

// groups is the group names the app reads, without duplicates, member first.
func (d Desired) groups() []string {
	var out []string
	for _, g := range []string{d.MemberGroup, d.AdminGroup} {
		if g != "" && (len(out) == 0 || out[0] != g) {
			out = append(out, g)
		}
	}
	return out
}

// restricted reports whether the client should admit only some groups.
func (d Desired) restricted() bool { return d.MemberGroup != "" }

// StepKind is one kind of mutation.
type StepKind int

const (
	CreateGroup StepKind = iota
	CreateClient
	AllowGroups
	// AddSecret generates a secret, records it, then sends it.
	AddSecret
	// SendRecordedSecret sends the secret already recorded, which Pocket ID
	// does not hold because an earlier run stopped between the two.
	SendRecordedSecret
	// RecordClientID writes only the client ID, when the recorded secret is
	// live but the ID beside it is missing.
	RecordClientID
	// SetLaunchURL gives an existing client the launch URL it lacks, or moves
	// it off a value the toolkit set by default.
	SetLaunchURL
	// SetSkipConsent corrects an existing client's consent setting.
	SetSkipConsent
	// SetName renames the client the secrets file records to the name the
	// configuration gives it.
	SetName
)

// Step is one mutation. Title is the short line an operator sees by
// default; Detail is what it sends, the request and its body, shown with
// --verbose, since a body is what explains a step and is noise when it works.
type Step struct {
	Kind  StepKind
	Group string
	// Extra marks a CreateGroup step for one of Desired.ExtraGroups, which apply confirms before it runs.
	Extra  bool
	Title  string
	Detail string
}

// Plan is a probe, what is already right, and the steps that remain.
type Plan struct {
	Desired  Desired
	Recorded Recorded
	State    State
	Present  []string
	Steps    []Step
	// Replaces is the recorded client ID of a client Pocket ID does not have,
	// whose credentials the plan's new client is recorded over. Empty when
	// nothing recorded is replaced.
	Replaces string
	// Warnings are things the operator should know and the plan does not
	// change. They do not stop the run.
	Warnings []string
	// Report is where Execute reports each mutation as a step. Nil reports
	// nothing.
	Report ui.Reporter
}

// Build decides the steps. It is a pure function of the probe and the
// recorded credentials, which is what makes re-running safe: everything
// already right plans nothing.
//
// It refuses rather than changes an existing client that differs in a way the
// app cannot work with. Rewriting a client means a full update of every
// field (dto/oidc_dto.go:41-60), and a client somebody shaped by hand is not
// this command's to reshape.
//
// The launch URL is the one exception, and only when the toolkit put the
// current value there: empty, which is a client made before this command set
// one, or one of the toolkit's own earlier defaults (Desired.ToolkitLaunchURLs).
// Neither is a choice anybody made. A launch URL set to anything else may be
// deliberate and is left alone, with a warning when the operator has chosen a
// different one in the configuration.
//
// Credentials recorded for a client Pocket ID does not have are planned over
// as if nothing were recorded, and the plan names the client it replaces
// (Plan.Replaces). Nothing can be signing in with a client that does not
// exist, so nothing is lost by recording a new one in its place.
func Build(d Desired, rec Recorded, s State) (*Plan, error) {
	p := &Plan{Desired: d, Recorded: rec, State: s}

	client := s.Client
	if client != nil {
		if err := checkExisting(d, client); err != nil {
			return nil, err
		}
		p.Present = append(p.Present, fmt.Sprintf("client %s (id %s)", d.App, client.ID))
		if client.Name != d.Name {
			p.Steps = append(p.Steps, Step{Kind: SetName, Title: "rename client for " + d.App + " to " + d.Name, Detail: fmt.Sprintf("rename client %s to %q: PUT /api/oidc/clients/%s with name %q and every other field sent back as it is now", client.Name, d.Name, client.ID, d.Name)})
		}
	}

	for _, g := range d.groups() {
		if s.Groups[g] == nil {
			body, _ := json.Marshal(map[string]string{"name": g, "friendlyName": g})
			p.Steps = append(p.Steps, Step{Kind: CreateGroup, Group: g, Title: "create group " + g, Detail: fmt.Sprintf("create group %s: POST /api/user-groups %s", g, body)})
		} else {
			p.Present = append(p.Present, "group "+g)
		}
	}

	for _, g := range d.ExtraGroups {
		if s.Groups[g] == nil {
			body, _ := json.Marshal(map[string]string{"name": g, "friendlyName": g})
			p.Steps = append(p.Steps, Step{Kind: CreateGroup, Group: g, Extra: true, Title: "create group " + g, Detail: fmt.Sprintf("create group %s, which %s reads from the groups claim: POST /api/user-groups %s", g, d.App, body)})
		} else {
			p.Present = append(p.Present, "group "+g)
		}
	}

	if client == nil {
		body, _ := json.Marshal(d.newClient())
		line := fmt.Sprintf("create client %s: POST /api/oidc/clients %s", d.App, body)
		if rec.ClientID != "" {
			p.Replaces = rec.ClientID
			line += fmt.Sprintf(". It replaces the recorded client %s, which Pocket ID does not have", rec.ClientID)
		}
		p.Steps = append(p.Steps, Step{Kind: CreateClient, Title: "create OIDC client " + d.App, Detail: line})
	} else if d.LaunchURL != "" {
		current := ""
		if client.LaunchURL != nil {
			current = *client.LaunchURL
		}
		switch {
		case current == d.LaunchURL:
			p.Present = append(p.Present, fmt.Sprintf("client %s launch URL %s", d.App, d.LaunchURL))
		case current == "" || d.setByToolkit(current):
			p.Steps = append(p.Steps, Step{Kind: SetLaunchURL, Title: "set launch URL for " + d.App, Detail: fmt.Sprintf("set launch URL for client %s: PUT /api/oidc/clients/%s with launchURL %q and every other field sent back as it is now", d.App, client.ID, d.LaunchURL)})
		default:
			p.Present = append(p.Present, fmt.Sprintf("client %s launch URL %s, not %s; left as it is, since it may have been set deliberately", d.App, current, d.LaunchURL))
			if d.LaunchURLChosen {
				p.Warnings = append(p.Warnings, fmt.Sprintf("apps.%s.settings.sso_dashboard_link gives launch URL %s, but client %s has %s, which this toolkit did not set and will not overwrite. To use the setting, clear the client's launch URL in Pocket ID's admin UI and re-run; to keep the client's, remove the setting or make it match", d.App, d.LaunchURL, d.App, current))
			}
		}
	}

	// Consent is corrected rather than refused: whether the screen shows
	// harms no one and is the configuration's to say, so checkExisting does
	// not compare it.
	if client != nil {
		if client.SkipConsent == d.SkipConsent {
			p.Present = append(p.Present, fmt.Sprintf("client %s consent screen %s", d.App, consentWord(d.SkipConsent)))
		} else {
			p.Steps = append(p.Steps, Step{Kind: SetSkipConsent, Title: "set consent screen for " + d.App, Detail: fmt.Sprintf("set skipConsent %v for client %s: PUT /api/oidc/clients/%s with skipConsent %v and every other field sent back as it is now", d.SkipConsent, d.App, client.ID, d.SkipConsent)})
		}
	}

	if d.restricted() {
		var missing []string
		for _, g := range d.groups() {
			if client == nil || !allows(client, g) {
				missing = append(missing, g)
			}
		}
		if len(missing) > 0 {
			title := "allow group " + missing[0] + " on " + d.App
			if len(missing) > 1 {
				title = "allow groups " + strings.Join(missing, ", ") + " on " + d.App
			}
			p.Steps = append(p.Steps, Step{Kind: AllowGroups, Title: title, Detail: fmt.Sprintf("allow groups %s on client %s: PUT /api/oidc/clients/<id>/allowed-user-groups with exactly %s", strings.Join(missing, ", "), d.App, strings.Join(d.groups(), ", "))})
		} else {
			p.Present = append(p.Present, fmt.Sprintf("client %s allows %s", d.App, strings.Join(d.groups(), ", ")))
		}
	}

	key := "oidc_clients." + d.App
	switch {
	case client == nil || d.RotateSecret || rec.ClientSecret == "" || (rec.ClientID != "" && rec.ClientID != client.ID):
		if client != nil && len(s.Secrets) >= pocketid.MaxClientSecrets {
			return nil, fmt.Errorf("client %s already holds %d secrets, Pocket ID's limit (model/oidc.go:41). Delete an unused one before adding another", d.App, len(s.Secrets))
		}
		line := fmt.Sprintf("create client secret for %s: generated on this workstation, written to %s.client_id and %s.client_secret, then sent to POST /api/oidc/clients/<id>/secrets. Never printed", d.App, key, key)
		if client != nil && len(s.Secrets) > 0 {
			line += fmt.Sprintf(". The %d secret(s) it holds now stay valid until deleted", len(s.Secrets))
		}
		p.Steps = append(p.Steps, Step{Kind: AddSecret, Title: "create client secret for " + d.App, Detail: line})
	case !pocketid.HasActiveSecret(s.Secrets, rec.ClientSecret):
		if len(s.Secrets) >= pocketid.MaxClientSecrets {
			return nil, fmt.Errorf("client %s already holds %d secrets, Pocket ID's limit (model/oidc.go:41). Delete an unused one before adding another", d.App, len(s.Secrets))
		}
		p.Steps = append(p.Steps, Step{Kind: SendRecordedSecret, Title: "send recorded secret for " + d.App, Detail: fmt.Sprintf("send the secret recorded at %s.client_secret, which client %s does not hold, to POST /api/oidc/clients/%s/secrets. Never printed", key, d.App, client.ID)})
	case rec.ClientID == "":
		p.Steps = append(p.Steps, Step{Kind: RecordClientID, Title: "record client id for " + d.App, Detail: fmt.Sprintf("record %s.client_id %s, beside a secret that is already live", key, client.ID)})
	default:
		p.Present = append(p.Present, fmt.Sprintf("%s.client_secret, which matches an active secret of client %s by its first %d characters", key, d.App, pocketid.SecretPrefixLength))
	}

	return p, nil
}

// setByToolkit reports whether a launch URL is one this toolkit sets by
// default, and so is not an operator's choice.
func (d Desired) setByToolkit(launchURL string) bool {
	for _, u := range d.ToolkitLaunchURLs {
		if u == launchURL {
			return true
		}
	}
	return false
}

func consentWord(skip bool) string {
	if skip {
		return "skipped"
	}
	return "shown"
}

// newClient is exactly what a create sends, and what the plan prints. A
// confidential client: every app here holds a secret, and a public client
// cannot have one (service/oidc_service.go:312 at v2.14.0).
func (d Desired) newClient() pocketid.NewOIDCClient {
	return pocketid.NewOIDCClient{
		Name:              d.Name,
		CallbackURLs:      []string{d.CallbackURL},
		PkceEnabled:       d.PKCE,
		IsGroupRestricted: d.restricted(),
		LaunchURL:         d.LaunchURL,
		SkipConsent:       d.SkipConsent,
	}
}

// checkExisting is why an existing client cannot be the app's, or nil. It
// refuses a client that differs from the spec in either direction: one that
// allows less than the app needs cannot sign anyone in, and one that allows
// more than the app needs is a privilege leak. An extra callback lets a sign
// in started at this app finish at another host, and an extra allowed group
// admits people the member group was meant to keep out. Neither is narrowed,
// since a client somebody shaped by hand is not this command's to reshape;
// both are named so a person can.
func checkExisting(d Desired, c *pocketid.OIDCClient) error {
	var wrong []string
	found := false
	var extraCallbacks []string
	for _, u := range c.CallbackURLs {
		if u == d.CallbackURL {
			found = true
		} else {
			extraCallbacks = append(extraCallbacks, u)
		}
	}
	if !found {
		wrong = append(wrong, fmt.Sprintf("its callback URLs %v do not include %s", c.CallbackURLs, d.CallbackURL))
	}
	if len(extraCallbacks) > 0 {
		wrong = append(wrong, fmt.Sprintf("its callback URLs allow more than the app's %s: %s, which would let a sign in started here finish elsewhere", d.CallbackURL, strings.Join(extraCallbacks, ", ")))
	}
	if d.PKCE && !c.PkceEnabled {
		wrong = append(wrong, "PKCE is off, and the app always sends a code challenge")
	}
	if !d.PKCE && c.PkceEnabled {
		wrong = append(wrong, "PKCE is on, which makes Pocket ID refuse every request without a code challenge (oidc/authorization_service.go:750-755 at v2.14.0), and the app never sends one")
	}
	if c.IsPublic {
		wrong = append(wrong, "it is a public client, which cannot hold the secret the app signs in with")
	}
	if d.restricted() && !c.IsGroupRestricted {
		wrong = append(wrong, fmt.Sprintf("it is not restricted to groups, and the app's configuration names a member group, %s", d.MemberGroup))
	}
	if d.restricted() {
		var extraGroups []string
		for _, g := range c.AllowedUserGroups {
			if !slices.Contains(d.groups(), g.Name) {
				extraGroups = append(extraGroups, g.Name)
			}
		}
		if len(extraGroups) > 0 {
			wrong = append(wrong, fmt.Sprintf("it allows more than the app's groups %s: %s, whose members the member group was meant to keep out", strings.Join(d.groups(), ", "), strings.Join(extraGroups, ", ")))
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	return fmt.Errorf("client %s exists at Pocket ID (id %s) and differs from what the app needs: %s. This command creates clients and does not reshape one that exists; fix it there, or delete it and re-run", d.App, c.ID, strings.Join(wrong, "; "))
}

func allows(c *pocketid.OIDCClient, group string) bool {
	for _, g := range c.AllowedUserGroups {
		if g.Name == group {
			return true
		}
	}
	return false
}

// Execute runs the plan's steps in order and stops at the first failure.
// newSecret makes a fresh client secret. After the steps it probes again and
// refuses to report success unless a fresh plan would do nothing, because a
// call that answers 2xx without doing its job is the failure nobody notices.
func Execute(ctx context.Context, p *Plan, c *pocketid.Client, rec Recorder, newSecret func() (string, error)) error {
	d := p.Desired
	groups := map[string]*pocketid.Group{}
	for name, g := range p.State.Groups {
		groups[name] = g
	}
	client := p.State.Client
	recorded := p.Recorded
	var secretsToHide []string

	report := p.Report
	if report == nil {
		report = ui.Discard
	}
	for _, step := range p.Steps {
		s := report.Step(step.Title)
		s.Detail("%s", step.Detail)
		result := ""
		var err error
		switch step.Kind {
		case CreateGroup:
			var g pocketid.Group
			if g, err = c.CreateGroup(ctx, step.Group); err == nil {
				groups[step.Group] = &g
				result = "created"
			}
		case CreateClient:
			var created pocketid.OIDCClient
			if created, err = c.CreateOIDCClient(ctx, d.newClient()); err == nil {
				client = &created
				result = "created"
			}
		case SetLaunchURL:
			err = c.SetLaunchURL(ctx, client.ID, d.LaunchURL)
		case SetSkipConsent:
			err = c.SetSkipConsent(ctx, client.ID, d.SkipConsent)
		case SetName:
			err = c.SetClientName(ctx, client.ID, d.Name)
		case AllowGroups:
			// Exactly the app's groups. checkExisting has refused a client
			// allowing any other, so nothing is dropped here that was not
			// already a refusal.
			ids := map[string]bool{}
			for _, name := range d.groups() {
				if groups[name] == nil {
					err = fmt.Errorf("group %s was not created", name)
					break
				}
				ids[groups[name].ID] = true
			}
			if err == nil {
				err = c.SetAllowedGroups(ctx, client.ID, sortedKeys(ids))
			}
		case AddSecret:
			var secret string
			if secret, err = newSecret(); err != nil {
				break
			}
			secretsToHide = append(secretsToHide, secret)
			if err = rec.Record(client.ID, secret); err != nil {
				err = fmt.Errorf("recording oidc_clients.%s: %w. Pocket ID was sent nothing", d.App, err)
				break
			}
			recorded = Recorded{ClientID: client.ID, ClientSecret: secret}
			s.Detail("recorded oidc_clients.%s.client_id and oidc_clients.%s.client_secret", d.App, d.App)
			if err = c.AddClientSecret(ctx, client.ID, secret); err != nil {
				err = fmt.Errorf("%w. oidc_clients.%s holds the new secret and Pocket ID does not: re-run to send it, and do not apply %s until it has", err, d.App, d.App)
			}
		case SendRecordedSecret:
			secretsToHide = append(secretsToHide, recorded.ClientSecret)
			if recorded.ClientID != client.ID {
				if err = rec.Record(client.ID, recorded.ClientSecret); err != nil {
					break
				}
				s.Detail("recorded oidc_clients.%s.client_id and oidc_clients.%s.client_secret", d.App, d.App)
			}
			err = c.AddClientSecret(ctx, client.ID, recorded.ClientSecret)
		case RecordClientID:
			secretsToHide = append(secretsToHide, recorded.ClientSecret)
			if err = rec.Record(client.ID, recorded.ClientSecret); err == nil {
				s.Detail("recorded oidc_clients.%s.client_id and oidc_clients.%s.client_secret", d.App, d.App)
			}
			recorded.ClientID = client.ID
		default:
			err = fmt.Errorf("no step %d", step.Kind)
		}
		if err != nil {
			err = pocketid.Redact(fmt.Errorf("%s: %w", firstWords(step.Detail), err), secretsToHide...)
			s.Fail(err)
			return err
		}
		s.Done(result)
	}

	// A rotation is done once its secret is sent; asking the fresh plan for
	// another would make the check below fail on its own success.
	d.RotateSecret = false
	after, err := Probe(ctx, c, d, recorded.ClientID)
	if err != nil {
		return pocketid.Redact(err, secretsToHide...)
	}
	again, err := Build(d, recorded, after)
	if err != nil {
		return pocketid.Redact(fmt.Errorf("after the changes: %w", err), secretsToHide...)
	}
	if len(again.Steps) > 0 {
		var left []string
		for _, s := range again.Steps {
			left = append(left, firstWords(s.Detail))
		}
		return errors.New("the calls succeeded but a fresh probe still plans: " + strings.Join(left, "; "))
	}
	return nil
}

// firstWords is a step line up to its colon, for an error that should name
// the step without repeating its payload.
func firstWords(line string) string {
	if i := strings.Index(line, ":"); i > 0 {
		return line[:i]
	}
	return line
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Deterministic, so a request is the same for the same state.
	sort.Strings(out)
	return out
}

// Retire deletes every secret of the recorded client but the recorded one, so
// that a value the app presented before a rotation stops being valid: an old
// secret that stays valid is a secret not rotated. It is the last step of a
// rotation, run only once every site of the app presents the recorded secret,
// and only by the run the operator started with --execute, since each delete
// is a Pocket ID client mutation.
//
// Pocket ID keeps the first four characters of a secret and nothing more of
// it that can be compared (pocketid.SecretPrefixLength), so the recorded
// secret is found by that prefix among the active ones. When none matches,
// the app may still present an older secret and nothing is deleted. When more
// than one does, those cannot be told apart and are kept, the others are
// deleted, and the error names them: a rotation run again records a secret
// with another prefix and retires them all. Running Retire again deletes
// nothing more. No secret's value is sent or printed.
func Retire(ctx context.Context, c *pocketid.Client, app string, rec Recorded, report ui.Reporter) error {
	if report == nil {
		report = ui.Discard
	}
	if rec.ClientID == "" || rec.ClientSecret == "" {
		return fmt.Errorf("oidc_clients.%s records no client and secret to keep, so no other secret of it can be told to be old. Nothing was deleted", app)
	}
	held, err := c.ClientSecrets(ctx, rec.ClientID)
	if err != nil {
		return pocketid.Redact(fmt.Errorf("listing client %s's secrets: %w", app, err), rec.ClientSecret)
	}
	var keep, old []pocketid.ClientSecret
	for _, s := range held {
		if pocketid.HasActiveSecret([]pocketid.ClientSecret{s}, rec.ClientSecret) {
			keep = append(keep, s)
		} else {
			old = append(old, s)
		}
	}
	if len(keep) == 0 {
		return fmt.Errorf("client %s (id %s) holds no active secret beginning as oidc_clients.%s.client_secret does, so the app may still sign in with an older one. Nothing was deleted", app, rec.ClientID, app)
	}
	for _, s := range old {
		step := report.Step(fmt.Sprintf("delete old client secret %s of %s", s.ID, app))
		step.Detail("DELETE /api/oidc/clients/%s/secrets/%s", rec.ClientID, s.ID)
		if err := c.DeleteClientSecret(ctx, rec.ClientID, s.ID); err != nil {
			err = pocketid.Redact(fmt.Errorf("deleting client %s's secret %s: %w", app, s.ID, err), rec.ClientSecret)
			step.Fail(err)
			return err
		}
		step.Done("deleted")
	}
	// The gate: a fresh read holds only the secrets kept.
	after, err := c.ClientSecrets(ctx, rec.ClientID)
	if err != nil {
		return pocketid.Redact(fmt.Errorf("listing client %s's secrets after the deletes: %w", app, err), rec.ClientSecret)
	}
	var left []string
	for _, s := range after {
		left = append(left, s.ID)
	}
	if len(after) != len(keep) {
		return fmt.Errorf("the deletes succeeded but client %s still holds secrets %s", app, strings.Join(left, ", "))
	}
	if len(keep) > 1 {
		return fmt.Errorf("client %s holds %d active secrets, %s, whose first %d characters are those of oidc_clients.%s.client_secret, and Pocket ID keeps no more of a secret to tell them apart, so none of them was deleted. Rotate it again: the next secret begins differently, and its retirement deletes them", app, len(keep), strings.Join(left, ", "), pocketid.SecretPrefixLength, app)
	}
	return nil
}
