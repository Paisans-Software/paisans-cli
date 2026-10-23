package apply

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// patroniFacts is what Build gathers about a site's Patroni from its rendered
// files and the host's.
type patroniFacts struct {
	// file is the rendered patroni.env, empty on a site that runs no Patroni,
	// and onHost whether the host had one before this apply.
	file   string
	onHost bool
	// envRecreate is a change to patroni.env beyond ETCD3_HOSTS, which only a
	// new container reads.
	envRecreate bool
	// composeBefore and composeAfter are the infrastructure compose file on
	// the host and as rendered, set only when it changes.
	composeBefore, composeAfter string
}

// planPatroni decides what this apply does to a running Patroni, which on
// the leader is a failover, and says so in the plan.
//
// Whether Patroni runs comes from probeStopped's read of which containers
// run. One that is stopped or has no container is left to the stack's
// `up -d`, which starts it from the file this apply writes and loses no
// leader. Of a running one, the container is asked which etcd hosts it was
// created with (ETCD3_HOSTS); nothing else in the container is read, so any
// other difference between the file on the host and what the container runs
// is not seen here. A running Patroni that does not answer is planned as one
// that must be recreated, since an `up -d` may replace it.
//
// Patroni is recreated when its configuration changes: patroni.env beyond
// ETCD3_HOSTS, its compose service, or a bind mounted file of its own. It is
// recreated alone too when the hosts it runs with no longer include this
// site's own etcd member (any current member, on a site that is none):
// Patroni reaches etcd from that list when its container starts again
// (docker restart, a reboot, its restart policy) and learns the rest from
// it, and its own member comes up with it. Each such plan warns that a
// standby takes the leader key if the site leads.
//
// Otherwise a running Patroni is left running. Compose replaces a container
// whose env_file content changed, so an `up -d` of the stack would replace
// Patroni whenever its patroni.env holds etcd hosts it was not created with;
// the stack's `up -d` names every other service instead, with --no-deps.
// When the running hosts differ from the file, the plan says so.
func (p *Plan) planPatroni(f patroniFacts, refresh map[string]bool, t Transport) error {
	if f.file == "" || !f.onHost || !p.containers[infraStack+"/patroni"] {
		return nil
	}
	d := p.Deployment
	want := envValue(f.file, "ETCD3_HOSTS")
	i := slices.IndexFunc(p.Actions, func(a Action) bool { return a.Stack == infraStack })

	out, err := t.Run(runningEtcdHosts(d))
	running := strings.TrimSpace(out)
	unread := err != nil || running == ""
	differ := !unread && !sameHosts(running, want)

	must := ""
	switch {
	case unread:
		must = "could not read the etcd hosts Patroni runs with (" + lastLines(strings.TrimSpace(out), 1) + "), so the `up -d` cannot leave it out"
	case f.envRecreate:
		must = "patroni.env changed beyond ETCD3_HOSTS, which Patroni reads only in a new container"
	case f.composeAfter != "" && patroniServiceDiffers(f.composeBefore, f.composeAfter):
		must = "the patroni service in the infrastructure compose file changed"
	case refresh["patroni"]:
		must = "a file Patroni mounts changed"
	case differ && !reachesEtcd(running, want, p.meshAddress):
		must = fmt.Sprintf("Patroni runs with ETCD3_HOSTS=%s, which no longer names this site's own etcd member (patroni.env names %s), so after a restart of its container it could not count on reaching etcd", running, want)
		p.recreatePatroni(&i, must)
	}

	if i >= 0 {
		a := &p.Actions[i]
		switch {
		case a.Down, a.Force && (len(a.Up) == 0 || slices.Contains(a.Up, "patroni")):
			if must == "" {
				must = "the infrastructure stack's action replaces Patroni's container"
			}
		case a.Force:
			// An owed `up -d` that named the other services stays narrowed.
		case a.Recreate && must == "":
			up, err := composeServices(f.composeAfterOr(p))
			if err != nil {
				return fmt.Errorf("reading the services the infrastructure compose file declares: %w", err)
			}
			// A data site runs etcd beside Patroni, so the list is never
			// empty; one that were would name every service, Patroni too.
			a.Up = slices.DeleteFunc(up, func(s string) bool { return s == "patroni" })
			if len(a.Up) == 0 {
				must = "Patroni is the only service of the infrastructure stack"
			} else {
				a.Reason += "; Patroni is left out of the `up -d`, since nothing it reads changed"
			}
		case !a.Recreate && must == "" && (len(a.Services) == 0 || slices.Contains(a.Services, "patroni")):
			must = "the infrastructure stack's restart restarts Patroni"
		}
	}

	if differ && must == "" {
		p.Notes = append(p.Notes, Note{
			Hint: "Patroni on " + p.Site + " runs with other etcd hosts than its patroni.env names",
			Text: fmt.Sprintf("it runs with ETCD3_HOSTS=%s and patroni.env names %s. It asks etcd for the cluster's members as it runs, so it reaches every current one; a restart of its container keeps the list it was created with, which still names this site's own etcd member and so reaches etcd. The next apply that recreates Patroni here takes the file, and its plan says so", running, want),
		})
	}
	if must != "" && i >= 0 {
		p.patroniRecreates = true
		p.Notes = append(p.Notes, Note{
			Hint: "this apply recreates Patroni on " + p.Site + ", so if it leads, a standby takes the leader key",
			Text: fmt.Sprintf("%s (%s). When this site leads, a standby takes the leader key while Patroni restarts, every app's database connections end, and the leader stays on the other site; the apply says so if it does", must, ActionTitle(p.Actions[i])),
		})
	}
	return nil
}

// recreatePatroni makes the infrastructure stack's action, at *i or a new
// one, force-recreate Patroni for why: alone when there is no other action,
// beside the services a restart named, and the whole stack when the action
// was already a whole restart or an `up -d`.
func (p *Plan) recreatePatroni(i *int, why string) {
	if *i < 0 {
		p.Actions = append([]Action{{Stack: infraStack, Recreate: true, Force: true, Up: []string{"patroni"}, Reason: why}}, p.Actions...)
		*i = 0
		return
	}
	a := &p.Actions[*i]
	switch {
	case !a.Recreate && len(a.Services) > 0:
		// A restart keeps the container, and the list with it.
		a.Up = append(slices.Clone(a.Services), "patroni")
		a.Services = nil
	case !a.Force || len(a.Up) > 0:
		a.Up = nil
	}
	a.Recreate, a.Force = true, true
	a.Reason += "; " + why
}

// composeAfterOr is the rendered infrastructure compose file: the changed
// one, or the host's when it is unchanged.
func (f patroniFacts) composeAfterOr(p *Plan) string {
	if f.composeAfter != "" {
		return f.composeAfter
	}
	for _, c := range p.renderedChanges {
		if c.Path == p.Deployment.Path(gatewayCompose) {
			return c.content
		}
	}
	return ""
}

// patroniServiceDiffers reports whether the patroni service, or anything in
// the compose file outside its services, differs between two versions: what
// makes `up -d` recreate Patroni. A file that does not parse counts as
// different.
func patroniServiceDiffers(before, after string) bool {
	var a, b map[string]any
	if yaml.Unmarshal([]byte(before), &a) != nil || yaml.Unmarshal([]byte(after), &b) != nil {
		return true
	}
	keep := func(doc map[string]any) {
		if services, ok := doc["services"].(map[string]any); ok {
			doc["services"] = services["patroni"]
		}
	}
	keep(a)
	keep(b)
	return !reflect.DeepEqual(a, b)
}

// hostSet is a comma separated host list as a set.
func hostSet(list string) map[string]bool {
	out := map[string]bool{}
	for _, h := range strings.Split(list, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out[h] = true
		}
	}
	return out
}

// sameHosts reports whether two host lists name the same hosts, in any
// order.
func sameHosts(a, b string) bool { return reflect.DeepEqual(hostSet(a), hostSet(b)) }

// reachesEtcd reports whether a Patroni running with the hosts running can
// count on reaching etcd when its container starts again: the list names
// this site's own etcd member, at mesh, the site's mesh address with or
// without its prefix length, while that member is current. On a site whose
// own member is not current, any current member will do.
func reachesEtcd(running, want, mesh string) bool {
	own := strings.SplitN(mesh, "/", 2)[0]
	current, have := hostSet(want), hostSet(running)
	for h := range current {
		if own != "" && strings.SplitN(h, ":", 2)[0] == own {
			return have[h]
		}
	}
	for h := range have {
		if current[h] {
			return true
		}
	}
	return false
}
