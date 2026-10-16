package apply

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// VolumePrune is what `paisans prune` would do on one site: every dangling
// volume, and whether it goes.
//
// A dangling volume is one no container, running or stopped, mounts. On a
// site host that is almost always an anonymous volume a replaced container
// left behind: an image declared a VOLUME its compose file did not mount,
// and Docker made a new volume for every container. The apply time guard
// (VolumeCheck) stops new ones; this clears what accumulated before it, or
// what any other path left.
type VolumePrune struct {
	Site      string
	Transport string
	Volumes   []DanglingVolume
}

// DanglingVolume is one volume no container uses.
type DanglingVolume struct {
	Name string
	// Size is what Docker's `system df -v` reports for it, in bytes, -1 when
	// it reported none it could read.
	Size int64
	// Entries are its top level names, at most ten, so an operator can see
	// what it held (Mbin's leaked volumes hold cache/ and log/).
	Entries []string
	// Remove is the verdict, and Reason says why, either way.
	Remove bool
	Reason string
}

// Removals are the volumes the prune would remove.
func (p *VolumePrune) Removals() []DanglingVolume {
	var out []DanglingVolume
	for _, v := range p.Volumes {
		if v.Remove {
			out = append(out, v)
		}
	}
	return out
}

// PruneHeader is printed above the plan. It says what the verdicts rest on,
// because "anonymous and unused means ours" is an assumption about the host,
// and an operator should read it before agreeing to it.
const PruneHeader = "A host may carry several deployments and things that are no deployment's at all, so a volume is removed only when it carries this deployment's label, community.paisans.deployment with this deployment's id. Every other volume is kept: one labelled with another id is that deployment's, and one with no such label, anonymous ones included, cannot be attributed to any deployment. apply removes the anonymous volumes its own recreates abandon, read from its own containers before they are replaced."

// danglingProbe prints Docker's size for every volume, then each dangling
// volume's labels and top level entries, one tab separated line each, and
// ends with a marker so a cut short answer is not read as a short list. The
// sizes come from Docker, which reads them as the daemon: the deployment's
// user cannot read a directory a container's user owns, so du on a volume's
// mountpoint can be denied. ls of the top level is allowed to fail the same
// way, and then lists nothing. Volume names cannot contain whitespace (Docker
// allows [a-zA-Z0-9][a-zA-Z0-9_.-]), so the word split over the list is safe.
const danglingProbe = `set -e; docker system df -v --format '{{json .Volumes}}'; echo volumes; vs=$(docker volume ls -qf dangling=true); for v in $vs; do m=$(docker volume inspect --format '{{.Mountpoint}}' "$v") || continue; l=$(docker volume inspect --format '{{json .Labels}}' "$v") || continue; e=$(ls -A "$m" 2>/dev/null | head -n 10 | paste -sd ' ' -); printf 'volume\t%s\t%s\t%s\n' "$v" "$l" "$e"; done; echo end`

// composeProject is the label Compose puts on every volume it creates.
const composeProject = "com.docker.compose.project"

// anonymousName is the name Docker gives an anonymous volume.
// IsAnonymousVolume reports whether name is one Docker generated, which is
// unreadable at any length and so is the only kind worth shortening.
func IsAnonymousVolume(name string) bool { return anonymousName.MatchString(name) }

var anonymousName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BuildVolumePrune reads a site's dangling volumes and decides each one for
// deployment d. It changes nothing.
func BuildVolumePrune(d deployment.Deployment, site string, t Transport) (*VolumePrune, error) {
	out, err := t.Run(danglingProbe)
	if err != nil {
		return nil, fmt.Errorf("%s: listing dangling volumes: %w\n%s", site, err, out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "end") {
		return nil, fmt.Errorf("%s: listing dangling volumes: the answer was cut short:\n%s", site, out)
	}
	head, rest, _ := strings.Cut(out, "\nvolumes\n")
	var listed []struct{ Name, Size string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(head)), &listed); err != nil {
		return nil, fmt.Errorf("%s: reading Docker's volume sizes: %w", site, err)
	}
	sizes := map[string]int64{}
	for _, v := range listed {
		if n, ok := ParseDockerSize(v.Size); ok {
			sizes[v.Name] = n
		}
	}
	p := &VolumePrune{Site: site, Transport: t.Describe()}
	for _, line := range strings.Split(rest, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 3 || fields[0] != "volume" {
			continue
		}
		v := DanglingVolume{Name: fields[1]}
		v.Size = -1
		if n, ok := sizes[v.Name]; ok {
			v.Size = n
		}
		var labels map[string]string
		if err := json.Unmarshal([]byte(fields[2]), &labels); err != nil {
			return nil, fmt.Errorf("%s: volume %s has unreadable labels %q", site, v.Name, fields[2])
		}
		if len(fields) > 3 && strings.TrimSpace(fields[3]) != "" {
			v.Entries = strings.Fields(fields[3])
		}
		v.Remove, v.Reason = pruneVerdict(d, labels)
		p.Volumes = append(p.Volumes, v)
	}
	return p, nil
}

// pruneVerdict decides one dangling volume from its labels. Ownership is the
// deployment label and nothing else: a name or a compose project name is
// chosen by whoever created the volume, and two deployments on one host
// share every naming convention.
func pruneVerdict(d deployment.Deployment, labels map[string]string) (bool, string) {
	switch owner, ok := labels[deployment.Label]; {
	case ok && owner == d.ID:
		return true, "labelled with this deployment's id"
	case ok:
		return false, "labelled with deployment " + owner + ", which is not this one"
	}
	if project, ok := labels[composeProject]; ok {
		return false, "labelled by compose project " + project + " but with no deployment id, so it is not this deployment's"
	}
	return false, "carries no deployment label, so no deployment can be shown to own it"
}

// ExecuteVolumePrune removes every volume the prune decided to. `docker
// volume rm` refuses a volume a container has started using since, which is
// the right answer, so a failure is collected and reported rather than
// stopping the rest.
func ExecuteVolumePrune(p *VolumePrune, t Transport) error {
	var failed []string
	for _, v := range p.Removals() {
		if out, err := t.Run("docker volume rm " + shellQuote(v.Name)); err != nil {
			failed = append(failed, v.Name+": "+firstLine(strings.TrimSpace(out)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s: %d volume(s) were not removed:\n  %s", p.Site, len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}

// dockerSize is a size as Docker prints one (go-units HumanSize): a decimal
// number and a unit in powers of 1000.
var dockerSize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(B|kB|KB|MB|GB|TB|PB)$`)

var dockerUnits = map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}

// ParseDockerSize reads a size Docker printed, Eg: 48.38MB. Anything else,
// N/A included, is not a size.
func ParseDockerSize(s string) (int64, bool) {
	m := dockerSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(n * dockerUnits[m[2]])), true
}
