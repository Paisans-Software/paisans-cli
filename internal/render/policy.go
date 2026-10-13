package render

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A blueprint read from a catalog writes its own compose file, so what that
// file asks of the host is checked before it is written
// (docs/specs/2026-10-12-blueprints-and-catalogs.md, "The grant review"). Each
// refused key is a way for a container to reach past its stack: into the
// host's network, processes or devices, into the Docker daemon, which is
// root on the host, or into files outside the stack's own directory. The
// blueprints built into the toolkit are reviewed with its code and are not
// checked here; the uptime monitor's cap_add is one of them.

// refusedServiceKeys are compose service keys a catalog blueprint may not
// set at all, and why.
var refusedServiceKeys = map[string]string{
	"privileged":    "a privileged container is root on the host",
	"cap_add":       "an added capability reaches past the container",
	"devices":       "a device is the host's hardware",
	"security_opt":  "a security option can turn the container's confinement off",
	"cgroup_parent": "it places the container outside its stack's control group",
}

// hostModes are the namespace keys that may not be "host".
var hostModes = []string{"network_mode", "pid", "ipc", "uts", "userns_mode", "cgroup"}

// checkComposePolicy refuses a catalog blueprint's compose file that asks
// for any of those, or that mounts a host path outside root, the stack's own
// directory on the host, or the Docker socket anywhere.
func checkComposePolicy(content, root string) error {
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return fmt.Errorf("compose.yaml does not parse: %w", err)
	}
	var problems []string
	for _, name := range sortedServiceNames(doc.Services) {
		svc := doc.Services[name]
		for key, why := range refusedServiceKeys {
			if _, ok := svc[key]; ok {
				problems = append(problems, fmt.Sprintf("services.%s.%s: %s", name, key, why))
			}
		}
		for _, key := range hostModes {
			if v, ok := svc[key].(string); ok && (v == "host" || strings.HasPrefix(v, "container:")) {
				problems = append(problems, fmt.Sprintf("services.%s.%s: %q shares a namespace the container should have alone", name, key, v))
			}
		}
		volumes, _ := svc["volumes"].([]any)
		for _, v := range volumes {
			source := ""
			switch m := v.(type) {
			case string:
				source, _, _ = strings.Cut(m, ":")
			case map[string]any:
				if t, _ := m["type"].(string); t == "bind" || t == "" {
					source, _ = m["source"].(string)
				}
			}
			if source == "" || !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, ".") {
				continue // a named volume
			}
			clean := path.Clean(source)
			switch {
			case strings.Contains(clean, "docker.sock"):
				problems = append(problems, fmt.Sprintf("services.%s.volumes: %s is the Docker socket, which is root on the host", name, source))
			case strings.HasPrefix(source, "/") && clean != root && !strings.HasPrefix(clean, root+"/"):
				problems = append(problems, fmt.Sprintf("services.%s.volumes: %s is outside the stack's own directory, %s", name, source, root))
			case strings.HasPrefix(source, ".") && strings.HasPrefix(clean, ".."):
				problems = append(problems, fmt.Sprintf("services.%s.volumes: %s leaves the stack's own directory", name, source))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("its compose.yaml asks for what a blueprint from a catalog may not:\n  %s", strings.Join(problems, "\n  "))
}

func sortedServiceNames(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
