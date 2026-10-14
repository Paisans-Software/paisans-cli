package create

import "github.com/paisans-software/paisans-stack/internal/config"

// Answers is what the questions collected, in the order they were asked. It
// holds no YAML and decides nothing: placement, storage and the rest follow
// from it in Render, so that the same answers always give the same file.
type Answers struct {
	Name   string
	Domain string
	// Provider is the DNS provider certificates are issued through.
	Provider  string
	Operators []Operator
	Shape     Shape
	Sites     []Site
	// Monitor is the health page's own machine, added when the health page
	// was chosen and no site holds the monitor role. It is kept apart from
	// Sites so that answering the sites again does not lose it.
	Monitor *Site
	// MonitorOnGateway puts the health page beside the gateway instead of on
	// a machine of its own: AllSites adds the monitor role to the site holding
	// the gateway role, so answering the sites again keeps it.
	MonitorOnGateway bool
	Apps             []App
	// Management is who manages accounts: blueprints.ManagementPaisans, the
	// account service, or blueprints.ManagementPocketID.
	Management string
	// Account is the account service's answers, under management paisans.
	Account Account
	// PocketID is Pocket ID's own account settings, under management
	// pocket-id.
	PocketID PocketIDAccounts
	// SMTP is the mail every app sends through. Its Host is empty when the
	// community sends no mail, which only management pocket-id allows.
	SMTP SMTP
}

// Account is what the account app's settings need: how people join, invite
// or open, and the code of conduct everyone who signs up agrees to, read from
// the Markdown file named.
type Account struct {
	Hostname string
	// Signup is blueprints.SignupInvite or blueprints.SignupOpen.
	Signup            string
	CodeOfConductPath string
	CodeOfConduct     string
}

// PocketIDAccounts is how Pocket ID manages accounts itself.
type PocketIDAccounts struct {
	// Signups is allow_user_signups: disabled, withToken or open.
	Signups string
	// EmailCode is email_one_time_access_as_unauthenticated: a member whose
	// link expired asks for an email code at the sign-in page.
	EmailCode bool
	// EmailLinks is email_one_time_access_as_admin: Pocket ID emails a
	// login link an administrator issues, apply's to each operator among
	// them.
	EmailLinks bool
}

// Operator is one person who runs the machines: they log in to the sites,
// hold the secrets, and run apply.
type Operator struct {
	ID    string
	Name  string
	Email string
	Keys  []string
}

// Site is one machine.
type Site struct {
	Name          string
	Roles         []config.Role
	PublicAddress string
	Endpoint      string
	SSHHost       string
	SSHUser       string
}

// Has reports whether the site holds role.
func (s Site) Has(role config.Role) bool {
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// App is one declared app. Sign in is among them, so Render reads one list.
type App struct {
	Key       string
	Blueprint config.Blueprint
	Hostname  string
	// DisplayName is what a person signing in is shown for the app. Render
	// writes it only when it differs from Key, the name the client has
	// without one.
	DisplayName string
	// MembersOnly puts the app behind the gate, which admits members and
	// admins. The toolkit runs the gate itself whenever an app is gated.
	MembersOnly bool
}

// SMTP is the mail every app that sends mail sends it through. The password
// is a secret and is not here.
type SMTP struct {
	Host        string
	Port        int
	Security    string
	Username    string
	FromAddress string
	FromName    string
}

// AllSites is every site in the order the file declares them: the shape's,
// the gateway's holding the monitor role too when the health page is beside
// it, then the monitor's own machine.
func (a Answers) AllSites() []Site {
	sites := append([]Site{}, a.Sites...)
	if a.MonitorOnGateway {
		for i, s := range sites {
			if s.Has(config.RoleGateway) {
				s.Roles = append(append([]config.Role{}, s.Roles...), config.RoleMonitor)
				sites[i] = s
				break
			}
		}
	}
	if a.Monitor != nil {
		sites = append(sites, *a.Monitor)
	}
	return sites
}

// App returns the app declared under key.
func (a Answers) App(key string) (App, bool) {
	for _, app := range a.Apps {
		if app.Key == key {
			return app, true
		}
	}
	return App{}, false
}
