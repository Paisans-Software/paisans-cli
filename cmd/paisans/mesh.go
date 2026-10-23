package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/diagnosis"
	"github.com/paisans-software/paisans-cli/internal/mesh"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// initHost is how `paisans init` reaches a site to read its manifest and its
// networks. A test replaces it.
var initHost = func(name string, site config.Site) registry.Runner {
	return siteTransport(name, site, "")
}

// meshRandom is where init's subnet rolls come from. A test replaces it.
var meshRandom io.Reader = rand.Reader

// checkMeshLive refuses when anything on the host h reaches, outside this
// deployment's own interface, holds a network overlapping the mesh subnet:
// a route, an address, a Docker network or one of Docker's address pools.
// apply and site prepare run it before they change the host, dry run or not,
// so a clash stops them with nothing changed. See internal/mesh.
func checkMeshLive(cfg *config.Config, site string, h mesh.Host) error {
	subnet, err := mesh.ParsePrefix(cfg.Mesh.Subnet)
	if err != nil {
		return fmt.Errorf("mesh.subnet %q is not a network: %w", cfg.Mesh.Subnet, err)
	}
	probed, err := mesh.ProbeUser(h)
	if err != nil {
		return err
	}
	taken, err := probed.Taken(cfg.Deployment().Interface(), site)
	if err != nil {
		return fmt.Errorf("%s: %w", h.Describe(), err)
	}
	clash := mesh.Clashes(subnet, taken)
	if len(clash) == 0 {
		return nil
	}
	what := make([]string, len(clash))
	for i, c := range clash {
		what[i] = c.What
	}
	return &ui.Problem{
		Hint:    "mesh subnet " + cfg.Mesh.Subnet + " is in use" + holder(clash),
		Explain: "Remove it if not in use or run paisans init to pick a free subnet.",
		Cause:   fmt.Errorf("%s: the mesh subnet %s overlaps %s, so nothing was changed", h.Describe(), cfg.Mesh.Subnet, strings.Join(what, ", ")),
	}
}

// holder names what holds a clashing subnet, for the refusal's hint: the
// paisans deployment whose interface it is, by its token, or else the first
// clash itself.
func holder(clash []mesh.Taken) string {
	for _, c := range clash {
		if token, ok := strings.CutPrefix(c.Dev, deployment.InterfacePrefix); ok && token != "" {
			return " by paisans deployment " + token
		}
	}
	return " (" + clash[0].What + ")"
}

// initAdmissions is init's first look at each site: whether its host has
// admitted the deployment, read as settleMesh and the deployment record are,
// as the deployment's user. Each site is a step, in name order: ✓ admitted,
// ○ not admitted yet, which init lists with why (see listNotAdmitted) and
// offers to admit or skips (see offerAdmissions), and ✗ one it could not
// ask. A skipped site is a warning, not a refusal, since admit checks the
// subnet against it as root, and refuses one that overlaps anything there.
// It returns the admitted sites, in name order, the sites not admitted,
// and unread,
// the refusal for the sites it could not ask: settleMesh refuses with it,
// nothing written, unless the subnet is already deployed, since such a site
// may be admitted, and an unchecked host is exactly where a chosen subnet
// would collide unseen.
//
// A site whose admission does not match the declaration, or whose record
// cannot be read, is still read, and warned about, since the deployment's
// user logs in there: what it lacks is the host owner's to fix, and apply
// refuses the site until they have. A login refused as the deployment's own
// user is a site not admitted (apply.ErrLoginNotAdmitted); one refused as a
// user the site names is a key problem, and init cannot ask it.
func initAdmissions(r ui.Reporter, cfg *config.Config, hosts map[string]registry.Runner) (admitted []string, skipped []notAdmitted, unread error) {
	var unreachable, advice, referrals []string
	var failures []error
	token := cfg.Deployment().Token()
	for _, name := range cfg.SiteNames() {
		s := r.Step("check " + name + "'s admission")
		h := hosts[name]
		_, err := admissionForSite(h, cfg, name)
		var se *admission.SiteError
		var unreadable *admission.Error
		switch {
		case err == nil:
			admitted = append(admitted, name)
			s.End(ui.OK, "admitted")
		case errors.Is(err, apply.ErrLoginNotAdmitted), errors.Is(err, admission.ErrNotAdmitted):
			hint, _ := ui.Describe(err)
			skipped = append(skipped, notAdmitted{site: name, why: hint})
			s.Detail("%s", err)
			s.End(ui.Pending, "not admitted")
		case errors.As(err, &se), errors.As(err, &unreadable):
			admitted = append(admitted, name)
			result := "admitted, does not match"
			if unreadable != nil {
				result = "admission record unreadable"
			}
			hint, explain := ui.Describe(admissionProblem(name, token, err))
			s.End(ui.Warned, result)
			r.Warn(hint, explain+"\napply refuses the site until then.")
		default:
			err = fmt.Errorf("%s: %w", h.Describe(), err)
			// Each site is one line in the operator's words, and what to do
			// about it follows the list. The full chain, with the file it
			// failed at, is the cause, for --verbose.
			hint, explain := ui.Describe(err)
			s.End(ui.Failed, hint)
			unreachable = append(unreachable, fmt.Sprintf("- %s: %s", name, hint))
			if explain != "" {
				advice = append(advice, explain)
			}
			// A site ssh did not answer is doctor's reach finding, which
			// says what the deployment is without while it is gone. Any
			// other failure answered ssh, and doctor would not name it.
			if errors.Is(err, apply.ErrUnreachable) {
				referrals = append(referrals, diagnosis.Unreachable(name).Sentence)
			}
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(unreachable) > 0 {
		explain := slices.Concat(unreachable, advice, referrals)
		unread = &ui.Problem{
			Hint:    "init could not read every site, so mesh.subnet is not settled",
			Explain: strings.Join(append(explain, "Nothing was written. Every host has to be checked, since a subnet chosen without one is a subnet that may collide on it."), "\n"),
			Cause:   errors.Join(failures...),
		}
	}
	return admitted, skipped, unread
}

// notAdmitted is a site whose host has not admitted the deployment, and
// why init reads it so: what its admission check said.
type notAdmitted struct{ site, why string }

// listNotAdmitted is the list, before init asks or settles anything, of
// every site not admitted and why, as one warning.
func listNotAdmitted(r ui.Reporter, skipped []notAdmitted) {
	if len(skipped) == 0 {
		return
	}
	names := make([]string, 0, len(skipped))
	lines := make([]string, 0, len(skipped))
	for _, n := range skipped {
		names = append(names, n.site)
		lines = append(lines, fmt.Sprintf("- %s: %s", n.site, n.why))
	}
	hint := names[0] + " has not admitted this deployment yet"
	if len(names) > 1 {
		hint = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " have not admitted this deployment yet"
	}
	// A note, so the reasons show without --verbose.
	r.Note(hint, strings.Join(lines, "\n"))
}

// offerAdmissions is what init does about each site not admitted. At a
// terminal, without --yes, it asks whether to admit the site now from this
// machine, and on a yes admits it (see admitSite). With no terminal, with
// --yes, on a no, or before mesh.subnet is set, which every admission is
// checked against, it prints the commands that admit the site and skips it.
// --yes never admits a host: admitting is the owner's step.
//
// It returns the admitted sites, in name order, the sites admit refused
// over the mesh subnet alone (see subnetBlocked), which settleMesh moves the
// subnet clear of and readmit admits again, and the configuration as the
// file now holds it, since admit's first login as the deployment's user may
// record ssh.user there; hosts is updated for each site admitted. An admit
// that fails once its plan runs stops init with its error; one that stops
// before for anything else, with nothing changed (see admitStopped), is a
// no, whose reason the skip names.
func offerAdmissions(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, skipped []notAdmitted, ans answerer, stdin io.Reader, stdout io.Writer) ([]string, []blockedSite, *config.Config, error) {
	var blocked []blockedSite
	ask := !ans.yes && stdinIsTerminal(stdin) && cfg.Mesh.Subnet != ""
	if len(skipped) > 0 && !ans.yes && stdinIsTerminal(stdin) && cfg.Mesh.Subnet == "" {
		r.Note("mesh.subnet is not set yet, so init offers to admit no site", "site admit checks the subnet on the host, and init chooses it in this run. Once init has run, admit each site with the commands below, or run init again.")
	}
	for _, n := range skipped {
		var why admitStop
		if ask {
			resume := ui.Hold(r)
			yes, err := ans.yesNo(stdin, stdout, "", fmt.Sprintf("Admit %s now from this machine?", n.site), "")
			resume()
			if err != nil {
				return nil, nil, nil, fmt.Errorf("init: reading your answer: %w", err)
			}
			if yes {
				var b *blockedSite
				var ok bool
				applied := func() (bool, error) { return appliedOn(cfg, hosts, admitted) }
				if cfg, ok, b, why, err = admitSite(r, cfg, path, hosts, n.site, applied, stdin, stdout); err != nil {
					return nil, nil, nil, err
				}
				if ok {
					admitted = append(admitted, n.site)
					continue
				}
				if b != nil {
					blocked = append(blocked, *b)
					continue
				}
			}
		}
		skipNote(r, path, n.site, why)
	}
	sort.Strings(admitted)
	return admitted, blocked, cfg, nil
}

// blockedSite is a site whose admit refused the mesh subnet, with the
// networks on its host that the subnet overlaps, and why admit stopped.
// readmit is whether the subnet is all admit refused, so that admitting
// the site again on a subnet clear of them can succeed.
type blockedSite struct {
	site    string
	taken   []mesh.Taken
	why     admitStop
	readmit bool
}

// admitStop is why site admit stopped with nothing changed, empty when it
// did not run. held is a host where another deployment holds what the site
// needs, which admitting the site again from the commands init prints is
// refused for in turn.
type admitStop struct {
	why  string
	held bool
}

// commands is what follows why admit stopped, for site: the commands that
// admit it, unless the host is held.
func (s admitStop) commands(path, site string) string {
	if s.held {
		return ""
	}
	return admitCommands(path, site)
}

// skipNote says init skipped site, with why admit stopped, if it ran, and
// the commands that admit it. A note, so the commands show without
// --verbose.
func skipNote(r ui.Reporter, path, site string, why admitStop) {
	r.Note(site+" has not admitted this deployment yet, so init skipped it", strings.TrimSpace(why.why+why.commands(path, site)))
}

// admitSite runs site admit --site's own flow for site (admitFromInit),
// then checks the site's admission again. It returns the configuration as
// the file now holds it, whether the site is admitted, and otherwise, for
// an admit that stopped with nothing changed, the site as blocked when
// admit refused the mesh subnet, or why it stopped, for the skip. An
// admit that fails once its plan runs, or whose admission does not read
// back, is the error.
//
// A site whose WireGuard listen port another deployment on the host holds,
// and nothing else, has its port moved to a free one admit found, when the
// port is init's to move (config.Site.ListenPortMovable) and applied says
// the deployment has never been applied, since the port is in every other
// site's WireGuard file from then on: see moveListenPort. The site is then
// admitted again on it, once. A site applied could not read leaves the
// port where it is, with a note naming it.
func admitSite(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, site string, applied func() (bool, error), stdin io.Reader, stdout io.Writer) (*config.Config, bool, *blockedSite, admitStop, error) {
	var random io.Reader
	if cfg.Sites[site].ListenPortMovable() {
		random = meshRandom
	}
	err := admitFromInit(r, path, site, random, stdin, stdout)
	var moved *portMoved
	if random != nil && errors.As(err, &moved) && movable(r, site, applied) {
		if err := moveListenPort(r, path, site, moved); err != nil {
			return cfg, false, nil, admitStop{}, err
		}
		if cfg, err = config.LoadForInit(path); err != nil {
			return cfg, false, nil, admitStop{}, err
		}
		hosts[site] = initHost(site, cfg.Sites[site])
		err = admitFromInit(r, path, site, nil, stdin, stdout)
	}
	if err != nil {
		if !admitStopped(err) {
			return cfg, false, nil, admitStop{}, err
		}
		why := admitStop{why: admitStopReason(err) + "\n\n", held: errors.As(err, new(registry.Conflict))}
		if taken, readmit, ok := subnetBlocked(site, err); ok {
			return cfg, false, &blockedSite{site: site, taken: taken, why: why, readmit: readmit}, admitStop{}, nil
		}
		return cfg, false, nil, why, nil
	}
	if cfg, err = config.LoadForInit(path); err != nil {
		return cfg, false, nil, admitStop{}, err
	}
	hosts[site] = initHost(site, cfg.Sites[site])
	s := r.Step("check " + site + "'s admission")
	if _, err := admissionForSite(hosts[site], cfg, site); err != nil {
		s.Fail(err)
		return cfg, false, nil, admitStop{}, fmt.Errorf("init: %s was admitted, but its admission does not read back: %w", site, err)
	}
	s.End(ui.OK, "admitted")
	return cfg, true, nil, admitStop{}, nil
}

// moveListenPort writes the free port admit found for site into its
// endpoint in paisans-stack.yaml, as a port alone beside public_address,
// keeping every comment, and says so as one step: the port is the one
// every other site dials, so whatever is in front of the host has to let
// it in.
func moveListenPort(r ui.Reporter, path, site string, moved *portMoved) error {
	s := r.Step("move " + site + "'s WireGuard listen port")
	endpoint := fmt.Sprintf(":%d", moved.Free)
	if err := config.SetEndpoint(path, site, endpoint); err != nil {
		s.Fail(err)
		return err
	}
	holder := "another deployment"
	var c registry.Conflict
	if errors.As(moved, &c) {
		holder = "deployment " + c.ID
	}
	s.Detail("sites.%s.endpoint: %d/udp is held by %s; moved to %s. Open %d/udp in any firewall in front of the host, and forward it where the host is behind NAT.", site, moved.Held, holder, endpoint, moved.Free)
	s.Done("moved to " + endpoint)
	return nil
}

// movable reports whether applied says the deployment has never been
// applied, so site's listen port may move. A site applied could not read
// is a note naming it, since the refusal init then skips the site with
// says nothing of why its port stayed.
func movable(r ui.Reporter, site string, applied func() (bool, error)) bool {
	done, err := applied()
	if err != nil {
		r.Note(site+"'s WireGuard listen port was not moved", fmt.Sprintf("init could not read %v, so it cannot tell whether this deployment has been applied, and once it has, the port is in every other site's WireGuard file. Fix the read and run init again, or give %s's endpoint another port in the configuration.", err, site))
		return false
	}
	return !done
}

// appliedOn reports whether any of sites holds this deployment's manifest.
// Every site is asked, so one that holds it answers before one that could
// not be asked, which is the error, naming it.
func appliedOn(cfg *config.Config, hosts map[string]registry.Runner, sites []string) (bool, error) {
	manifest := cfg.Deployment().Manifest()
	var unread error
	for _, name := range sites {
		_, found, err := hosts[name].ReadFile(manifest)
		switch {
		case found && err == nil:
			return true, nil
		case err != nil && unread == nil:
			hint, _ := ui.Describe(err)
			unread = fmt.Errorf("%s (%s)", name, hint)
		}
	}
	return unread != nil, unread
}

// subnetBlocked is what admit refused the mesh subnet for on site's host,
// if it did: a mesh subnet another deployment holds there, or a network
// the host holds. These are networks init cannot read itself, since the
// deployment's user cannot log in there, and settleMesh steers clear of
// them. readmit is whether the subnet is all admit refused. A refusal that
// also names a role, the token, the root, the interface or the listen port
// is not freed by any subnet, so the site is skipped for it, while the
// subnet still steers clear of the other deployment's.
func subnetBlocked(site string, err error) (taken []mesh.Taken, readmit, ok bool) {
	var overlap *mesh.OverlapError
	if errors.As(err, &overlap) {
		taken := make([]mesh.Taken, len(overlap.Taken))
		for i, t := range overlap.Taken {
			t.What += " on " + site
			taken[i] = t
		}
		return taken, true, true
	}
	var c registry.Conflict
	if !errors.As(err, &c) || !c.Holds("mesh subnet") {
		return nil, false, false
	}
	p, err := mesh.ParsePrefix(c.Entry.Subnet)
	if err != nil {
		return nil, false, false
	}
	readmit = !slices.ContainsFunc([]string{"token", "root", "WireGuard interface", "WireGuard listen port", "the gateway role", "the data role"}, c.Holds)
	return []mesh.Taken{{Prefix: p, What: fmt.Sprintf("deployment %s's mesh subnet %s on %s", c.ID, c.Entry.Subnet, site)}}, readmit, true
}

// maxMoves is how many times init moves the subnet in one run. Each move
// steers clear of everything the admits before it were refused for, so a
// second is already rare.
const maxMoves = 3

// settleAndReadmit settles the mesh subnet (see settleMesh) clear of every
// network the blocked sites' admits were refused for. Once it moves the
// subnet, every site admitted on the old one, and every blocked site, is
// admitted on the new one (see admitSite), since site admit built each
// host's firewall and Docker address pools around the subnet; a site
// refused the new subnet in turn moves it again, up to maxMoves times.
// Moving a subnet some site has admitted needs those admits, so it is done
// only when move, at a terminal; without it settleMesh refuses. A blocked
// site the subnet is not moved for, since it is deployed or init has moved
// it maxMoves times, is skipped with why, and so is one admit refused for
// more than the subnet (see subnetBlocked), whose networks the subnet is
// still moved clear of.
//
// It returns the configuration as the file now holds it, the admitted
// sites, in name order, and whether it moved the subnet. A site admitted
// on an old subnet and not admitted on the one init leaves, on a no or a
// stop, is warned about once settled: its admission no longer matches, and
// apply refuses it until it is admitted again.
func settleAndReadmit(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, blocked []blockedSite, unread error, move bool, stdin io.Reader, stdout io.Writer) (*config.Config, []string, bool, error) {
	var taken []mesh.Taken
	// stale is each site admitted on an old subnet and not yet on the
	// current one, with why its last admit stopped.
	stale := map[string]admitStop{}
	// held is each blocked site admit refused for more than the subnet:
	// never admitted again, skipped once settled.
	var held []blockedSite
	hold := func(bs []blockedSite) []blockedSite {
		out := bs[:0:0]
		for _, b := range bs {
			if b.readmit {
				out = append(out, b)
			} else {
				held = append(held, b)
			}
		}
		return out
	}
	blocked = hold(blocked)
	settled := func() {
		for _, b := range append(slices.Clone(held), blocked...) {
			if _, ok := stale[b.site]; ok {
				stale[b.site] = b.why
				continue
			}
			skipNote(r, path, b.site, b.why)
		}
		names := make([]string, 0, len(stale))
		for site := range stale {
			names = append(names, site)
		}
		sort.Strings(names)
		for _, site := range names {
			r.Warn(site+" is still admitted on an old mesh.subnet", strings.TrimSpace(stale[site].why+"apply refuses it until it is admitted again on "+cfg.Mesh.Subnet+". "+stale[site].commands(path, site)))
		}
	}
	for moves := 0; ; moves++ {
		for _, b := range append(slices.Clone(held), blocked...) {
			for _, t := range b.taken {
				if !slices.Contains(taken, t) {
					taken = append(taken, t)
				}
			}
		}
		if moves == maxMoves {
			settled()
			return cfg, admitted, true, nil
		}
		wrote, err := settleMesh(r, cfg, path, hosts, admitted, taken, move, unread, meshRandom)
		if err != nil {
			return nil, nil, false, err
		}
		if !wrote {
			settled()
			return cfg, admitted, moves > 0, nil
		}
		if cfg, err = config.LoadForInit(path); err != nil {
			return nil, nil, false, err
		}
		again := slices.Clone(admitted)
		for _, site := range admitted {
			stale[site] = admitStop{}
		}
		for _, b := range blocked {
			again = append(again, b.site)
		}
		sort.Strings(again)
		if len(again) == 0 {
			settled()
			return cfg, admitted, true, nil
		}
		r.Note("mesh.subnet is "+cfg.Mesh.Subnet+" now, so init admits "+strings.Join(again, ", ")+" on it", "site admit builds a host's firewall and Docker address pools around the subnet, so each site is admitted again on the new one, as before, with its plan shown first. A site admitted on the old one and not admitted again is refused by apply until it is.")
		admitted, blocked = nil, nil
		for _, site := range again {
			var ok bool
			var b *blockedSite
			var why admitStop
			// The subnet moved, which it does only before the first
			// apply, so a listen port is init's to move too.
			if cfg, ok, b, why, err = admitSite(r, cfg, path, hosts, site, func() (bool, error) { return false, nil }, stdin, stdout); err != nil {
				return nil, nil, false, err
			}
			_, wasAdmitted := stale[site]
			switch {
			case ok:
				admitted = append(admitted, site)
				delete(stale, site)
			case b != nil:
				blocked = append(blocked, *b)
			case wasAdmitted:
				stale[site] = why
			default:
				skipNote(r, path, site, why)
			}
		}
		before := len(held)
		blocked = hold(blocked)
		if len(blocked) == 0 && len(held) == before {
			settled()
			return cfg, admitted, true, nil
		}
	}
}

// admitStopReason is why site admit stopped with nothing changed, for
// init's skip: the hint, then the explanation on lines of its own, so a
// list such as the releases admit supports keeps one item to a line.
func admitStopReason(err error) string {
	hint, explain := ui.Describe(err)
	why := "site admit stopped with nothing changed: " + hint
	if explain != "" {
		why += "\n" + explain
	}
	return why
}

// admitCommands is what admits site, for init to print: site admit from
// this machine for an owner who is the operator, or site request and
// site admit --request on the host for one who is not.
func admitCommands(path, site string) string {
	withConfig := ""
	if path != config.FileName {
		withConfig = " --config " + shellQuote(path)
	}
	return fmt.Sprintf("site admit checks the mesh subnet against the host as root, and refuses one that overlaps anything there. Each command below shows its plan before it changes anything, and takes the flags init cannot pass, such as --data-dir and --bridge-netfilter. If the host is yours, admit the site from this machine, with your own login on the host, which runs admit through sudo:\n\n    paisans site admit --site %[1]s%[2]s --ssh <your login> --execute\n\nIf someone else owns the host, send them a request:\n\n    paisans site request --site %[1]s%[2]s > %[1]s.admit\n\nand ask them to run, on the host:\n\n    sudo paisans site admit --request %[1]s.admit --execute", site, withConfig)
}

// settleMesh is init's subnet decision, over the admitted sites and the
// networks admit refused on the others (see subnetBlocked). It asks each
// admitted site first whether it holds this deployment's manifest. Once any
// of them does, the subnet is deployed and settleMesh never changes it.
// Otherwise it refuses with unread, the sites initAdmissions could not ask,
// and with none it gathers everything each admitted host holds (see
// checkMeshLive) beside blocked, keeps the declared subnet if it overlaps
// none of it, and otherwise rolls a random /24 that overlaps none, moving
// each site's address into it with its host number kept. A roll with a site
// admitted needs move, since each such site is admitted again on the new
// subnet (see settleAndReadmit); without it settleMesh refuses. It writes
// paisans-stack.yaml only when it rolled, and says why. It reports whether
// it wrote.
//
// The decision is one step, "settle mesh subnet", whose result is what
// happened to the subnet; why is its details. Its progress names the site
// being read.
func settleMesh(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, blocked []mesh.Taken, move bool, unread error, random io.Reader) (bool, error) {
	s := r.Step("settle mesh subnet")
	wrote, result, err := chooseMesh(s, cfg, path, hosts, admitted, blocked, move, unread, random)
	if err != nil {
		s.Fail(err)
		return false, err
	}
	s.Done(result)
	return wrote, nil
}

// chooseMesh is settleMesh's decision, reporting what it finds to s, and
// returning whether it wrote and the short result for the step.
func chooseMesh(s ui.Step, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, blocked []mesh.Taken, move bool, unread error, random io.Reader) (bool, string, error) {
	manifest := cfg.Deployment().Manifest()
	var claimedOn []string
	var failures []error
	for _, name := range admitted {
		s.Progress("read " + name)
		h := hosts[name]
		_, found, err := h.ReadFile(manifest)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: reading %s: %w", h.Describe(), manifest, err))
			continue
		}
		if found {
			claimedOn = append(claimedOn, name)
			s.Detail("%s holds this deployment's manifest", name)
		}
	}

	if len(claimedOn) > 0 {
		if cfg.Mesh.Subnet == "" {
			return false, "", fmt.Errorf("%s: mesh.subnet is missing, and this deployment is already on %s, so its subnet is deployed and is not chosen again. Restore it in %s", path, strings.Join(claimedOn, ", "), path)
		}
		s.Detail("%s: mesh.subnet %s is deployed (%s holds this deployment's manifest), so it is kept as it is.", path, cfg.Mesh.Subnet, strings.Join(claimedOn, ", "))
		return false, "deployed, kept", nil
	}
	if unread != nil {
		return false, "", unread
	}
	if len(failures) > 0 {
		return false, "", fmt.Errorf("%w. Nothing was written", errors.Join(failures...))
	}

	iface := cfg.Deployment().Interface()
	taken := slices.Clone(blocked)
	for _, name := range admitted {
		s.Progress("read the networks on " + name)
		probed, err := mesh.ProbeUser(hosts[name])
		if err != nil {
			return false, "", fmt.Errorf("%v. Nothing was written", err)
		}
		live, err := probed.Taken(iface, name)
		if err != nil {
			return false, "", fmt.Errorf("%s: %v. Nothing was written", name, err)
		}
		taken = append(taken, live...)
	}

	choice, err := mesh.Choose(cfg.Mesh.Subnet, taken, random)
	if err != nil {
		return false, "", fmt.Errorf("%s: %w. Nothing was written", path, err)
	}
	subnet := choice.Subnet.String()
	addresses := map[string]string{}
	if choice.Rolled {
		current := map[string]string{}
		for _, name := range cfg.SiteNames() {
			current[name] = cfg.Sites[name].Address
		}
		if addresses, err = mesh.Readdress(cfg.Mesh.Subnet, choice.Subnet, current); err != nil {
			return false, "", fmt.Errorf("%s: rolled %s, but %v. Nothing was written", path, subnet, err)
		}
	}

	if !choice.Rolled && len(admitted) == 0 {
		s.Detail("%s: mesh.subnet %s is kept; no site has admitted this deployment yet, so site admit checks it on each.", path, subnet)
		return false, "kept", nil
	}
	if !choice.Rolled {
		s.Detail("%s: mesh.subnet %s overlaps nothing on %s; kept.", path, subnet, strings.Join(admitted, ", "))
		return false, "kept", nil
	}
	if len(admitted) > 0 && !move {
		overlaps := "something on " + strings.Join(admitted, ", ")
		if current, err := mesh.ParsePrefix(cfg.Mesh.Subnet); err == nil {
			if hits := mesh.Clashes(current, taken); len(hits) > 0 {
				whats := make([]string, len(hits))
				for i, h := range hits {
					whats[i] = h.What
				}
				overlaps = strings.Join(whats, ", ")
			}
		}
		return false, "", &ui.Problem{
			Hint:    "mesh.subnet " + cfg.Mesh.Subnet + " overlaps " + overlaps + ", and is not rolled",
			Explain: fmt.Sprintf("%s admitted this deployment with mesh.subnet %s, which site admit built the host's firewall and Docker address pools around, so a new one means admitting each again. Run paisans init at a terminal, without --yes, which moves the subnet and admits each site again, or free the overlapping network on the host. Nothing was written.", strings.Join(admitted, ", "), cfg.Mesh.Subnet),
			Cause:   fmt.Errorf("%s: %s", path, strings.Join(choice.Why, "; ")),
		}
	}
	if err := config.SetMesh(path, subnet, addresses); err != nil {
		return false, "", err
	}
	s.Detail("%s: rolled mesh.subnet %s (%s).", path, subnet, strings.Join(choice.Why, "; "))
	names := make([]string, 0, len(addresses))
	for name := range addresses {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s.Detail("  sites.%s.address %s -> %s", name, cfg.Sites[name].Address, addresses[name])
	}
	s.Detail("It is fixed from the first apply on: every app trusts it for forwarded client addresses and every service binds an address in it.")
	return true, "rolled to " + subnet, nil
}

// initHosts reaches every declared site for init.
func initHosts(cfg *config.Config) map[string]registry.Runner {
	hosts := map[string]registry.Runner{}
	for _, name := range cfg.SiteNames() {
		hosts[name] = initHost(name, cfg.Sites[name])
	}
	return hosts
}

// notAdmittedHost is a site init skipped as not admitted, as the deployment
// record is read from it: it holds no file, since the deployment has never
// been there, and runs nothing.
type notAdmittedHost struct{ site string }

func (h notAdmittedHost) Run(string) (string, error) {
	return "", fmt.Errorf("%s has not admitted this deployment: %w", h.site, apply.ErrLoginRefused)
}
func (notAdmittedHost) ReadFile(string) (string, bool, error) { return "", false, nil }
func (h notAdmittedHost) Describe() string                    { return h.site }
