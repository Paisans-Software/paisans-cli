package doctor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ContainersCommand lists every container of deployment d on a host, running
// or not. Every rendered service carries the deployment label with the
// deployment's id (the `labels:` of each compose template), so the filter is
// that label, never a name: another deployment on the same host has
// containers named paisans-<its token>-..., and something that is no
// deployment's may be named anything. The keys of `{{json .}}` are the
// methods of docker/cli's ContainerContext (docker/cli v27.3.1,
// cli/command/formatter/container.go): Names, State, Status, Labels among
// them, and from docker/cli v29.5.0 HealthStatus.
func ContainersCommand(d deployment.Deployment) string {
	return "docker ps -a --filter " + shellQuote(d.LabelFilter()) + " --format '{{json .}}'"
}

// InspectCommand reads the state Docker recorded for one container: its
// status, exit code, the error Docker itself hit starting it, and how often
// it was restarted, and for a container with a healthcheck what its last
// checks printed. The fields are the Engine API's ContainerState, its Health,
// and RestartCount (moby v27.3.1, api/types/types.go).
func InspectCommand(name string) string {
	return "docker inspect --format '{\"State\":{{json .State}},\"RestartCount\":{{.RestartCount}}}' " + shellQuote(name)
}

// LogCommand is the end of one container's log.
func LogCommand(name string) string {
	return "docker logs --tail 20 " + shellQuote(name) + " 2>&1"
}

// PSEntry is one line of ContainersCommand's output.
type PSEntry struct {
	Names        string `json:"Names"`
	State        string `json:"State"`
	Status       string `json:"Status"`
	HealthStatus string `json:"HealthStatus"`
	Labels       string `json:"Labels"`
}

// ParsePS reads ContainersCommand's output, one JSON object per line.
func ParsePS(out string) ([]PSEntry, error) {
	var list []PSEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e PSEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("unreadable docker ps line %q", firstLine(line))
		}
		list = append(list, e)
	}
	return list, nil
}

// Down reports whether a container needs a look: anything not running, which
// includes one Docker keeps restarting, and one running whose healthcheck
// reports it unhealthy.
func (e PSEntry) Down() bool { return e.State != "running" || e.Health() == "unhealthy" }

// Health is the container's healthcheck status, "healthy", "unhealthy" or
// "starting", and empty for a container with no healthcheck. Docker 29.5
// and later report it in HealthStatus, "none" where there is no healthcheck;
// every release also ends Status with it, "Up 5 minutes (unhealthy)" or
// "Up 5 seconds (health: starting)", which is all an earlier release has.
func (e PSEntry) Health() string {
	switch e.HealthStatus {
	case "none":
		return ""
	case "":
	default:
		return e.HealthStatus
	}
	_, health, ok := strings.Cut(e.Status, "(")
	if !ok || !strings.HasSuffix(health, ")") {
		return ""
	}
	health = strings.TrimPrefix(strings.TrimSuffix(health, ")"), "health: ")
	switch health {
	case "healthy", "unhealthy", "starting":
		return health
	}
	return ""
}

// label is the value of one label in the entry's comma separated Labels.
func (e PSEntry) label(key string) (string, bool) {
	for _, label := range strings.Split(e.Labels, ",") {
		if v, ok := strings.CutPrefix(label, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// Owned reports whether the container carries deployment d's label. The
// command already filters on it; this holds the same line for output that
// came from anywhere else.
func (e PSEntry) Owned(d deployment.Deployment) bool {
	id, ok := e.label(deployment.Label)
	return ok && id == d.ID
}

// Stack is the stack a container of deployment d belongs to, from its compose
// project label, paisans-<token>-<stack>.
func (e PSEntry) Stack(d deployment.Deployment) string {
	if v, ok := e.label("com.docker.compose.project"); ok {
		if stack, ok := strings.CutPrefix(v, d.Prefix()+"-"); ok {
			return stack
		}
	}
	return ""
}

// Inspected is InspectCommand's output.
type Inspected struct {
	State struct {
		Status     string `json:"Status"`
		Restarting bool   `json:"Restarting"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		// Health is absent for a container with no healthcheck. Docker
		// keeps the last five checks in Log, oldest first.
		Health *struct {
			Status        string        `json:"Status"`
			FailingStreak int           `json:"FailingStreak"`
			Log           []HealthCheck `json:"Log"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
}

// HealthCheck is one healthcheck Docker ran: how it exited and what it
// printed.
type HealthCheck struct {
	ExitCode int    `json:"ExitCode"`
	Output   string `json:"Output"`
}

// ParseInspect reads InspectCommand's output.
func ParseInspect(out string) (Inspected, error) {
	var in Inspected
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &in); err != nil {
		return in, fmt.Errorf("unreadable docker inspect %q", firstLine(out))
	}
	return in, nil
}

// ContainerProbe is one container that is not running, or runs unhealthy,
// with what Docker says about it.
type ContainerProbe struct {
	Entry      PSEntry
	Inspect    string
	InspectErr string
	Logs       string
}

// SiteContainers is one reachable site's containers.
type SiteContainers struct {
	Site string
	// PS is ContainersCommand's output, and PSErr why it failed.
	PS    string
	PSErr string
	// Down is a probe of each container PS lists as down (PSEntry.Down).
	Down []ContainerProbe
}

// databaseTrouble is what an app that cannot reach its database prints, in
// the forms the blueprints here use.
var databaseTrouble = regexp.MustCompile(`(?i)failed to connect|connection refused|ping postgres|database`)

// networkUnreachable is a container whose own network has no route to the
// mesh address it was given, which a container whose network setup failed
// and was then started again can be left with. Recreating the container
// builds its network afresh.
var networkUnreachable = regexp.MustCompile(`(?i)network is unreachable`)

// Containers is one line per site, one per container that is down, and a
// warning for each running container whose healthcheck has not passed yet.
// primary is the Patroni leader doctor found, empty when there is none or the
// cluster did not answer: an app that cannot reach its database is waiting on
// Patroni only when there is no primary.
func Containers(d deployment.Deployment, sites []SiteContainers, primary string) []Finding {
	var out []Finding
	for _, s := range sites {
		if s.PSErr != "" {
			out = append(out, Finding{Section: SectionContainers, Level: Warn, Line: fmt.Sprintf("%s: could not list containers (%s)", s.Site, firstLine(s.PSErr))})
			continue
		}
		all, err := ParsePS(s.PS)
		if err != nil {
			out = append(out, Finding{Section: SectionContainers, Level: Warn, Line: fmt.Sprintf("%s: %v", s.Site, err)})
			continue
		}
		var list []PSEntry
		for _, e := range all {
			if e.Owned(d) {
				list = append(list, e)
			}
		}
		running := 0
		for _, e := range list {
			if !e.Down() {
				running++
			}
		}
		var down []ContainerProbe
		for _, probe := range s.Down {
			if probe.Entry.Owned(d) {
				down = append(down, probe)
			}
		}
		level := OK
		if len(down) > 0 {
			level = Fail
		}
		if len(list) == 0 {
			out = append(out, Finding{Section: SectionContainers, Level: Warn, Line: fmt.Sprintf("%s: no paisans containers at all; has it been applied?", s.Site)})
			continue
		}
		if level == OK {
			out = append(out, Finding{Section: SectionContainers, Level: OK, Line: fmt.Sprintf("%s: %d paisans container(s), all running", s.Site, running)})
		}
		for _, probe := range down {
			out = append(out, containerFinding(d, s.Site, probe, primary))
		}
		for _, e := range list {
			if e.State == "running" && e.Health() == "starting" {
				out = append(out, Finding{Section: SectionContainers, Level: Warn, Line: fmt.Sprintf("%s: %s running, its healthcheck has not passed yet", s.Site, e.Names), More: []string{
					"Docker has not yet seen it pass a healthcheck, nor fail enough of them to call it unhealthy, as in its start period. It is a failure only once Docker reports it unhealthy; run doctor again in a minute.",
				}})
			}
		}
	}
	return out
}

func containerFinding(dep deployment.Deployment, site string, d ContainerProbe, primary string) Finding {
	name := d.Entry.Names
	line := fmt.Sprintf("%s: %s %s", site, name, d.Entry.Status)
	var in Inspected
	inspected := false
	if d.InspectErr == "" {
		if got, err := ParseInspect(d.Inspect); err == nil {
			in, inspected = got, true
		}
	}
	if inspected {
		line = fmt.Sprintf("%s: %s %s (exit %d, restarted %d time(s))", site, name, in.State.Status, in.State.ExitCode, in.RestartCount)
		if in.State.Error != "" {
			line += ": " + in.State.Error
		}
	}
	h := in.State.Health
	unhealthy := inspected && in.State.Status == "running" && h != nil && h.Status == "unhealthy"
	if unhealthy {
		line = fmt.Sprintf("%s: %s running but unhealthy, failed its last %d health check(s)", site, name, h.FailingStreak)
	}
	f := Finding{Section: SectionContainers, Level: Fail, Line: line}
	restarting := d.Entry.State == "restarting" || in.State.Restarting
	logs := lastLines(d.Logs, 20)

	switch {
	case strings.Contains(in.State.Error, "cannot assign requested address"):
		recreate := "`paisans deployment apply --site " + site + " --execute`"
		if stack := d.Entry.Stack(dep); stack != "" {
			recreate = "`paisans deployment apply --site " + site + " --recreate " + stack + " --execute`"
		}
		f.More = []string{
			fmt.Sprintf("Its Docker daemon started it before %s was up. The container publishes its port on the site's mesh address, which exists only once %s has brought %s up, and Docker does not retry a container whose network setup failed.", dep.Interface(), dep.WireGuardUnit(), dep.Interface()),
			"recover:",
			fmt.Sprintf("  1. `docker start %s` on %s, as the deployment's user, or %s.", name, site, recreate),
			fmt.Sprintf("  2. If it happens at every boot, `paisans doctor --site %s` says under linger whether the user's manager is ordered after the mesh.", site),
		}
	case restarting && anyMatch(logs, networkUnreachable):
		f.More = []string{
			"Its own network has no route to the mesh address it connects to, so it cannot reach its database however healthy that is. A container whose network setup failed at boot and was then started again can be left this way, and restarting it does not rebuild its network.",
			"recover:",
			"  " + recreateCommand(site, d.Entry.Stack(dep)) + ", which replaces the stack's containers and builds their network afresh.",
		}
		f.More = append(f.More, indent(lastMatching(logs, networkUnreachable, 3))...)
	case restarting && anyMatch(logs, databaseTrouble) && primary != "":
		f.More = []string{
			fmt.Sprintf("It cannot reach its database although %s is the primary, so it is not waiting on Patroni. Check this site's HAProxy (the infra stack) and the container's own network.", primary),
			"recover:",
			"  " + recreateCommand(site, d.Entry.Stack(dep)) + ", which replaces the stack's containers and builds their network afresh.",
		}
		f.More = append(f.More, indent(lastMatching(logs, databaseTrouble, 3))...)
	case restarting && anyMatch(logs, databaseTrouble):
		f.More = []string{"It cannot reach its database, and Docker keeps restarting it. See the patroni section: it comes back on its own once there is a primary."}
		f.More = append(f.More, indent(lastMatching(logs, databaseTrouble, 3))...)
	default:
		if !inspected && d.InspectErr != "" {
			f.More = append(f.More, "docker inspect failed: "+firstLine(d.InspectErr))
		}
		if unhealthy {
			f.More = append(f.More, healthOutput(h.Log)...)
		}
		if tail := lastLines(d.Logs, 3); len(tail) > 0 {
			f.More = append(f.More, "last log lines:")
			f.More = append(f.More, indent(tail)...)
		}
	}
	return f
}

// healthOutput is the end of what a container's recent healthchecks printed,
// oldest first, a line repeated by consecutive checks shown once. A check
// that printed nothing is named by its exit code, so the finding always says
// what the check reported.
func healthOutput(log []HealthCheck) []string {
	var lines []string
	for _, check := range log {
		for _, line := range lastLines(check.Output, 20) {
			if len(lines) == 0 || lines[len(lines)-1] != line {
				lines = append(lines, line)
			}
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	if len(lines) == 0 {
		if len(log) == 0 {
			return nil
		}
		return []string{fmt.Sprintf("the last health check exited %d and printed nothing", log[len(log)-1].ExitCode)}
	}
	return append([]string{"last health check output:"}, indent(lines)...)
}

func anyMatch(lines []string, re *regexp.Regexp) bool {
	for _, line := range lines {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func lastMatching(lines []string, re *regexp.Regexp, n int) []string {
	var out []string
	for _, line := range lines {
		if re.MatchString(line) {
			out = append(out, line)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func indent(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = "  " + line
	}
	return out
}

// recreateCommand is the apply that replaces one stack's containers on a
// site, touching nothing else there.
func recreateCommand(site, stack string) string {
	if stack == "" {
		return "`paisans deployment apply --site " + site + " --execute`"
	}
	return "`paisans deployment apply --site " + site + " --only " + stack + " --recreate " + stack + " --execute`"
}
