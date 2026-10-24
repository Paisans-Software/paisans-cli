package apply

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// namespaceKeys are the compose keys by which one service runs in another's
// namespace, as "service:<name>".
var namespaceKeys = []string{"pid", "ipc", "network_mode"}

// restartTiers returns the order a restart of every service of a rendered
// compose file must take, nil when no service joins another's namespace and
// one `docker compose restart` will do.
//
// A joined service cannot be restarted with the service it joins. Docker
// gives it the container of that service by ID (container:<id>), so stopping
// the app ends the namespace and the joined container with it, and Compose
// then starts the joined container while the app is still down, which fails
// with "failed to join PID namespace: container ... is not running". Its
// restart policy does not retry a start that failed during a manual restart,
// so it stays exited. Each tier is restarted once the one before it is
// running again: `docker compose restart` returns after the containers it
// names have started.
func restartTiers(content string) ([][]string, error) {
	var doc struct {
		Services map[string]map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	joins, joined := map[string][]string{}, false
	for name, service := range doc.Services {
		for _, key := range namespaceKeys {
			node, ok := service[key]
			if !ok || node.Kind != yaml.ScalarNode {
				continue
			}
			if other, ok := strings.CutPrefix(node.Value, "service:"); ok && other != name {
				joins[name] = append(joins[name], other)
				joined = true
			}
		}
	}
	if !joined {
		return nil, nil
	}
	tier := map[string]int{}
	var depth func(name string, seen map[string]bool) (int, error)
	depth = func(name string, seen map[string]bool) (int, error) {
		if d, ok := tier[name]; ok {
			return d, nil
		}
		if seen[name] {
			return 0, fmt.Errorf("services %s share namespaces in a cycle", name)
		}
		seen[name] = true
		d := 0
		for _, other := range joins[name] {
			od, err := depth(other, seen)
			if err != nil {
				return 0, err
			}
			d = max(d, od+1)
		}
		tier[name] = d
		return d, nil
	}
	var out [][]string
	for name := range doc.Services {
		d, err := depth(name, map[string]bool{})
		if err != nil {
			return nil, err
		}
		for len(out) <= d {
			out = append(out, nil)
		}
		out[d] = append(out[d], name)
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

// orderRestarts sets Tiers on each action that restarts a whole stack whose
// services share a namespace. See restartTiers.
func (p *Plan) orderRestarts() error {
	composes := map[string]Change{}
	for _, c := range p.renderedChanges {
		if isStackCompose(c) {
			composes[c.Stack] = c
		}
	}
	for i, a := range p.Actions {
		c, ok := composes[a.Stack]
		if a.Recreate || len(a.Services) > 0 || !ok {
			continue
		}
		tiers, err := restartTiers(c.content)
		if err != nil {
			return fmt.Errorf("reading which services of %s share a namespace: %w", c.Path, err)
		}
		p.Actions[i].Tiers = tiers
	}
	return nil
}

// RestartStack is the command restarting every service of stack, whose
// compose file reads compose: one `docker compose restart`, or one per tier
// when a service joins another's namespace. See restartTiers.
func RestartStack(d deployment.Deployment, stack, compose string) (string, error) {
	tiers, err := restartTiers(compose)
	if err != nil {
		return "", fmt.Errorf("reading which services of %s share a namespace: %w", d.Compose(stack), err)
	}
	return Action{Stack: stack, Tiers: tiers}.Command(d), nil
}
