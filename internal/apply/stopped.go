package apply

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ContainersRead lists every compose container on the host, of any project, as
// "<project> <service> <state> <status>". One read covers every stack, so
// planning a site with nothing to do costs one command, not one per stack.
// `--all` because an exited container is exactly what it is for. The status,
// Eg: "Exited (0) 3 minutes ago", is the one place `docker ps` prints an exit
// code, which a finished one shot is told apart by.
const ContainersRead = `docker ps --all --filter label=com.docker.compose.project --format '{{.Label "com.docker.compose.project"}} {{.Label "com.docker.compose.service"}} {{.State}} {{.Status}}'`

// exitedZero matches the status of a container that exited with code 0.
var exitedZero = regexp.MustCompile(`^Exited \(0\)( |$)`)

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

// composeRestartPolicies returns each service's restart key in a rendered
// compose file, "" for a service that sets none.
func composeRestartPolicies(content string) (map[string]string, error) {
	var doc struct {
		Services map[string]struct {
			Restart string `yaml:"restart"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(doc.Services))
	for name, svc := range doc.Services {
		out[name] = svc.Restart
	}
	return out, nil
}

// probeStopped returns, for each rendered stack with a declared service that
// has no running container, why it is owed an `up -d`. Files that match the
// render are no evidence that a stack runs: a host that rebooted with a
// container whose restart policy gave up, or whose containers and images an
// operator removed to free space, holds every file the last apply wrote, and
// without this read an apply would plan nothing and report the site
// converged with its apps down. Building the plan only reads the host, so a
// dry run shows the start without making it.
//
// A one shot, a service whose compose sets restart "no", that exited with
// code 0 is satisfied: it finished, and an `up -d` would only run it again.
// Exited with any other code, created, dead or missing, it is owed one like
// any other service.
func (p *Plan) probeStopped(t Transport) (map[string]string, error) {
	declared := map[string][]string{}
	oneShot := map[string]bool{}
	for _, c := range p.renderedChanges {
		if c.Stack == "" || !isStackCompose(c) {
			continue
		}
		services, err := ComposeServices(c.content)
		if err != nil {
			return nil, fmt.Errorf("reading the services %s declares: %w", c.Path, err)
		}
		declared[c.Stack] = services
		policies, err := composeRestartPolicies(c.content)
		if err != nil {
			return nil, fmt.Errorf("reading the restart policies %s declares: %w", c.Path, err)
		}
		for service, policy := range policies {
			if policy == "no" {
				oneShot[c.Stack+"/"+service] = true
			}
		}
	}
	if len(declared) == 0 {
		return nil, nil
	}
	out, err := t.Run(ContainersRead)
	if err != nil {
		return nil, fmt.Errorf("reading which containers run: %w\n%s", err, out)
	}
	// seen is every "<project> <service>" with a container in any state,
	// down each one with a container `up -d` would start, and finished each
	// one that exited with code 0.
	seen, down, finished := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 4)
		if len(fields) < 3 {
			continue
		}
		key := fields[0] + " " + fields[1]
		seen[key] = true
		if downStates[fields[2]] {
			down[key] = true
		}
		if fields[2] == "exited" && len(fields) == 4 && exitedZero.MatchString(fields[3]) {
			finished[key] = true
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
			case down[key] && finished[key] && oneShot[stack+"/"+service]:
				p.containers[stack+"/"+service] = true
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
