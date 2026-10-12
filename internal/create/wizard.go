package create

import (
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Section is a group of questions that can be answered again on its own.
type Section int

const (
	SectionAll Section = iota
	SectionCommunity
	SectionAdmins
	SectionSites
	SectionApps
	SectionMail
)

var sectionNames = map[Section]string{
	SectionAll:       "every question",
	SectionCommunity: "the community",
	SectionAdmins:    "the admins",
	SectionSites:     "the machines",
	SectionApps:      "the apps",
	SectionMail:      "mail",
}

func (s Section) String() string { return sectionNames[s] }

// SectionFor is the section whose answers a validate finding's key comes
// from, so a refusal leads back to the questions that can change it.
func SectionFor(key string) Section {
	top, _, _ := strings.Cut(key, ".")
	switch top {
	case "community", "acme":
		return SectionCommunity
	case "admins":
		return SectionAdmins
	case "sites", "mesh", "cluster", "etcd":
		return SectionSites
	case "apps", "storage":
		return SectionApps
	case "smtp":
		return SectionMail
	}
	return SectionAll
}

// Wizard asks the questions and keeps the answers. Every answer already
// given is the default when its section is asked again, so answering again
// is mostly Enter.
type Wizard struct {
	p *Prompter
	// Answers is what has been answered so far.
	Answers Answers
	// asked is set once the apps have been asked, after which an app's
	// default is whether it was chosen rather than the capability's own.
	asked bool
}

// NewWizard asks its questions through p.
func NewWizard(p *Prompter) *Wizard { return &Wizard{p: p} }

// Ask asks section's questions, or every question for SectionAll.
func (w *Wizard) Ask(section Section) error {
	steps := []struct {
		s   Section
		ask func() error
	}{
		{SectionCommunity, w.community},
		{SectionAdmins, w.admins},
		{SectionSites, w.sites},
		{SectionApps, w.apps},
		{SectionMail, w.mail},
	}
	for _, step := range steps {
		if section != SectionAll && section != step.s {
			continue
		}
		if err := step.ask(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Wizard) community() error {
	a := &w.Answers
	w.p.Say("\nThe community")
	name, err := w.p.Text("Community name", a.Name, nil)
	if err != nil {
		return err
	}
	w.p.Say("The domain is permanent once the community federates: other servers")
	w.p.Say("know its members by it, so changing it later is a rebuild, not a rename.")
	domain, err := w.p.Text("Domain", a.Domain, checkHostname)
	if err != nil {
		return err
	}
	w.p.Say("Certificates are issued through the domain's DNS, so the toolkit needs")
	w.p.Say("to know where it is hosted. Its API token is asked for later, by init.")
	providers := []string{"cloudflare", "desec"}
	def := 0
	for i, p := range providers {
		if p == a.Provider {
			def = i
		}
	}
	i, err := w.p.Choose("Where the domain's DNS is hosted", providers, def)
	if err != nil {
		return err
	}
	// The apps' default hostnames follow the domain, so a changed domain
	// takes them with it.
	if a.Domain != "" && a.Domain != domain {
		for j, app := range a.Apps {
			if strings.HasSuffix(app.Hostname, "."+a.Domain) {
				a.Apps[j].Hostname = strings.TrimSuffix(app.Hostname, a.Domain) + domain
			}
		}
	}
	a.Name, a.Domain, a.Provider = name, domain, providers[i]
	return nil
}

func (w *Wizard) admins() error {
	a := &w.Answers
	w.p.Say("\nAdmins: the people who log in to the machines and administer the apps.")
	w.p.Say("Each becomes a sign in administrator on the first apply.")
	prev := a.Admins
	var admins []Admin
	for i := 0; ; i++ {
		var d Admin
		if i < len(prev) {
			d = prev[i]
		}
		var taken []string
		for _, ad := range admins {
			taken = append(taken, ad.ID)
		}
		id, err := w.p.Text("Username", d.ID, notIn(taken, "admin", checkAdminID))
		if err != nil {
			return err
		}
		name, err := w.p.Text("Full name (first and last)", d.Name, checkFullName)
		if err != nil {
			return err
		}
		email, err := w.p.Text("Email", d.Email, checkEmail)
		if err != nil {
			return err
		}
		keys, err := w.p.Lines("SSH public keys", d.Keys, checkKey)
		if err != nil {
			return err
		}
		admins = append(admins, Admin{ID: id, Name: name, Email: email, Keys: keys})
		more, err := w.p.YesNo("Add another admin?", i+1 < len(prev))
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	a.Admins = admins
	return nil
}

func (w *Wizard) sites() error {
	a := &w.Answers
	w.p.Say("\nThe machines")
	labels := make([]string, len(shapes))
	for i, s := range shapes {
		labels[i] = s.label
	}
	before := len(a.Sites) > 0
	i, err := w.p.Choose("How many machines, and what each does", labels, int(a.Shape))
	if err != nil {
		return err
	}
	shape := Shape(i)
	prev := a.Sites
	if before && shape != a.Shape {
		prev = nil
	}
	var sites []Site
	if shape == ShapeCustom {
		sites, err = w.customSites(prev)
	} else {
		sites, err = w.namedSites(shape, prev)
	}
	if err != nil {
		return err
	}
	a.Shape, a.Sites = shape, sites
	return nil
}

func (w *Wizard) siteNames(sites []Site) []string {
	var names []string
	for _, s := range sites {
		names = append(names, s.Name)
	}
	return names
}

func (w *Wizard) namedSites(shape Shape, prev []Site) ([]Site, error) {
	var sites []Site
	for i, ss := range shapes[shape].sites {
		d := Site{Name: ss.name, SSHUser: "ubuntu"}
		if i < len(prev) {
			d = prev[i]
		}
		w.p.Say("\nMachine %d of %d. It:", i+1, len(shapes[shape].sites))
		for _, r := range ss.roles {
			w.p.Say("  - %s", roleWhat[r])
		}
		w.p.Say("Give it a short name: paisans.yaml and every command call it by this.")
		name, err := w.p.Text("Name", d.Name, notIn(w.siteNames(sites), "machine", checkSiteName))
		if err != nil {
			return nil, err
		}
		site := Site{Name: name, Roles: ss.roles}
		if ss.public {
			w.p.Say("The other machines dial it, and every hostname points at it.")
			site.PublicAddress, err = w.p.Text("Public IPv4 address", d.PublicAddress, checkIPv4)
		}
		if err != nil {
			return nil, err
		}
		if ss.endpoint {
			w.p.Say("The other home dials this one directly, so losing the VPS does not")
			w.p.Say("leave each home alone: give a dynamic DNS name and a forwarded UDP port.")
			site.Endpoint, err = w.p.Text("Endpoint (host:port)", d.Endpoint, checkEndpoint)
			if err != nil {
				return nil, err
			}
		}
		if err := w.ssh(&site, d); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}

func (w *Wizard) customSites(prev []Site) ([]Site, error) {
	def := ""
	if len(prev) > 0 {
		def = strconv.Itoa(len(prev))
	}
	n, err := w.p.Text("How many machines", def, checkCount)
	if err != nil {
		return nil, err
	}
	count, _ := strconv.Atoi(n)
	w.p.Say("Roles: %s. The whole file is checked before it is written.", roleList(allRoles))
	var sites []Site
	for i := range count {
		d := Site{Name: "site-" + strconv.Itoa(i+1), SSHUser: "ubuntu"}
		if i < len(prev) {
			d = prev[i]
		}
		w.p.Say("\nMachine %d", i+1)
		name, err := w.p.Text("Name", d.Name, notIn(w.siteNames(sites), "machine", checkSiteName))
		if err != nil {
			return nil, err
		}
		var roles []config.Role
		if _, err := w.p.Text("Roles, comma separated", roleList(d.Roles), func(s string) error {
			var e error
			roles, e = parseRoles(s)
			return e
		}); err != nil {
			return nil, err
		}
		site := Site{Name: name, Roles: roles}
		if site.PublicAddress, err = w.p.Optional("Public IPv4 address (blank: none)", d.PublicAddress, checkIPv4); err != nil {
			return nil, err
		}
		if site.Endpoint, err = w.p.Optional("Endpoint the other machines dial, host:port (blank: none)", d.Endpoint, checkEndpoint); err != nil {
			return nil, err
		}
		if err := w.ssh(&site, d); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, nil
}

// ssh asks how the toolkit reaches site. The host defaults to the public
// address, so it is asked only as an override when there is one.
func (w *Wizard) ssh(site *Site, d Site) error {
	var err error
	if site.PublicAddress != "" {
		site.SSHHost, err = w.p.Optional("SSH host (blank: the public address)", d.SSHHost, checkHost)
	} else {
		site.SSHHost, err = w.p.Text("SSH host", d.SSHHost, checkHost)
	}
	if err != nil {
		return err
	}
	user := d.SSHUser
	if user == "" {
		user = "ubuntu"
	}
	site.SSHUser, err = w.p.Text("SSH user (must already exist)", user, checkUnixUser)
	return err
}

func (w *Wizard) apps() error {
	a := &w.Answers
	w.p.Say("\nThe apps. Each gets a hostname under %s.", a.Domain)
	prev := map[string]App{}
	for _, app := range a.Apps {
		prev[app.Key] = app
	}
	hostname := func(spec appSpec) (string, error) {
		def := spec.sub + "." + a.Domain
		if p, ok := prev[spec.key]; ok {
			def = p.Hostname
		}
		return w.p.Text("Hostname for the "+spec.what, def, checkHostname)
	}

	w.p.Say("Sign in (Pocket ID) is always included: every other app signs in through it.")
	host, err := hostname(signIn)
	if err != nil {
		return err
	}
	apps := []App{{Key: signIn.key, Kind: signIn.kind, Hostname: host}}

	status := false
	for _, c := range capabilities {
		if c.twoSites && len(a.Sites) < 2 {
			w.p.Say("Chat needs a second machine: its server shares ports with the database")
			w.p.Say("and its web client shares one with the gateway, so it is not offered here.")
			continue
		}
		def := c.def
		if w.asked {
			_, def = prev[c.apps[0].key]
		}
		yes, err := w.p.YesNo(c.question, def)
		if err != nil {
			return err
		}
		if !yes {
			continue
		}
		for _, spec := range c.apps {
			host, err := hostname(spec)
			if err != nil {
				return err
			}
			app := App{Key: spec.key, Kind: spec.kind, Hostname: host}
			if spec.gateable {
				def := true
				if p, ok := prev[spec.key]; ok {
					def = p.MembersOnly
				}
				w.p.Say("Who can see the %s?", spec.what)
				w.p.Say("  yes: members only. Anyone else is sent to sign in first;")
				w.p.Say("       other communities still federate with it.")
				w.p.Say("  no:  anyone who can reach it.")
				if app.MembersOnly, err = w.p.YesNo("Members only?", def); err != nil {
					return err
				}
			}
			apps = append(apps, app)
		}
		status = status || c.apps[0].key == statusApp
	}
	w.asked = true

	if status {
		keep, err := w.monitor()
		if err != nil {
			return err
		}
		if !keep {
			apps = slices.DeleteFunc(apps, func(app App) bool { return app.Key == statusApp })
		}
	} else {
		a.Monitor = nil
	}
	a.Apps = apps
	return nil
}

// monitor finds the status page a machine of its own, and reports whether
// the status page is kept.
func (w *Wizard) monitor() (bool, error) {
	a := &w.Answers
	for _, s := range a.Sites {
		if s.Has(config.RoleMonitor) {
			a.Monitor = nil
			return true, nil
		}
	}
	w.p.Say("\nThe status page runs on a machine of its own, never the gateway or the")
	w.p.Say("witness: its job is to report their failures, so it must not fail with")
	w.p.Say("them. It serves its own hostname, so it needs a public address.")
	add, err := w.p.YesNo("Add a machine for it?", true)
	if err != nil || !add {
		a.Monitor = nil
		return false, err
	}
	d := Site{Name: monitorSite, SSHUser: "ubuntu"}
	if a.Monitor != nil {
		d = *a.Monitor
	}
	name, err := w.p.Text("Name", d.Name, notIn(w.siteNames(a.Sites), "machine", checkSiteName))
	if err != nil {
		return false, err
	}
	site := Site{Name: name, Roles: []config.Role{config.RoleMonitor}}
	if site.PublicAddress, err = w.p.Text("Public IPv4 address", d.PublicAddress, checkIPv4); err != nil {
		return false, err
	}
	if err := w.ssh(&site, d); err != nil {
		return false, err
	}
	a.Monitor = &site
	return true, nil
}

func (w *Wizard) mail() error {
	a := &w.Answers
	d := a.SMTP
	w.p.Say("\nMail. Sign in mails each admin's login link and every member's login")
	w.p.Say("codes, the status page alerts by mail, and the forums send account mail,")
	w.p.Say("so a server to send through is required. Its password is set afterwards,")
	w.p.Say("with secrets set, so it is never written here.")
	host, err := w.p.Text("SMTP host", d.Host, checkHostname)
	if err != nil {
		return err
	}
	modes := []string{config.SMTPStartTLS, config.SMTPTLS}
	def := 0
	if d.Security == config.SMTPTLS {
		def = 1
	}
	i, err := w.p.Choose("SMTP security", modes, def)
	if err != nil {
		return err
	}
	port := "587"
	if modes[i] == config.SMTPTLS {
		port = "465"
	}
	if d.Port != 0 && d.Security == modes[i] {
		port = strconv.Itoa(d.Port)
	}
	if port, err = w.p.Text("SMTP port", port, checkPort); err != nil {
		return err
	}
	from, err := w.p.Text("From address", d.FromAddress, checkEmail)
	if err != nil {
		return err
	}
	user := d.Username
	if user == "" {
		user = from
	}
	if user, err = w.p.Text("SMTP username", user, nil); err != nil {
		return err
	}
	fromName := d.FromName
	if fromName == "" {
		fromName = a.Name
	}
	if fromName, err = w.p.Text("From name", fromName, nil); err != nil {
		return err
	}
	n, _ := strconv.Atoi(port)
	a.SMTP = SMTP{Host: host, Port: n, Security: modes[i], Username: user, FromAddress: from, FromName: fromName}
	return nil
}
