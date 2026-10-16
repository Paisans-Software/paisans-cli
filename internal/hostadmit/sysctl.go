package hostadmit

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

const (
	keyLowPorts = "net.ipv4.ip_unprivileged_port_start"
	keyForward  = "net.ipv4.ip_forward"
)

// SysctlFile is /etc/sysctl.d/60-paisans.conf as admit writes it: a line
// for each key some admitted deployment needs. It is shared by every
// admitted deployment.
func SysctlFile(low, forward bool) string {
	out := "# Written by paisans host admit, for every admitted deployment that needs\n" +
		"# it; paisans host evict restores each value once none does.\n"
	if low {
		out += keyLowPorts + " = 80\n"
	}
	if forward {
		out += keyForward + " = 1\n"
	}
	return out
}

// before is /etc/paisans/sysctl.before: each key's value as admit found it
// before it first changed it, which evict restores.
type before map[string]string

func parseBefore(content string) before {
	b := before{}
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			b[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return b
}

// check refuses a before file holding anything but a value for one of
// admit's keys, in digits: its values reach a command line run as root.
// by is the command that read it, admit or evict.
func (b before) check(by string) error {
	for k, v := range b {
		if (k != keyLowPorts && k != keyForward) || v == "" || strings.Trim(v, "0123456789") != "" {
			return fmt.Errorf("%s holds %s, which is not a value admit wrote. Remove the line or the file and %s again. Nothing was changed", sysctlBeforePath, ui.Visible(k+" = "+v), by)
		}
	}
	return nil
}

func (b before) text() string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "# Written by paisans host admit: each value as it was before admit\n# first changed it. paisans host evict restores them.\n"
	for _, k := range keys {
		out += k + " = " + b[k] + "\n"
	}
	return out
}

// others is every other admitted deployment's readable record on the host,
// and the names of the records that cannot be read, a newer paisans' among
// them. An unreadable record is not guessed at: planSysctls keeps what
// 60-paisans.conf holds for it and says so.
func others(p *planner) ([]admission.Record, []string, error) {
	var out []admission.Record
	var unreadable []string
	for _, file := range p.f.Records {
		token := strings.TrimSuffix(strings.TrimPrefix(path.Base(file), "admitted."), ".json")
		if token == p.d.Token() {
			continue
		}
		content, _, err := p.t.ReadFile(file)
		if err != nil {
			return nil, nil, shown(err)
		}
		rec, err := admission.ParseRecord([]byte(content), token)
		if err != nil {
			unreadable = append(unreadable, file)
			p.warn(ui.Visible("sysctls: "+file+" cannot be read, so the sysctls are kept as they are for its deployment"), visibleLines(ui.RedactError(err).Error()))
			continue
		}
		out = append(out, rec)
	}
	return out, unreadable, nil
}

// checkSysctls is a shell check that each key 60-paisans.conf sets has its
// value, failing with the key's name otherwise.
func checkSysctls(low, forward bool) string {
	var checks []string
	for _, kv := range [][3]string{{keyLowPorts, "80", fmt.Sprint(low)}, {keyForward, "1", fmt.Sprint(forward)}} {
		if kv[2] == "true" {
			checks = append(checks, `{ [ "$(sysctl -n `+kv[0]+`)" = `+kv[1]+` ] || { echo '`+kv[0]+` is not `+kv[1]+` after sysctl --system: a file read after `+sysctlPath+` sets it'; exit 1; }; }`)
		}
	}
	return strings.Join(checks, " && ")
}

// holds reports whether the sysctl file content sets key.
func holds(content, key string) bool {
	for _, line := range strings.Split(content, "\n") {
		k, _, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

// sysctlState is what the sysctl parts plan from, read once per Build:
// which keys some admitted deployment needs, which 60-paisans.conf sets,
// the before file as it is (was), as first recorded (first: was with the
// keys this run turns on, at their values now), and as left once the keys
// no deployment needs are restored (b, with restore the sysctl -w
// arguments).
type sysctlState struct {
	// others are the other admitted deployments' readable records.
	others           []admission.Record
	wantLow, wantFwd bool
	was, first, b    before
	restore          []string
	lowFor, fwdFor   []string
}

// sysctls reads sysctlState for p, once: the record of another deployment
// that cannot be read is warned about once.
func (p *planner) sysctls() (*sysctlState, error) {
	if p.sx != nil {
		return p.sx, nil
	}
	low, fwd := p.in.Flags.PublicPorts, p.in.Request.Endpoint
	st := &sysctlState{}
	recs, unreadable, err := others(p)
	if err != nil {
		return nil, err
	}
	st.others = recs
	current, _, err := p.t.ReadFile(sysctlPath)
	if err != nil {
		return nil, shown(err)
	}
	// A key 60-paisans.conf sets is paisans' to keep for a record admit
	// cannot read, and its live value is not the host's own.
	setLow, setFwd := holds(current, keyLowPorts), holds(current, keyForward)
	for _, r := range recs {
		if r.Grants.PublicPorts {
			st.lowFor = append(st.lowFor, ui.Visible(r.Deployment))
		}
		if r.Forwarding {
			st.fwdFor = append(st.fwdFor, ui.Visible(r.Deployment))
		}
	}
	keep := len(unreadable) > 0
	st.wantLow, st.wantFwd = low || len(st.lowFor) > 0 || keep && setLow, fwd || len(st.fwdFor) > 0 || keep && setFwd
	for _, f := range []string{sysctlPath, sysctlBeforePath} {
		if p.f.Files[f].Link {
			return nil, fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", f)
		}
	}
	beforeContent, _, err := p.t.ReadFile(sysctlBeforePath)
	if err != nil {
		return nil, shown(err)
	}
	st.was = parseBefore(beforeContent)
	if err := st.was.check("admit"); err != nil {
		return nil, err
	}
	st.first, st.b = before{}, before{}
	for k, v := range st.was {
		st.first[k], st.b[k] = v, v
	}
	// The first admit to change a key records what this Build's read of
	// the host found; once 60-paisans.conf sets the key the live value is
	// paisans', not the host's. A key recorded already keeps its value.
	if _, ok := st.first[keyLowPorts]; st.wantLow && !setLow && !ok {
		st.first[keyLowPorts], st.b[keyLowPorts] = p.f.PortStart, p.f.PortStart
	}
	if _, ok := st.first[keyForward]; st.wantFwd && !setFwd && !ok {
		st.first[keyForward], st.b[keyForward] = p.f.IPForward, p.f.IPForward
	}
	for _, k := range []struct {
		key  string
		want bool
	}{{keyLowPorts, st.wantLow}, {keyForward, st.wantFwd}} {
		if v, ok := st.b[k.key]; !k.want && ok {
			st.restore = append(st.restore, k.key+"="+v)
			delete(st.b, k.key)
		}
	}
	p.sx = st
	return st, nil
}

// recordBefore is the step that writes content to sysctl.before.
func recordBefore(p *planner, content string) {
	p.step(Step{Title: "record the sysctls' values before", Describe: "sysctls: " + sysctlBeforePath + ", for host evict to restore", File: &hostprep.File{Path: sysctlBeforePath, Content: content, Mode: 0o644}, Kinds: []string{"sysctl"}})
}

// planSysctlsBefore records, before anything else changes the host, the
// value each key this run turns on has now, so evict restores the host's
// own: a dockerd that starts turns forwarding on, and admit's install holds
// the system daemon so that it does not, but a run that stopped after one
// started would otherwise read that value on its next try. A key the file
// holds already keeps the value it holds.
func planSysctlsBefore(p *planner) error {
	st, err := p.sysctls()
	if err != nil {
		return err
	}
	if len(st.first) > 0 && st.first.text() != st.was.text() {
		recordBefore(p, st.first.text())
	}
	return nil
}

// planSysctls keeps 60-paisans.conf holding a key while any admitted
// deployment needs it: the low ports for one granted them, forwarding for
// one with an endpoint, which relays mesh traffic between sites behind
// NAT. A key no deployment needs any more is restored to its recorded
// value, and only then forgotten, so a restore that fails leaves the
// value for the next run.
func planSysctls(p *planner) error {
	st, err := p.sysctls()
	if err != nil {
		return err
	}
	if p.in.Flags.PublicPorts {
		p.warn("public ports: every local user on this host may bind ports 80 to 1023", keyLowPorts+" = 80 is host wide: no user's process needs root for a port from 80 up, the deployment's Caddy among them.")
	}
	if p.in.Request.Endpoint {
		p.warn("forwarding: this turns on routing between every interface on this host", "The site has an endpoint, so it relays mesh traffic between sites behind NAT, which cannot dial each other. The firewall's forward policy governs what is routed.")
	} else if len(st.fwdFor) == 0 {
		p.present("forwarding: not needed, this site has no endpoint")
	}
	file := SysctlFile(st.wantLow, st.wantFwd)
	live := (!st.wantLow || p.f.PortStart == "80") && (!st.wantFwd || p.f.IPForward == "1")
	switch {
	case !st.wantLow && !st.wantFwd:
		if p.f.Files[sysctlPath].Present {
			p.step(Step{Title: "remove " + sysctlPath, Describe: "sysctls: no admitted deployment needs it", Command: "rm -f " + sysctlPath, Kinds: []string{"sysctl"}})
		}
	case p.f.Files[sysctlPath].Sum != sha(file) || !live:
		p.step(Step{Title: "set the host's sysctls", Describe: "sysctls: " + sysctlPath + ", applied with sysctl --system, which applies every sysctl file on the host again, then each key checked, since a file read after it could set one back",
			File: &hostprep.File{Path: sysctlPath, Content: file, Mode: 0o644}, Command: "sysctl --system >/dev/null && " + checkSysctls(st.wantLow, st.wantFwd), Kinds: []string{"sysctl"}})
	default:
		p.present("sysctls: " + sysctlPath + " as written and applied")
	}
	if len(st.restore) > 0 {
		p.step(Step{Title: "restore " + strings.Join(st.restore, ", "), Describe: "sysctls: no admitted deployment needs them any more; set each back to what it was before admit",
			Command: "sysctl -w " + strings.Join(st.restore, " ") + " >/dev/null", Kinds: []string{"sysctl"}})
		switch {
		case len(st.b) == 0 && (p.f.Files[sysctlBeforePath].Present || st.first.text() != st.was.text()):
			p.step(Step{Title: "remove " + sysctlBeforePath, Describe: "sysctls: nothing left to restore", Command: "rm -f " + sysctlBeforePath, Kinds: []string{"sysctl"}})
		case len(st.b) > 0 && st.b.text() != st.first.text():
			recordBefore(p, st.b.text())
		}
	}
	for _, d := range st.lowFor {
		p.present("sysctls: low ports kept for deployment " + d)
	}
	for _, d := range st.fwdFor {
		p.present("sysctls: forwarding kept for deployment " + d)
	}
	return nil
}
