package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

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
// apply and host prepare run it before they change the host, dry run or not,
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

// settleMesh is init's subnet decision. It asks every site first whether it
// holds this deployment's manifest. Once any of them does, the subnet is
// deployed and settleMesh never changes it. A site that refuses the login
// has not admitted the deployment yet, and is left to host admit, which
// checks the subnet against it as root; every other site must answer, since
// an unchecked host is exactly where a collision would go unseen. It gathers
// everything each admitted host holds (see checkMeshLive), keeps the
// declared subnet if it overlaps none of it, and otherwise rolls a random
// /24 that overlaps none, moving each site's address into it with its host
// number kept. It writes paisans-stack.yaml only when it rolled, and says
// why. It reports whether it wrote.
//
// The decision is one step, "settle mesh subnet", whose result is what
// happened to the subnet; why is its details. Its progress names the site
// being read, since a site that does not answer is waited on for its connect
// timeout on each of ssh's attempts.
func settleMesh(r ui.Reporter, cfg *config.Config, path string, hosts map[string]registry.Runner, random io.Reader) (bool, error) {
	s := r.Step("settle mesh subnet")
	wrote, result, err := chooseMesh(s, cfg, path, hosts, random)
	if err != nil {
		s.Fail(err)
		return false, err
	}
	s.Done(result)
	return wrote, nil
}

// chooseMesh is settleMesh's decision, reporting what it finds to s, and
// returning whether it wrote and the short result for the step.
func chooseMesh(s ui.Step, cfg *config.Config, path string, hosts map[string]registry.Runner, random io.Reader) (bool, string, error) {
	manifest := cfg.Deployment().Manifest()
	var admitted, unreachable, advice, referrals []string
	var failures []error
	var claimedOn []string
	for _, name := range cfg.SiteNames() {
		s.Progress("read " + name)
		h := hosts[name]
		_, found, err := h.ReadFile(manifest)
		if errors.Is(err, apply.ErrLoginRefused) {
			s.Detail("%s has not admitted this deployment yet; host admit checks the subnet against it as root, and refuses one that overlaps anything there", name)
			continue
		}
		if err != nil {
			err = fmt.Errorf("%s: reading %s: %w", h.Describe(), manifest, err)
			// Each site is one line in the operator's words, and what to do
			// about it follows the list. The full chain, with the file it
			// failed at, is the cause, for --verbose.
			hint, explain := ui.Describe(err)
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
			continue
		}
		admitted = append(admitted, name)
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
	if len(unreachable) > 0 {
		explain := slices.Concat(unreachable, advice, referrals)
		return false, "", &ui.Problem{
			Hint:    "init could not read every site, so mesh.subnet is not settled",
			Explain: strings.Join(append(explain, "Nothing was written. Every host has to be checked, since a subnet chosen without one is a subnet that may collide on it."), "\n"),
			Cause:   errors.Join(failures...),
		}
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
		s.Detail("%s: mesh.subnet %s is kept; no site has admitted this deployment yet, so host admit checks it on each.", path, subnet)
		return false, "kept", nil
	}
	if !choice.Rolled {
		s.Detail("%s: mesh.subnet %s overlaps nothing on %s; kept.", path, subnet, strings.Join(admitted, ", "))
		return false, "kept", nil
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
