package appadmin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/pocketid"
)

// pocketID creates an administrator through Pocket ID's REST API, with the
// static API key, from curl on the site's host (see internal/pocketid).
//
// Pocket ID users sign in with passkeys, so there is no password to set. A
// new administrator is useless until they have registered one, and the way
// Pocket ID itself offers for that is a one-time access token: an admin
// creates one for a user (POST /api/users/:id/one-time-access-token,
// onetimeaccess/module.go:68), and opening {APP_URL}/lc/{token} signs that
// user in once, where they add a passkey. The binary's own
// `one-time-access-token` subcommand does the same
// (cmds/one_time_access_token.go:19-82) but takes the username on argv and
// always issues a one hour token, so the API is used instead, with a shorter
// one.
//
// Rejected: Pocket ID's first-admin signup page (POST /api/signup/setup,
// usersignup/module.go:87). It is open to whoever reaches it first while no
// user exists, which on a public hostname is a race with the internet, and it
// is a browser step.
//
// The account is made with its email marked verified, and an existing
// account whose email is not verified is marked so. Pocket ID otherwise sends
// email_verified false, and the paisans Mbin fork refuses an OIDC sign in
// whose email matches an existing local account unless the provider marked it
// verified: a guard against taking over an account by claiming its address
// at the provider. The operator making an administrator vouches for the
// address they typed, which is the verification the guard asks for.
//
// Rejected: turning that guard off in the fork. It is correct for every member
// who signs up at Pocket ID by themselves, and the founder's case is fixed
// where the vouching happens.
type pocketID struct{}

// loginTTL is how long a login link stays usable. It is 20 minutes, not
// Pocket ID's own 15 minute default, because the length of the code depends
// on it: a TTL of 15 minutes or less gets a 6 character code
// (onetimeaccess/service.go:271-274 at v2.14.0), and the login page accepts a
// 6 character code only when unauthenticated email login is enabled
// (frontend login/alternative/code/+page.svelte:24-29); otherwise it waits
// for 12 and its submit button stays disabled. On the first real host, which
// has no SMTP, the 15 minute link was unusable. Over 15 minutes gets the 12
// character code, which the page accepts either way.
const loginTTL = 20 * time.Minute

func (pocketID) client(t Transport, req Request) *pocketid.Client {
	return &pocketid.Client{Transport: t, BaseURL: req.APIBase, APIKey: req.APIKey}
}

// find is the user a request is about: the one with its username, or for a
// Declared request whose username no account has, the one with its email.
// The pocket-id blueprint renders ALLOW_OWN_ACCOUNT_EDIT=false, so a user can
// change neither; only an administrator can, from Pocket ID's admin page, and
// an account renamed there is still found by its address. The address is
// compared whole, ignoring case, since Pocket ID's search is a substring
// match.
func (p pocketID) find(c *pocketid.Client, req Request) (*pocketid.User, error) {
	u, err := c.FindUser(req.Username)
	if err != nil || u != nil || !req.Declared || req.Email == "" {
		return u, err
	}
	users, err := c.SearchUsers(req.Email)
	if err != nil {
		return nil, err
	}
	for _, candidate := range users {
		if candidate.Email != nil && strings.EqualFold(*candidate.Email, req.Email) {
			return &candidate, nil
		}
	}
	return nil, nil
}

// linkTTL is the request's link lifetime, or loginTTL.
func linkTTL(req Request) time.Duration {
	if req.LinkTTL > 0 {
		return req.LinkTTL
	}
	return loginTTL
}

func (p pocketID) Probe(t Transport, req Request) (State, error) {
	c := p.client(t, req)
	u, err := p.find(c, req)
	if err != nil {
		return State{}, fmt.Errorf("looking up %s in %s: %w", req.Username, req.App, err)
	}
	if u == nil {
		return State{MissingGroups: req.Groups()}, nil
	}
	if u.Disabled {
		return State{}, fmt.Errorf("%s exists in %s and is disabled. Re-enabling an account is not something this command decides; do it deliberately, then re-run", req.Username, req.App)
	}
	// An account without an email has nothing to verify, and no address an
	// app could match against one of its own.
	state := State{Exists: true, Verified: u.Email == nil || u.EmailVerified, Admin: u.IsAdmin}
	if req.Declared {
		has, err := c.HasPasskey(u.ID)
		if err != nil {
			return State{}, fmt.Errorf("reading whether %s in %s has a passkey: %w", req.Username, req.App, err)
		}
		state.Passkey = has
	}
	for _, name := range req.Groups() {
		g, err := p.client(t, req).FindGroup(name)
		if err != nil {
			return State{}, fmt.Errorf("looking up group %s in %s: %w", name, req.App, err)
		}
		if g == nil || !u.InGroup(g.ID) {
			state.MissingGroups = append(state.MissingGroups, name)
		}
	}
	return state, nil
}

// Steps for a passkey account: a new one is created as an administrator, or
// without the flag for a Member request, put in every app's admin group (and,
// when declared, every member group), and given a link, since it cannot sign
// in without one. An existing one is only made an administrator if it is not
// and the request is not a member's, has its email marked verified if it is
// not, is put in the groups it is missing, and is given a link while it has
// no passkey yet.
func (pocketID) Steps(state State, req Request) []Action {
	if !state.Exists {
		out := []Action{ActionCreate}
		if len(state.MissingGroups) > 0 {
			out = append(out, ActionJoinGroups)
		}
		return withLink(out, req)
	}
	var out []Action
	if !state.Verified {
		out = append(out, ActionVerify)
	}
	if !state.Admin && !req.Member {
		out = append(out, ActionGrantAdmin)
	}
	if len(state.MissingGroups) > 0 {
		out = append(out, ActionJoinGroups)
	}
	if req.Declared && !state.Passkey {
		out = withLink(out, req)
	}
	return out
}

// withLink adds the login link, and the email of one when asked for.
func withLink(out []Action, req Request) []Action {
	out = append(out, ActionLoginLink)
	if req.EmailLink {
		out = append(out, ActionEmailLink)
	}
	return out
}

// newUser is exactly what a create sends, and what the plan prints. A
// declared operator always has a first and a last name, since validate refuses
// one without; were the first name empty, the username would stand in.
func (pocketID) newUser(req Request) pocketid.NewUser {
	first := req.FirstName
	if first == "" {
		first = req.Username
	}
	u := pocketid.NewUser{
		Username:    req.Username,
		FirstName:   first,
		LastName:    req.LastName,
		DisplayName: strings.TrimSpace(first + " " + req.LastName),
		IsAdmin:     !req.Member,
	}
	if req.Email != "" {
		email := req.Email
		u.Email = &email
		u.EmailVerified = true
	}
	return u
}

func (p pocketID) Describe(a Action, req Request) string {
	switch a {
	case ActionCreate:
		body, _ := json.Marshal(p.newUser(req))
		if req.Member {
			return fmt.Sprintf("create user %s: POST /api/users %s", req.Username, body)
		}
		return fmt.Sprintf("create user %s as an administrator: POST /api/users %s", req.Username, body)
	case ActionVerify:
		return fmt.Sprintf("mark email verified for %s: PUT /api/users/<id> with emailVerified true and every other field sent back as it is now", req.Username)
	case ActionGrantAdmin:
		return fmt.Sprintf("grant admin %s: PUT /api/users/<id> with isAdmin true and every other field sent back as it is now", req.Username)
	case ActionJoinGroups:
		return fmt.Sprintf("add %s to %s: POST /api/user-groups for any that does not exist yet, then PUT /api/users/<id>/user-groups with the groups %s is in now, plus these", req.Username, req.GroupsPhrase(), req.Username)
	case ActionLoginLink:
		return fmt.Sprintf("issue one-time login link for %s: valid %s and for one sign in, printed once and only with --execute", req.Username, linkTTL(req))
	case ActionEmailLink:
		return fmt.Sprintf("email one-time login link to %s: POST /api/users/<id>/one-time-access-email, valid %s, a link of Pocket ID's own", req.Username, linkTTL(req))
	}
	return string(a) + " " + req.Username
}

func (p pocketID) Execute(t Transport, req Request, actions []Action) (Outcome, error) {
	c := p.client(t, req)
	var user *pocketid.User
	load := func() error {
		if user != nil {
			return nil
		}
		found, err := p.find(c, req)
		if err != nil {
			return err
		}
		if found == nil {
			return fmt.Errorf("%s does not exist in %s", req.Username, req.App)
		}
		user = found
		return nil
	}
	var out Outcome
	for _, a := range actions {
		switch a {
		case ActionCreate:
			created, err := c.CreateUser(p.newUser(req))
			if err != nil {
				return Outcome{}, fmt.Errorf("creating %s: %w", req.Username, err)
			}
			user = &created
		// Each update sends the whole user, so the copy held here is kept
		// as Pocket ID now has it, or the second update would undo the first.
		case ActionVerify:
			if err := load(); err != nil {
				return Outcome{}, err
			}
			if err := c.VerifyEmail(*user); err != nil {
				return Outcome{}, fmt.Errorf("marking %s's email verified: %w", req.Username, err)
			}
			user.EmailVerified = true
		case ActionGrantAdmin:
			if err := load(); err != nil {
				return Outcome{}, err
			}
			if err := c.SetAdmin(*user); err != nil {
				return Outcome{}, fmt.Errorf("making %s an administrator: %w", req.Username, err)
			}
			user.IsAdmin = true
		case ActionJoinGroups:
			if err := load(); err != nil {
				return Outcome{}, err
			}
			ids := map[string]bool{}
			for _, g := range user.UserGroups {
				ids[g.ID] = true
			}
			for _, name := range req.Groups() {
				g, err := c.FindGroup(name)
				if err != nil {
					return Outcome{}, fmt.Errorf("looking up group %s: %w", name, err)
				}
				if g == nil {
					created, err := c.CreateGroup(name)
					if err != nil {
						return Outcome{}, fmt.Errorf("creating group %s: %w", name, err)
					}
					g = &created
				}
				ids[g.ID] = true
			}
			all := make([]string, 0, len(ids))
			for id := range ids {
				all = append(all, id)
			}
			sort.Strings(all)
			if err := c.SetUserGroups(user.ID, all); err != nil {
				return Outcome{}, fmt.Errorf("adding %s to %s: %w", req.Username, strings.Join(req.Groups(), ", "), err)
			}
		case ActionLoginLink:
			if err := load(); err != nil {
				return Outcome{}, err
			}
			token, err := c.LoginToken(user.ID, linkTTL(req))
			if err != nil {
				return Outcome{}, fmt.Errorf("issuing a login link for %s: %w", req.Username, err)
			}
			out.LoginLink = pocketid.LoginLink(req.PublicURL, token)
			out.ExpiresIn = linkTTL(req)
		case ActionEmailLink:
			if err := load(); err != nil {
				return Outcome{}, err
			}
			if err := c.EmailLoginLink(user.ID, linkTTL(req)); err != nil {
				return Outcome{}, fmt.Errorf("asking Pocket ID to email %s a login link: %w", req.Username, err)
			}
			out.Emailed = true
		default:
			return Outcome{}, fmt.Errorf("pocket-id has no step for %q", a)
		}
	}
	return out, nil
}

// MagicLink issues a declared operator a one-time sign-in link at Pocket ID, has
// Pocket ID email one as well when req.EmailLink is set, and does nothing
// else: making the account, its groups and its administrator flag are
// apply's. It serves a first sign in whose link expired and an operator who lost
// their passkey alike. Without execute it only finds the account, so a dry run
// says whether there is one to sign in to.
func MagicLink(t Transport, req Request, execute bool) (Outcome, error) {
	req.Declared = true
	p := pocketID{}
	c := p.client(t, req)
	u, err := p.find(c, req)
	if err != nil {
		return Outcome{}, redact(fmt.Errorf("looking up %s in %s: %w", req.Username, req.App, err), req.APIKey)
	}
	if u == nil {
		return Outcome{}, fmt.Errorf("%s has no account in %s yet. apply makes every declared operator's account, with a sign-in link of its own", req.Username, req.App)
	}
	if u.Disabled {
		return Outcome{}, fmt.Errorf("%s's account in %s is disabled. Re-enabling an account is not something this command decides; do it deliberately, then run it again", req.Username, req.App)
	}
	if !execute {
		return Outcome{}, nil
	}
	actions := withLink(nil, req)
	out, err := p.Execute(t, req, actions)
	if err != nil {
		return Outcome{}, redact(err, req.APIKey, out.LoginLink)
	}
	return out, nil
}
