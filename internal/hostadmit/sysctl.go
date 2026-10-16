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

// planSysctls keeps 60-paisans.conf holding a key while any admitted
// deployment needs it: the low ports for one granted them, forwarding for
// one with an endpoint, which relays mesh traffic between sites behind
// NAT. The value each key had before admit first changed it is recorded,
// for evict to restore.
func planSysctls(p *planner) error {
	low, fwd := p.in.Flags.PublicPorts, p.in.Request.Endpoint
	var lowFor, fwdFor []string
	recs, unreadable, err := others(p)
	if err != nil {
		return err
	}
	current, _, err := p.t.ReadFile(sysctlPath)
	if err != nil {
		return shown(err)
	}
	// A key 60-paisans.conf sets is paisans' to keep for a record admit
	// cannot read, and its live value is not the host's own.
	setLow, setFwd := holds(current, keyLowPorts), holds(current, keyForward)
	for _, r := range recs {
		if r.Grants.PublicPorts {
			lowFor = append(lowFor, ui.Visible(r.Deployment))
		}
		if r.Forwarding {
			fwdFor = append(fwdFor, ui.Visible(r.Deployment))
		}
	}
	keep := len(unreadable) > 0
	wantLow, wantFwd := low || len(lowFor) > 0 || keep && setLow, fwd || len(fwdFor) > 0 || keep && setFwd
	if low {
		p.warn("public ports: every local user on this host may bind ports 80 to 1023", keyLowPorts+" = 80 is host wide: no user's process needs root for a port from 80 up, the deployment's Caddy among them.")
	}
	if fwd {
		p.warn("forwarding: this turns on routing between every interface on this host", "The site has an endpoint, so it relays mesh traffic between sites behind NAT, which cannot dial each other. The firewall's forward policy governs what is routed.")
	} else if len(fwdFor) == 0 {
		p.present("forwarding: not needed, this site has no endpoint")
	}
	for _, f := range []string{sysctlPath, sysctlBeforePath} {
		if p.f.Files[f].Link {
			return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", f)
		}
	}
	beforeContent, _, err := p.t.ReadFile(sysctlBeforePath)
	if err != nil {
		return shown(err)
	}
	was := parseBefore(beforeContent)
	if err := was.check("admit"); err != nil {
		return err
	}
	b := before{}
	for k, v := range was {
		b[k] = v
	}
	// The first admit to change a key records what it found; once
	// 60-paisans.conf sets the key the live value is paisans', not the host's.
	if wantLow && !setLow {
		if _, ok := b[keyLowPorts]; !ok {
			b[keyLowPorts] = p.f.PortStart
		}
	}
	if wantFwd && !setFwd {
		if _, ok := b[keyForward]; !ok {
			b[keyForward] = p.f.IPForward
		}
	}
	var restore []string
	if !wantLow {
		if v, ok := b[keyLowPorts]; ok {
			restore = append(restore, keyLowPorts+"="+v)
			delete(b, keyLowPorts)
		}
	}
	if !wantFwd {
		if v, ok := b[keyForward]; ok {
			restore = append(restore, keyForward+"="+v)
			delete(b, keyForward)
		}
	}
	if len(b) > 0 && b.text() != was.text() {
		p.step(Step{Title: "record the sysctls' values before", Describe: "sysctls: " + sysctlBeforePath + ", for host evict to restore", File: &hostprep.File{Path: sysctlBeforePath, Content: b.text(), Mode: 0o644}, Kinds: []string{"sysctl"}})
	}
	file := SysctlFile(wantLow, wantFwd)
	live := (!wantLow || p.f.PortStart == "80") && (!wantFwd || p.f.IPForward == "1")
	switch {
	case !wantLow && !wantFwd:
		if p.f.Files[sysctlPath].Present {
			p.step(Step{Title: "remove " + sysctlPath, Describe: "sysctls: no admitted deployment needs it", Command: "rm -f " + sysctlPath, Kinds: []string{"sysctl"}})
		}
	case p.f.Files[sysctlPath].Sum != sha(file) || !live:
		p.step(Step{Title: "set the host's sysctls", Describe: "sysctls: " + sysctlPath + ", applied with sysctl --system",
			File: &hostprep.File{Path: sysctlPath, Content: file, Mode: 0o644}, Command: "sysctl --system >/dev/null", Kinds: []string{"sysctl"}})
	default:
		p.present("sysctls: " + sysctlPath + " as written and applied")
	}
	if len(restore) > 0 {
		p.step(Step{Title: "restore " + strings.Join(restore, ", "), Describe: "sysctls: no admitted deployment needs them any more; set each back to what it was before admit",
			Command: "sysctl -w " + strings.Join(restore, " ") + " >/dev/null", Kinds: []string{"sysctl"}})
		if len(b) == 0 && p.f.Files[sysctlBeforePath].Present {
			p.step(Step{Title: "remove " + sysctlBeforePath, Describe: "sysctls: nothing left to restore", Command: "rm -f " + sysctlBeforePath, Kinds: []string{"sysctl"}})
		}
	}
	for _, d := range lowFor {
		p.present("sysctls: low ports kept for deployment " + d)
	}
	for _, d := range fwdFor {
		p.present("sysctls: forwarding kept for deployment " + d)
	}
	return nil
}
