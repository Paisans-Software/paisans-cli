package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/diagnosis"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// How long a stack has to come up healthy after its action, and how often to
// ask. Five minutes covers a first start that runs migrations or initdb behind
// a healthcheck's start period, without leaving an operator watching a frozen
// terminal for longer than it takes to read the logs themselves.
var (
	healthWait = 5 * time.Minute
	healthPoll = 5 * time.Second
)

// container is the part of one `docker compose ps --format json` entry the
// health gate reads. The keys are the method names of Compose's
// ContainerContext (docker/compose, cmd/formatter/container.go), which
// `--format json` renders through docker/cli's `{{json .}}`; Health is empty
// unless the container is running and defines a healthcheck
// (pkg/compose/ps.go, containerHealthAndExitCode).
type container struct {
	ID       string `json:"ID"`
	Service  string `json:"Service"`
	Name     string `json:"Name"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

// parseContainers reads either shape Compose has printed. Current Compose
// prints one JSON object per line, one per container (cmd/formatter/
// container.go, NewContainerFormat); older Compose v2 releases printed the
// whole list as one JSON array (cmd/formatter/formatter.go, Print, case JSON
// over a slice). Both carry the same keys.
func parseContainers(out string) ([]container, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []container
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var list []container
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c container
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, nil
}

// verdict is what one look at a stack's containers says.
type verdict int

const (
	// waiting means not every container is up yet, and nothing has failed.
	waiting verdict = iota
	// healthy means every container runs and every healthcheck passes.
	healthy
	// failed means a container exited, is in a crash loop, or reports
	// unhealthy. Waiting longer would not help: Docker keeps restarting it,
	// or its own check has given up.
	failed
)

// crashLoop is the restart count at which a restarting container has failed.
// Below it, a restart is a start that lost a race, such as MAS looking its
// database up before Docker's DNS had the name, and up half a second later;
// the gate waits on it as on any container still starting. Docker restarts a
// container that keeps exiting after 100ms, then twice as long each time,
// so a crash loop reaches this count within seconds.
const crashLoop = 3

// judge decides a stack's state, and names the services behind anything
// other than healthy. A stack with no containers at all is waiting: after an
// `up -d` or a restart there must be at least one.
//
// A container that exited with code 0 under the restart policy "no" is a one
// shot that finished, done rather than failed: Docker will never start it
// again, and its exit is what the services after it wait for
// (service_completed_successfully). Exited 0 under any other policy is not:
// unless-stopped or always would have restarted it, so it was stopped, and
// on-failure leaves exited 0 only for a service meant to keep running.
// restartPolicy reads the policy, and is asked only about such a container.
//
// A restarting container is waited on while restarts, its restart count,
// is below crashLoop, and failed from then on, or when restarts cannot read
// the count (-1).
func judge(list []container, restartPolicy func(container) string, restarts func(container) int) (verdict, []string) {
	if len(list) == 0 {
		return waiting, nil
	}
	var bad, slow []string
	for _, c := range list {
		switch {
		case c.State == "exited" && c.ExitCode == 0 && restartPolicy(c) == "no":
			// A one shot that finished.
		case c.State == "restarting" && waitOnRestart(restarts(c)):
			slow = append(slow, fmt.Sprintf("%s (restarting, exit code %d)", c.Service, c.ExitCode))
		case c.State == "exited" || c.State == "dead" || c.State == "restarting":
			bad = append(bad, fmt.Sprintf("%s (%s, exit code %d)", c.Service, c.State, c.ExitCode))
		case c.Health == "unhealthy":
			bad = append(bad, c.Service+" (unhealthy)")
		case c.State != "running":
			slow = append(slow, fmt.Sprintf("%s (%s)", c.Service, c.State))
		case c.Health == "" || c.Health == "none" || c.Health == "healthy":
			// A container without a healthcheck counts once it runs.
		default:
			slow = append(slow, fmt.Sprintf("%s (%s)", c.Service, c.Health))
		}
	}
	if len(bad) > 0 {
		return failed, bad
	}
	if len(slow) > 0 {
		return waiting, slow
	}
	return healthy, nil
}

// waitOnRestart reports whether a container restarted n times is still worth
// waiting on: a count read, and below crashLoop.
func waitOnRestart(n int) bool { return n >= 0 && n < crashLoop }

// containerIDShape is a Docker container ID, full or short, which is all
// restartPolicyOf puts on a command line.
var containerIDShape = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// restartPolicyOf reads a container's restart policy on the host, as
// HostConfig.RestartPolicy.Name: "no", "always", "unless-stopped" or
// "on-failure". `compose ps` does not print it. A container it cannot read
// answers "", which judge counts as failed, never as done.
func restartPolicyOf(t Transport) func(container) string {
	return func(c container) string {
		id := strings.TrimSpace(c.ID)
		if !containerIDShape.MatchString(id) {
			return ""
		}
		out, err := t.Run("docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' " + id)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
}

// restartCountOf reads how many times Docker has restarted a container under
// its restart policy, its RestartCount, which `compose ps` does not print. A
// start that is not the policy's, such as the action's own `up` or
// `restart`, sets it back to 0 (moby daemon/start.go, ResetRestartManager).
// A container it cannot read answers -1, which judge counts as failed.
func restartCountOf(t Transport) func(container) int {
	return func(c container) int {
		id := strings.TrimSpace(c.ID)
		if !containerIDShape.MatchString(id) {
			return -1
		}
		out, err := t.Run("docker inspect --format '{{.RestartCount}}' " + id)
		if err != nil {
			return -1
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return -1
		}
		return n
	}
}

// waitHealthy is the gate after each stack's action: every container of the
// compose project running, and every one with a healthcheck reporting
// healthy. `up -d` and `restart` return as soon as the containers start, so
// without it an apply reported success over an Mbin that could not reach its
// database.
//
// `--all` because a container that exited is exactly what this gate is for,
// and plain `ps` lists only running ones.
func waitHealthy(plan *Plan, stack string, t Transport) error {
	return waitStack(plan.Deployment, plan.Site, stack, t, applyStopped, plan.Served)
}

// applyStopped is what a failed gate tells the operator inside an apply.
const applyStopped = "Nothing after this stack was started; the next apply recreates it."

// WaitHealthy is the same gate for a command outside apply that has just
// started a stack: it waits as apply does, and on failure names the services
// with the tail of their logs, then stopped, which says what the caller did
// and did not do.
func WaitHealthy(d deployment.Deployment, site, stack string, t Transport, stopped string) error {
	return waitStack(d, site, stack, t, stopped, nil)
}

// waitStack is the gate. served is the hostnames each Caddy site serves, for
// recognising a TLS failure as Caddy's (see diagnosis.FromLog); nil
// recognises the causes that need none.
func waitStack(d deployment.Deployment, site, stack string, t Transport, stopped string, served diagnosis.Served) error {
	compose := d.ComposeCmd(stack)
	attempts := int(healthWait / healthPoll)
	if attempts < 1 {
		attempts = 1
	}
	var named []string
	var last string
	for i := 0; i < attempts; i++ {
		out, err := t.Run(compose + " ps --all --format json")
		if err != nil {
			last = err.Error()
		} else if list, perr := parseContainers(out); perr != nil {
			last = fmt.Sprintf("unreadable `ps` output: %v", perr)
		} else {
			v, names := judge(list, restartPolicyOf(t), restartCountOf(t))
			switch v {
			case healthy:
				return nil
			case failed:
				return unhealthy(site, stack, compose, "is not healthy", names, stopped, t, served)
			}
			named, last = names, ""
			if len(list) == 0 {
				last = "no containers"
			}
		}
		if i < attempts-1 {
			sleep(healthPoll)
		}
	}
	if last != "" && len(named) == 0 {
		return fmt.Errorf("%s: stack %s did not come up healthy within %s (%s). %s", site, stack, healthWait, last, stopped)
	}
	return unhealthy(site, stack, compose, fmt.Sprintf("did not come up healthy within %s", healthWait), named, stopped, t, served)
}

// unhealthy builds the gate's error: the stack and its failing services, one
// line of cause per service read from the tail of its log, a line referring
// to doctor for each service whose log shows a cause doctor diagnoses (see
// internal/diagnosis), and stopped. The referral is in the explanation, so
// the default output carries it without --verbose. The
// tail itself, 30 lines a service, is the Problem's cause, shown with
// --verbose: by default it is a wall of JSON and stack traces around the one
// line that matters.
func unhealthy(site, stack, compose, what string, names []string, stopped string, t Transport, served diagnosis.Served) error {
	seen := map[string]bool{}
	var services []string
	for _, name := range names {
		service, _, _ := strings.Cut(name, " ")
		if !seen[service] {
			seen[service] = true
			services = append(services, service)
		}
	}
	sort.Strings(services)
	var causes, referrals []string
	var logs strings.Builder
	for _, service := range services {
		out, err := t.Run(fmt.Sprintf("%s logs --no-color --tail 30 %s", compose, shellQuote(service)))
		if err != nil && out == "" {
			out = err.Error()
		} else {
			if cause := logCause(out); cause != "" {
				causes = append(causes, service+": "+cause)
			}
			if r, ok := diagnosis.FromLog(out, served); ok {
				referrals = append(referrals, service+": "+r.Sentence)
			}
		}
		if logs.Len() > 0 {
			logs.WriteString("\n\n")
		}
		fmt.Fprintf(&logs, "--- %s, last 30 log lines ---\n%s", service, strings.TrimRight(out, "\n"))
	}
	p := &ui.Problem{
		Hint:    fmt.Sprintf("%s: stack %s %s: %s", site, stack, what, strings.Join(names, ", ")),
		Explain: strings.Join(slices.Concat(causes, referrals, []string{stopped}), "\n"),
	}
	if logs.Len() > 0 {
		p.Cause = errors.New(logs.String())
	}
	return p
}

// maxCause is the longest cause line unhealthy shows, in characters.
const maxCause = 160

// composePrefix is the `name-1  | ` that `docker compose logs` puts before
// each line.
var composePrefix = regexp.MustCompile(`^[A-Za-z0-9_.-]+-[0-9]+ +\| ?`)

// plainError is a plain-text log line that reports a failure.
var plainError = regexp.MustCompile(`(?i)\b(error|fatal|panic)`)

// goFrame is a line of a Go stack trace that is not indented: a call such as
// `main.main()` or `panic({0x1, 0x2})`, or a goroutine header.
var goFrame = regexp.MustCompile(`^([\w./*()-]+\(.*\)|goroutine \d+ .*:)$`)

// logCause is the most likely cause of a failure in the tail of one service's
// log, newest first, or "" when no line reads as one. A JSON line (etcd and
// most Go services log that way) counts when its level is error, fatal or
// panic, and gives its error field, else err, else msg; a stack trace field is
// never read. Any other line counts when it says error, fatal or panic, and
// is not part of a Go stack trace.
func logCause(logs string) string {
	lines := strings.Split(logs, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := composePrefix.ReplaceAllString(strings.TrimRight(lines[i], "\r"), "")
		var cause string
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			cause = jsonCause(line)
		} else if line != "" && line == strings.TrimLeft(line, " \t") && !goFrame.MatchString(line) && plainError.MatchString(line) {
			cause = line
		}
		if cause = strings.Join(strings.Fields(cause), " "); cause != "" {
			if r := []rune(cause); len(r) > maxCause {
				cause = string(r[:maxCause-3]) + "..."
			}
			return cause
		}
	}
	return ""
}

// jsonCause is logCause's reading of one JSON log line.
func jsonCause(line string) string {
	var entry map[string]any
	if json.Unmarshal([]byte(line), &entry) != nil {
		return ""
	}
	level, _ := entry["level"].(string)
	if level == "" {
		level, _ = entry["lvl"].(string)
	}
	switch strings.ToLower(level) {
	case "error", "fatal", "panic", "dpanic", "crit", "critical":
	default:
		return ""
	}
	for _, key := range []string{"error", "err", "msg", "message"} {
		switch v := entry[key].(type) {
		case nil:
		case string:
			if v != "" {
				return v
			}
		default:
			return fmt.Sprint(v)
		}
	}
	return ""
}

// StackHealthy is the health gate's judgement from one look, for a command
// that checks a running stack rather than one it just started: nil when every
// container of the stack runs and passes its healthcheck, and otherwise an
// error naming the services that do not. It does not wait; a caller that
// expects a stack to recover polls it.
func StackHealthy(d deployment.Deployment, stack string, t Transport) error {
	compose := d.ComposeCmd(stack)
	out, err := t.Run(compose + " ps --all --format json")
	if err != nil {
		return fmt.Errorf("stack %s: %v", stack, err)
	}
	list, err := parseContainers(out)
	if err != nil {
		return fmt.Errorf("stack %s: unreadable `ps` output: %v", stack, err)
	}
	switch v, names := judge(list, restartPolicyOf(t), restartCountOf(t)); {
	case v == healthy:
		return nil
	case len(list) == 0:
		return fmt.Errorf("stack %s: no containers", stack)
	default:
		return fmt.Errorf("stack %s: %s", stack, strings.Join(names, ", "))
	}
}
