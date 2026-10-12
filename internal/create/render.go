package create

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

//go:embed paisans-stack.yaml.tmpl
var templateText string

var fileTemplate = template.Must(template.New(config.FileName).Funcs(template.FuncMap{
	"q": quote,
}).Parse(templateText))

// MeshSubnet is the subnet the file declares. init checks it against every
// site before the first apply and moves it, with every site's address, when
// it overlaps anything there.
const MeshSubnet = "10.44.0.0/24"

// Origin is what the file's first line says wrote it: the paisans binary's
// version and the date it was written.
type Origin struct {
	Version string
	Date    string
}

// Render writes the answers as a commented paisans-stack.yaml with deployment id
// id, naming by as what wrote it. What follows from the answers rather than being asked (placement,
// object storage, replication, etcd timings) is decided here, so the same
// answers always give the same file.
func Render(a Answers, id string, by Origin) ([]byte, error) {
	v := build(a, id)
	v.Origin = by
	var out bytes.Buffer
	if err := fileTemplate.Execute(&out, v); err != nil {
		return nil, fmt.Errorf("rendering "+config.FileName+": %w", err)
	}
	return out.Bytes(), nil
}

// view is the template's data: the answers, and what follows from them.
type view struct {
	Origin   Origin
	ID       string
	Subnet   string
	Name     string
	Domain   string
	Provider string
	Admins   []Admin
	AdminIDs string
	Sites    []siteView
	// Synchronous is the high availability shape's synchronous replication.
	Synchronous bool
	// Voters is the number of etcd members the roles derive: every witness
	// and every data site.
	Voters      int
	Garage      []string
	Replication int
	Apps        []appView
	SMTP        SMTP
}

type siteView struct {
	Site
	Address  string
	RoleList string
}

type appView struct {
	App
	// Placement is the value written after `placement:`.
	Placement string
	// Homeserver is the chat server's hostname, for the web client.
	Homeserver string
}

func build(a Answers, id string) view {
	v := view{
		ID:          id,
		Subnet:      MeshSubnet,
		Name:        a.Name,
		Domain:      a.Domain,
		Provider:    a.Provider,
		Admins:      a.Admins,
		Synchronous: a.Shape == ShapeHighAvailability,
		SMTP:        a.SMTP,
	}
	ids := make([]string, len(a.Admins))
	for i, ad := range a.Admins {
		ids[i] = ad.ID
	}
	v.AdminIDs = strings.Join(ids, ", ")

	sites := a.AllSites()
	var data []string
	pinTo, monitor, chat, web := "", "", "", ""
	for i, s := range sites {
		roles := make([]string, len(s.Roles))
		for j, r := range s.Roles {
			roles[j] = string(r)
		}
		v.Sites = append(v.Sites, siteView{Site: s, Address: fmt.Sprintf("10.44.0.%d", i+1), RoleList: strings.Join(roles, ", ")})
		if s.Has(config.RoleData) {
			data = append(data, s.Name)
		}
		if s.Has(config.RoleData) || s.Has(config.RoleWitness) {
			v.Voters++
		}
		if pinTo == "" && s.Has(config.RoleApps) {
			pinTo = s.Name
		}
		if monitor == "" && s.Has(config.RoleMonitor) {
			monitor = s.Name
		}
		// The chat server binds the ports Patroni and bg_mon do, so it goes
		// where no data is; its web client binds 80, which the gateway's Caddy
		// holds, so it goes where no gateway is.
		if chat == "" && !s.Has(config.RoleData) && !s.Has(config.RoleMonitor) {
			chat = s.Name
		}
		if web == "" && s.Has(config.RoleApps) && !s.Has(config.RoleGateway) {
			web = s.Name
		}
	}
	if pinTo == "" && len(sites) > 0 {
		pinTo = sites[0].Name
	}
	if chat == "" {
		chat = pinTo
	}
	if web == "" {
		web = pinTo
	}

	homeserver := ""
	for _, app := range a.Apps {
		if app.Kind == config.KindSynapse {
			homeserver = app.Hostname
		}
	}
	objects := false
	for _, app := range a.Apps {
		av := appView{App: app, Homeserver: homeserver}
		switch {
		case app.Kind == config.KindUptime && monitor != "":
			av.Placement = pinned(monitor)
		case app.Kind == config.KindSynapse:
			av.Placement = pinned(chat)
		case app.Kind == config.KindElement:
			av.Placement = pinned(web)
		case clusterable(app.Kind) && len(data) > 0:
			// Clustered whenever any site holds data, so that adding a data
			// site later is an addition rather than a move.
			av.Placement = "cluster"
			if storesObjects(app.Kind) {
				objects = true
			}
		default:
			av.Placement = pinned(pinTo)
		}
		v.Apps = append(v.Apps, av)
	}
	// A clustered app may not keep its files on one disk
	// (storage-local-on-cluster), so Garage runs wherever the data does.
	if objects {
		v.Garage = data
		v.Replication = min(len(data), 2)
	}
	return v
}

func pinned(site string) string { return "{ pinned: " + site + " }" }

// clusterable is a kind with a Postgres database, which may join the
// cluster. The rest keep nothing in Postgres and are refused cluster
// placement (cluster-placement-without-a-cluster), or must be pinned.
func clusterable(k config.Kind) bool {
	switch k {
	case config.KindPocketID, config.KindMbin, config.KindOutline, config.KindWriteFreely:
		return true
	}
	return false
}

// storesObjects is a kind whose files go to object storage when it is
// clustered.
func storesObjects(k config.Kind) bool {
	switch k {
	case config.KindMbin, config.KindOutline, config.KindWriteFreely:
		return true
	}
	return false
}

// quote is s as a YAML scalar that reads back as exactly s: marshalled by
// the same library that will read it, so a name holding `:` or `#`, a quote
// or a leading `-` is a string rather than a syntax error or a list.
func quote(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		// A string always marshals; this is unreachable.
		panic(err)
	}
	return strings.TrimSuffix(string(out), "\n")
}
