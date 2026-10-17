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
// ○ not admitted yet, which init skips and leaves to site admit, and ✗ one
// it could not ask. A skipped site is a warning, not a refusal, since admit
// checks the subnet against it as root, and refuses one that overlaps
// anything there. It returns the admitted sites, in name order, and unread,
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
func initAdmissions(r ui.Reporter, cfg *config.Config, hosts map[string]registry.Runner) (admitted []string, unread error) {
	var skipped, unreachable, advice, referrals []string
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
			skipped = append(skipped, name)
			s.Detail("%s", err)
			s.End(ui.Pending, "not admitted, skipped")
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
	for _, name := range skipped {
		r.Warn(name+" has not admitted this deployment yet, so init skipped it",
			fmt.Sprintf("site admit checks the mesh subnet against it as root, and refuses one that overlaps anything there. Send the host's owner a request once init has run:\n\n    paisans site request --site %[1]s > %[1]s.admit\n\nand ask them to run, on the host:\n\n    sudo paisans site admit --request %[1]s.admit", name))
	}
	return admitted, unread
}

// settleMesh is init's subnet decision, over the admitted sites alone (see
// initAdmissions). It asks each first whether it holds this deployment's
// manifest. Once any of them does, the subnet is deployed and settleMesh
// never changes it. Otherwise it refuses with unread, the sites
// initAdmissions could not ask, and with none it gathers everything each
// admitted host holds (see checkMeshLive), keeps the declared subnet if it
// overlaps none of it, and otherwise rolls a random /24 that overlaps none,
// moving each site's address into it with its host number kept. It writes
// paisans-stack.yaml only when it rolled, and says why. It reports whether
// it wrote.
//
// The decision is one step, "settle mesh subnet", whose result is what
// happened to the subnet; why is its details. Its progress names the site
// being read.
func settleMesh(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, unread error, random io.Reader) (bool, error) {
	s := r.Step("settle mesh subnet")
	wrote, result, err := chooseMesh(s, cfg, path, hosts, admitted, unread, random)
	if err != nil {
		s.Fail(err)
		return false, err
	}
	s.Done(result)
	return wrote, nil
}

// chooseMesh is settleMesh's decision, reporting what it finds to s, and
// returning whether it wrote and the short result for the step.
func chooseMesh(s ui.Step, cfg *config.Config, path string, hosts map[string]registry.Runner, admitted []string, unread error, random io.Reader) (bool, string, error) {
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
	var taken []mesh.Taken
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
	if len(admitted) > 0 {
		return false, "", &ui.Problem{
			Hint:    "mesh.subnet " + cfg.Mesh.Subnet + " overlaps something on " + strings.Join(admitted, ", ") + ", and is not rolled",
			Explain: fmt.Sprintf("%s admitted this deployment with mesh.subnet %s, which site admit built the host's firewall and Docker address pools around, so a new one would break the admission. Free the overlapping network on the host, or have each host's owner evict the deployment, run paisans init again, and send them a new request. Nothing was written.", strings.Join(admitted, ", "), cfg.Mesh.Subnet),
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
