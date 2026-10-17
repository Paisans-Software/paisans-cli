package create

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// Section is a group of questions that can be answered again on its own.
type Section int

const (
	SectionAll Section = iota
	SectionCommunity
	SectionOperators
	SectionSites
	SectionApps
	SectionMail
)

var sectionNames = map[Section]string{
	SectionAll:       "every question",
	SectionCommunity: "the community",
	SectionOperators: "the operators",
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
	case "operators":
		return SectionOperators
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
	// mailAsked is set once mail has been asked, after which sending none
	// is the default when none was chosen.
	mailAsked bool
	// DefaultUser is the deployment's own user, paisans-<token>, which a
	// machine whose SSH user is left empty is reached as. create sets it
	// from the id it made; "" names it generically.
	DefaultUser string
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
		{SectionOperators, w.operators},
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
	w.p.Say("The domain is permanent once the community federates: other servers know its members by it, so changing it later is a rebuild, not a rename.")
	domain, err := w.p.Text("Domain", a.Domain, checkHostname)
	if err != nil {
		return err
	}
	w.p.Say("Certificates are issued through the domain's DNS, so the toolkit needs to know where it is hosted. Its API token is asked for later, by init.")
	providers := []string{"cloudflare", "desec"}
	def := 0
	for i, p := range providers {
		if p == a.Provider {
			def = i
		}
	}
	i, err := w.p.Choose("Where is the domain's DNS hosted?", providers, def)
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

func (w *Wizard) operators() error {
	a := &w.Answers
	w.p.Say("\nOperators: the people who run the machines. They log in to every site, hold the secrets and run apply, and each becomes a Pocket ID user and a community admin on the first apply. A deployment has at least two, so that one lost workstation does not leave nobody able to run it.")
	prev := a.Operators
	var operators []Operator
	for i := 0; ; i++ {
		var d Operator
		if i < len(prev) {
			d = prev[i]
		}
		var taken []string
		keys := map[string]string{}
		for _, op := range operators {
			taken = append(taken, op.ID)
			for _, k := range op.Keys {
				if fp, err := checkKey(k); err == nil {
					keys[fp] = op.ID
				}
			}
		}
		if i > 0 {
			w.p.Say("\nOperator %d", i+1)
		}
		id, err := w.p.Text("Username", d.ID, notIn(taken, "operator", checkOperatorID))
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
		lines, err := w.p.Lines("SSH public keys", d.Keys, keyCheck(keys))
		if err != nil {
			return err
		}
		operators = append(operators, Operator{ID: id, Name: name, Email: email, Keys: lines})
		if len(operators) < 2 {
			continue
		}
		more, err := w.p.YesNo("Add another operator?", i+1 < len(prev))
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	a.Operators = operators
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
	i, err := w.p.Choose("How many machines, and what does each do?", labels, int(a.Shape))
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
	w.settleMonitor()
	return nil
}

// settleMonitor drops the health page's place beside the gateway when the
// machines answered again no longer give it one: exactly one gateway with a
// public address, and no machine holding the monitor role already. The health
// page then goes to the machine that holds the role, or to none, which
// validate refuses and answering the apps again settles.
func (w *Wizard) settleMonitor() {
	a := &w.Answers
	if !a.MonitorOnGateway {
		return
	}
	for _, s := range a.Sites {
		if s.Has(config.RoleMonitor) {
			a.MonitorOnGateway = false
			return
		}
	}
	if w.publicGateway() == "" {
		a.MonitorOnGateway = false
	}
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
		d := Site{Name: ss.name}
		if shape == ShapeOneMachine {
			// The one machine is the whole deployment, so it is named for it.
			if n := cmp.Or(slug(w.Answers.Name), slug(w.Answers.Domain)); n != "" {
				d.Name = n
			}
		}
		if i < len(prev) {
			d = prev[i]
		}
		w.p.Say("\nMachine %d of %d. It:", i+1, len(shapes[shape].sites))
		for _, r := range ss.roles {
			w.p.Say("  - %s", roleWhat[r])
		}
		name, err := w.p.Text("Machine name", d.Name, notIn(w.siteNames(sites), "machine", checkSiteName))
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
			w.p.Say("The other home dials this one directly, so losing the VPS does not leave each home alone: give a dynamic DNS name and a forwarded UDP port.")
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
	n, err := w.p.Text("How many machines?", def, checkCount)
	if err != nil {
		return nil, err
	}
	count, _ := strconv.Atoi(n)
	w.p.Say("Roles: %s. The whole file is checked before it is written.", roleList(allRoles))
	var sites []Site
	for i := range count {
		d := Site{Name: "site-" + strconv.Itoa(i+1)}
		if i < len(prev) {
			d = prev[i]
		}
		w.p.Say("\nMachine %d", i+1)
		name, err := w.p.Text("Machine name", d.Name, notIn(w.siteNames(sites), "machine", checkSiteName))
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
// address, so it is asked only as an override when there is one. The user may
// be left empty: the machine is then reached as the deployment's own user,
// which must exist on the host.
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
	own := "the deployment's own user"
	if w.DefaultUser != "" {
		own = w.DefaultUser + ", the deployment's own"
	}
	site.SSHUser, err = w.p.Optional("SSH user (blank: "+own+")", d.SSHUser, checkUnixUser)
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
	// displayName is asked only of an app whose blueprint has a client at
	// Pocket ID, since that is all it names. The app's key is the default,
	// which is the client's name when display_name is not set.
	displayName := func(spec appSpec, host string) (string, error) {
		if _, ok := blueprints.OIDCClient(spec.blueprint, host, spec.key); !ok {
			return "", nil
		}
		def := spec.key
		if p, ok := prev[spec.key]; ok && p.DisplayName != "" {
			def = p.DisplayName
		}
		return w.p.Text("Name shown at sign in for the "+spec.what, def, checkDisplayName)
	}

	w.p.Say("Sign in (Pocket ID) is always included: every other app signs in through it.")
	host, err := hostname(signIn)
	if err != nil {
		return err
	}
	apps := []App{{Key: signIn.key, Blueprint: signIn.blueprint, Hostname: host}}
	account, err := w.management()
	if err != nil {
		return err
	}
	if account != nil {
		apps = append(apps, *account)
	}

	offered, err := capabilities()
	if err != nil {
		return err
	}
	health := false
	for _, c := range offered {
		if c.separateSites && len(a.Sites) < 2 {
			w.p.Say("%s needs a second machine: its apps share ports with the roles one machine holds, so it is not offered here.", c.name)
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
			app := App{Key: spec.key, Blueprint: spec.blueprint, Hostname: host}
			if app.DisplayName, err = displayName(spec, host); err != nil {
				return err
			}
			if spec.gateable {
				def := true
				if p, ok := prev[spec.key]; ok {
					def = p.MembersOnly
				}
				w.p.Say("Who can see the %s?", spec.what)
				w.p.Say("  - yes: members only. Anyone else is sent to sign in first; other communities still federate with it.")
				w.p.Say("  - no: public. Anyone, including search engines and bots, can read what the app shows to visitors who are not signed in.")
				if app.MembersOnly, err = w.p.YesNo("Members only?", def); err != nil {
					return err
				}
			}
			apps = append(apps, app)
		}
		health = health || c.apps[0].key == healthApp
	}
	w.asked = true

	if health {
		keep, err := w.monitor()
		if err != nil {
			return err
		}
		if !keep {
			apps = slices.DeleteFunc(apps, func(app App) bool { return app.Key == healthApp })
		}
	} else {
		a.Monitor, a.MonitorOnGateway = nil, false
	}
	a.Apps = apps
	return nil
}

// management asks who manages accounts, and what that choice needs: the
// account app's hostname, how people join and the code of conduct under paisans, or
// Pocket ID's own signup and email settings under pocket-id. It returns the
// account app to declare, nil under pocket-id.
func (w *Wizard) management() (*App, error) {
	a := &w.Answers
	w.p.Say("\nWho manages the community's accounts: who may sign up, bans, and a member's way back in.")
	w.p.Say("  - the account service: people join by a member's invite or on a registration page, and agree to the code of conduct first. Nobody is a Pocket ID administrator.")
	w.p.Say("  - Pocket ID: its own admin pages, and its signup settings. Every operator is its administrator, and mail is optional.")
	def := 0
	if a.Management == blueprints.ManagementPocketID {
		def = 1
	}
	i, err := w.p.Choose("Who manages accounts?", []string{"the account service", "Pocket ID"}, def)
	if err != nil {
		return nil, err
	}
	if i == 1 {
		a.Management = blueprints.ManagementPocketID
		return nil, w.pocketIDAccounts()
	}
	a.Management = blueprints.ManagementPaisans
	d := a.Account
	if d.Hostname == "" {
		d.Hostname = "account." + a.Domain
	}
	if d.Signup == "" {
		d.Signup = blueprints.SignupInvite
	}
	host, err := w.p.Text("Hostname for the account service", d.Hostname, checkHostname)
	if err != nil {
		return nil, err
	}
	modes := []string{blueprints.SignupInvite, blueprints.SignupOpen}
	w.p.Say("People sign up on join.%s: by opening an invite link a member made (invite), or on a registration page anyone can use (open).", a.Domain)
	i, err = w.p.Choose("How do people join?", []string{"a member's invite link", "a registration page anyone can use"}, max(slices.Index(modes, d.Signup), 0))
	if err != nil {
		return nil, err
	}
	acc := Account{Hostname: host, Signup: modes[i]}
	w.p.Say("Everyone who signs up agrees to the code of conduct before an account exists. Give the Markdown file that holds it; its text is written into the configuration.")
	path, err := w.p.Text("Code of conduct file", d.CodeOfConductPath, func(s string) error {
		text, err := readCodeOfConduct(s)
		acc.CodeOfConduct = text
		return err
	})
	if err != nil {
		return nil, err
	}
	acc.CodeOfConductPath = path
	a.Account = acc
	return &App{Key: accountApp, Blueprint: config.BlueprintAccount, Hostname: host}, nil
}

// pocketIDAccounts asks Pocket ID's own account settings.
func (w *Wizard) pocketIDAccounts() error {
	a := &w.Answers
	d := a.PocketID
	if d.Signups == "" {
		d = PocketIDAccounts{Signups: "disabled", EmailLinks: true}
	}
	choices := []string{"disabled", "withToken", "open"}
	def := max(slices.Index(choices, d.Signups), 0)
	w.p.Say("Who may sign up at Pocket ID: nobody but those an operator adds (disabled), those given a signup link (withToken), or anyone (open).")
	i, err := w.p.Choose("Pocket ID signup", choices, def)
	if err != nil {
		return err
	}
	code, err := w.p.YesNo("May a member whose sign-in link expired ask for an email code? (needs mail)", d.EmailCode)
	if err != nil {
		return err
	}
	links, err := w.p.YesNo("Does Pocket ID email operators their login links? (needs mail)", d.EmailLinks)
	if err != nil {
		return err
	}
	a.PocketID = PocketIDAccounts{Signups: choices[i], EmailCode: code, EmailLinks: links}
	return nil
}

// monitor places the health page, on a machine of its own or beside the
// gateway, and reports whether the health page is kept.
func (w *Wizard) monitor() (bool, error) {
	a := &w.Answers
	for _, s := range a.Sites {
		if s.Has(config.RoleMonitor) {
			a.Monitor, a.MonitorOnGateway = nil, false
			return true, nil
		}
	}
	w.p.Say("\nThe health page reports the other machines failing, so it is best on a machine of its own.")
	if gateway := w.publicGateway(); gateway != "" {
		def := 0
		if a.MonitorOnGateway {
			def = 1
		}
		i, err := w.p.Choose("Where should it run?", []string{
			"on a machine of its own (recommended)",
			"on " + gateway + " (takes the monitor down with it, no alerts when the gateway fails)",
		}, def)
		if err != nil {
			return false, err
		}
		if i == 1 {
			a.Monitor, a.MonitorOnGateway = nil, true
			return true, nil
		}
	}
	a.MonitorOnGateway = false
	w.p.Say("It serves its own hostname, so it needs a public address.")
	d := Site{Name: monitorSite}
	if a.Monitor != nil {
		d = *a.Monitor
	}
	name, err := w.p.Text("Machine name", d.Name, notIn(w.siteNames(a.Sites), "machine", checkSiteName))
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

// publicGateway is the one site holding the gateway role with a public
// address, which the health page needs to sit beside it, or "" when there is
// not exactly one.
func (w *Wizard) publicGateway() string {
	var found []string
	for _, s := range w.Answers.Sites {
		if s.Has(config.RoleGateway) && s.PublicAddress != "" {
			found = append(found, s.Name)
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

func (w *Wizard) mail() error {
	a := &w.Answers
	d := a.SMTP
	if a.Management == blueprints.ManagementPocketID {
		w.p.Say("\nMail. The apps send sign in links and codes, alerts and notifications. With Pocket ID managing accounts it is optional, but nothing that needs it is sent without it. Its password is set afterwards, with secrets set, so it is never written here.")
		send, err := w.p.YesNo("Send mail?", d.Host != "" || !w.mailAsked)
		if err != nil {
			return err
		}
		w.mailAsked = true
		if !send {
			a.SMTP = SMTP{}
			return nil
		}
	} else {
		w.p.Say("\nMail. The apps send sign in links and codes, alerts, notifications and account mail, so a server to send through is required. Its password is set afterwards, with secrets set, so it is never written here.")
	}
	host, err := w.p.Text("SMTP host", d.Host, checkHostname)
	if err != nil {
		return err
	}
	modes := []string{config.SMTPStartTLS, config.SMTPTLS}
	def := 0
	if d.Security == config.SMTPTLS {
		def = 1
	}
	i, err := w.p.Choose("Which security does the SMTP server use?", modes, def)
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
