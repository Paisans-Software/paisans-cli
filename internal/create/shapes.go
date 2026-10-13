package create

import (
	"fmt"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Shape is a layout of sites and roles.
type Shape int

const (
	ShapeOneMachine Shape = iota
	ShapeHomeAndVPS
	ShapeHighAvailability
	ShapeCustom
)

// shapeSite is one site a named shape declares.
type shapeSite struct {
	name  string
	roles []config.Role
	// public is a site the other sites dial and every hostname points at,
	// so its public address is required.
	public bool
	// endpoint is the home that must be dialled too: with only the VPS
	// dialled, two homes reach each other only through it, and losing it
	// leaves each voter alone (validate's voters-share-a-relay).
	endpoint bool
}

// shape is a named layout: the role layouts validate accepts with no
// refusal. The VPS beside one home carries no witness, since a witness there
// would make two etcd voters, and two survive nothing while requiring both
// (validate's two-etcd-voters).
type shape struct {
	label string
	sites []shapeSite
}

var shapes = []shape{
	ShapeOneMachine: {
		label: "one machine",
		sites: []shapeSite{
			{name: "all-in-one", roles: []config.Role{config.RoleData, config.RoleApps, config.RoleGateway}, public: true},
		},
	},
	ShapeHomeAndVPS: {
		label: "one home and one VPS",
		sites: []shapeSite{
			{name: "home", roles: []config.Role{config.RoleData, config.RoleApps}},
			{name: "gateway", roles: []config.Role{config.RoleGateway}, public: true},
		},
	},
	ShapeHighAvailability: {
		label: "two homes and a VPS (high availability)",
		sites: []shapeSite{
			{name: "home-a", roles: []config.Role{config.RoleData, config.RoleApps}, endpoint: true},
			{name: "home-b", roles: []config.Role{config.RoleData, config.RoleApps}},
			{name: "gateway", roles: []config.Role{config.RoleGateway, config.RoleWitness}, public: true},
		},
	},
	ShapeCustom: {label: "custom: choose each site's roles"},
}

// roleWhat says what a machine of each role does, for the named shapes,
// which give a machine its roles rather than asking for them.
var roleWhat = map[config.Role]string{
	config.RoleData:    "holds the databases and uploaded files",
	config.RoleApps:    "runs the apps",
	config.RoleGateway: "receives all web traffic and passes it to the apps",
	config.RoleWitness: "breaks ties between the homes' databases",
}

// allRoles is the custom shape's checklist, in the order it is offered.
var allRoles = []config.Role{
	config.RoleData, config.RoleApps, config.RoleGateway,
	config.RoleWitness, config.RoleStorage, config.RoleMonitor,
}

// capability is one thing a community may want, in its own words, and the
// apps that provide it.
type capability struct {
	question string
	// name is what the capability is called on the line saying it is not
	// offered, for one with separateSites.
	name string
	def  bool
	// separateSites is a capability one machine cannot hold: its apps
	// collide with other roles' ports, so each goes where its on and avoid
	// let it.
	separateSites bool
	apps          []appSpec
}

// appSpec is how a capability's app is declared: its key, its kind, the
// label its hostname is under, whether the gate can stand in front of it
// (kinds.Gate's Gateable), and the roles its site must hold (on) and must
// not (avoid).
type appSpec struct {
	key      string
	kind     config.Kind
	sub      string
	gateable bool
	what     string
	on       []config.Role
	avoid    []config.Role
}

// health is the one capability the toolkit asks about itself, after the
// core catalog's (kinds.Wizard), since the monitor is its own blueprint.
var health = capability{question: "A health page (uptime monitor)?", def: true, apps: []appSpec{
	{key: healthApp, kind: config.KindUptime, sub: "health", what: "health page"},
}}

// capabilities is what the wizard offers: the core catalog's menu, then the
// health page.
func capabilities() ([]capability, error) {
	entries, err := kinds.Wizard()
	if err != nil {
		return nil, err
	}
	var out []capability
	for _, e := range entries {
		c := capability{question: e.Question, name: e.Name, def: e.Default, separateSites: e.SeparateSites}
		for _, app := range e.Apps {
			kind := config.Kind(app.Blueprint)
			if _, ok := kinds.For(kind); !ok {
				return nil, fmt.Errorf("the core catalog's wizard offers %s, which it does not hold", app.Blueprint)
			}
			c.apps = append(c.apps, appSpec{key: app.Key, kind: kind, sub: app.Subdomain, gateable: kinds.GateFor(kind).Gateable, what: app.What, on: app.On, avoid: app.Avoid})
		}
		out = append(out, c)
	}
	return append(out, health), nil
}

// specFor is the capability app declared under key, false for one the
// wizard does not offer (sign in, or an app added by hand).
func specFor(key string) (appSpec, bool) {
	all, err := capabilities()
	if err != nil {
		return appSpec{}, false
	}
	for _, c := range all {
		for _, spec := range c.apps {
			if spec.key == key {
				return spec, true
			}
		}
	}
	return appSpec{}, false
}

// signIn is the app the wizard declares without being asked: every other app
// signs in through it.
var signIn = appSpec{key: "auth", kind: config.KindPocketID, sub: "id", what: "sign in"}

const (
	healthApp   = "health"
	monitorSite = "health"
)
