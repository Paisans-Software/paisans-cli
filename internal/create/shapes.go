package create

import "github.com/paisans-software/paisans-stack/internal/config"

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
			{name: "server", roles: []config.Role{config.RoleData, config.RoleApps, config.RoleGateway}, public: true},
		},
	},
	ShapeHomeAndVPS: {
		label: "one home and one VPS",
		sites: []shapeSite{
			{name: "home", roles: []config.Role{config.RoleData, config.RoleApps}},
			{name: "vps", roles: []config.Role{config.RoleGateway}, public: true},
		},
	},
	ShapeHighAvailability: {
		label: "two homes and a VPS (high availability)",
		sites: []shapeSite{
			{name: "home-a", roles: []config.Role{config.RoleData, config.RoleApps}, endpoint: true},
			{name: "home-b", roles: []config.Role{config.RoleData, config.RoleApps}},
			{name: "vps", roles: []config.Role{config.RoleGateway, config.RoleWitness}, public: true},
		},
	},
	ShapeCustom: {label: "custom: choose each site's roles"},
}

// roleWhat says what a machine of each role does, for the named shapes,
// which give a machine its roles rather than asking for them.
var roleWhat = map[config.Role]string{
	config.RoleData:    "holds the databases and uploaded files",
	config.RoleApps:    "runs the apps",
	config.RoleGateway: "is where visitors arrive: every hostname points at it",
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
	def      bool
	// twoSites is a capability one machine cannot hold: the chat server
	// binds the ports the database does, and its web client the port the
	// gateway does (validate's port-collision).
	twoSites bool
	apps     []appSpec
}

// appSpec is how a capability's app is declared: its key, its kind, the
// label its hostname is under, and whether the gate can stand in front of it
// (kinds.Gate's Gateable).
type appSpec struct {
	key      string
	kind     config.Kind
	sub      string
	gateable bool
	what     string
}

var capabilities = []capability{
	{question: "Forums and social (Mbin)?", def: true, apps: []appSpec{
		{key: "talk", kind: config.KindMbin, sub: "talk", gateable: true, what: "forums and social"},
	}},
	{question: "A wiki (Outline)?", def: true, apps: []appSpec{
		{key: "docs", kind: config.KindOutline, sub: "docs", gateable: true, what: "wiki"},
	}},
	{question: "Blogs (WriteFreely)?", def: true, apps: []appSpec{
		{key: "blog", kind: config.KindWriteFreely, sub: "blog", gateable: true, what: "blog"},
	}},
	{question: "Chat (Matrix: Synapse and the Element web client)?", def: false, twoSites: true, apps: []appSpec{
		{key: "chat", kind: config.KindSynapse, sub: "chat", what: "chat server"},
		{key: "web", kind: config.KindElement, sub: "web", gateable: true, what: "chat web client"},
	}},
	{question: "A status page (uptime monitor)?", def: true, apps: []appSpec{
		{key: statusApp, kind: config.KindUptime, sub: "health", what: "status page"},
	}},
}

// signIn is the app the wizard declares without being asked: every other app
// signs in through it.
var signIn = appSpec{key: "auth", kind: config.KindPocketID, sub: "id", what: "sign in"}

const (
	statusApp   = "status"
	monitorSite = "health"
)
