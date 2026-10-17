package siteremove

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// BuildForced plans `site remove --force`: the host at dest cleaned of this
// deployment, and nothing else. No other site is reached, the cluster's
// refusals are not checked, and neither paisans-stack.yaml nor the secrets file is
// edited, because --force is for a host the cluster cannot be asked about or
// no longer counts on (docs/specs/2026-10-09-site-remove-force.md).
//
// dest may be any host. What is removed is only what the host proves is this
// deployment's, by its id or its token, so a host it never used plans
// nothing.
//
// The host stage runs against a copy of the configuration in which the site
// is declared with dest as its ssh destination and the roles its admission
// record on the host names. Those are what the host was deployed with, which
// is what cleaning it needs, for an undeclared site and for a host the site
// has left alike.
func BuildForced(cfg *config.Config, secrets *config.Secrets, site string, dest config.Destination, t apply.Transport, o Options) (*Plan, error) {
	if o.HostGone {
		return nil, fmt.Errorf("site remove %s: --force cleans one host, and --host-gone reaches none. Drop one of them", site)
	}
	declared, isDeclared := cfg.Sites[site]
	p := &Plan{Site: site, Options: o, secrets: secrets, transports: map[string]apply.Transport{site: t}, forced: true, declared: isDeclared}
	p.Current = isDeclared && declared.Destination() == dest

	if err := answers(site, dest, t); err != nil {
		return nil, err
	}
	s := declared
	s.SSH.User, s.SSH.Host, s.SSH.Port = dest.User, dest.Host, dest.Port
	rec, err := admission.Read(t, cfg.Deployment().Token())
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
		// Nothing of this deployment's is admitted here: the host stage
		// plans what the host still holds, and refuses the login if it
		// cannot say whose host this is.
	case err != nil:
		return nil, fmt.Errorf("site remove %s: %s: %w. Nothing was changed", site, dest, err)
	default:
		// The record names the site the host was admitted as, which is the
		// proof of whose host this is, whatever --ssh spelled.
		if rec.Site != site {
			if _, live := cfg.Sites[rec.Site]; live {
				return nil, fmt.Errorf("site remove %s: %s is %s's host, which "+config.FileName+" still declares, by this deployment's admission record on it. Nothing was changed. To clean it, name that site: paisans site remove %s --force", site, dest, rec.Site, rec.Site)
			}
		}
		p.Current = isDeclared && rec.Site == site
		s.Roles = recordRoles(rec)
	}
	forcedCfg := *cfg
	forcedCfg.Sites = map[string]config.Site{}
	for name, v := range cfg.Sites {
		forcedCfg.Sites[name] = v
	}
	forcedCfg.Sites[site] = s
	p.cfg = &forcedCfg

	host, err := p.buildHost()
	if err != nil {
		return nil, err
	}
	if len(host.Steps) > 0 {
		p.notePocketID(host)
	}
	if p.Current && len(host.Steps) > 0 {
		host.Steps = append([]Step{{Site: site, Verb: "note", Title: "note the cluster", Text: fmt.Sprintf("%s is still declared and this is its host: its etcd member, Patroni replica and Garage node, and its place in every other site's mesh, stay in the cluster until a full `paisans site remove %s`, and its next `paisans apply --site %s` deploys it again", site, site, site)}}, host.Steps...)
	}
	p.Stages = []*Stage{host}
	// A site paisans-stack.yaml still declares keeps its records, which are still
	// wanted. One it does not declare is removed from it already.
	if !isDeclared {
		dnsSt, err := p.buildDNS(cfg, cfg.WithoutSite(site))
		if err != nil {
			return nil, err
		}
		p.Stages = append(p.Stages, dnsSt)
	}
	return p, nil
}

// answers checks that dest answers over ssh before anything on it is read.
func answers(what string, dest config.Destination, t apply.Transport) error {
	if out, err := t.Run("true"); err != nil {
		if errors.Is(err, apply.ErrUnreachable) {
			return fmt.Errorf("site remove %s: %s does not answer over ssh (%v), so what is on it cannot be read or cleaned. Fix ssh and run again", what, dest, err)
		}
		return fmt.Errorf("site remove %s: %s answered `true` with an error: %v: %s", what, dest, err, lastLines(out, 2))
	}
	return nil
}

// recordRoles is the roles an admission record names, as a site holds them.
func recordRoles(rec admission.Record) []config.Role {
	var roles []config.Role
	for _, r := range rec.Roles {
		roles = append(roles, config.Role(r))
	}
	return roles
}

// BuildForcedByID plans `site remove --force --id`: the host at dest cleaned
// of the deployment ref names, a full id or a token, with no
// paisans-stack.yaml and no secrets file (docs/specs/2026-10-10-remove-without-
// config.md).
//
// The deployment, its site and its roles are the login's own admission
// record's: the login is the deployment's user, who cannot read the
// registry, which is root's. A login with no record of its own is planned
// from --id's record, if the host holds one, so that the host stage refuses
// it naming the user to log in as.
//
// The host stage reads the configuration for the id, the site's ssh user and
// destination, and the site's roles, which the record and dest hold. So the
// plan runs against a configuration of just those, and is the forced
// removal's own host stage, without the Pocket ID note, which reads the
// configuration's apps. A gateway's kept Caddy is reduced from the
// Caddyfile on the host, as with a configuration. The remains say what was
// not read.
func BuildForcedByID(dest config.Destination, t apply.Transport, ref string, o Options) (*Plan, error) {
	what := "--id " + ref
	if o.HostGone {
		return nil, fmt.Errorf("site remove %s: --force cleans one host, and --host-gone reaches none. Drop one of them", what)
	}
	if err := answers(what, dest, t); err != nil {
		return nil, err
	}
	rec, err := admission.Mine(t)
	if errors.Is(err, admission.ErrNotMine) {
		rec, err = byRef(dest, t, ref, err)
	}
	if err != nil {
		return nil, fmt.Errorf("site remove %s: %w", what, err)
	}
	d := deployment.Deployment{ID: rec.Deployment}
	if ref != d.ID && ref != d.Token() {
		return nil, fmt.Errorf("site remove --id %s: this login's admission record on %s is for deployment %s (%s). Log in as that deployment's user to clean it. Nothing was changed", ref, dest, d.ID, rec.Site)
	}

	s := config.Site{Roles: recordRoles(rec)}
	s.SSH.User, s.SSH.Host, s.SSH.Port = dest.User, dest.Host, dest.Port
	cfg := &config.Config{ID: d.ID, Sites: map[string]config.Site{rec.Site: s}}
	p := &Plan{Site: rec.Site, Options: o, transports: map[string]apply.Transport{rec.Site: t}, forced: true, byID: true, dest: dest, cfg: cfg}
	host, err := p.buildHost()
	if err != nil {
		return nil, err
	}
	p.Stages = []*Stage{host}
	return p, nil
}

// byRef is the admission record ref names, for a login with none of its own
// (notMine says so): the host stage then refuses the login, naming the user
// the record names. With no record for ref either, nothing says whose host
// this is.
func byRef(dest config.Destination, t apply.Transport, ref string, notMine error) (admission.Record, error) {
	token, evictID := ref, "<the deployment's id>"
	if deployment.ValidID(ref) {
		token, evictID = deployment.Deployment{ID: ref}.Token(), ref
	}
	rec, err := admission.Read(t, token)
	switch {
	case errors.Is(err, admission.ErrNotAdmitted):
		return rec, fmt.Errorf("%s: %v, and none is for %s, so nothing says who the deployment's user is, and site remove cleans a host only as that user. Nothing was changed. If the deployment's user is still there, the host's owner removes it and everything root's with sudo paisans site evict --deployment %s --user <the deployment's user> --execute", dest, notMine, ref, evictID)
	case err != nil:
		return rec, fmt.Errorf("%s: %w. Nothing was changed", dest, err)
	}
	return rec, nil
}

// Confirmation is what --execute says before it asks for the site's name:
// what is cleaned off dest, and what goes with it that the plan alone shows,
// then, with --delete-data, the sentence saying the data is deleted, apart so
// that it can be set off.
func (p *Plan) Confirmation(dest config.Destination) (what, deletes string) {
	deployed := p.cfg.ID
	if p.cfg.Community.Domain != "" {
		deployed += " (" + p.cfg.Community.Domain + ")"
	}
	out := fmt.Sprintf("This cleans deployment %s, site %s, off %s, and nothing says whether it still runs.", deployed, p.Site, dest)
	if len(p.CaddyKept) > 0 {
		out += fmt.Sprintf(" Its Caddy is kept, reduced to the host owner's sites in %s: %s.", render.HostSitesDir(p.dep()), strings.Join(p.CaddyKept, ", "))
	}
	if p.DeleteData {
		deletes = fmt.Sprintf("This permanently deletes this deployment's data on %s.", dest)
	}
	return out, deletes
}

// DeploymentID is the id of the deployment the plan removes.
func (p *Plan) DeploymentID() string { return p.cfg.ID }
