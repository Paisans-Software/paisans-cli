package siteadd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// startEtcd starts only the etcd service of a site's infrastructure stack.
// `up -d` with a service name acts on that service and nothing else, so a
// gateway's Caddy and a data site's Patroni are not touched: Patroni waits
// for stage 4, and the gateway has its own gates in apply.
func (p *Plan) startEtcd() string { return p.dep().ComposeCmd("infra") + " up -d etcd" }

// probeEtcd finds a site whose etcd is a running voter, and the membership as
// it reports it. Live members the configuration does not name are refused:
// removing one is a separate command with its own gates.
func (p *Plan) probeEtcd() ([]apply.EtcdMember, error) {
	order := []string{}
	for _, name := range p.cfg.Etcd.Members {
		if name != p.Site {
			order = append(order, name)
		}
	}
	order = append(order, p.Site)
	for _, name := range order {
		cluster, running, err := apply.ReadEtcdCluster(p.transports[name], p.dep(), p.cfg.Sites[name].Address)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		members := cluster.Members
		if !running {
			continue
		}
		voter := false
		for _, m := range members {
			if apply.EtcdMemberSite(p.cfg, m) == name && !m.IsLearner {
				voter = true
			}
		}
		if !voter {
			continue
		}
		for _, m := range members {
			if site := apply.EtcdMemberSite(p.cfg, m); !contains(p.cfg.Etcd.Members, site) {
				return nil, fmt.Errorf("site add %s: etcd has a member %s that %s does not list. Removing a member is not part of site add; list it, or remove it by hand on %s with `etcdctl --endpoints=http://%s:%d member remove %s`", p.Site, site, p.cfg.EtcdMembersKey(), name, p.cfg.Sites[name].Address, render.EtcdClientPort, m.HexID())
			}
		}
		p.control = name
		p.clusterID = cluster.ID
		return members, nil
	}
	return nil, fmt.Errorf("site add %s: no etcd answers as a voter on any of %s. site add grows a running cluster; a deployment not started yet is started with `paisans deployment apply`", p.Site, strings.Join(order, ", "))
}

// joiners are the configured members that are not yet voters: every witness
// first, an existing site gaining the role or a new site holding it, then any
// other member, then the new site. A witness has no replica and so no site add
// of its own: it joins here, in the join of the data site it makes the voters
// odd with. One at a time, because etcd admits one learner at a time
// (maxLearners = 1, etcd v3.5.16, server/etcdserver/api/membership/
// cluster.go).
func (p *Plan) joiners(live []apply.EtcdMember) []string {
	voter := map[string]bool{}
	for _, m := range live {
		if !m.IsLearner {
			voter[apply.EtcdMemberSite(p.cfg, m)] = true
		}
	}
	var witnesses, others []string
	for _, name := range sortedCopy(p.cfg.Etcd.Members) {
		switch {
		case voter[name] || name == p.Site:
		case p.cfg.Sites[name].Has(config.RoleWitness):
			witnesses = append(witnesses, name)
		default:
			others = append(others, name)
		}
	}
	out := append(witnesses, others...)
	if !voter[p.Site] {
		out = append(out, p.Site)
	}
	return out
}

// buildEtcd is stage 3: each joiner added as a learner, started with the
// membership it joins, and promoted once it has caught up. A joiner not in
// etcd's member list has its etcd data cleared before it is added; see
// clearEtcdData.
func (p *Plan) buildEtcd(live []apply.EtcdMember) *Stage {
	st := &Stage{
		Number: 3,
		Name:   "etcd",
		Short:  "etcd healthy, voters match",
		Gate:   fmt.Sprintf("etcd's health across the cluster, read on %s, reports every member healthy, and the voters are exactly %s", p.control, strings.Join(sortedCopy(p.cfg.Etcd.Members), ", ")),
	}
	predicted := map[string]string{}
	present := map[string]bool{}
	started := map[string]bool{}
	for _, m := range live {
		site := apply.EtcdMemberSite(p.cfg, m)
		predicted[site] = peerURL(m)
		present[site] = true
		started[site] = m.Started()
	}
	ids := map[string]string{}
	for _, m := range live {
		ids[apply.EtcdMemberSite(p.cfg, m)] = m.HexID()
	}
	joiners := p.joiners(live)
	for _, j := range joiners {
		url := render.EtcdPeerURL(p.cfg.Sites[j].Address)
		if !started[j] {
			st.Steps = append(st.Steps, Step{Site: j, Verb: "stop", Title: "stop etcd on " + j, Text: "any etcd container of this deployment there, found by its labels, so nothing writes its data while it is read and cleared: " + apply.RemoveEtcdContainers(p.dep())})
		}
		if present[j] && !started[j] {
			st.Steps = append(st.Steps, Step{Site: p.control, Verb: "remove", Title: "remove unstarted learner " + j, Text: fmt.Sprintf("%s's learner %s, which an earlier run added and which never started, read again first and removed only once its data is read and accepted: one that received data before it stopped cannot start again under its ID, so it is added again under a new one. The removal is recorded first on the other members' hosts, in %s: %s", j, ids[j], apply.EtcdRemovedPath(p.dep()), apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, "member remove "+ids[j]))})
		}
		if !started[j] {
			st.Steps = append(st.Steps, Step{Site: j, Verb: "clear", Title: "clear old etcd data on " + j, Text: fmt.Sprintf("%s's old etcd data, %s, if it holds any: a removed member's, which etcd refuses to rejoin with. It is read on disk and deleted only when it is empty or a member whose write-ahead logs name this cluster, %x, and a member this cluster removed for %s; anything else refuses: %s", j, apply.EtcdDataDir(p.dep()), p.clusterID, j, p.clearEtcdData())})
		}
		if !started[j] {
			st.Steps = append(st.Steps, Step{Site: j, Verb: "add", Title: "add etcd learner " + j, Text: fmt.Sprintf("an etcd learner %s with peer URL %s, through %s", j, url, p.control)})
		}
		predicted[j] = url
		initial, ok := p.initial[j]
		if !ok || initial.State != render.EtcdStateExisting {
			initial = render.EtcdInitial{State: render.EtcdStateExisting, Cluster: clusterString(predicted)}
		}
		st.Steps = append(st.Steps,
			Step{Site: j, Verb: "write", Title: "write etcd files on " + j, Text: fmt.Sprintf("%s and /%s: --initial-cluster-state=%s --initial-cluster=%s", p.dep().Compose("infra"), render.EtcdInitialPath(p.dep()), initial.State, initial.Cluster)},
			Step{Site: j, Verb: "start", Title: "start etcd on " + j, Text: "etcd alone: " + p.startEtcd()},
			Step{Site: j, Verb: "promote", Title: "promote " + j + " to voter", Text: fmt.Sprintf("the learner once it has caught up; etcd refuses until then, so it is retried up to %d times", promoteAttempts)},
		)
	}
	st.run = func() error {
		for _, j := range joiners {
			if err := p.joinEtcd(j); err != nil {
				return err
			}
		}
		return nil
	}
	st.gate = p.etcdGate
	return st
}

// clearEtcdData empties a joiner's etcd data directory before it is added
// as a learner, and removes any etcd container of this deployment on its
// host first, so a container restarting in a loop does not write to the
// directory while it is emptied. site add starts the container again,
// alone, once the learner is added.
//
// It runs for every joiner whose member has not started: one not in etcd's
// member list, however its host came to hold data (a member removed by site
// remove, one taken out with --host-gone whose host comes back, or a removal
// that stopped part way), and a learner an earlier run added that never
// started, whose host may still hold a removed member's data. Each would
// otherwise start as the removed member, which the cluster refuses
// (apply.EmptyEtcdData says why deleting it loses nothing). Such a learner
// may have received a snapshot or log entries before it stopped, and the
// leader then remembers its progress, so it is not restarted from empty
// data under the same ID: it is removed from etcd first and added again
// under a new one. On a site that never ran etcd there is nothing to
// delete. A voter or a learner that has started is never cleared: its data
// is its own. Before anything is deleted, checkEtcdData refuses unless the
// data is shown to be a member this cluster removed for this site, or
// nothing.
//
// The delete goes through rootlesskit because the files are expected to be
// owned by a subordinate UID under rootless Docker. That is expected, not
// observed: it is confirmed on a host before this comment says otherwise.
func (p *Plan) clearEtcdData() string {
	return apply.RemoveEtcdContainers(p.dep()) + " && { " + apply.EmptyEtcdData(p.dep()) + "; }"
}

// planEtcdData is checkEtcdData when the join is planned, for a joiner
// whose member has not started. Nothing is stopped then, so an etcd running
// there is asked too, and one that answers for another cluster is refused;
// one that does not answer (an unstarted learner serves no client request)
// or answers without an ID is left to the run, which stops it and reads its
// data on disk before anything is deleted.
func (p *Plan) planEtcdData(j string, live []apply.EtcdMember) error {
	if err := p.knowCluster(j); err != nil {
		return err
	}
	c, running, err := apply.ReadEtcdCluster(p.transports[j], p.dep(), p.cfg.Sites[j].Address)
	if err == nil && running && c.ID != 0 && c.ID != p.clusterID {
		return p.otherCluster(j, c.ID, "its running etcd answers for it", "Nothing was changed")
	}
	var learner uint64
	if m := p.find(live, j); m != nil && m.IsLearner && !m.Started() {
		learner = m.ID
	}
	return p.checkEtcdData(j, live, learner, "Nothing was changed")
}

// knowCluster refuses when the control voter's answer carried no cluster ID:
// with nothing to compare a joiner's data with, nothing of it is deleted.
// etcdctl's member list always carries the ID in its header (etcd v3.5.16,
// etcdctl/ctlv3/command/printer_json.go).
func (p *Plan) knowCluster(j string) error {
	if p.clusterID == 0 {
		return fmt.Errorf("site add %s: %s's etcd answered without a cluster ID, so the cluster ID could not be read, and %s's etcd data cannot be checked against it before it is cleared. Run paisans doctor on %s and run site add again. Nothing was changed", p.Site, p.control, j, p.control)
	}
	return nil
}

// checkEtcdData decides, from what a joiner's etcd data path holds on disk
// (apply.ReadEtcdData, whatever runs there), whether it may be deleted, and
// refuses unless there is positive evidence there is nothing to lose or the
// data is a member this cluster removed for this site:
//
//   - nothing: no directory, or an empty one;
//   - a member: etcd's member layout whose write-ahead logs all name this
//     cluster's ID and one member ID, which is the learner being replaced or
//     a member this cluster removed for this site and recorded
//     (apply.EtcdRemoved), and is not a member the cluster has now.
//
// The cluster ID alone is not that evidence. etcd derives a founder's member
// ID from its peer URLs and the cluster token, and the cluster's ID from its
// founders' (render.WithEtcdFoundingToken), so two clusters founded on the
// same addresses with the same token share both. A member added later gets
// an ID that includes the time it was added, so its ID is its own. A cluster
// founded with a token chosen at random has founder IDs of its own too. One
// founded before tokens were recorded, with the fixed token, does not: a
// removed founder's data is then accepted on its recorded ID and the cluster
// ID, and data of another cluster founded on the same addresses with the
// fixed token, whose founder held this site's address, would match both.
// That cluster would be another of this deployment's own, on its own data
// path, and is the risk that remains.
//
// Anything else refuses, naming what was found: another cluster's ID, an ID
// this cluster has no record of removing for this site, a member it has
// now, a layout that is not etcd's, logs that disagree or do not decode, a
// symbolic link, a path that is not a directory or cannot be read. A member
// directory with no write-ahead log refuses too, with the command that
// deletes it by hand: etcd leaves a database alone when it exits before it
// reaches a peer, Eg a learner that never started, but nothing in the
// database says whose it is or whether it holds keys (a defragmented one
// looks like a fresh one), so only its owner can judge it.
func (p *Plan) checkEtcdData(j string, members []apply.EtcdMember, learner uint64, changed string) error {
	data, err := apply.ReadEtcdData(p.transports[j], p.dep())
	if err != nil {
		return p.unknownEtcdData(j, fmt.Sprintf("could not be read (%v)", err), changed)
	}
	v := data.Assess()
	switch v.Kind {
	case apply.EtcdDataNothing:
		return nil
	case apply.EtcdDataMember:
	default:
		if v.NoWAL {
			return p.noWALEtcdData(j, v.Why, changed)
		}
		return p.unknownEtcdData(j, v.Why, changed)
	}
	if v.ClusterID != p.clusterID {
		return p.otherCluster(j, v.ClusterID, "its write-ahead logs name it", changed)
	}
	for _, m := range members {
		if m.ID == v.NodeID && m.ID != learner {
			return p.unknownEtcdData(j, fmt.Sprintf("is etcd member %x's, which this cluster has now (%s)", v.NodeID, apply.EtcdMemberSite(p.cfg, m)), changed)
		}
	}
	if learner != 0 && v.NodeID == learner {
		return nil
	}
	// The records are read only here, where a member's data needs them, and
	// read again each time: a host or list that cannot be read refuses this
	// clear and no other join.
	if err := p.readRemoved(); err != nil {
		return p.unknownEtcdData(j, fmt.Sprintf("is etcd member %x's of this cluster, and the records of removed members could not be read (%v)", v.NodeID, err), changed)
	}
	for _, r := range p.removed {
		if r.Site == j && r.Cluster == p.clusterID && r.Member == v.NodeID {
			return nil
		}
	}
	return p.unknownEtcdData(j, fmt.Sprintf("is etcd member %x's of this cluster, which no host of this deployment records as removed for %s in %s", v.NodeID, j, apply.EtcdRemovedPath(p.dep())), changed)
}

func (p *Plan) unknownEtcdData(j, what, changed string) error {
	return fmt.Errorf("site add %s: %s's etcd data, %s, %s, so it is not shown to be a member this cluster removed for %s, and it is not deleted. Read it on %s: data of another cluster is its owner's to move off the host, or the site is taken out of this deployment; data of this deployment's own is deleted by hand once that is certain. %s", p.Site, j, apply.EtcdDataDir(p.dep()), what, j, j, changed)
}

func (p *Plan) noWALEtcdData(j, what, changed string) error {
	dir := apply.EtcdDataDir(p.dep())
	return fmt.Errorf("site add %s: %s's etcd data, %s, %s. site add does not delete it. Read it on %s, as the deployment's user (`rootlesskit ls -lR %s`); once it is certain to be this deployment's and nothing to lose, delete it there with `rootlesskit rm -rf -- %s/member` and run site add again. Data of another cluster is its owner's to move off the host. %s", p.Site, j, dir, what, j, dir, dir, changed)
}

func (p *Plan) otherCluster(j string, id uint64, how, changed string) error {
	return fmt.Errorf("site add %s: %s's etcd data, %s, belongs to etcd cluster %x (%s), and this deployment's etcd, read on %s, is cluster %x. site add deletes a joiner's old etcd data only when it is a member this cluster removed. That data is another cluster's: the host's owner moves it off the host, or the site is taken out of this deployment. %s", p.Site, j, apply.EtcdDataDir(p.dep()), id, how, p.control, p.clusterID, changed)
}

// readRemoved reads every etcd member removal the deployment's hosts
// record, from every site's host, so a removal recorded on any of them is
// known whichever hosts have since been rebuilt or replaced.
func (p *Plan) readRemoved() error {
	var all []apply.EtcdRemoved
	for _, name := range p.cfg.SiteNames() {
		removed, err := apply.ReadEtcdRemoved(p.transports[name], p.dep())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		all = append(all, removed...)
	}
	p.removed = all
	return nil
}

// recordRemoved records, on the host of every other etcd member, that this
// cluster removes j's member id, before it is removed: checkEtcdData accepts
// j's data later only on such a record.
func (p *Plan) recordRemoved(j string, id uint64) error {
	r := apply.EtcdRemoved{Site: j, Cluster: p.clusterID, Member: id}
	for _, name := range p.cfg.Etcd.Members {
		if name == j {
			continue
		}
		if err := apply.RecordEtcdRemoved(p.transports[name], p.dep(), r); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	p.removed = append(p.removed, r)
	return nil
}

// joinInitial is the --initial-cluster-state and --initial-cluster a joiner
// is started with. A member that has started keeps what its host records,
// the flags it was first started with, so its compose file does not change.
// Any other joiner starts from an empty data directory and is given cluster,
// the membership as it stands with it added, which etcd requires to match
// exactly; a record its host keeps from an earlier member names a membership
// that has since changed.
func joinInitial(recorded render.EtcdInitial, found, started bool, cluster string) render.EtcdInitial {
	if found && started && recorded.State == render.EtcdStateExisting {
		return recorded
	}
	return render.EtcdInitial{State: render.EtcdStateExisting, Cluster: cluster}
}

// startedMember reports whether site's etcd member is in the member list and
// has started.
func startedMember(cfg *config.Config, live []apply.EtcdMember, site string) bool {
	for _, m := range live {
		if apply.EtcdMemberSite(cfg, m) == site && m.Started() {
			return true
		}
	}
	return false
}

func peerURL(m apply.EtcdMember) string {
	if len(m.PeerURLs) > 0 {
		return m.PeerURLs[0]
	}
	return ""
}

// clusterString is an --initial-cluster value, sorted by name the way the
// render sorts a founding one.
func clusterString(members map[string]string) string {
	var names []string
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		parts = append(parts, name+"="+members[name])
	}
	return strings.Join(parts, ",")
}

func (p *Plan) members() ([]apply.EtcdMember, error) {
	out, err := p.transports[p.control].Run(apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, "member list -w json"))
	if err != nil {
		return nil, fmt.Errorf("%s: etcd's member list: %w", p.control, err)
	}
	return apply.ParseEtcdMembers(out)
}

func (p *Plan) find(members []apply.EtcdMember, site string) *apply.EtcdMember {
	for i := range members {
		if apply.EtcdMemberSite(p.cfg, members[i]) == site {
			return &members[i]
		}
	}
	return nil
}

// prepareJoiner readies a joiner whose member has not started, m being its
// unstarted learner or nil, and returns the member to carry on with: nil to
// add, or a learner that started meanwhile, or a voter.
//
// Its etcd container is removed first, found by this deployment's labels, so
// nothing runs there while its data is read and cleared and an unstarted
// learner cannot start. An unstarted learner is then read again: one that
// started before its container went is a started learner, whose data is its
// own and is not touched. Then the data is read on disk, and only when
// checkEtcdData accepts it is a learner still unstarted removed from etcd,
// recorded first, since it may have received a snapshot or log entries the
// leader remembers, and the data cleared.
func (p *Plan) prepareJoiner(j string, m *apply.EtcdMember) (*apply.EtcdMember, error) {
	t := p.transports[j]
	if err := p.knowCluster(j); err != nil {
		return nil, err
	}
	p.work("stop etcd on " + j)
	if out, err := t.Run(apply.RemoveEtcdContainers(p.dep())); err != nil {
		return nil, fmt.Errorf("%s: removing its etcd container: %w: %s. Its etcd data was not touched", j, err, lastLines(out, 3))
	}
	members, err := p.members()
	if err != nil {
		return nil, err
	}
	var learner uint64
	if m != nil {
		if m = p.find(members, j); m != nil && (!m.IsLearner || m.Started()) {
			return m, nil
		}
		if m != nil {
			learner = m.ID
		}
	}
	// The data is read before the learner is removed: with its container
	// gone it can no longer start or write, so what is read is what the
	// clear deletes, and a refusal leaves the learner in etcd.
	p.work("clear old etcd data on "+j).Detail("%s, on %s", apply.EtcdDataDir(p.dep()), j)
	if err := p.checkEtcdData(j, members, learner, "Its etcd container was removed; its etcd data was not touched"); err != nil {
		return nil, err
	}
	if m != nil {
		p.work("remove unstarted learner "+j).Detail("%s's learner %s, through %s", j, m.HexID(), p.control)
		if err := p.recordRemoved(j, m.ID); err != nil {
			return nil, err
		}
		if out, err := p.transports[p.control].Run(apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, "member remove "+m.HexID())); err != nil {
			return nil, fmt.Errorf("%s: removing %s's unstarted learner %s: %s", p.control, j, m.HexID(), lastLines(out, 3))
		}
		p.work("clear old etcd data on " + j)
	}
	if out, err := t.Run(p.clearEtcdData()); err != nil {
		return nil, fmt.Errorf("%s: clearing its old etcd data, %s: %w: %s. Its etcd was not started", j, apply.EtcdDataDir(p.dep()), err, lastLines(out, 3))
	}
	return nil, nil
}

// joinEtcd takes one joiner from wherever it is to a voter. Every step reads
// the cluster first, so a run that stopped half way picks up where it was.
func (p *Plan) joinEtcd(j string) error {
	control := p.transports[p.control]
	t := p.transports[j]
	members, err := p.members()
	if err != nil {
		return err
	}
	m := p.find(members, j)
	if m != nil && !m.IsLearner {
		return nil
	}
	if m == nil || !m.Started() {
		if m, err = p.prepareJoiner(j, m); err != nil {
			return err
		}
		if m != nil && !m.IsLearner {
			return nil
		}
	}
	if m == nil {
		url := render.EtcdPeerURL(p.cfg.Sites[j].Address)
		p.work("add etcd learner "+j).Detail("%s as an etcd learner, through %s", j, p.control)
		// etcd's strict reconfiguration check refuses a membership change
		// with "unhealthy cluster" until every voter has been connected for
		// its health interval, five seconds in v3.5.16
		// (server/etcdserver/server.go, mayAddMember and
		// isConnectedToQuorumSince). On the first real join the second
		// learner was added moments after the first was promoted and was
		// refused for exactly that, so the add is retried on that answer
		// alone, the way promotion is retried while a learner catches up.
		command := apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, fmt.Sprintf("member add %s --peer-urls=%s --learner", j, url))
		delay := promoteFirstDelay
		for i := 0; ; i++ {
			out, err := control.Run(command)
			if err == nil {
				break
			}
			if !strings.Contains(out, "unhealthy cluster") || i >= promoteAttempts-1 {
				return fmt.Errorf("%s: adding %s as a learner: %s", p.control, j, lastLines(out, 3))
			}
			sleep(delay)
			delay *= 2
			if delay > promoteMaxDelay {
				delay = promoteMaxDelay
			}
		}
		if members, err = p.members(); err != nil {
			return err
		}
		if m = p.find(members, j); m == nil {
			return fmt.Errorf("%s: %s was added, and is not in the member list", p.control, j)
		}
	}

	// What the member is born with: see joinInitial.
	recorded, found, err := apply.ReadEtcdInitial(t, p.dep())
	if err != nil {
		return fmt.Errorf("%s: %w", j, err)
	}
	current := map[string]string{}
	for _, member := range members {
		current[apply.EtcdMemberSite(p.cfg, member)] = peerURL(member)
	}
	p.initial[j] = joinInitial(recorded, found, m.Started(), clusterString(current))

	p.work("write etcd files on " + j)
	rendered, err := p.render()
	if err != nil {
		return err
	}
	paths := []string{p.dep().RelPath("infra", "compose.yaml"), render.EtcdInitialPath(p.dep())}
	// compose parses every service's env_file when it loads the project, so
	// each one the site's infrastructure stack names has to exist before its
	// etcd can start: a data site's patroni.env, and on a site the join
	// brings in from blank, a new witness, the Caddy and heartbeat files its
	// other services name.
	for _, env := range []string{
		p.dep().RelPath("infra", "patroni.env"),
		p.dep().RelPath("infra", "caddy", "caddy.env"),
		p.dep().RelPath("infra", "heartbeat", "heartbeat.env"),
	} {
		if renders(rendered, j, env) {
			paths = append(paths, env)
		}
	}
	sp, err := apply.Build(j, rendered, "", t, apply.Scope(paths...))
	if err != nil {
		return err
	}
	if c := sp.Conflicts(); len(c) > 0 {
		return fmt.Errorf("%s: %s differs from what the last apply recorded, so somebody edited it on the host. The learner was added and not started; restore the file and run site add again", j, c[0].Path)
	}
	if err := apply.Execute(sp, t); err != nil {
		return err
	}
	p.work("start etcd on " + j)
	if out, err := t.Run(p.startEtcd()); err != nil {
		return fmt.Errorf("%s: starting etcd: %s", j, lastLines(out, 5))
	}

	n := attempts(learnerWait, learnerPoll)
	for i := 0; ; i++ {
		if members, err = p.members(); err != nil {
			return err
		}
		if m = p.find(members, j); m != nil && m.Started() {
			break
		}
		if i >= n-1 {
			return fmt.Errorf("%s's etcd has not joined after %s: it is a learner that never started. Read `%s logs etcd` on %s", j, learnerWait, p.dep().ComposeCmd("infra"), j)
		}
		sleep(learnerPoll)
	}

	p.work("promote " + j + " to voter")
	delay := promoteFirstDelay
	var last string
	for i := 0; i < promoteAttempts; i++ {
		out, err := control.Run(apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, "member promote "+m.HexID()))
		if err == nil {
			return nil
		}
		last = lastLines(out, 3)
		if i < promoteAttempts-1 {
			sleep(delay)
			delay *= 2
			if delay > promoteMaxDelay {
				delay = promoteMaxDelay
			}
		}
	}
	return fmt.Errorf("%s: etcd would not promote %s after %d attempts, so it is still a learner and the cluster still has its quorum. Last answer: %s", p.control, j, promoteAttempts, last)
}

// etcdGate waits for every member of etcd.members to be a voter and every
// member to answer healthy. etcdctl exits 1 when any endpoint is unhealthy
// and still prints every endpoint, so its list is read whatever the exit and
// an unhealthy member is named.
func (p *Plan) etcdGate() error {
	want := sortedCopy(p.cfg.Etcd.Members)
	var problems []string
	n := attempts(etcdWait, etcdPoll)
	for i := 0; i < n; i++ {
		problems = nil
		out, err := p.transports[p.control].Run(apply.Etcdctl(p.dep(), p.cfg.Sites[p.control].Address, "endpoint health --cluster -w json"))
		if health, ok := apply.ParseEtcdEndpointHealth(out); !ok {
			problems = append(problems, "endpoint health: "+lastLines(out, 3))
		} else {
			listed := map[string]bool{}
			for _, h := range health {
				listed[apply.EtcdEndpointSite(p.cfg, h.Endpoint)] = true
				if !h.Health {
					problems = append(problems, apply.EtcdEndpointSite(p.cfg, h.Endpoint)+"'s etcd is unhealthy"+apply.EtcdHealthError(h))
				}
			}
			for _, site := range want {
				if !listed[site] {
					problems = append(problems, site+"'s etcd is not in etcd's endpoint health, so it is not known to be healthy")
				}
			}
			if err != nil && len(problems) == 0 {
				problems = append(problems, "endpoint health: "+err.Error())
			}
		}
		members, err := p.members()
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			var voters []string
			for _, m := range members {
				site := apply.EtcdMemberSite(p.cfg, m)
				if m.IsLearner {
					problems = append(problems, site+" is still a learner")
					continue
				}
				voters = append(voters, site)
			}
			sort.Strings(voters)
			if strings.Join(voters, ",") != strings.Join(want, ",") {
				problems = append(problems, fmt.Sprintf("the voters are %s, and etcd.members is %s", strings.Join(voters, ", "), strings.Join(want, ", ")))
			}
		}
		if len(problems) == 0 {
			return nil
		}
		if i < n-1 {
			sleep(etcdPoll)
		}
	}
	return fmt.Errorf("etcd is not whole after %s:\n  %s", etcdWait, strings.Join(problems, "\n  "))
}

func sortedCopy(list []string) []string {
	out := append([]string(nil), list...)
	sort.Strings(out)
	return out
}
