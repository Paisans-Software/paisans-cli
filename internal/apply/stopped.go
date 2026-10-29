package apply

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ContainersRead lists every compose container on the host, of any project, as
// "<project> <service> <state>". One read covers every stack, so planning a
// site with nothing to do costs one command, not one per stack. `--all`
// because an exited container is exactly what it is for.
const ContainersRead = `docker ps --all --filter label=com.docker.compose.project --format '{{.Label "com.docker.compose.project"}} {{.Label "com.docker.compose.service"}} {{.State}}'`

// downStates are the states `up -d` starts a container from. A restarting
// container is not one: Docker is already starting it, `up -d` would leave it
// as it is, and the health gate is what says why it keeps failing. A paused
// one was paused by somebody, on purpose.
var downStates = map[string]bool{"exited": true, "created": true, "dead": true}

// ComposeServices returns the services a compose file declares.
func ComposeServices(content string) ([]string, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// DeclaredServices is the services each stack this site renders declares, by
// stack, read from its rendered compose file. apply's "left over" section
// compares it with the host's containers, since compose leaves running a
// service its file no longer declares.
func (p *Plan) DeclaredServices() (map[string][]string, error) {
	declared := map[string][]string{}
	for _, c := range p.renderedChanges {
		if !isStackCompose(c) {
			continue
		}
		services, err := ComposeServices(c.content)
		if err != nil {
			return nil, fmt.Errorf("reading the services %s declares: %w", c.Path, err)
		}
		declared[c.Stack] = services
	}
	return declared, nil
}

// probeStopped returns, for each rendered stack with a declared service that
// has no running container, why it is owed an `up -d`. Files that match the
// render are no evidence that a stack runs: a host that rebooted with a
// container whose restart policy gave up, or whose containers and images an
// operator removed to free space, holds every file the last apply wrote, and
// without this read an apply would plan nothing and report the site
// converged with its apps down. Building the plan only reads the host, so a
// dry run shows the start without making it.
func (p *Plan) probeStopped(t Transport) (map[string]string, error) {
	declared, err := p.DeclaredServices()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	out, err := t.Run(ContainersRead)
	if err != nil {
		return nil, fmt.Errorf("reading which containers run: %w\n%s", err, out)
	}
	// seen is every "<project> <service>" with a container in any state, and
	// down each one with a container `up -d` would start.
	seen, down := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		key := fields[0] + " " + fields[1]
		seen[key] = true
		if downStates[fields[2]] {
			down[key] = true
		}
	}
	owed := map[string]string{}
	p.containers = map[string]bool{}
	for stack, services := range declared {
		project := p.Deployment.Project(stack)
		var missing, stopped []string
		for _, service := range services {
			key := project + " " + service
			switch {
			case down[key]:
				stopped = append(stopped, service)
			case !seen[key]:
				missing = append(missing, service)
			default:
				p.containers[stack+"/"+service] = true
			}
		}
		var why []string
		if len(missing) > 0 {
			why = append(why, "no container for "+strings.Join(missing, ", "))
		}
		if len(stopped) > 0 {
			why = append(why, strings.Join(stopped, ", ")+" stopped")
		}
		if len(why) > 0 {
			owed[stack] = strings.Join(why, ", and ") + ", though its files match the render, so `up -d` starts it"
		}
	}
	return owed, nil
}
