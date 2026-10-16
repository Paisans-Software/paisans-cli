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
// gateway answered or any gateway's could not be read, and etcd's membership
// when no record was found and etcd answered only with errors.
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
			ev.Unread = append(ev.Unread, "the deployment record (no gateway answered)")
		case len(unreadable) > 0:
			ev.Unread = append(ev.Unread, "the deployment record ("+strings.Join(unreadable, "; ")+")")
		}
	}

	// etcd's and Patroni's members are read from the first member that
	// answers; one that answers nowhere adds nothing. With no deployment
	// record found, etcd is the evidence of a site a first apply joined
	// before the gateway recorded it, since the data sites and the witness
	// are applied before the gateway: an etcd that answered only with errors
	// is then unread.
	var etcdFailed []string
	for _, m := range answered(cfg.Etcd.Members) {
		list, running, err := apply.ReadEtcdMembers(transports[m], d)
		if err != nil {
			etcdFailed = append(etcdFailed, m+": "+err.Error())
			continue
		}
		if !running {
			continue
		}
		var sites []string
		for _, em := range list {
			sites = append(sites, apply.EtcdMemberSite(cfg, em))
		}
		ev.Joined["etcd's membership"] = sites
		break
	}
	_, recorded := ev.Joined["the deployment record"]
	_, membership := ev.Joined["etcd's membership"]
	if !recorded && !membership && len(etcdFailed) > 0 {
		ev.Unread = append(ev.Unread, "etcd's membership ("+strings.Join(etcdFailed, "; ")+")")
	}

	for _, s := range answered(cfg.Cluster.Sites) {
		api := fmt.Sprintf("%s:%d", cfg.Sites[s].Address, render.PatroniAPIPort)
		out, err := transports[s].Run(patroni.ClusterCommand(d, api))
		if err != nil {
			continue
		}
		c, err := patroni.Parse(out)
		if err != nil {
			continue
		}
		var sites []string
		for _, m := range c.Members {
			sites = append(sites, m.Name)
		}
		slices.Sort(sites)
		ev.Joined["Patroni's members"] = sites
		break
	}
	return ev
}
