package doctor

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Outcome is how a node ended, which is what the nodes that need it read.
type Outcome int

const (
	// Passed is a node whose reads answered: what needs it may run.
	Passed Outcome = iota
	// Failed is a node whose reads did not answer, Eg: a site whose ssh did
	// not. What needs it hard is not checked.
	Failed
	// Skipped is a node that made no reads, for a reason of its own (see
	// Node.Unless). What needs it hard is not checked either.
	Skipped
)

// Need is a hard need: the nodes one of which must have passed for a node to
// run. It is met once every one of them has ended and one passed, and it
// fails once every one has ended and none did. A need on one node is that
// node; one on several is any of them, Eg: etcd, read from the first member
// that answers.
type Need []string

// Hard is a need on one node.
func Hard(name string) Need { return Need{name} }

// AnyOf is a need on any of several nodes. A need on none is no need, so a
// node whose alternatives were all left out (Eg: by --site) still runs, and
// says what it could not ask.
func AnyOf(names ...string) Need { return Need(names) }

// Node is one check: what it reads, what it needs first, and how its answer
// is judged. Its name is the line it is reported under, after "check".
type Node struct {
	// Name is "<section> <subject>", Eg: "mesh home-a", "settings home-a
	// talk", "etcd". Names are unique in a graph.
	Name string
	// Section is the section its findings are filed under (SectionMesh).
	Section string
	// Site is the site it reads, "" for one that reads the workstation or
	// only judges. A pruned node is listed under its site.
	Site string
	// Subject is the prefix of its findings' lines that its name already
	// says, Eg: "home-a" for "home-a: psns-f2a9 is up", so its line does not
	// repeat it. Empty when its findings name something else.
	Subject string
	// Hard are its hard needs. Each names nodes added before it, which keeps
	// the graph free of cycles.
	Hard []Need
	// Soft are nodes that must have ended, however they ended, before it
	// runs: its judge uses what they found, or words its advice without it.
	Soft []string
	// Unless is checked once its needs are met, before it reads anything. A
	// reason it returns skips the node with that reason, with no read, and
	// skips what needs it: Eg: a check that must not call a provider that is
	// locking its token out. Nil never skips.
	Unless func(in Input) string
	// Gather makes the node's reads. in holds what every node ended so far
	// recorded, which includes everything it needs. It returns what to
	// record, which the run applies to its Input once the node ends; nil
	// records nothing. Gather runs at the same time as other nodes' and must
	// not touch in's maps.
	Gather func(in Input) (record func(*Input))
	// Outcome is how the node ended, read from the Input once its record is
	// in. Nil is Passed: a node whose reads failing does not stop another.
	Outcome func(in Input) Outcome
	// Judge turns what the hosts said into the node's findings, a pure
	// function of in. Nil is a node that only reads for another's judge,
	// and has no line of its own.
	Judge func(in Input) []Finding
}

// Graph is doctor's checks and what each needs, in the order they are
// reported when one runs at a time: the bottom of the stack first.
type Graph struct {
	nodes []*Node
	index map[string]int
	sites []string
}

// NewGraph is an empty graph for a run that asks sites.
func NewGraph(sites []string) *Graph {
	return &Graph{index: map[string]int{}, sites: sites}
}

// Add puts n at the end of the graph. A need on a node not added yet is a
// mistake in the code that builds the graph, and panics.
func (g *Graph) Add(n Node) {
	if _, dup := g.index[n.Name]; dup {
		panic("doctor: two checks named " + n.Name)
	}
	var hard []Need
	for _, need := range n.Hard {
		if len(need) == 0 {
			continue
		}
		for _, name := range need {
			g.mustHave(n.Name, name)
		}
		hard = append(hard, need)
	}
	n.Hard = hard
	for _, name := range n.Soft {
		g.mustHave(n.Name, name)
	}
	g.index[n.Name] = len(g.nodes)
	g.nodes = append(g.nodes, &n)
}

func (g *Graph) mustHave(from, name string) {
	if _, ok := g.index[name]; !ok {
		panic(fmt.Sprintf("doctor: %s needs %s, which is not added before it", from, name))
	}
}

// Has reports whether a node is in the graph.
func (g *Graph) Has(name string) bool {
	_, ok := g.index[name]
	return ok
}

// Existing is the names that are nodes of the graph, in their order: the
// alternatives of an any-of need, from which a node left out of the run
// (Eg: a site outside --site) is dropped.
func (g *Graph) Existing(names ...string) []string {
	var out []string
	for _, name := range names {
		if g.Has(name) {
			out = append(out, name)
		}
	}
	return out
}

// Names is every node's name, in order.
func (g *Graph) Names() []string {
	out := make([]string, len(g.nodes))
	for i, n := range g.nodes {
		out[i] = n.Name
	}
	return out
}

// state is where a node is in a run.
type state int

const (
	waiting state = iota
	running
	ended
	pruned
)

// needState is whether a need is met, failed, or not decided yet.
func needState(g *Graph, need Need, st []state, out []Outcome) (met, failed bool) {
	all := true
	for _, name := range need {
		i := g.index[name]
		switch {
		case st[i] == ended && out[i] == Passed:
			met = true
		case st[i] == ended || st[i] == pruned:
		default:
			all = false
		}
	}
	// An any-of need waits for every alternative, so the node reads from
	// the first in order that answered, as the order of the graph says,
	// and not from whichever answered first.
	if !all {
		return false, false
	}
	return met, !met
}

// ready reports whether node i may start: every hard need met, and every
// soft need ended.
func (g *Graph) ready(i int, st []state, out []Outcome) bool {
	n := g.nodes[i]
	for _, need := range n.Hard {
		if met, _ := needState(g, need, st, out); !met {
			return false
		}
	}
	for _, name := range n.Soft {
		if s := st[g.index[name]]; s != ended && s != pruned {
			return false
		}
	}
	return true
}

// prune marks every waiting node that can no longer run, because a hard
// need failed, as pruned, and those that need it in turn, and returns them
// in order.
func (g *Graph) prune(st []state, out []Outcome) []int {
	var cut []int
	for changed := true; changed; {
		changed = false
		for i, n := range g.nodes {
			if st[i] != waiting {
				continue
			}
			for _, need := range n.Hard {
				if _, failed := needState(g, need, st, out); failed {
					st[i] = pruned
					cut = append(cut, i)
					changed = true
					break
				}
			}
		}
	}
	slices.Sort(cut)
	return cut
}

// Run runs every node, as many at once as atOnce allows, or all that are
// ready when it is 0, and reports each to rep as it ends (see report). A
// node starts once its needs are met; one whose hard need failed or was
// skipped is not run and has no line, and the line of the node that
// stopped it says so. With atOnce one, the nodes run one at a time in the
// graph's order, so a cause is always reported before what it causes and
// the report reads as Show's does.
//
// The Input it returns holds every probe recorded, and the report's
// findings are its nodes' judges run over it.
func (g *Graph) Run(rep ui.Reporter, atOnce int) (Report, Input) {
	in := Input{Sites: g.sites, Patroni: PatroniProbe{Cluster: map[string]string{}, ClusterErr: map[string]string{}, Logs: map[string]string{}}}
	n := len(g.nodes)
	st := make([]state, n)
	out := make([]Outcome, n)
	findings := make([][]Finding, n)
	lines := make([]ui.Line, n)
	stream := ui.Stream(rep)

	type result struct {
		i      int
		record func(*Input)
	}
	done := make(chan result)
	active := 0
	start := func() {
		for i, node := range g.nodes {
			if atOnce > 0 && active >= atOnce {
				return
			}
			if st[i] != waiting || !g.ready(i, st, out) {
				continue
			}
			if node.Unless != nil {
				if why := node.Unless(in); why != "" {
					st[i], out[i] = ended, Skipped
					line := why
					if node.Subject != "" {
						line = node.Subject + ": " + why
					}
					findings[i] = []Finding{{Section: node.Section, Level: Skip, Line: line}}
					g.show(stream, stream.Add("check "+node.Name), node, findings[i], g.notChecked(g.prune(st, out)))
					continue
				}
			}
			st[i] = running
			active++
			if node.Judge != nil {
				lines[i] = stream.Add("check " + node.Name)
			}
			snapshot := in.clone()
			go func() {
				var record func(*Input)
				if node.Gather != nil {
					record = node.Gather(snapshot)
				}
				done <- result{i, record}
			}()
		}
	}
	for start(); active > 0; start() {
		r := <-done
		active--
		node := g.nodes[r.i]
		if r.record != nil {
			r.record(&in)
		}
		st[r.i] = ended
		if node.Outcome != nil {
			out[r.i] = node.Outcome(in)
		}
		if node.Judge != nil {
			findings[r.i] = node.Judge(in)
		}
		var cut []int
		if out[r.i] != Passed {
			cut = g.prune(st, out)
		}
		if lines[r.i] != nil {
			g.show(stream, lines[r.i], node, findings[r.i], g.notChecked(cut))
		}
	}
	stream.Close()

	report := Report{Sites: len(in.Sites), Reached: len(in.Reached())}
	for i := range g.nodes {
		if st[i] == pruned {
			report.Pruned++
		}
		report.Findings = append(report.Findings, findings[i]...)
	}
	return report, in
}

// Judge is the report the graph gives for in, a complete Input, without
// reading anything: each node's outcome is read from in, a node whose hard
// need failed is pruned, and every other node's judge is run over in. It is
// what Run reported, for the Input Run returned. With all set, every node's
// judge runs, pruned or not.
func (g *Graph) Judge(in Input, all bool) Report {
	n := len(g.nodes)
	st := make([]state, n)
	out := make([]Outcome, n)
	report := Report{Sites: len(in.Sites), Reached: len(in.Reached())}
	for i, node := range g.nodes {
		if !all {
			g.prune(st, out)
			if st[i] == pruned {
				report.Pruned++
				continue
			}
		}
		st[i] = ended
		if node.Unless != nil && !all {
			if why := node.Unless(in); why != "" {
				out[i] = Skipped
				line := why
				if node.Subject != "" {
					line = node.Subject + ": " + why
				}
				report.Findings = append(report.Findings, Finding{Section: node.Section, Level: Skip, Line: line})
				continue
			}
		}
		if node.Outcome != nil {
			out[i] = node.Outcome(in)
		}
		if node.Judge != nil {
			report.Findings = append(report.Findings, node.Judge(in)...)
		}
	}
	return report
}

// mark is the mark a finding's line ends with: ✓ for ok and info, ! for a
// warning or a skipped check, ✗ for a failure.
func mark(l Level) ui.Mark {
	switch l {
	case Fail:
		return ui.Failed
	case Warn, Skip:
		return ui.Warned
	}
	return ui.OK
}

// show reports a node's findings when it ends. The first is the node's own
// line, named by the node, and each further finding (Eg: a second mesh
// check, or each container that is down) a line of its own under it,
// named as Show names it, all printed together. A failure's further lines
// are its recovery, shown at every verbosity; anything else's are details.
// notChecked, what the node's failure kept from running, goes under its
// first failure, or under its own line.
func (g *Graph) show(stream ui.Lines, first ui.Line, n *Node, fs []Finding, notChecked string) {
	if len(fs) == 0 {
		if notChecked != "" {
			first.Explain(notChecked)
		}
		first.End(ui.OK, "")
		return
	}
	at := 0
	for i, f := range fs {
		if f.Level == Fail {
			at = i
			break
		}
	}
	for i, f := range fs {
		var line ui.Line
		result := f.Line
		if i == 0 {
			line = first
			if n.Subject != "" {
				result = strings.TrimPrefix(result, n.Subject+": ")
			}
		} else {
			title, rest := f.Title()
			line, result = stream.Add(title), rest
		}
		if f.Level == Skip {
			result = "skipped: " + result
		}
		for _, more := range f.More {
			if f.Level == Fail {
				line.Explain(more)
			} else {
				line.Detail("%s", more)
			}
		}
		if i == at && notChecked != "" {
			line.Explain(notChecked)
		}
		line.End(mark(f.Level), result)
	}
}

// notChecked is the sentence a failed node's line carries for the nodes it
// kept from running: each named without its site, and grouped by site, Eg:
// "mesh, containers on home-b; etcd".
func (g *Graph) notChecked(cut []int) string {
	if len(cut) == 0 {
		return ""
	}
	var order []string
	bySite := map[string][]string{}
	for _, i := range cut {
		n := g.nodes[i]
		label := n.Name
		if n.Site != "" {
			words := strings.Fields(n.Name)
			if j := slices.Index(words, n.Site); j >= 0 {
				label = strings.Join(slices.Delete(words, j, j+1), " ")
			}
		}
		if _, seen := bySite[n.Site]; !seen {
			order = append(order, n.Site)
		}
		bySite[n.Site] = append(bySite[n.Site], label)
	}
	var parts []string
	for _, site := range order {
		part := strings.Join(bySite[site], ", ")
		if site != "" {
			part += " on " + site
		}
		parts = append(parts, part)
	}
	return "Not checked because of this: " + strings.Join(parts, "; ") + "."
}

// clone is a copy of in that a gather may read while the run records other
// nodes' answers into in.
func (in Input) clone() Input {
	out := in
	out.Sites = slices.Clone(in.Sites)
	out.Reach = slices.Clone(in.Reach)
	out.Admission = slices.Clone(in.Admission)
	out.Users = slices.Clone(in.Users)
	out.Docker = slices.Clone(in.Docker)
	out.Linger = slices.Clone(in.Linger)
	out.SudoRights = slices.Clone(in.SudoRights)
	out.Mesh = slices.Clone(in.Mesh)
	out.Etcd.Tried = slices.Clone(in.Etcd.Tried)
	out.Patroni.Cluster = maps.Clone(in.Patroni.Cluster)
	out.Patroni.ClusterErr = maps.Clone(in.Patroni.ClusterErr)
	out.Patroni.Logs = maps.Clone(in.Patroni.Logs)
	out.Containers = slices.Clone(in.Containers)
	out.Standby = slices.Clone(in.Standby)
	out.Clocks = slices.Clone(in.Clocks)
	out.Leftovers = slices.Clone(in.Leftovers)
	out.Settings = slices.Clone(in.Settings)
	out.Caddy = slices.Clone(in.Caddy)
	out.Tokens = slices.Clone(in.Tokens)
	out.Records.Entries = slices.Clone(in.Records.Entries)
	out.Pending = slices.Clone(in.Pending)
	out.Down = slices.Clone(in.Down)
	return out
}
