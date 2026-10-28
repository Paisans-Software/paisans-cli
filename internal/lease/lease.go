// Package lease is the deployment's lease: a record on every site, written
// under the deployment's one lock, saying which command of which operator is
// changing the deployment. A command that changes the deployment takes it on
// every site it reaches before it publishes or changes anything, renews it
// while it runs and releases it at its end, and a second command finding it
// live is refused, naming who holds it.
//
// The configuration store already refuses a fork, but only at the next
// gather: two applies drafted from one version both pass their gathers, both
// publish, and both go on changing hosts. The per-site lock serialises one
// write at a time and nothing longer. The lease is what makes a whole run
// exclusive, from its first write to its last.
//
// A lease is live while its holder renews it. A holder that dies, its
// workstation lost or its ssh cut, stops renewing, and the lease expires
// TTL after the last renewal, judged by the site's own clock against the
// time it wrote, so two workstations' clocks never need to agree. An
// operator who knows a holder is gone takes the lease over before then by
// typing a word (see cmd/paisans).
package lease

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// TTL is how long a lease stays live after its last renewal. Long enough to
// outlast a slow ssh round trip and a step that holds the connection, short
// enough that a dead holder blocks the next operator for minutes, not hours.
const TTL = 2 * time.Minute

// Heartbeat is how often a held lease is renewed: four renewals inside TTL,
// so a renewal or two lost to a slow host never lets it expire.
var Heartbeat = 30 * time.Second

// TakenMarker is what a site prints once it has written or renewed a
// lease, and what it prints for a release too; a test's host answers with
// it.
const TakenMarker = "paisans: the deployment lease is taken"

const heldMarker = "paisans: the deployment lease is held"

// Runner is how a site is reached: one shell command, its output.
type Runner interface {
	Run(command string) (string, error)
}

// Path is the lease on a site, in the deployment's state directory, which
// only the deployment's user writes.
func Path(d deployment.Deployment) string { return d.StatePath("lease") }

// Holder is who takes a lease: an id fresh for each command, and for a
// person reading it who and what.
type Holder struct {
	ID string
	// Who is the operator, Eg: "alice@laptop".
	Who string
	// Command is what holds it, Eg: "deployment apply".
	Command string
}

// NewHolder is a holder with a fresh id.
func NewHolder(who, command string) (Holder, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Holder{}, err
	}
	return Holder{ID: hex.EncodeToString(b[:]), Who: oneLine(who), Command: oneLine(command)}, nil
}

// Other is a live lease another command holds on Site.
type Other struct {
	Site    string
	Who     string
	Command string
	// Held is how long it has been held, Idle how long since it was
	// last renewed, both by the site's clock.
	Held, Idle time.Duration
}

// HeldError is a lease refused because another command holds it.
type HeldError struct{ Other Other }

func (e *HeldError) Error() string {
	o := e.Other
	return fmt.Sprintf("%s: %s by %s holds the deployment lease, taken %s ago and renewed %s ago", o.Site, o.Command, o.Who, round(o.Held), round(o.Idle))
}

// ErrLost is a renewal that found the lease taken over: another operator
// took it as expired or by typing the word, and this command no longer
// holds it.
var ErrLost = errors.New("the deployment lease was taken over by another command")

// takeCommand writes h's lease, unless another live one is there and
// takeover is false, in which case it prints that one. It prints a marker
// either way and exits 0, so a refusal is told from an ssh failure. A lease
// of h's own is renewed, keeping when it was first taken.
func takeCommand(d deployment.Deployment, h Holder, takeover bool) string {
	f := quote(Path(d))
	over := "0"
	if takeover {
		over = "1"
	}
	return strings.Join([]string{
		d.StateLockCommand("the deployment lease"),
		"f=" + f,
		"now=$(date +%s)",
		"held=''",
		"renewed=0",
		`started=$now`,
		`if [ -f "$f" ]; then held=$(sed -n 's/^id=//p' "$f"); renewed=$(sed -n 's/^renewed=//p' "$f"); fi`,
		fmt.Sprintf(`if [ -n "$held" ] && [ "$held" != %s ] && [ $((now - ${renewed:-0})) -lt %d ] && [ %s != 1 ]; then echo %s; echo "now=$now"; cat "$f"; exit 0; fi`, quote(h.ID), int(TTL.Seconds()), over, quote(heldMarker)),
		fmt.Sprintf(`if [ "$held" = %s ]; then started=$(sed -n 's/^started=//p' "$f"); fi`, quote(h.ID)),
		fmt.Sprintf(`printf 'id=%%s\nwho=%%s\ncommand=%%s\nstarted=%%s\nrenewed=%%s\n' %s %s %s "${started:-$now}" "$now" > "$f.tmp" && mv -f "$f.tmp" "$f"`, quote(h.ID), quote(h.Who), quote(h.Command)),
		"echo " + quote(TakenMarker),
	}, "; ")
}

// releaseCommand removes h's lease, and leaves one another command holds.
func releaseCommand(d deployment.Deployment, h Holder) string {
	f := quote(Path(d))
	return strings.Join([]string{
		d.StateLockCommand("the deployment lease"),
		"f=" + f,
		fmt.Sprintf(`if [ -f "$f" ] && [ "$(sed -n 's/^id=//p' "$f")" = %s ]; then rm -f "$f"; fi`, quote(h.ID)),
	}, "; ")
}

// take takes or renews h's lease on one site.
func take(t Runner, site string, d deployment.Deployment, h Holder, takeover bool) error {
	out, err := t.Run(takeCommand(d, h, takeover))
	if err != nil {
		return err
	}
	switch {
	case strings.Contains(out, TakenMarker):
		return nil
	case strings.Contains(out, heldMarker):
		return &HeldError{Other: parseOther(site, out)}
	}
	return fmt.Errorf("%s: the lease command printed neither answer: %s", site, strings.TrimSpace(out))
}

func parseOther(site, out string) Other {
	v := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, val, ok := strings.Cut(line, "="); ok {
			v[k] = val
		}
	}
	num := func(k string) int64 { n, _ := strconv.ParseInt(v[k], 10, 64); return n }
	now := num("now")
	return Other{Site: site, Who: v["who"], Command: v["command"],
		Held: time.Duration(now-num("started")) * time.Second, Idle: time.Duration(now-num("renewed")) * time.Second}
}

// Lease is a lease held on some sites, renewed every Heartbeat until
// Release.
type Lease struct {
	holder Holder
	d      deployment.Deployment
	sites  map[string]Runner
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	lost   error
	once   sync.Once
}

// Take takes h's lease on every site at once. A site that refuses, holding
// another command's live lease, refuses the whole take: the sites it took
// are released, and the error is a *HeldError. A site that cannot be reached
// is left out, and named in unreached, so a site that is down never blocks
// a run; at least one site must take it. takeover takes it whatever another
// holds.
func Take(sites map[string]Runner, d deployment.Deployment, h Holder, takeover bool) (l *Lease, unreached map[string]error, err error) {
	errs := each(sites, func(site string, t Runner) error { return take(t, site, d, h, takeover) })
	taken := map[string]Runner{}
	unreached = map[string]error{}
	var held *HeldError
	for site, e := range errs {
		var he *HeldError
		switch {
		case e == nil:
			taken[site] = sites[site]
		case errors.As(e, &he):
			if held == nil || site < held.Other.Site {
				held = he
			}
		default:
			unreached[site] = e
		}
	}
	if held != nil || len(taken) == 0 {
		each(taken, func(_ string, t Runner) error { _, err := t.Run(releaseCommand(d, h)); return err })
		if held != nil {
			return nil, unreached, held
		}
		return nil, unreached, fmt.Errorf("no site took the deployment lease")
	}
	l = &Lease{holder: h, d: d, sites: taken, stop: make(chan struct{}), done: make(chan struct{})}
	go l.renew()
	return l, unreached, nil
}

func (l *Lease) renew() {
	defer close(l.done)
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-tick.C:
			for site, e := range each(l.sites, func(site string, t Runner) error { return take(t, site, l.d, l.holder, false) }) {
				var he *HeldError
				if errors.As(e, &he) {
					l.mu.Lock()
					l.lost = fmt.Errorf("%w on %s: %s by %s holds it now", ErrLost, site, he.Other.Command, he.Other.Who)
					l.mu.Unlock()
				}
			}
		}
	}
}

// Lost is why the lease is no longer held, or nil while it is.
func (l *Lease) Lost() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost
}

// Holder is who holds it.
func (l *Lease) Holder() Holder { return l.holder }

// Covers is whether the lease is held on site.
func (l *Lease) Covers(site string) bool {
	if l == nil {
		return false
	}
	_, ok := l.sites[site]
	return ok
}

// Release stops renewing and removes the lease from every site it was
// taken on, leaving any another command holds since. A site it cannot reach
// keeps it until it expires. It is safe to call more than once and on nil.
func (l *Lease) Release() map[string]error {
	if l == nil {
		return nil
	}
	var errs map[string]error
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		errs = map[string]error{}
		for site, e := range each(l.sites, func(_ string, t Runner) error { _, err := t.Run(releaseCommand(l.d, l.holder)); return err }) {
			if e != nil {
				errs[site] = e
			}
		}
	})
	return errs
}

// each runs work on every site at once, and returns each site's error.
func each(sites map[string]Runner, work func(site string, t Runner) error) map[string]error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]error{}
	for site, t := range sites {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := work(site, t)
			mu.Lock()
			out[site] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func round(d time.Duration) string { return d.Round(time.Second).String() }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
