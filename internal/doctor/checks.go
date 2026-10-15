package doctor

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Reads is how the checks read the hosts: one method per read, each on one
// site. cmd/paisans makes them over each site's transport; a test answers
// them from a table. Each is called only once the checks it needs have
// passed, so none is ever asked of a site that did not answer ssh.
//
// A read that returns (out, failure) gives what the command printed and,
// when it failed, why, as one line; failure is empty when it did not fail.
type Reads interface {
	// Reach runs ReachCommand.
	Reach(site string) SiteReach
	// Mesh reads the site's mesh interface and the networks on the host.
	Mesh(site string) MeshProbe
	// EtcdHealth runs EtcdHealthCommand on a member's host.
	EtcdHealth(member string) (out, failure string)
	// EtcdVersion runs EtcdVersionCommand on a member's host.
	EtcdVersion(member string) (out, failure string)
	// PatroniCluster asks the site's Patroni for /cluster.
	PatroniCluster(site string) (out, failure string)
	// Sync runs SyncCommand on a member's host.
	Sync(member string) (out, failure string)
	// PatroniLog runs PatroniLogCommand on the site.
	PatroniLog(site string) string
	// Containers lists the site's containers and looks closer at each one
	// that is not running.
	Containers(site string) SiteContainers
	// Settings asks an app's running copy on the site for its gate settings.
	Settings(site, app string) SettingsProbe
	// Standby asks every site running a Pocket ID app whether its instance
	// is active, of the sites in reached alone.
	Standby(app string, reached map[string]bool) StandbyProbe
	// Clock reads the site's clock, with the workstation's on either side.
	Clock(site string) ClockSample
	// Leftovers takes the site's inventory and classifies it.
	Leftovers(site string) LeftoverProbe
}

// Checks is every check doctor makes on sites, the ones it asks, as a
// graph: each site's reach first, and everything read over a site's
// connection needing it, then from the bottom of the stack up, the order
// Diagnose reports in. Each check's judge is the section function Diagnose
// calls, given the probes of the check's own subject, so the graph's
// findings for a complete Input are Diagnose's.
//
// Reads that answer DNSReads as well get the DNS checks after the rest:
// Caddy's certificates on each Caddy site, the DNS token from each place
// that uses it, and the zone's records.
//
// A check that needs any of several sites' checks names only the sites
// asked; one whose sites were all left out by --site needs nothing, runs,
// and says what it could not ask, as Diagnose does.
func Checks(cfg *config.Config, sites []string, reads Reads) *Graph {
	g := NewGraph(sites)
	d := cfg.Deployment()
	asked := map[string]bool{}
	for _, s := range sites {
		asked[s] = true
	}
	reach := func(site string) string { return SectionReach + " " + site }
	reachOf := func(list []string) Need {
		var names []string
		for _, s := range list {
			names = append(names, reach(s))
		}
		return AnyOf(g.Existing(names...)...)
	}
	soft := func(names ...string) []string { return g.Existing(names...) }

	for _, site := range sites {
		g.Add(Node{Name: reach(site), Section: SectionReach, Site: site, Subject: site,
			Gather: func(Input) func(*Input) {
				r := reads.Reach(site)
				return func(in *Input) { in.Reach = append(in.Reach, r) }
			},
			Outcome: func(in Input) Outcome {
				if in.Reached()[site] {
					return Passed
				}
				return Failed
			},
			Judge: func(in Input) []Finding { return Reach(cfg, bySite(in.Reach, site, SiteReach.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionMesh + " " + site, Section: SectionMesh, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Mesh(site)
				return func(in *Input) { in.Mesh = append(in.Mesh, p) }
			},
			Judge: func(in Input) []Finding { return Mesh(cfg, bySite(in.Mesh, site, MeshProbe.site)) },
		})
	}

	if len(cfg.Etcd.Members) > 0 {
		g.Add(Node{Name: SectionEtcd, Section: SectionEtcd,
			Hard: []Need{reachOf(cfg.Etcd.Members)},
			// etcd, from the first member that answers, in the order
			// etcd.members gives.
			Gather: func(in Input) func(*Input) {
				reached := in.Reached()
				var p EtcdProbe
				for _, name := range cfg.Etcd.Members {
					if !reached[name] {
						continue
					}
					out, failure := reads.EtcdHealth(name)
					if _, ok := ParseEtcdHealth(out); !ok {
						why := failure
						if why == "" {
							why = "unreadable answer: " + firstLine(out)
						}
						p.Tried = append(p.Tried, fmt.Sprintf("%s: %s", name, why))
						continue
					}
					p.Site, p.Health = name, out
					break
				}
				return func(in *Input) {
					in.Etcd.Site, in.Etcd.Health, in.Etcd.Tried = p.Site, p.Health, p.Tried
				}
			},
			Outcome: func(in Input) Outcome {
				if in.Etcd.Site == "" {
					return Failed
				}
				return Passed
			},
			Judge: func(in Input) []Finding { return EtcdQuorum(cfg, in) },
		})
		g.Add(Node{Name: SectionEtcd + " version", Section: SectionEtcd, Subject: "version",
			Hard: []Need{Hard(SectionEtcd)},
			Gather: func(in Input) func(*Input) {
				out, failure := reads.EtcdVersion(in.Etcd.Site)
				return func(in *Input) {
					if failure != "" {
						in.Etcd.VersionErr = failure
					} else {
						in.Etcd.Version = out
					}
				}
			},
			Judge: func(in Input) []Finding { return EtcdVersionFindings(cfg, in) },
		})
	}

	if len(cfg.Cluster.Sites) > 0 {
		var members []string
		for _, site := range cfg.Cluster.Sites {
			if !asked[site] {
				continue
			}
			name := SectionPatroni + " " + site
			members = append(members, name)
			g.Add(Node{Name: name, Section: SectionPatroni, Site: site, Subject: site,
				Hard: []Need{Hard(reach(site))},
				Soft: soft(SectionEtcd),
				Gather: func(Input) func(*Input) {
					out, failure := reads.PatroniCluster(site)
					return func(in *Input) {
						if failure != "" {
							in.Patroni.ClusterErr[site] = failure
						} else {
							in.Patroni.Cluster[site] = out
						}
					}
				},
			})
		}
		g.Add(Node{Name: SectionPatroni, Section: SectionPatroni,
			Hard: []Need{AnyOf(members...)},
			Soft: soft(SectionEtcd),
			// /sync from the etcd member that answered, and, when no
			// member names a leader, the log lines that say why.
			Gather: func(in Input) func(*Input) {
				var sync, syncErr string
				if in.Etcd.Site != "" {
					sync, syncErr = reads.Sync(in.Etcd.Site)
				} else {
					syncErr = "no etcd member answered"
				}
				logs := map[string]string{}
				if !HasLeader(in.Patroni.Cluster) {
					reached := in.Reached()
					for _, site := range cfg.Cluster.Sites {
						if reached[site] {
							logs[site] = reads.PatroniLog(site)
						}
					}
				}
				return func(in *Input) {
					if syncErr != "" {
						in.Patroni.SyncErr = syncErr
					} else {
						in.Patroni.Sync = sync
					}
					for site, out := range logs {
						in.Patroni.Logs[site] = out
					}
				}
			},
			Judge: func(in Input) []Finding { return Patroni(cfg, in) },
		})
	}

	for _, site := range sites {
		g.Add(Node{Name: SectionContainers + " " + site, Section: SectionContainers, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Soft: soft(SectionPatroni),
			Gather: func(Input) func(*Input) {
				c := reads.Containers(site)
				return func(in *Input) { in.Containers = append(in.Containers, c) }
			},
			Judge: func(in Input) []Finding {
				return Containers(d, bySite(in.Containers, site, SiteContainers.site), LeaderName(in.Patroni.Cluster))
			},
		})
	}

	placed := render.AppSites(cfg)
	for _, app := range cfg.AppNames() {
		declared := cfg.Apps[app]
		if _, ok := blueprints.LiveSettingsFor(declared.Blueprint); !ok || len(blueprints.GateSettingsFor(declared)) == 0 {
			continue
		}
		for _, site := range placed[app] {
			if !asked[site] {
				continue
			}
			g.Add(Node{Name: SectionSettings + " " + site + " " + app, Section: SectionSettings, Site: site, Subject: app + " on " + site,
				Hard: []Need{Hard(reach(site))},
				Soft: soft(SectionContainers + " " + site),
				Gather: func(Input) func(*Input) {
					p := reads.Settings(site, app)
					return func(in *Input) { in.Settings = append(in.Settings, p) }
				},
				Judge: func(in Input) []Finding {
					var list []SettingsProbe
					for _, p := range in.Settings {
						if p.Site == site && p.App == app {
							list = append(list, p)
						}
					}
					return Settings(cfg, list)
				},
			})
		}
	}

	for _, app := range apply.StandbyApps(cfg) {
		g.Add(Node{Name: SectionPocketID + " " + app, Section: SectionPocketID, Subject: app,
			Hard: []Need{reachOf(placed[app])},
			Soft: soft(SectionPatroni),
			Gather: func(in Input) func(*Input) {
				p := reads.Standby(app, in.Reached())
				return func(in *Input) { in.Standby = append(in.Standby, p) }
			},
			Judge: func(in Input) []Finding {
				var list []StandbyProbe
				for _, p := range in.Standby {
					if p.App == app {
						list = append(list, p)
					}
				}
				// Only a cluster that answered can be said to have no primary.
				noPrimary := len(in.Patroni.Cluster) > 0 && !HasLeader(in.Patroni.Cluster)
				return Standby(d, list, noPrimary)
			},
		})
	}

	for _, site := range sites {
		g.Add(Node{Name: SectionClocks + " " + site, Section: SectionClocks, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				c := reads.Clock(site)
				return func(in *Input) { in.Clocks = append(in.Clocks, c) }
			},
			Judge: func(in Input) []Finding { return Clocks(bySite(in.Clocks, site, ClockSample.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionLeftovers + " " + site, Section: SectionLeftovers, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Leftovers(site)
				return func(in *Input) { in.Leftovers = append(in.Leftovers, p) }
			},
			Judge: func(in Input) []Finding { return Leftovers(bySite(in.Leftovers, site, LeftoverProbe.site)) },
		})
	}
	if dr, ok := reads.(DNSReads); ok {
		addDNS(g, cfg, sites, dr)
	}
	return g
}

// bySite is the probes in list about site.
func bySite[T any](list []T, site string, of func(T) string) []T {
	var out []T
	for _, p := range list {
		if of(p) == site {
			out = append(out, p)
		}
	}
	return out
}

func (r SiteReach) site() string      { return r.Site }
func (p MeshProbe) site() string      { return p.Site }
func (s SiteContainers) site() string { return s.Site }
func (c ClockSample) site() string    { return c.Site }
func (p LeftoverProbe) site() string  { return p.Site }
