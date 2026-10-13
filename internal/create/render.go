package create

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
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
	// MonitorBesideGateway is the health page sharing the gateway's machine,
	// which its comment in the file says.
	MonitorBesideGateway bool
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
	// Links is, for each role the app's blueprint consumes, the setting it
	// names and the hostname of the app providing the role.
	Links map[string]string
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
	pinTo, monitor := "", ""
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
			v.MonitorBesideGateway = s.Has(config.RoleGateway)
		}
	}
	if pinTo == "" && len(sites) > 0 {
		pinTo = sites[0].Name
	}

	// An app whose blueprint consumes a role is pointed at the app that
	// provides it, through the setting the blueprint names.
	providers := map[string]string{}
	for _, app := range a.Apps {
		for _, role := range kinds.Provides(app.Kind) {
			providers[role] = app.Hostname
		}
	}
	objects := false
	for _, app := range a.Apps {
		av := appView{App: app}
		for _, c := range kinds.Consumes(app.Kind) {
			if host, ok := providers[c.Role]; ok {
				if av.Links == nil {
					av.Links = map[string]string{}
				}
				av.Links[c.Setting] = host
			}
		}
		switch {
		case app.Kind == config.KindUptime && monitor != "":
			av.Placement = pinned(monitor)
		case placedApart(app.Key):
			av.Placement = pinned(siteFor(app.Key, sites, pinTo))
		case kinds.UsesPostgres(app.Kind) && len(data) > 0:
			// Clustered whenever any site holds data, so that adding a data
			// site later is an addition rather than a move. A clustered app
			// keeps its files in object storage when its blueprint can.
			av.Placement = "cluster"
			if kinds.UsesObjectStorage(app.Kind) {
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

// placedApart reports whether the app under key says where it may go (its
// wizard entry's on and avoid), which decides its site over the defaults.
func placedApart(key string) bool {
	spec, ok := specFor(key)
	return ok && (len(spec.on) > 0 || len(spec.avoid) > 0)
}

// siteFor is the first site the app under key may go on: one holding every
// role its spec's on names and none its avoid names, and not a monitor away
// from the gateway, which serves the monitor alone. pinTo when none may.
func siteFor(key string, sites []Site, pinTo string) string {
	spec, _ := specFor(key)
	for _, s := range sites {
		if s.Has(config.RoleMonitor) && !s.Has(config.RoleGateway) {
			continue
		}
		ok := true
		for _, r := range spec.on {
			ok = ok && s.Has(r)
		}
		for _, r := range spec.avoid {
			ok = ok && !s.Has(r)
		}
		if ok {
			return s.Name
		}
	}
	return pinTo
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
