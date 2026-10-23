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
// It asks the running container which etcd hosts it was created with. No
// answer means Patroni does not run here, and an `up -d` starts it from the
// file this apply writes, which loses no leader.
//
// Patroni is recreated when its configuration changes: patroni.env beyond
// ETCD3_HOSTS, its compose service, or a bind mounted file of its own. It is
// recreated too when the hosts it runs with name no current etcd member,
// since Patroni reaches etcd from that list when its container starts again
// (docker restart, a reboot, its restart policy) and from it learns the
// rest: a list with one current member is enough, and one with none would
// leave it unable to reach etcd at all. Each such plan warns that a standby
// takes the leader key if the site leads.
//
// Otherwise a running Patroni is left running. Compose replaces a container
// whose env_file content changed, so an `up -d` of the stack would replace
// Patroni whenever its patroni.env holds new etcd hosts it was not created
// with; the stack's `up -d` names every other service instead, with
// --no-deps. When the running hosts differ from the file, the plan says so.
func (p *Plan) planPatroni(f patroniFacts, refresh map[string]bool, t Transport) error {
	if f.file == "" || !f.onHost {
		return nil
	}
	d := p.Deployment
	out, err := t.Run(runningEtcdHosts(d))
	running := strings.TrimSpace(out)
	if err != nil || running == "" {
		return nil
	}
	want := envValue(f.file, "ETCD3_HOSTS")
	i := slices.IndexFunc(p.Actions, func(a Action) bool { return a.Stack == infraStack })

	must := ""
	switch {
	case f.envRecreate:
		must = "patroni.env changed beyond ETCD3_HOSTS, which Patroni reads only in a new container"
	case f.composeAfter != "" && patroniServiceDiffers(f.composeBefore, f.composeAfter):
		must = "the patroni service in the infrastructure compose file changed"
	case refresh["patroni"]:
		must = "a file Patroni mounts changed"
	case running != want && !sharesHost(running, want):
		must = fmt.Sprintf("Patroni runs with ETCD3_HOSTS=%s, which names no current etcd member (patroni.env names %s), so after a restart of its container it could not reach etcd", running, want)
		if i < 0 {
			p.Actions = append([]Action{{Stack: infraStack, Recreate: true, Up: []string{"patroni"}, Reason: must}}, p.Actions...)
			i = 0
		} else {
			a := &p.Actions[i]
			if !a.Recreate {
				// A restart keeps the container, and the list with it.
				a.Recreate, a.Refresh, a.Services = true, a.Services, nil
				if len(a.Refresh) == 0 {
					a.Force = true
				}
			}
			a.Reason += "; " + must
		}
	}

	if i >= 0 {
		a := &p.Actions[i]
		switch {
		case a.Force, a.Down:
			must = "the infrastructure stack's action replaces every container"
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

	if running != want && must == "" {
		p.Notes = append(p.Notes, Note{
			Hint: "Patroni on " + p.Site + " runs with other etcd hosts than its patroni.env names",
			Text: fmt.Sprintf("it runs with ETCD3_HOSTS=%s and patroni.env names %s. It asks etcd for the cluster's members as it runs, so it reaches every current one; a restart of its container keeps the list it was created with, which still names a current member and is enough to reach etcd. The next apply that recreates Patroni here takes the file, and its plan says so", running, want),
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

// sharesHost reports whether two comma separated host lists have a host in
// common.
func sharesHost(running, want string) bool {
	current := map[string]bool{}
	for _, h := range strings.Split(want, ",") {
		current[strings.TrimSpace(h)] = true
	}
	for _, h := range strings.Split(running, ",") {
		if current[strings.TrimSpace(h)] {
			return true
		}
	}
	return false
}
