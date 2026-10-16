package hostadmit

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Flags are the owner's choices: the two grants, where Docker's data goes,
// and the two host-wide permissions a host may need.
type Flags struct {
	Watchdog, PublicPorts bool
	// DataDir is --data-dir: admit creates DataDir/paisans-<token> for the
	// deployment's Docker data. "" leaves it under the user's home.
	DataDir string
	// BridgeNetfilter is --bridge-netfilter: load br_netfilter on a host
	// whose own bridges its FORWARD chain will then filter.
	BridgeNetfilter bool
	// MeshLAN is --mesh-lan: let the mesh dial the host's own LAN.
	MeshLAN bool
}

// Grants are the grants the flags give.
func (f Flags) Grants() admission.Grants {
	return admission.Grants{Watchdog: f.Watchdog, PublicPorts: f.PublicPorts}
}

// Input is everything admit is given: the request, from a file or built
// from the configuration, its digest ("" with --site), the flags, and the
// time the record will carry.
type Input struct {
	Request admission.Request
	Digest  string
	Flags   Flags
	Now     time.Time
}

// Grant is a grant the site's roles need and the flags leave out, for the
// owner to give: Flag names it, Hint and Explain say why it is needed and
// what it gives, Unless ("; ...") names a way not to need it, and Give sets
// it.
type Grant struct {
	Flag, Hint, Explain, Unless string
	Give                        func(*Flags)
}

// Missing is every grant the request's roles need and in's flags do not
// give, in the order Early names them.
func Missing(in Input) []Grant {
	needed, given := in.Request.Grants, in.Flags.Grants()
	var out []Grant
	if needed.Watchdog && !given.Watchdog {
		out = append(out, Grant{Flag: "--watchdog", Hint: "this is a data site, and admitting it needs --watchdog",
			Explain: "Its Patroni member needs the host's watchdog to fail over safely: without one, a member that has lost its lock can keep acting as leader. The watchdog reboots the whole machine when Patroni stops renewing it.",
			Give:    func(f *Flags) { f.Watchdog = true }})
	}
	if needed.PublicPorts && !given.PublicPorts {
		out = append(out, Grant{Flag: "--public-ports", Hint: "this site runs the toolkit's Caddy, and admitting it needs --public-ports",
			Explain: "The gateway, or a monitor with web_server.managed_by: paisans, binds ports 80 and 443. --public-ports lowers net.ipv4.ip_unprivileged_port_start to 80 for the whole host, so every local user may then bind ports 80 to 1023, and opens 80 and 443 in the firewall.",
			Unless:  "; a monitor behind your own web server (web_server.managed_by: external) needs neither",
			Give:    func(f *Flags) { f.PublicPorts = true }})
	}
	return out
}

// Early is admit's first check, before anything reaches the host: a grant
// the site's roles need and the owner did not give, a grant it does not
// need, and a --data-dir that is not an absolute, clean path. Each is the
// owner's to decide, so admit stops having read and changed nothing.
func Early(in Input) error {
	needed, given := in.Request.Grants, in.Flags.Grants()
	type stop struct{ hint, explain string }
	var stops []stop
	for _, m := range Missing(in) {
		stops = append(stops, stop{m.Hint, m.Explain + " Admit it with " + m.Flag + ", or do not admit it" + m.Unless + "."})
	}
	if given.Watchdog && !needed.Watchdog {
		stops = append(stops, stop{"--watchdog is not needed: this site holds no data role",
			"The watchdog can reboot the machine and is given only to a data site's Patroni. Drop --watchdog."})
	}
	if given.PublicPorts && !needed.PublicPorts {
		stops = append(stops, stop{"--public-ports is not needed: this site binds no port below 1024",
			"--public-ports lets every local user bind ports 80 to 1023 and is given only to a site that runs the toolkit's Caddy. Drop --public-ports."})
	}
	if d := in.Flags.DataDir; d != "" && !plainDir(d) {
		stops = append(stops, stop{"--data-dir must be an absolute path in its plain form",
			fmt.Sprintf("%q is not: give the directory as /srv/data, with no trailing slash, no . or .., no control character, and not / itself.", d)})
	}
	switch len(stops) {
	case 0:
		return nil
	case 1:
		return &ui.Problem{Hint: stops[0].hint, Explain: about(in.Request) + " " + stops[0].explain + " Nothing was read or changed."}
	}
	explain := about(in.Request)
	for _, s := range stops {
		explain += "\n- " + s.hint + ". " + s.explain
	}
	return &ui.Problem{Hint: "the flags given do not suit this request", Explain: explain + "\nNothing was read or changed."}
}

// plainDir reports whether d is an absolute path in its clean form, not /,
// with no control character: a directory admit can name in a plan, a probe
// and a record as it is.
func plainDir(d string) bool {
	_, bad := config.ControlRune(d)
	return !bad && path.IsAbs(d) && path.Clean(d) == d && d != "/"
}

// about names the site a request is for and its roles, so an owner holding
// requests for two sites of one deployment knows which one this is.
func about(r admission.Request) string {
	roles := "no role"
	if len(r.Roles) > 0 {
		roles = "roles: " + strings.Join(r.Roles, ", ")
	}
	return ui.Visible(fmt.Sprintf("The request is for site %s (%s).", r.Site, roles))
}

// Step is one change admit still needs to make, in order.
type Step struct {
	// Title is the step's short name, Describe its sentence.
	Title, Describe string
	// File, when set, is written first, by the transport, as root.
	File *hostprep.File
	// Command, when set, runs after File, with Input on its stdin when
	// Input is not empty: content written as the deployment's user goes
	// there, never on a command line.
	Command, Input string
	Kinds          []string
	// Provisional marks a step rendered before the user existed, with
	// <uid> and <subordinate range> in place of the values useradd will
	// give it. Execute never runs one: it reads the host again once the
	// user exists and runs the step as rebuilt.
	Provisional bool
}

// Plan is what admit found on one host for one site of a deployment.
type Plan struct {
	Deployment, User, Profile, Host string
	// Site is the site the request is for, "" in evict's plan.
	Site     string
	Steps    []Step
	Present  []string
	Foreign  []string
	Warnings []hostprep.Warning
}

// planner is one Build: the host, the input and what the probe found, and
// the plan as the parts fill it.
type planner struct {
	t       apply.Transport
	in      Input
	d       deployment.Deployment
	f       Facts
	profile hostprep.Profile
	host    hostprep.OSRelease
	plan    *Plan
	// record is the admission record the record part writes; parts before
	// it fill what they decide (data_dir, bridge_netfilter, web_server).
	record admission.Record
	// sx caches sysctls(), which both sysctl parts plan from.
	sx *sysctlState
	// heldDocker is a system Docker admit installed whose units none
	// enables and that left no network store: it has never run, so it has
	// no networks.
	heldDocker bool
	// prev caches previous(): this deployment's record as the host holds it.
	prev struct {
		read bool
		rec  *admission.Record
		err  error
	}
}

// step adds s to the plan, present what was found in place, warn a warning.
func (p *planner) step(s Step)         { p.plan.Steps = append(p.plan.Steps, s) }
func (p *planner) present(what string) { p.plan.Present = append(p.plan.Present, what) }
func (p *planner) warn(hint, detail string) {
	p.plan.Warnings = append(p.plan.Warnings, hostprep.Warning{Hint: hint, Detail: detail})
}

// section adds what one of hostprep's planners found, its steps as admit's.
func (p *planner) section(s hostprep.Section) {
	for _, st := range s.Steps {
		p.step(Step{Title: st.Title, Describe: st.Describe, File: st.File, Command: st.Command, Kinds: st.Kinds})
	}
	p.plan.Present = append(p.plan.Present, s.Present...)
	p.plan.Foreign = append(p.plan.Foreign, s.Foreign...)
	p.plan.Warnings = append(p.plan.Warnings, s.Warnings...)
}

// part is one stage of admit, in the order parts lists them.
type part struct {
	name string
	plan func(*planner) error
}

// parts are admit's stages in the order they run: the spec's What it does,
// with the claim first, and delegation and the egress table before linger
// starts the user's manager. AppArmor's check follows Docker's packages,
// though none of them brings the rootlesskit profile, which is Ubuntu's
// apparmor package's; like every part, it refuses while the plan is drawn,
// before anything runs. Each is added by the task that writes it, at the
// place its comment names. Build wraps a part's error with the part's name and
// nothing else, so a part that quotes what it found on the host passes
// that through ui.Visible itself.
var parts = []part{
	{"claim", planClaim}, // 1
	{"admission directory", planAdmissionDir},
	{"sysctls before", planSysctlsBefore},
	{"docker", planDocker},
	{"apparmor", planAppArmor},
	// 4. user
	{"user", planUser},
	// 5. delegation
	{"delegation", planDelegation},
	// 6. egress
	{"egress", planEgress},
	// 7. linger
	{"linger", planLinger},
	// 8. bridge netfilter
	{"bridge netfilter", planBrNetfilter},
	// 9. daemon
	{"daemon", planDaemon},
	{"deployment directory", planDeploymentDir}, // 10
	{"mesh subnet", planMeshSubnet},             // 10a
	{"wireguard", planWireGuard},                // 11
	{"firewall", planFirewall},                  // 12
	{"watchdog", planWatchdogGrant},             // 13
	{"sysctls", planSysctls},                    // 14
	{"web server", planWebServer},               // 14a
	{"record", planRecord},                      // 15
	{"keys", planKeys},                          // 16
	// 17. the check, CheckUser, runs after Execute, on both paths.
}

// Build reads the host t reaches and plans admitting in.Request there. A
// refusal is an error; a plan with no steps is a host already admitted as
// asked.
func Build(t apply.Transport, in Input) (*Plan, error) {
	p, err := build(t, in)
	if err != nil {
		return nil, err
	}
	return p.plan, nil
}

// build is Build, returning the planner, whose facts fill the placeholders
// of an approved plan when Execute rebuilds it.
func build(t apply.Transport, in Input) (*planner, error) {
	if err := Early(in); err != nil {
		return nil, err
	}
	content, found, err := t.ReadFile("/etc/os-release")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/os-release on %s: %w", t.Describe(), shown(err))
	}
	if !found {
		return nil, fmt.Errorf("%s has no /etc/os-release, so no host profile can be chosen for it", t.Describe())
	}
	host := hostprep.ParseOSRelease(content)
	profile, err := hostprep.Select(host)
	if err != nil {
		return nil, err
	}
	d := deployment.Deployment{ID: in.Request.Deployment}
	facts, err := probe(t, d, in)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", t.Describe(), shown(err))
	}
	p := &planner{t: t, in: in, d: d, f: facts, profile: profile, host: host,
		plan: &Plan{Deployment: in.Request.Deployment, User: in.Request.User, Site: in.Request.Site,
			Profile: profile.ID() + " " + profile.Version(), Host: t.Describe()},
		record: admission.NewRecord(in.Request, in.Flags.Grants(), in.Digest, in.Now)}
	if err := refuseUnsafe(facts, "admit"); err != nil {
		return nil, err
	}
	for _, pt := range parts {
		if err := pt.plan(p); err != nil {
			return nil, fmt.Errorf("%s: %w", pt.name, err)
		}
	}
	return p, nil
}

// refuseUnsafe stops admit, or evict, as by names, before any part plans,
// when the probe found a path it reads, writes or removes as root below a
// link, or below a directory that is not root's or that its group or
// others can write, or a file there that is neither a regular file nor a
// link. What is below such a directory is not what its path names, or can
// be swapped between the probe and the change, and a FIFO or a device is
// no file admit wrote, so no part may act there; refusing here means no
// part can forget to.
func refuseUnsafe(f Facts, by string) error {
	var bad []string
	why := func(mode string) string {
		switch mode {
		case "link-above":
			return "a link above it"
		case "other":
			return "not a regular file"
		}
		return "a directory above it that is not root's, or that its group or others can write"
	}
	for _, path := range sortedKeys(f.Dirs) {
		if d := f.Dirs[path]; d.Unsafe {
			bad = append(bad, fmt.Sprintf("- %s (%s)", ui.Visible(path), why(d.Mode)))
		}
	}
	for _, path := range sortedKeys(f.Files) {
		if s := f.Files[path]; s.Unsafe {
			bad = append(bad, fmt.Sprintf("- %s (%s)", ui.Visible(path), why(s.Mode)))
		}
	}
	if f.Data.Unsafe {
		bad = append(bad, fmt.Sprintf("- %s, where Docker's data would go (%s)", ui.Visible(f.Data.Path), why(f.Data.Above)))
	}
	if len(bad) == 0 {
		return nil
	}
	act := map[string]string{"admit": "writes", "evict": "removes"}[by]
	return &ui.Problem{Hint: "a path " + by + " " + act + " as root is not root's alone",
		Explain: "Each of these is below a link or a directory another user could change, or is not a regular file, so what " + by + " would " + strings.TrimSuffix(act, "s") + " there is not what it names:\n" + strings.Join(bad, "\n") + "\nMake every directory above them a real directory, owned by root and writable by root alone, move aside anything that is not a regular file, then run host " + by + " again. Nothing was changed."}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// uidOf is the user's UID for a step's command or file, "<uid>" while the
// user does not exist; a step that uses it is Provisional then.
func uidOf(p *planner) string {
	if !p.f.User.Exists {
		return "<uid>"
	}
	return fmt.Sprint(p.f.User.UID)
}

// rangeOf is the user's subordinate UID range as nft writes one,
// first-last, or "<subordinate range>" while the user does not exist; a
// step that uses it is Provisional then. A user that exists with no range,
// or with more than one, is an error the part returns, so Build refuses
// rather than plan a runnable step with the placeholder in it.
func rangeOf(p *planner) (string, error) {
	if !p.f.User.Exists {
		return "<subordinate range>", nil
	}
	r := p.f.SubUID[p.in.Request.User]
	if len(r) != 1 {
		return "", fmt.Errorf("%s has %d subordinate UID ranges in /etc/subuid, and admit needs exactly one: the egress table and the user's Docker name it. Give %s one range of its own with usermod --add-subuids, then run host admit again. Nothing was changed", ui.Visible(p.in.Request.User), len(r), ui.Visible(p.in.Request.User))
	}
	return fmt.Sprintf("%d-%d", r[0].Start, r[0].Start+r[0].Count-1), nil
}

// placeholders fills, in an approved step, every placeholder uidOf and
// rangeOf draw while the user does not exist, with what they give on p's
// host. It is the one place a placeholder is filled, so a new one is a new
// pair here.
//
// A user with other than one range has none to fill in: the part that
// needed it refused while p was built, so the rebuilt plan holds no step
// naming it, and an approved step that does differs from it.
func placeholders(p *planner) *strings.Replacer {
	pairs := []string{"<uid>", uidOf(p)}
	if r, err := rangeOf(p); err == nil {
		pairs = append(pairs, "<subordinate range>", r)
	}
	return strings.NewReplacer(pairs...)
}

// Text is the plan as a reviewer reads it in a golden file: every step with
// its command, its file and its stdin in full, byte for byte, then what was
// present, what was another's, and each warning. It is for review, not for
// a terminal; Show is what an owner sees.
func (p *Plan) Text() string {
	var b strings.Builder
	if p.Site == "" {
		fmt.Fprintf(&b, "evict %s as %s on %s (%s)\n", p.Deployment, p.User, p.Host, p.Profile)
	} else {
		fmt.Fprintf(&b, "admit site %s of %s as %s on %s (%s)\n", p.Site, p.Deployment, p.User, p.Host, p.Profile)
	}
	indent := func(prefix, text string) {
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			fmt.Fprintf(&b, "%s%s\n", prefix, line)
		}
	}
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "\nstep %d: %s\n", i+1, s.Title)
		indent("  ", s.Describe)
		if s.Provisional {
			b.WriteString("  provisional: run once the user exists\n")
		}
		if s.File != nil {
			fmt.Fprintf(&b, "  write %s mode %04o:\n", s.File.Path, s.File.Mode)
			indent("    | ", s.File.Content)
		}
		if s.Command != "" {
			b.WriteString("  run:\n")
			indent("    $ ", s.Command)
		}
		if s.Input != "" {
			b.WriteString("  stdin:\n")
			indent("    | ", s.Input)
		}
	}
	for _, s := range p.Present {
		fmt.Fprintf(&b, "\npresent: %s", s)
	}
	for _, s := range p.Foreign {
		fmt.Fprintf(&b, "\nforeign: %s", s)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", w.Hint)
		if w.Detail != "" {
			fmt.Fprintf(&b, "\n  %s", w.Detail)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// Show reports the plan as host prepare's does: each step pending under
// its title, its sentence as a detail, a provisional step noted as one,
// then under --verbose what was present and another's, then the
// warnings. Everything it shows passes through ui.Visible, since a part
// may quote what it found on the host.
func (p *Plan) Show(r ui.Reporter) {
	if p.Site != "" {
		r.Detail("site %s of deployment %s", ui.Visible(p.Site), p.Deployment)
	}
	r.Detail("host profile %s", p.Profile)
	for _, s := range p.Steps {
		st := r.Step(ui.Visible(s.Title))
		st.Detail("%s", ui.Visible(s.Describe))
		note := ""
		if s.Provisional {
			note = "runs once the user exists"
		}
		st.End(ui.Pending, note)
	}
	if r.Verbose() {
		for _, s := range p.Present {
			r.Step(ui.Visible(s)).End(ui.OK, "")
		}
		for _, s := range p.Foreign {
			r.Step(ui.Visible(s)).End(ui.OK, "not paisans")
		}
	}
	p.Warn(r)
}

// Warn reports the warnings alone, as --execute does.
func (p *Plan) Warn(r ui.Reporter) {
	for _, w := range p.Warnings {
		r.Warn(ui.Visible(w.Hint), ui.Visible(w.Detail))
	}
}

// Execute runs approved's steps in order and stops at the first failure,
// naming the step. At the first provisional step it reads the host again
// and plans again, now that the user exists, and carries on with the plan
// it rebuilt, provided that plan's steps are the ones the owner approved
// from there on, less any now done (sameSteps): anything else means the
// host changed while admit ran, and nothing more is done.
func Execute(t apply.Transport, in Input, approved *Plan, r ui.Reporter) error {
	steps := approved.Steps
	for i := 0; i < len(steps); i++ {
		if steps[i].Provisional {
			rebuilt, err := build(t, in)
			if err != nil {
				return stoppedOnRebuild(approved.User, steps[:i], err)
			}
			done, err := sameSteps(steps[i:], rebuilt.plan.Steps, placeholders(rebuilt))
			if err != nil {
				return err
			}
			for _, s := range done {
				r.Step(ui.Visible(s.Title)).End(ui.OK, "done already")
			}
			steps = append(steps[:i:i], rebuilt.plan.Steps...)
		}
		if err := run(t, steps[i], r); err != nil {
			return err
		}
	}
	return nil
}

// stoppedOnRebuild is a refusal from reading the host again once user
// existed. The steps that ran have changed the host, so a part's "Nothing
// was changed" is not so: the refusal names them and says nothing more
// was done.
func stoppedOnRebuild(user string, ran []Step, err error) error {
	text := strings.ReplaceAll(visibleLines(err.Error()), "Nothing was changed", "Nothing more was done")
	return &ui.Problem{Hint: "admit stopped once " + user + " existed",
		Explain: "These steps ran:\n" + titles(ran, nil) + "\nReading the host again then found: " + text + "\nRun host admit again to see the plan.",
		Cause:   err}
}

// sameSteps is nil when rebuilt is approved with its placeholders filled
// by fill, less the steps now done: the approved steps, in the same order,
// each with the same title, file path, content and mode, command and
// stdin, and none of them still provisional. An approved step the rebuilt
// plan no longer has is done (installing wireguard-tools creates
// /etc/wireguard, which admit would otherwise create), so it is not drift;
// a step added, moved or changed is. Describe and Kinds are what the owner
// read, not what runs, so they are not compared. A step that was approved
// with a different file, command or stdin is named by the first of its
// fields that differs. It returns the approved steps found done, which
// Execute reports as such.
func sameSteps(approved, rebuilt []Step, fill *strings.Replacer) ([]Step, error) {
	for _, s := range rebuilt {
		if s.Provisional {
			return nil, fmt.Errorf("the host changed while admit ran: %q still needs the user, which was to exist by now. Nothing more was done. Run host admit again to see the plan", s.Title)
		}
	}
	var done []Step
	i := 0
	for _, s := range rebuilt {
		k := i
		for k < len(approved) && (fill.Replace(approved[k].Title) != s.Title || differs(approved[k], s, fill) != "") {
			k++
		}
		if k < len(approved) {
			done = append(done, approved[i:k]...)
			i = k + 1
			continue
		}
		for _, a := range approved[i:] {
			if fill.Replace(a.Title) == s.Title {
				return nil, fmt.Errorf("the host changed while admit ran: step %q now has a different %s from the one approved. Nothing more was done. Run host admit again to see the plan", s.Title, differs(a, s, fill))
			}
		}
		return nil, fmt.Errorf("the host changed while admit ran: the steps left to run were\n%s\nand are now\n%s\nNothing more was done. Run host admit again to see the plan", titles(approved, fill), titles(rebuilt, nil))
	}
	return append(done, approved[i:]...), nil
}

// titles lists steps' titles, each quoted on its own line, filled by fill
// when it is not nil.
func titles(steps []Step, fill *strings.Replacer) string {
	var out []string
	for _, s := range steps {
		t := s.Title
		if fill != nil {
			t = fill.Replace(t)
		}
		out = append(out, fmt.Sprintf("- %q", t))
	}
	return strings.Join(out, "\n")
}

// differs names the first field of the approved step a, its placeholders
// filled, that b does not share, or "" when they are the same step.
func differs(a, b Step, fill *strings.Replacer) string {
	switch {
	case (a.File == nil) != (b.File == nil):
		return "file"
	case a.File != nil && fill.Replace(a.File.Path) != b.File.Path:
		return "file path"
	case a.File != nil && fill.Replace(a.File.Content) != b.File.Content:
		return "file content"
	case a.File != nil && a.File.Mode != b.File.Mode:
		return "file mode"
	case fill.Replace(a.Command) != b.Command:
		return "command"
	case fill.Replace(a.Input) != b.Input:
		return "stdin"
	}
	return ""
}

// run runs one step, as host prepare's Execute does. A failure names the
// step and quotes what the host printed, through ui.Visible and redaction.
func run(t apply.Transport, s Step, r ui.Reporter) error {
	st := r.Step(ui.Visible(s.Title))
	st.Detail("%s", ui.Visible(s.Describe))
	if s.File != nil {
		if err := t.WriteFile(s.File.Path, s.File.Content, s.File.Mode); err != nil {
			err = fmt.Errorf("%s: %w", s.Title, shown(err))
			st.Fail(err)
			return err
		}
	}
	if s.Command != "" {
		var out string
		var err error
		if s.Input != "" {
			out, err = t.RunInput(s.Command, s.Input)
		} else {
			out, err = t.Run(s.Command)
		}
		if err != nil {
			text := visibleLines(err.Error())
			if o := strings.TrimSpace(out); o != "" && !strings.Contains(err.Error(), o) {
				text += ": " + visibleLines(o)
			}
			err = fmt.Errorf("%s: %w", s.Title, ui.RedactError(shownError{text, err}))
			st.Fail(err)
			return err
		}
	}
	st.Done("")
	return nil
}

// shownError is an error whose text is what a host said, made visible and
// redacted, that still unwraps to the error the transport returned.
type shownError struct {
	text string
	err  error
}

func (e shownError) Error() string { return e.text }
func (e shownError) Unwrap() error { return e.err }

// shown is err with its text, which quotes a host, made safe to print.
func shown(err error) error {
	return ui.RedactError(shownError{visibleLines(err.Error()), err})
}

// visibleLines is text with each line passed through ui.Visible, so a
// host's escape sequence is shown and never drawn.
func visibleLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = ui.Visible(l)
	}
	return strings.Join(lines, "\n")
}
