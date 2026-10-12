package create

import "github.com/paisans-software/paisans-stack/internal/config"

// Answers is what the questions collected, in the order they were asked. It
// holds no YAML and decides nothing: placement, storage and the rest follow
// from it in Render, so that the same answers always give the same file.
type Answers struct {
	Name   string
	Domain string
	// Provider is the DNS provider certificates are issued through.
	Provider string
	Admins   []Admin
	Shape    Shape
	Sites    []Site
	// Monitor is the status page's own machine, added when the status page
	// was chosen and no site holds the monitor role. It is kept apart from
	// Sites so that answering the sites again does not lose it.
	Monitor *Site
	Apps    []App
	SMTP    SMTP
}

// Admin is one person who may log in to the sites and administer the apps.
type Admin struct {
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
	Key      string
	Kind     config.Kind
	Hostname string
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
// then the monitor's.
func (a Answers) AllSites() []Site {
	sites := append([]Site{}, a.Sites...)
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
