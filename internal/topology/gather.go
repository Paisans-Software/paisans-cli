package topology

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/patroni"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// Gather reads a run's evidence: each site's login and, where it answered,
// its admission record; then what the deployment says has joined, each from
// the first site holding it that answers: the gateways' deployment record,
// etcd's membership and Patroni's. The deployment record is Unread when no
// gateway answered or any gateway's could not be read; etcd's membership,
// for etcd's voters, and Patroni's, for the cluster's sites, when none that
// answered could be read and one of them has run it.
func Gather(cfg *config.Config, transports map[string]apply.Transport) Evidence {
	d := cfg.Deployment()
	ev := Evidence{Login: map[string]error{}, Records: map[string]RecordRead{}, Joined: map[string][]string{}}
	for _, name := range cfg.SiteNames() {
		t, ok := transports[name]
		if !ok {
			ev.Login[name] = fmt.Errorf("no way to reach it")
			continue
		}
		if _, err := t.Run("true"); err != nil {
			ev.Login[name] = err
			continue
		}
		ev.Login[name] = nil
		rec, err := admission.Read(t, d.Token())
		ev.Records[name] = RecordRead{Record: rec, Err: err}
	}
	answered := func(sites []string) []string {
		var out []string
		for _, s := range sites {
			if err, ok := ev.Login[s]; ok && err == nil {
				out = append(out, s)
			}
		}
		return out
	}

	// A gateway that does not answer is not asked for its record, and its
	// silence is not counted as unread: Refuse stops any run without every
	// gateway ready, so a site only its record lists is never dropped.
	gateways := map[string]registry.Runner{}
	for _, gw := range answered(cfg.GatewaySites()) {
		gateways[gw] = transports[gw]
	}
	// The deployment record lists every site a gateway's apply has
	// deployed, so it is the one source whose silence about a site is
	// evidence: a run that cannot read it cannot tell a pending site from a
	// down one. A gateway that answered with no record yet is a deployment
	// never applied, which lists nothing.
	if len(cfg.GatewaySites()) > 0 {
		rec, found, missing := deployrecord.Gather(gateways, d)
		var unreadable []string
		for gw, err := range missing {
			if !errors.Is(err, deployrecord.ErrNoRecord) {
				unreadable = append(unreadable, gw+": "+err.Error())
			}
		}
		slices.Sort(unreadable)
		// A record found on one gateway never stands for another's that
		// could not be read: an apply that missed a gateway catches it up
		// later, so the unreadable one may hold the only add of a site.
		if found > 0 {
			ev.Joined["the deployment record"] = rec.List("sites")
			ev.DeployedApps = rec.List("apps")
		}
		switch {
		case len(gateways) == 0:
			ev.Unread = append(ev.Unread, Source{Name: "the deployment record (no gateway answered)"})
		case len(unreadable) > 0:
			ev.Unread = append(ev.Unread, Source{Name: "the deployment record (" + strings.Join(unreadable, "; ") + ")"})
		}
	}

	// etcd's and Patroni's members are read from the first member that
	// answers; one that answers nowhere adds nothing. A member whose etcd
	// errors is read again from its founding record, as apply reads a
	// founding member alone, whose `member list` hangs until a second
	// member joins: a member founded `new` belongs to the cluster its
	// --initial-cluster names, so those are listed as joined. An etcd that
	// answered only with errors is unread for the sites it could list, its
	// voters, whether or not a deployment record was found: the record is
	// written when a gateway is applied, after the data sites, so it lags
	// what etcd lists. An etcd that does not run is an answer, "no etcd
	// here", only on a host that has never run one: on a host with an etcd
	// record it is etcd stopped, or the user's Docker not up after a reboot,
	// and is as unread as an error.
	var etcdFailed []string
	membership := false
	for _, m := range answered(cfg.Etcd.Members) {
		t := transports[m]
		list, running, err := apply.ReadEtcdMembers(t, d)
		if err == nil && running {
			ev.Joined["etcd's membership"] = memberSites(cfg, list)
			membership = true
			break
		}
		in, found, inErr := apply.ReadEtcdInitial(t, d)
		switch {
		case err != nil:
			etcdFailed = append(etcdFailed, m+": "+err.Error())
		case inErr != nil:
			etcdFailed = append(etcdFailed, m+": etcd does not run, and its etcd record cannot be read: "+inErr.Error())
		case found:
			etcdFailed = append(etcdFailed, m+": etcd does not run, though the host has run an etcd member")
		}
		if inErr == nil && found && in.State == render.EtcdStateNew {
			if founders, ok := apply.EtcdInitialMembers(in); ok {
				ev.Joined["etcd's founding record on "+m] = memberSites(cfg, founders)
			}
		}
	}
	if !membership && len(etcdFailed) > 0 {
		ev.Unread = append(ev.Unread, Source{Name: "etcd's membership (" + strings.Join(etcdFailed, "; ") + ")", Sites: slices.Clone(cfg.Etcd.Members)})
	}

	// Patroni lists the cluster's sites, some of which an etcd.members
	// written out may leave out. A Patroni that cannot be read is an
	// answer, "no Patroni here", only on a host never given one, which has
	// no patroni.env; on any other it is unread for the cluster's sites.
	var patroniFailed []string
	patroniRead := false
	for _, s := range answered(cfg.Cluster.Sites) {
		t := transports[s]
		api := fmt.Sprintf("%s:%d", cfg.Sites[s].Address, render.PatroniAPIPort)
		out, err := t.Run(patroni.ClusterCommand(d, api))
		var c patroni.Cluster
		if err == nil {
			c, err = patroni.Parse(out)
		}
		if err != nil {
			_, found, envErr := t.ReadFile(d.Path("infra", "patroni.env"))
			switch {
			case envErr != nil:
				patroniFailed = append(patroniFailed, s+": "+err.Error()+", and whether the host runs Patroni cannot be read: "+envErr.Error())
			case found:
				patroniFailed = append(patroniFailed, s+": "+err.Error())
			}
			continue
		}
		var sites []string
		for _, m := range c.Members {
			sites = append(sites, m.Name)
		}
		slices.Sort(sites)
		ev.Joined["Patroni's members"] = sites
		patroniRead = true
		break
	}
	if !patroniRead && len(patroniFailed) > 0 {
		ev.Unread = append(ev.Unread, Source{Name: "Patroni's members (" + strings.Join(patroniFailed, "; ") + ")", Sites: slices.Clone(cfg.Cluster.Sites)})
	}
	return ev
}

// memberSites names the site each etcd member runs on.
func memberSites(cfg *config.Config, members []apply.EtcdMember) []string {
	var sites []string
	for _, em := range members {
		sites = append(sites, apply.EtcdMemberSite(cfg, em))
	}
	return sites
}
