package doctor

import (
	"fmt"
	"slices"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/topology"
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
	// Admission reads the site's admission record, as the login user.
	Admission(site string) AdmissionProbe
	// User runs UserCommand on the site.
	User(site string) UserProbe
	// Docker runs DockerCommand on the site.
	Docker(site string) DockerProbe
	// Linger runs LingerCommand on the site.
	Linger(site string) LingerProbe
	// Sudo runs SudoCommand on the site.
	Sudo(site string) SudoProbe
	// Memory runs MemoryCommand on the site.
	Memory(site string) MemoryProbe
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
	// is active, of the sites in admitted alone.
	Standby(app string, admitted map[string]bool) StandbyProbe
	// Clock reads the site's clock, with the workstation's on either side.
	Clock(site string) ClockSample
	// Leftovers takes the site's inventory and classifies it.
	Leftovers(site string) LeftoverProbe
	// SelfCheck asks a site running the toolkit's Caddy, from the
	// workstation, through its public address, whether it answers as this
	// deployment, as apply asks the gateway.
	SelfCheck(site string) SelfCheckProbe
}

// Checks is every check doctor makes on sites, the ones it asks, as a
// graph: each site's reach first, then its admission record, which every
// other read of the site needs, since nothing is asked of a pending site;
// then the deployment's user, its rootless Docker, which every check of
// that site alone that runs docker there needs, linger and sudo; then from
// the bottom of the stack up, the order Diagnose reports in. A check that
// reads across sites (etcd, Patroni's /sync and logs, Pocket ID's
// instances) needs the admission of any of its sites, and asks only those
// in Input.Admitted; it does not need their Docker, so a stopped daemon on
// the member it asks is that check's own failure rather than a prune.
// Each check's judge is the section function Diagnose calls, given the
// probes of the check's own subject, so the graph's findings for a
// complete Input are Diagnose's.
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
	// admissionOf is a need on any of the sites' admission checks: a check
	// that reads across sites waits for every one of them, and asks only
	// the sites in Input.Admitted.
	admissionOf := func(list []string) Need {
		var names []string
		for _, s := range list {
			names = append(names, SectionAdmission+" "+s)
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
	admitted := func(site string) string { return SectionAdmission + " " + site }
	docker := func(site string) string { return SectionDocker + " " + site }
	for _, site := range sites {
		g.Add(Node{Name: admitted(site), Section: SectionAdmission, Site: site, Subject: site,
			Hard: []Need{Hard(reach(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Admission(site)
				return func(in *Input) { in.Admission = append(in.Admission, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Admission, site, AdmissionProbe.site) {
					if p.Pending() {
						return Failed
					}
				}
				return Passed
			},
			Judge: func(in Input) []Finding { return Admission(cfg, bySite(in.Admission, site, AdmissionProbe.site)) },
		})
	}
	var ended []string
	for _, site := range sites {
		ended = append(ended, reach(site), admitted(site))
	}
	g.Add(Node{Name: classification, Section: SectionAdmission, Soft: ended,
		Gather: func(Input) func(*Input) {
			cr, ok := reads.(ClassReads)
			if !ok {
				return nil
			}
			c := cr.Classification()
			if c == nil {
				return nil
			}
			var pending, down []string
			for _, name := range cfg.SiteNames() {
				switch c.Sites[name].Class {
				case topology.Pending:
					pending = append(pending, name)
				case topology.Down:
					down = append(down, name)
				}
			}
			return func(in *Input) { in.Pending, in.Down = pending, down }
		},
	})
	for _, site := range sites {
		g.Add(Node{Name: SectionUser + " " + site, Section: SectionUser, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.User(site)
				return func(in *Input) { in.Users = append(in.Users, p) }
			},
			Judge: func(in Input) []Finding { return User(bySite(in.Users, site, UserProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: docker(site), Section: SectionDocker, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Docker(site)
				return func(in *Input) { in.Docker = append(in.Docker, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Docker, site, DockerProbe.site) {
					if p.Answered() {
						return Passed
					}
				}
				return Failed
			},
			Judge: func(in Input) []Finding { return DockerSocket(bySite(in.Docker, site, DockerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionDocker + " engine " + site, Section: SectionDocker, Site: site, Subject: site,
			Hard:  []Need{Hard(docker(site))},
			Judge: func(in Input) []Finding { return DockerEngine(bySite(in.Docker, site, DockerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionDocker + " source mark " + site, Section: SectionDocker, Site: site, Subject: site,
			Hard:  []Need{Hard(docker(site))},
			Judge: func(in Input) []Finding { return SourceMark(bySite(in.Docker, site, DockerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionLinger + " " + site, Section: SectionLinger, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Linger(site)
				return func(in *Input) { in.Linger = append(in.Linger, p) }
			},
			Judge: func(in Input) []Finding { return Linger(d, bySite(in.Linger, site, LingerProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionSudo + " " + site, Section: SectionSudo, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Sudo(site)
				return func(in *Input) { in.SudoRights = append(in.SudoRights, p) }
			},
			Judge: func(in Input) []Finding { return SudoRights(cfg, bySite(in.SudoRights, site, SudoProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionSwap + " " + site, Section: SectionSwap, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Memory(site)
				return func(in *Input) { in.Memory = append(in.Memory, p) }
			},
			Outcome: func(in Input) Outcome {
				for _, p := range bySite(in.Memory, site, MemoryProbe.site) {
					if p.Answered() {
						return Passed
					}
				}
				return Failed
			},
			Judge: func(in Input) []Finding { return Swap(bySite(in.Memory, site, MemoryProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionMemory + " " + site, Section: SectionMemory, Site: site, Subject: site,
			Hard:  []Need{Hard(SectionSwap + " " + site)},
			Judge: func(in Input) []Finding { return Memory(cfg, bySite(in.Memory, site, MemoryProbe.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionMesh + " " + site, Section: SectionMesh, Site: site, Subject: site,
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Mesh(site)
				return func(in *Input) { in.Mesh = append(in.Mesh, p) }
			},
			Judge: func(in Input) []Finding { return Mesh(cfg, bySite(in.Mesh, site, MeshProbe.site)) },
		})
	}

	if len(cfg.Etcd.Members) > 0 {
		g.Add(Node{Name: SectionEtcd, Section: SectionEtcd,
			Hard: []Need{admissionOf(cfg.Etcd.Members)},
			Soft: soft(classification),
			// etcd, from the first admitted member that answers, in the
			// order etcd.members gives.
			Gather: func(in Input) func(*Input) {
				reached, admitted := in.Reached(), in.Admitted(cfg)
				var p EtcdProbe
				for _, name := range cfg.Etcd.Members {
					if !admitted[name] {
						if reached[name] {
							p.Tried = append(p.Tried, name+": "+in.notAsked(name))
						}
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
				Hard: []Need{Hard(docker(site))},
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
			Soft: soft(SectionEtcd, classification),
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
					admitted := in.Admitted(cfg)
					for _, site := range cfg.Cluster.Sites {
						if admitted[site] {
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
			Hard: []Need{Hard(docker(site))},
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
				Hard: []Need{Hard(docker(site))},
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
			Hard: []Need{admissionOf(placed[app])},
			Soft: soft(SectionPatroni, classification),
			Gather: func(in Input) func(*Input) {
				reached, admitted := in.Reached(), in.Admitted(cfg)
				p := reads.Standby(app, admitted)
				for i, inst := range p.Instances {
					if inst.State == apply.Unreachable && reached[inst.Site] && !admitted[inst.Site] {
						p.Instances[i].Detail = in.notAsked(inst.Site)
					}
				}
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
			Hard: []Need{Hard(admitted(site))},
			Gather: func(Input) func(*Input) {
				c := reads.Clock(site)
				return func(in *Input) { in.Clocks = append(in.Clocks, c) }
			},
			Judge: func(in Input) []Finding { return Clocks(bySite(in.Clocks, site, ClockSample.site)) },
		})
	}
	for _, site := range sites {
		g.Add(Node{Name: SectionLeftovers + " " + site, Section: SectionLeftovers, Site: site, Subject: site,
			Hard: []Need{Hard(docker(site))},
			Gather: func(Input) func(*Input) {
				p := reads.Leftovers(site)
				return func(in *Input) { in.Leftovers = append(in.Leftovers, p) }
			},
			Judge: func(in Input) []Finding { return Leftovers(bySite(in.Leftovers, site, LeftoverProbe.site)) },
		})
	}
	// Each site running the toolkit's Caddy, asked from outside: it needs
	// no ssh, so one whose ssh does not answer is still asked. It follows
	// the classification: a pending site was never deployed and is not
	// asked, and a down one is asked with its absence in mind.
	for _, site := range sites {
		if !cfg.Sites[site].RunsCaddy() {
			continue
		}
		g.Add(Node{Name: SectionSelfCheck + " " + site, Section: SectionSelfCheck, Site: site, Subject: site,
			Soft: soft(classification),
			Gather: func(in Input) func(*Input) {
				var p SelfCheckProbe
				if slices.Contains(in.Pending, site) {
					p = SelfCheckProbe{Site: site, Pending: true}
				} else {
					p = reads.SelfCheck(site)
					p.Down = slices.Contains(in.Down, site)
				}
				return func(in *Input) { in.SelfChecks = append(in.SelfChecks, p) }
			},
			Judge: func(in Input) []Finding { return SelfChecks(cfg, bySite(in.SelfChecks, site, SelfCheckProbe.site)) },
		})
	}
	if dr, ok := reads.(DNSReads); ok {
		addDNS(g, cfg, sites, dr)
	}
	return g
}

// notAdmitted and notClassified are why a check that reads across sites
// did not ask a site that answered ssh.
const (
	notAdmitted   = "not asked: its admission did not pass (see admission)"
	notClassified = "not asked: the run's classification found it pending or down, as apply would"
)

// ClassReads is a Reads that holds the run's classification of its sites,
// once one was made: cmd/paisans makes it the first time a site does not
// answer or its record does not cover it. Classification never makes one;
// nil is none made.
type ClassReads interface {
	Classification() *topology.Classification
}

// classification is the node that records the run's classification on the
// Input, once every site's reach and admission have ended, so that every
// check that reads across sites sees it whatever order those ran in. It
// reads nothing from a host and reports no line of its own.
const classification = "classification"

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
