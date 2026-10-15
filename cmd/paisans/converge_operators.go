package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/appadmin"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/pocketidlog"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// operatorLinkTTL is how long the login link apply issues a declared operator stays
// valid: a day, long enough to pass it on by hand and for its owner to open it.
// Over 15 minutes it is also the 12 character code the login page takes
// whether or not email login is on (see loginTTL in internal/appadmin).
const operatorLinkTTL = 24 * time.Hour

// operatorEmailWait is how long apply reads Pocket ID's log for a failed login
// link email. Pocket ID sends in the background after it answers and logs
// only a failure, so a send is reported sent once this passes without one.
// Tests shorten it.
var operatorEmailWait = 30 * time.Second

// operatorEmailPoll is how often the log is read while waiting.
var operatorEmailPoll = 5 * time.Second

// loginLinkOut is where the login links are written: standard output, once,
// and nowhere else. Tests replace it.
var loginLinkOut io.Writer = os.Stdout

// declaredOperator is the outcome of one operator on one Pocket ID app.
type declaredOperator struct {
	App, ID, Email string
	// Titles is the plan, one short phrase per action. Empty when the operator
	// has registered a passkey and there is nothing to do.
	Titles []string
	// Created, Link and ExpiresIn are what an execute did. Link is a
	// credential.
	Created   bool
	Link      string
	ExpiresIn time.Duration
	// Emailed is whether Pocket ID accepted a request to email a link.
	Emailed bool
}

// operatorRequest is the request apply makes for a declared operator: the
// operator's key as the username, their email, their name split at its first
// space, the group operators and every app's admin group, and the member group
// members and every app's member group, since an app whose member gate does
// not admit its administrator refuses their first login. Founder decision.
// Under management: paisans the account is a member, never a Pocket ID
// administrator; under pocket-id it is one. The username is fixed once the
// account exists: nobody can edit their own account
// (blueprints.PocketIDAccountEditSetting), and apply never renames one.
// validate holds the name to a first and a last.
func operatorRequest(cfg *config.Config, app, id string) appadmin.Request {
	a := cfg.Operators[id]
	first, last, _ := strings.Cut(strings.TrimSpace(a.Name), " ")
	admin := withGroup(adminGroups(cfg), blueprints.OperatorsGroup)
	member := withGroup(memberGroups(cfg), blueprints.MembersGroup)
	member = slices.DeleteFunc(member, func(g string) bool { return slices.Contains(admin, g) })
	return appadmin.Request{App: app, Username: id, Email: a.Email,
		FirstName: first, LastName: strings.TrimSpace(last), AdminGroups: admin,
		MemberGroups: member, Declared: true, LinkTTL: operatorLinkTTL,
		Member:    blueprints.PocketIDManagement(cfg.Apps[app].Settings) != blueprints.ManagementPocketID,
		EmailLink: operatorEmailOn(cfg, app),
		PublicURL: "https://" + cfg.Apps[app].Hostname,
	}
}

// withGroup is groups with group added, sorted and without duplicates.
func withGroup(groups []string, group string) []string {
	out := append(slices.Clone(groups), group)
	slices.Sort(out)
	return slices.Compact(out)
}

// operatorEmailOn reports whether a Pocket ID app emails its operators their links:
// it can send mail and does not set email_one_time_access_as_admin false.
func operatorEmailOn(cfg *config.Config, app string) bool {
	return blueprints.PocketIDAdminEmail(cfg.Apps[app].Settings, cfg.CanMail(app))
}

// convergeDeclaredOperator plans, and with execute makes, one declared
// operator of a Pocket ID app, reaching the site Pocket ID answers on with the
// run's sudo. Tests replace it.
var convergeDeclaredOperator = func(cfg *config.Config, app, id string, o convergeOptions, execute bool) (declaredOperator, error) {
	out := declaredOperator{App: app, ID: id, Email: cfg.Operators[id].Email}
	where, err := pocketIDSite(ui.Discard, cfg, app, "", "apply")
	if err != nil {
		return out, err
	}
	key, err := pocketIDKey(cfg, o.Config, o.Secrets, app)
	if err != nil {
		return out, err
	}
	req := operatorRequest(cfg, app, id)
	req.APIKey, req.APIBase, req.APINetworks = key, pocketIDBase(cfg, where), meshNetworks(cfg)
	transport := adminTransport(siteTransport(where, cfg.Sites[where], "", o.Sudo))
	plan, err := appadmin.Build(cfg.Apps[app].Blueprint, transport, req)
	if err != nil {
		return out, err
	}
	out.Titles = plan.Titles()
	if !execute || len(plan.Actions) == 0 {
		return out, nil
	}
	outcome, err := appadmin.Execute(plan, transport)
	if err != nil {
		return out, err
	}
	out.Created = !plan.State.Exists
	out.Link, out.ExpiresIn, out.Emailed = outcome.LoginLink, outcome.ExpiresIn, outcome.Emailed
	return out, nil
}

// convergeEmailFailures reads a Pocket ID app's log for failed login link
// emails to addresses, for operatorEmailWait, and returns each failure's error by
// address. It stops early once every address has failed. Tests replace it.
var convergeEmailFailures = func(cfg *config.Config, app string, o convergeOptions, addresses []string) (map[string]string, error) {
	where, err := pocketIDSite(ui.Discard, cfg, app, "", "apply")
	if err != nil {
		return nil, err
	}
	transport := adminTransport(siteTransport(where, cfg.Sites[where], "", o.Sudo))
	project := cfg.Deployment().Project(app)
	start := time.Now()
	failed := map[string]string{}
	for {
		// The window reaches back past the calls that asked for the emails.
		logs, err := transport.Run(pocketidlog.LogCommand(project, time.Since(start)+time.Minute))
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if why, ok := pocketidlog.EmailFailure(logs, address); ok {
				failed[address] = why
			}
		}
		if len(failed) == len(addresses) || time.Since(start) >= operatorEmailWait {
			return failed, nil
		}
		time.Sleep(min(operatorEmailPoll, operatorEmailWait-time.Since(start)))
	}
}

// convergeOperators makes every operator the configuration declares a user of
// each Pocket ID app up says is running, and returns the login links issued.
// A dry run only plans, reading Pocket ID and changing nothing. A deployment
// with a Pocket ID app declares at least two operators, which validate holds
// it to, so one that declares none has nothing to make.
//
// An operator who could not be made fails the run, since the deployment cannot
// be signed in to, but only after every other operator has been tried and the
// links already issued are returned to be printed.
func convergeOperators(r ui.Reporter, cfg *config.Config, o convergeOptions, execute bool, up func(app string) bool) ([]declaredOperator, error) {
	if len(cfg.Operators) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(cfg.Operators))
	for id := range cfg.Operators {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var links []declaredOperator
	var failures []error
	section := false
	for _, app := range cfg.AppNames() {
		if cfg.Apps[app].Blueprint != config.BlueprintPocketID || !up(app) {
			continue
		}
		if !section {
			r.Section("pocket id")
			section = true
		}
		var emailed []declaredOperator
		issued := len(links)
		for _, id := range ids {
			s := r.Step(id + " on " + app)
			d, err := convergeDeclaredOperator(cfg, app, id, o, execute)
			switch {
			case err != nil:
				s.End(ui.Failed, errLine(err))
				failures = append(failures, fmt.Errorf("making %s a user of %s: %w", id, app, err))
			case len(d.Titles) == 0:
				s.Done("registered")
			case !execute:
				for _, title := range d.Titles {
					s.Detail("%s", title)
				}
				s.End(ui.Pending, strings.Join(d.Titles, ", "))
			default:
				s.Done(operatorDone(d))
				if d.Link != "" {
					links = append(links, d)
				}
				if d.Emailed {
					emailed = append(emailed, d)
				}
			}
		}
		if execute && !operatorEmailOn(cfg, app) && len(links) > issued {
			r.Note("Pocket ID "+app+" emails no login links", operatorEmailOffWhy(cfg, app))
		}
		if len(emailed) > 0 {
			convergeEmailResults(r, cfg, app, o, emailed)
		}
	}
	return links, errors.Join(failures...)
}

// operatorDone is what an execute did for one operator, in a few words.
func operatorDone(d declaredOperator) string {
	var did []string
	if d.Created {
		did = append(did, "created")
	}
	if d.Link != "" {
		did = append(did, "link issued")
	}
	if len(did) == 0 {
		return "updated"
	}
	return strings.Join(did, ", ")
}

// operatorEmailOffWhy says why a Pocket ID app sends no login link email.
func operatorEmailOffWhy(cfg *config.Config, app string) string {
	if !cfg.CanMail(app) {
		return "Its smtp settings resolve no host or no from_address, so the links below are the only copies. Pass each one on yourself."
	}
	return "apps." + app + ".settings." + blueprints.PocketIDAdminEmailSetting + " is false, so the links below are the only copies. Pass each one on yourself."
}

// convergeEmailResults waits on Pocket ID's log and gives each emailed operator
// a line: sent, or failed with Pocket ID's own error. A failed email fails
// nothing else: the printed link is still there.
func convergeEmailResults(r ui.Reporter, cfg *config.Config, app string, o convergeOptions, emailed []declaredOperator) {
	addresses := make([]string, len(emailed))
	for i, d := range emailed {
		addresses[i] = d.Email
	}
	failed, err := ui.Get(r, "wait for "+app+"'s login link emails", func() (map[string]string, error) {
		return convergeEmailFailures(cfg, app, o, addresses)
	})
	// One cause shared by every failure, Eg: the smtp settings Pocket ID
	// renders, is said once below rather than on each operator's line.
	shared := ""
	for _, d := range emailed {
		why, ok := failed[d.Email]
		if !ok {
			continue
		}
		why = pocketidlog.Brief(why)
		failed[d.Email] = why
		if shared == "" {
			shared = why
		} else if shared != why {
			shared = ""
			break
		}
	}
	if len(failed) < 2 {
		shared = ""
	}
	for _, d := range emailed {
		s := r.Step("email " + d.ID + " a login link")
		switch why, ok := failed[d.Email]; {
		case err != nil:
			s.End(ui.Failed, "could not read Pocket ID's log ("+errLine(err)+")")
		case ok && shared != "":
			s.End(ui.Failed, "failed")
		case ok:
			s.End(ui.Failed, "failed: "+why)
		default:
			s.Done("sent")
		}
	}
	if shared != "" {
		r.Note("Pocket ID "+app+" could not send those login link emails: "+shared, emailFailureFix(shared))
	}
}

// emailFailureFix says what to do about a cause every login link email
// failed with. The printed links are still there either way.
func emailFailureFix(why string) string {
	if strings.Contains(why, "missing SMTP password") {
		return "Its smtp settings name a username and the secrets file holds no password for it. `paisans secrets list` names the unset smtp_password; set it with `paisans secrets set <path>`, then apply. Until then, pass each link below on yourself."
	}
	return "Pass each link below on yourself."
}

// printLoginLinks writes every link issued, after the run's result, set apart
// by a blank line before and after, each link in italics on a terminal. Each
// is a credential: it goes to loginLinkOut once and is kept nowhere, so it is
// printed whether output is a terminal or a pipe, which is how an operator
// copies it to pass on.
func printLoginLinks(links []declaredOperator) {
	if len(links) == 0 {
		return
	}
	apps := map[string]bool{}
	for _, d := range links {
		apps[d.App] = true
	}
	names := make([]string, len(links))
	width := 0
	for i, d := range links {
		names[i] = d.ID
		if len(apps) > 1 {
			names[i] += " on " + d.App
		}
		width = max(width, len(names[i]))
	}
	fmt.Fprintf(loginLinkOut, "\nLogin links, each valid for %s and for one sign in. Treat each as a password:\n", humanTTL(links[0].ExpiresIn))
	for i, d := range links {
		fmt.Fprintf(loginLinkOut, "  %-*s  %s\n", width, names[i], ui.Italic(loginLinkOut, d.Link))
	}
	fmt.Fprintln(loginLinkOut)
}

// humanTTL is a link's lifetime as a reader says it.
func humanTTL(d time.Duration) string {
	if d%time.Hour == 0 {
		return plural(int(d/time.Hour), "hour")
	}
	return d.String()
}
