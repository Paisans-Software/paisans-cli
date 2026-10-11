package webserver

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// A server in a container reads the include directory only through a mount,
// and a container keeps the mounts it was created with: a line added to its
// compose file takes effect once the container is recreated, and a restart
// does not do that. When the mount is missing, DiagnoseMount reads the
// compose file that started the container, as its labels name it, and says
// which of the usual mistakes it is. It reads only: the compose file and
// the container are the operator's, so it never edits, recreates or
// restarts either.

// MountCase is what the server's compose file says about the mount its
// container lacks.
type MountCase int

const (
	// MountNotDeclared is a compose file that does not declare it, or one
	// that could not be read: the line to add.
	MountNotDeclared MountCase = iota
	// MountNotRecreated is a compose file that declares it on the server's
	// service, for a container created before the line was added.
	MountNotRecreated
	// MountOnOtherService is a compose file that declares it on another
	// service.
	MountOnOtherService
	// MountPathDiffers is a compose file that declares it on the server's
	// service, with a path that is not the one the server reads.
	MountPathDiffers
)

// MountDiagnosis is what DiagnoseMount found, and what to tell the operator.
type MountDiagnosis struct {
	Case MountCase
	Text string
}

// composeProject is what a container's compose labels and files say.
type composeProject struct {
	project, service, workingDir string
	files                        []string
	// sudo is whether docker needed sudo to answer.
	sudo    bool
	volumes []volume
}

// volume is one of a service's volumes as its compose file declares it:
// source resolved against the project's directory, and variable set when
// either path holds a variable, compared as written.
type volume struct {
	file, service    string
	source, target   string
	written          string
	variable, isBind bool
}

// DiagnoseMount says why a server in a container lacks the mount of the
// include directory: dir, the include_dir paisans.yaml names, or the
// directory DeriveInclude derives when dir is empty. name is the file apply
// writes there. Without a compose file to read it is the advice
// DeriveInclude or IncludeAt gives.
func DiagnoseMount(h Host, ws WebServer, dir, name string) MountDiagnosis {
	need := Mount{Destination: path.Join(path.Dir(ws.Main), IncludeName)}
	var why string
	if dir != "" {
		_, why = IncludeAt(ws, dir)
		need.Source = dir
	} else {
		_, why = DeriveInclude(ws)
		need.Source = "/path/on/the/host/" + IncludeName
		if ws.Config != "" {
			need.Source = path.Join(path.Dir(ws.Config), IncludeName)
		}
	}
	// The line including the directory where the server would see it: for
	// an include_dir that is wherever its mount puts it, not known yet.
	imported, note := "", ""
	if dir == "" {
		imported = wildcardLine(h, ws, need.Destination, name)
	}
	if imported != "" {
		note = fmt.Sprintf("Until the mount exists, %s's %s matches nothing, which is harmless.", ws.Server, imported)
	}
	p, unread := readCompose(h, ws.Container)
	if p == nil {
		return MountDiagnosis{MountNotDeclared, join(why, unread, note)}
	}
	fixed := dir != ""
	// right is the volume a server reads the directory through.
	right := func(v volume) bool {
		if fixed {
			_, ok := under(need.Source, v.source)
			return ok
		}
		return v.target == need.Destination
	}
	related := func(v volume) bool {
		return right(v) || v.source == need.Source || v.target == need.Destination ||
			path.Base(v.source) == IncludeName || path.Base(v.target) == IncludeName
	}
	var found *volume
	best := MountNotDeclared
	for i, v := range p.volumes {
		if !related(v) {
			continue
		}
		c := MountOnOtherService
		if v.service == p.service {
			c = MountPathDiffers
			if right(v) {
				c = MountNotRecreated
			}
		}
		if best == MountNotDeclared || rank(c) > rank(best) {
			best, found = c, &p.volumes[i]
		}
	}
	up := fmt.Sprintf("\n\n    %s\n\nthen run paisans apply --execute again.", p.up())
	recreate := fmt.Sprintf("Then recreate %s's container; a restart does not add a mount:", p.service)
	switch best {
	case MountNotRecreated:
		return MountDiagnosis{best, join(fmt.Sprintf("The compose file %s declares %s:%s for %s, but the running container %s was created before it. Recreate it; a restart does not add a mount:%s", found.file, found.source, found.target, p.service, ws.Container, up), note)}
	case MountPathDiffers:
		return MountDiagnosis{best, fmt.Sprintf("The compose file %s mounts %s at %s for %s, but %s\n%s%s", found.file, found.source, found.target, p.service, differs(ws, need, *found, fixed, imported, false), recreate, up)}
	case MountOnOtherService:
		text := fmt.Sprintf("The compose file %s declares %s:%s for %s, but %s runs in %s's container %s: move the line to %s's volumes.", found.file, found.source, found.target, found.service, ws.Server, p.service, ws.Container, p.service)
		if !right(*found) {
			text += "\n" + differs(ws, need, *found, fixed, imported, true)
			if imported != "" {
				note = ""
			}
		}
		return MountDiagnosis{best, join(text+"\n"+recreate+up, note)}
	}
	file := p.files[0]
	for _, v := range p.volumes {
		if v.service == p.service {
			file = v.file
			break
		}
	}
	text := fmt.Sprintf("%s Add this line to the volumes of %s in %s:\n\n    - %s:%s\n\nthen recreate its container; a restart does not add a mount:%s", unmounted(ws, dir), p.service, file, need.Source, need.Destination, up)
	if !fixed {
		text = strings.TrimSuffix(text, ".") + ": it finds the directory and records it in paisans.yaml."
	}
	for _, v := range p.volumes {
		if v.service == p.service && v.variable {
			text += fmt.Sprintf("\nSome of %s's volumes use variables, such as %s, which apply compared as written.", p.service, v.written)
			break
		}
	}
	return MountDiagnosis{best, join(text, note)}
}

// rank orders the cases by how near the compose file is to right: the
// nearest is the one said.
func rank(c MountCase) int {
	return map[MountCase]int{MountOnOtherService: 1, MountPathDiffers: 2, MountNotRecreated: 3}[c]
}

// differs says which path of v is not the one the server reads, and what
// to change it to: the host path for an include_dir, otherwise the
// container path. its starts the sentence with the volume already named.
func differs(ws WebServer, need Mount, v volume, fixed bool, imported string, its bool) string {
	if fixed {
		if its {
			return fmt.Sprintf("Its host path, %s, is not include_dir, %s: change it to %s.", v.source, need.Source, need.Source)
		}
		return fmt.Sprintf("include_dir in paisans.yaml is %s: change %s to %s.", need.Source, v.source, need.Source)
	}
	reads := fmt.Sprintf("the directory apply writes in beside %s's configuration", ws.Server)
	if imported != "" {
		verb := "includes"
		if ws.Server == ServerCaddy {
			verb = "imports"
		}
		reads = fmt.Sprintf("the directory %s's configuration %s (%s)", ws.Server, verb, imported)
		if !its {
			return fmt.Sprintf("%s's configuration %s %s (%s): change %s to %s.", ws.Server, verb, need.Destination, imported, v.target, need.Destination)
		}
	}
	if its {
		return fmt.Sprintf("Its container path, %s, is not %s, %s: change it to %s.", v.target, need.Destination, reads, need.Destination)
	}
	return fmt.Sprintf("apply writes in %s, beside %s's configuration: change %s to %s.", need.Destination, ws.Server, v.target, need.Destination)
}

// composeFiles are the names compose reads in a project's directory
// without -f.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml", "compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml"}

// up is the command that recreates the server's container from its compose
// project: from the project's directory when compose finds its files there
// unnamed, otherwise naming the project and each file.
func (p *composeProject) up() string {
	docker := "docker compose"
	if p.sudo {
		docker = "sudo docker compose"
	}
	plain := p.workingDir != ""
	for _, f := range p.files {
		if path.Dir(f) != p.workingDir || !slices.Contains(composeFiles, path.Base(f)) {
			plain = false
		}
	}
	if plain {
		return fmt.Sprintf("cd %s && %s up -d %s", p.workingDir, docker, p.service)
	}
	cmd := docker
	if p.project != "" {
		cmd += " -p " + p.project
	}
	for _, f := range p.files {
		cmd += " -f " + f
	}
	return cmd + " up -d " + p.service
}

// readCompose reads the compose project a container was started from: its
// labels, then each compose file they name, through sudo only where the
// ssh user may not. It is nil for a container compose did not start, with
// why for one whose compose file could not be read.
func readCompose(h Host, container string) (*composeProject, string) {
	command := "docker inspect --format '{{json .Config.Labels}}' " + quote(container)
	out, err := h.Plain.Run(command)
	sudo := false
	if err != nil && h.Sudo != nil && denied(out, err) {
		out, err = h.Sudo.Run(command)
		sudo = true
	}
	var labels map[string]string
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(out)), &labels) != nil {
		return nil, ""
	}
	p := &composeProject{
		project:    labels["com.docker.compose.project"],
		service:    labels["com.docker.compose.service"],
		workingDir: labels["com.docker.compose.project.working_dir"],
		sudo:       sudo,
	}
	for _, f := range strings.Split(labels["com.docker.compose.project.config_files"], ",") {
		if f = strings.TrimSpace(f); f != "" {
			p.files = append(p.files, f)
		}
	}
	if p.service == "" || len(p.files) == 0 {
		return nil, ""
	}
	for _, f := range p.files {
		content, err := run(h, "cat "+quote(f))
		if err != nil {
			return nil, fmt.Sprintf("Its compose file, %s, could not be read: %s.", f, firstLine(content, err))
		}
		volumes, err := parseVolumes(content, f, p.workingDir)
		if err != nil {
			return nil, fmt.Sprintf("Its compose file, %s, could not be read: %s.", f, err)
		}
		p.volumes = append(p.volumes, volumes...)
	}
	return p, ""
}

// parseVolumes is every service's volumes in a compose file, in either of
// compose's syntaxes: src:dst[:mode], or a mapping with source and target.
func parseVolumes(content, file, workingDir string) ([]volume, error) {
	var doc struct {
		Services map[string]struct {
			Volumes []yaml.Node `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, err
	}
	var names []string
	for name := range doc.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []volume
	for _, name := range names {
		for _, n := range doc.Services[name].Volumes {
			v := volume{file: file, service: name}
			switch n.Kind {
			case yaml.ScalarNode:
				parts := strings.Split(n.Value, ":")
				if len(parts) == 1 {
					v.target = parts[0]
				} else {
					v.source, v.target = parts[0], parts[1]
				}
				v.isBind = strings.HasPrefix(v.source, "/") || strings.HasPrefix(v.source, ".") || strings.HasPrefix(v.source, "~") || strings.HasPrefix(v.source, "$")
			case yaml.MappingNode:
				var long struct{ Type, Source, Target string }
				if err := n.Decode(&long); err != nil {
					return nil, err
				}
				v.source, v.target, v.isBind = long.Source, long.Target, long.Type == "bind"
			default:
				continue
			}
			v.written = v.source
			v.variable = strings.Contains(strings.ReplaceAll(v.source+v.target, "$$", ""), "$")
			if !v.variable {
				v.source = strings.ReplaceAll(v.source, "$$", "$")
				v.target = strings.ReplaceAll(v.target, "$$", "$")
			}
			if v.isBind && workingDir != "" && (v.source == "." || strings.HasPrefix(v.source, "./") || strings.HasPrefix(v.source, "../")) {
				v.source = path.Join(workingDir, v.source)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// join is the non empty of parts, a line each.
func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
