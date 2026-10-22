// Package doctor is `paisans doctor`: it reads a running deployment and says
// what is stuck, why, and how to recover, one line per finding.
//
// It never changes anything. There is no --execute: every command it sends a
// host is a read (a list, a status, a log tail), and the recovery it suggests
// is printed for a human to run. That is deliberate, because doctor is run in
// the middle of an outage, and the one recovery it describes that touches
// member data (a forced failover) loses commits. A tool that did that on its
// own would be choosing which data a community keeps.
//
// The package holds no transport. cmd/paisans runs the commands named here on
// each site, through Reads, and Checks' graph hands the outputs to the same
// judgements Diagnose makes, so every judgement below is a pure function of
// what the hosts said, and is tested with what real hosts said.
package doctor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/dns"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Level is how bad a finding is.
type Level int

const (
	// OK is a check that passed.
	OK Level = iota
	// Skip is a check that could not be made and says why. It is not a
	// failure: nothing was found wrong, and nothing was found right either.
	Skip
	// Warn is a risk the operator should know about, not an outage.
	Warn
	// Fail is something broken now. Any Fail makes doctor exit non zero.
	Fail
	// Info is something worth knowing that is neither a risk nor a fault,
	// Eg: a foreign site served by the gateway's Caddy.
	Info
)

func (l Level) String() string {
	switch l {
	case Skip:
		return "skip"
	case Warn:
		return "WARN"
	case Fail:
		return "FAIL"
	case Info:
		return "info"
	}
	return "ok"
}

// Sections, in the order they are printed. Reach is first because every
// other check runs only on the sites it found; the rest go from the bottom of
// the stack up, so the first failure printed is usually the cause of the
// ones after it.
const (
	SectionReach      = "reach"
	SectionMesh       = "mesh"
	SectionEtcd       = "etcd"
	SectionPatroni    = "patroni"
	SectionContainers = "containers"
	SectionSettings   = "settings"
	SectionPocketID   = "pocket-id"
	SectionClocks     = "clocks"
	SectionLeftovers  = "leftovers"
)

var sections = []string{SectionReach, SectionAdmission, SectionUser, SectionDocker, SectionLinger, SectionSudo, SectionSwap, SectionMemory, SectionMesh, SectionEtcd, SectionPatroni, SectionContainers, SectionSettings, SectionPocketID, SectionClocks, SectionLeftovers, SectionSelfCheck, SectionCaddy, SectionSecrets, SectionToken, SectionRecords}

// Finding is one line of the report, with any explanation, recovery advice or
// evidence under it.
type Finding struct {
	Section string
	Level   Level
	Line    string
	More    []string
}

// Report is every finding, and how many sites were asked and answered.
type Report struct {
	Sites    int
	Reached  int
	Findings []Finding
	// Pruned is how many checks were not made because a check they need
	// failed or was skipped (see Graph.Run). Each is reported once, on the
	// line of the check that stopped it, and counted as skipped.
	Pruned int
}

// Failed reports whether any finding is a Fail.
func (r Report) Failed() bool { return r.Count(Fail) > 0 }

// Count is the number of findings at a level.
func (r Report) Count(l Level) int {
	n := 0
	for _, f := range r.Findings {
		if f.Level == l {
			n++
		}
	}
	return n
}

// Show reports the findings: each check is a step titled by what it looked
// at, whose result is the short finding. A check that passed or is only worth
// knowing ends done. A warning is one warning line and a failure one refusal
// line, each naming the check and the finding with no step of its own, so a
// problem never shows two marks. A check that was skipped is a warning too:
// it found nothing right, and a mark that says it passed would tell the
// operator something doctor never looked at is fine. A failure carries its
// recovery as the refusal's explanation, since the operator is reading this
// in an outage and must have the advice without asking for it. A finding's
// further lines are details of a step that passed, the detail of a warning
// and the explanation of a failure.
func (r Report) Show(rep ui.Reporter) {
	rep.Detail("%d site(s), %d reached", r.Sites, r.Reached)
	for _, section := range sections {
		var opened bool
		for _, f := range r.Findings {
			if f.Section != section {
				continue
			}
			if !opened {
				rep.Section(section)
				opened = true
			}
			r.show(rep, f)
		}
	}
	rep.Detail("doctor changes nothing")
	rep.Result("%s", r.Summary())
}

func (r Report) show(rep ui.Reporter, f Finding) {
	title, result := f.Title()
	switch f.Level {
	case Skip:
		rep.Warn(title+" skipped: "+result, strings.Join(f.More, "\n"))
	case Warn:
		rep.Warn(title+": "+result, strings.Join(f.More, "\n"))
	case Fail:
		rep.Refuse(title+": "+result, strings.Join(f.More, "\n"))
	default:
		s := rep.Step(title)
		for _, more := range f.More {
			s.Detail("%s", more)
		}
		s.Done(result)
	}
}

// ShowEnd closes a report whose checks were reported as they ended (see
// Graph.Run): how many sites answered, then, when any check failed, the
// failures once more in the graph's order, deepest first, because in a run
// whose lines interleave the first failure on screen is the first to end,
// not the first to fix. Then that doctor changes nothing, and the summary.
func (r Report) ShowEnd(rep ui.Reporter) {
	rep.Detail("%d site(s), %d reached", r.Sites, r.Reached)
	if r.Failed() {
		rep.Section("failed, deepest first:")
		for _, f := range r.Findings {
			if f.Level == Fail {
				title, result := f.Title()
				rep.Step(title).End(ui.Failed, result)
			}
		}
	}
	rep.Detail("doctor changes nothing")
	rep.Result("%s", r.Summary())
}

// Title is the check's name, "check <section> <subject>" when the line
// names a subject before its first colon (a site, "leader", "version"), and
// the finding's text as the result. A subject is one short word: a longer
// prefix is the finding's own sentence, not a name.
func (f Finding) Title() (title, result string) {
	subject, rest, ok := strings.Cut(f.Line, ": ")
	if ok && subject != "" && len(subject) <= 20 && !strings.ContainsAny(subject, " ()`") {
		return "check " + f.Section + " " + subject, rest
	}
	return "check " + f.Section, f.Line
}

// Summary is the closing line: how many checks passed, were skipped, warned
// and failed. A skipped check is not a pass, so it is counted apart.
func (r Report) Summary() string {
	passed := r.Count(OK) + r.Count(Info)
	parts := []string{fmt.Sprintf("%d %s passed", passed, plural(passed, "check"))}
	if n := r.Count(Skip) + r.Pruned; n > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", n))
	}
	if n := r.Count(Warn); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "warning")))
	}
	if n := r.Count(Fail); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	return strings.Join(parts, ", ") + "."
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// Input is everything the hosts said, gathered by cmd/paisans.
type Input struct {
	// Sites are the sites asked, in order: every declared site, or the ones
	// --site named.
	Sites []string
	Reach []SiteReach
	// Admission, Users, Docker, Linger and SudoRights are each site's
	// rootless checks (admission.go, rootless.go, sudo.go). SudoRights is
	// not named Sudo, which the guard against commands reaching for root
	// refuses to see assigned (cmd/paisans/sudo_guard_test.go).
	Admission  []AdmissionProbe
	Users      []UserProbe
	Docker     []DockerProbe
	Linger     []LingerProbe
	SudoRights []SudoProbe
	// Memory is each site's RAM and swap (memory.go).
	Memory     []MemoryProbe
	Mesh       []MeshProbe
	Etcd       EtcdProbe
	Patroni    PatroniProbe
	Containers []SiteContainers
	Standby    []StandbyProbe
	Clocks     []ClockSample
	Leftovers  []LeftoverProbe
	Settings   []SettingsProbe
	// SelfChecks is each Caddy site's own answer, asked from outside (selfcheck.go).
	SelfChecks []SelfCheckProbe
	// Caddy is each Caddy site's certificate log, Secrets whether the
	// secrets opened, Tokens each place's DNS token check, and Records
	// the zone compared with what the deployment needs (see dnschecks.go).
	// None of them holds the token.
	Caddy   []CaddyLogProbe
	Secrets SecretsProbe
	Tokens  []dns.TokenResult
	Records RecordsProbe
	// Pending and Down are the sites the run's classification placed
	// pending or down, once every site's reach and admission have ended,
	// when it was made (see ClassReads). A site that answers with a record
	// covering it is pending when its every route into the mesh is through
	// a pending site, and one whose login failed only for the
	// classification's own read is down.
	Pending []string
	Down    []string
}

// Reached is the set of sites that answered ssh, and can be asked the rest.
func (in Input) Reached() map[string]bool {
	out := map[string]bool{}
	for _, r := range in.Reach {
		if r.Answered() {
			out[r.Site] = true
		}
	}
	return out
}

// Admitted is the set of sites whose admission check passed: the record
// was read and covers the site as cfg declares it, and the run's
// classification, when one was made, placed it neither pending nor down. A
// check that reads across sites asks only these, never a pending or down
// site or one whose admission failed.
func (in Input) Admitted(cfg *config.Config) map[string]bool {
	out := map[string]bool{}
	for _, p := range in.Admission {
		if p.Covered(cfg) && !in.classedOut(p.Site) {
			out[p.Site] = true
		}
	}
	return out
}

// classedOut reports whether the run's classification placed site pending
// or down.
func (in Input) classedOut(site string) bool {
	return slices.Contains(in.Pending, site) || slices.Contains(in.Down, site)
}

// notAsked is why a check that reads across sites did not ask a site that
// answered ssh.
func (in Input) notAsked(site string) string {
	if in.classedOut(site) {
		return notClassified
	}
	return notAdmitted
}

// Asked is the set of sites doctor was told to look at.
func (in Input) Asked() map[string]bool {
	out := map[string]bool{}
	for _, s := range in.Sites {
		out[s] = true
	}
	return out
}

// destination is how a site was reached, for advice that names a host.
func (in Input) destination(site string) string {
	for _, r := range in.Reach {
		if r.Site == site {
			return r.Destination
		}
	}
	return ""
}

// Diagnose turns what the hosts said into a report.
func Diagnose(cfg *config.Config, in Input) Report {
	reached := in.Reached()
	var findings []Finding
	findings = append(findings, Reach(cfg, in.Reach)...)
	findings = append(findings, Admission(cfg, in.Admission)...)
	findings = append(findings, User(in.Users)...)
	findings = append(findings, DockerSocket(in.Docker)...)
	findings = append(findings, DockerEngine(in.Docker)...)
	findings = append(findings, SourceMark(in.Docker)...)
	findings = append(findings, Linger(cfg.Deployment(), in.Linger)...)
	findings = append(findings, SudoRights(cfg, in.SudoRights)...)
	findings = append(findings, Swap(in.Memory)...)
	findings = append(findings, Memory(cfg, in.Memory)...)
	findings = append(findings, Mesh(cfg, in.Mesh)...)
	findings = append(findings, Etcd(cfg, in)...)
	findings = append(findings, Patroni(cfg, in)...)
	findings = append(findings, Containers(cfg.Deployment(), in.Containers, LeaderName(in.Patroni.Cluster))...)
	findings = append(findings, Settings(cfg, in.Settings)...)
	// Only a cluster that answered can be said to have no primary.
	noPrimary := len(in.Patroni.Cluster) > 0 && !HasLeader(in.Patroni.Cluster)
	findings = append(findings, Standby(cfg.Deployment(), in.Standby, noPrimary)...)
	findings = append(findings, Clocks(in.Clocks)...)
	findings = append(findings, Leftovers(in.Leftovers)...)
	findings = append(findings, SelfChecks(cfg, in.SelfChecks)...)
	findings = append(findings, CaddyCertificates(cfg, in.Caddy)...)
	findings = append(findings, Secrets(in.Secrets)...)
	findings = append(findings, Tokens(in)...)
	findings = append(findings, Records(in.Records)...)
	return Report{Sites: len(in.Sites), Reached: len(reached), Findings: findings}
}

// lastLines is the last n non empty lines of out.
func lastLines(out string, n int) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimRight(line, " \r\t"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// shellQuote wraps a value in single quotes for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
