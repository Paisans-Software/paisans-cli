package apply

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// LeaderMove is Patroni's leader key leaving a site during that site's
// apply: From led when the infrastructure stack's action started, and To
// leads once it is done.
type LeaderMove struct {
	From, To string
	// Reason is what the apply did on From, as the operator reads it.
	Reason string
}

// leaderWatch is told of every LeaderMove an apply in this process finds,
// nil for none. See OnLeaderMove.
var (
	leaderWatchMu sync.Mutex
	leaderWatch   func(LeaderMove)
)

// OnLeaderMove has f told of every LeaderMove an apply finds from now on, so
// a command that runs applies as steps of its own can say so where the
// operator reads its result. It returns a function restoring the watcher
// before it.
func OnLeaderMove(f func(LeaderMove)) (restore func()) {
	leaderWatchMu.Lock()
	defer leaderWatchMu.Unlock()
	old := leaderWatch
	leaderWatch = f
	return func() {
		leaderWatchMu.Lock()
		defer leaderWatchMu.Unlock()
		leaderWatch = old
	}
}

// ledBefore asks this site's own Patroni, once, whether it leads, just
// before the infrastructure stack's action, with curl in the Spilo container
// as the wait for a primary asks. GET /patroni answers with the node's own
// role, "primary" on the leader (Patroni v4.1.0, patroni/api.py,
// get_postgresql_status; "master" before v4). Any other answer, or none, as
// on a first apply with no Patroni yet, is not leading.
func ledBefore(plan *Plan, t Transport) bool {
	out, err := t.Run(fmt.Sprintf("%s curl -s --max-time 5 http://%s/patroni", patroniCompose(plan.Deployment), plan.Bootstrap.Patroni))
	if err != nil {
		return false
	}
	var status struct {
		Role string `json:"role"`
	}
	if json.Unmarshal([]byte(out), &status) != nil {
		return false
	}
	return status.Role == "primary" || status.Role == "master"
}

// leaderMoved is called once the wait for a primary has found leader. When
// this site led before the infrastructure stack's action and another site
// leads now, the action moved the leader: Patroni stopping on a leader
// lets a standby take the leader key, and a leader that starts again does not
// take it back. That is a switchover the operator did not ask for, and every
// app's database connections end with it, so it is said at every verbosity
// and never left for --verbose.
func (p *Plan) leaderMoved(leader string) {
	if !p.ledBefore || leader == "" || leader == p.Site {
		return
	}
	m := LeaderMove{From: p.Site, To: leader, Reason: p.infraReason(leader)}
	p.LeaderMoved = &m
	p.warn(fmt.Sprintf("Patroni's leader moved from %s to %s", m.From, m.To), m.Reason)
	leaderWatchMu.Lock()
	watch := leaderWatch
	leaderWatchMu.Unlock()
	if watch != nil {
		watch(m)
	}
}

// infraReason says what the infrastructure stack's action was and which of
// its files changed, for a LeaderMove.
func (p *Plan) infraReason(to string) string {
	var action Action
	for _, a := range p.Actions {
		if a.Stack == infraStack {
			action = a
		}
	}
	var changed []string
	for _, c := range p.Writes() {
		if c.Stack == infraStack {
			changed = append(changed, c.Path)
		}
	}
	why := action.Reason
	if len(changed) > 0 {
		why = "changed " + strings.Join(changed, ", ") + "; " + why
	}
	return fmt.Sprintf("this apply ran `%s` on %s (%s), which stopped Patroni there while it led, and %s took the leader key. %s leads from now on, and %s follows it as a replica; nothing moves the leader back on its own. `%s doctor` shows the cluster as it is now",
		ActionTitle(action), p.Site, why, to, to, p.Site, progname.Cmd())
}
