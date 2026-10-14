package blueprints

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServiceMounts is what one compose service mounts, and from which image.
type ServiceMounts struct {
	Image string
	// Targets are the container paths something persistent or deliberate is
	// mounted at: a bind, a named volume or a tmpfs. An anonymous volume the
	// file itself asks for (a bare `- /path`) is not one, since it leaks
	// exactly like the image's own.
	Targets []string
}

// ComposeMounts reads every service of a rendered compose file: its image,
// and the paths its volumes and tmpfs entries mount. Both compose syntaxes
// are read, the short `source:target[:mode]` string and the long mapping.
func ComposeMounts(content string) (map[string]ServiceMounts, error) {
	var doc struct {
		Services map[string]struct {
			Image   string      `yaml:"image"`
			Volumes []yaml.Node `yaml:"volumes"`
			Tmpfs   yaml.Node   `yaml:"tmpfs"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	out := map[string]ServiceMounts{}
	for name, service := range doc.Services {
		m := ServiceMounts{Image: service.Image}
		for _, v := range service.Volumes {
			target, ok, err := volumeTarget(v)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", name, err)
			}
			if ok {
				m.Targets = append(m.Targets, target)
			}
		}
		var tmpfs []string
		switch service.Tmpfs.Kind {
		case 0:
		case yaml.ScalarNode:
			tmpfs = []string{service.Tmpfs.Value}
		default:
			if err := service.Tmpfs.Decode(&tmpfs); err != nil {
				return nil, fmt.Errorf("service %s: tmpfs: %w", name, err)
			}
		}
		for _, entry := range tmpfs {
			target, _, _ := strings.Cut(entry, ":")
			m.Targets = append(m.Targets, target)
		}
		out[name] = m
	}
	return out, nil
}

// volumeTarget is the container path of one volumes entry, false for an
// anonymous volume.
func volumeTarget(v yaml.Node) (string, bool, error) {
	if v.Kind == yaml.ScalarNode {
		parts := strings.Split(v.Value, ":")
		if len(parts) == 1 {
			return "", false, nil
		}
		return parts[1], true, nil
	}
	var long struct {
		Type   string `yaml:"type"`
		Source string `yaml:"source"`
		Target string `yaml:"target"`
	}
	if err := v.Decode(&long); err != nil {
		return "", false, fmt.Errorf("volumes: %w", err)
	}
	if long.Type != "tmpfs" && long.Source == "" {
		return "", false, nil
	}
	return long.Target, true, nil
}

// Uncovered returns the declared volume paths no target mounts, cleaned and
// sorted.
//
// Only a mount at exactly the declared path counts. Docker skips creating an
// anonymous volume for a declared path only when the container already has a
// mount there; a bind at a parent leaves one at the declared path anyway,
// beneath it, which was seen on Docker 29.7.2 with a bind at /app and a
// declared /app/var/. A trailing slash is not a difference: Docker cleans
// the declared path, so /app/var/ is covered by a mount at /app/var.
func Uncovered(declared, targets []string) []string {
	have := map[string]bool{}
	for _, t := range targets {
		have[path.Clean(t)] = true
	}
	var out []string
	for _, d := range declared {
		if d = path.Clean(d); !have[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
