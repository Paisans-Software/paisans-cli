// Package meshkey is the one rule for which WireGuard key a site's mesh files
// name while a `paisans mesh rekey` may be part way.
//
// The secrets file names a site's current key, and during a rekey its next
// key beside it. The swap hands the host the next key before the file
// promotes it, so for a while the host runs a key the file does not call
// current. Every command that renders mesh files and writes them to a host
// mid rekey renders that site with the key its host runs, read through the
// WireGuard helper's show as the rekey's own resume reads it: rendering the
// file's current key would hand the old key back to every peer and cut the
// site's tunnels.
package meshkey

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/deployrecord"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// RunningKey is the public key site's interface runs, empty for none,
// reached through t and the WireGuard helper's show public-key, which prints
// it as `wg show <interface> public-key` does (wireguard-tools, wg(8)). It
// is read as apply reads it back: what sudo warns before running the helper
// is not the key, so a host that already runs a site's next key is never
// taken for one that does not.
func RunningKey(site string, t apply.Transport, d deployment.Deployment) (string, error) {
	out, err := apply.WGHelper{T: t, Iface: d.Interface()}.Show("public-key")
	if err != nil {
		return "", fmt.Errorf("%s: reading %s's key through the WireGuard helper: %w", site, d.Interface(), err)
	}
	return apply.ReadMeshKey(site, out)
}

// Peers is the public keys site's interface lists as peers, reached through
// t and the helper's show peers, one per line (wireguard-tools, wg(8)), read
// as strictly as RunningKey.
func Peers(site string, t apply.Transport, d deployment.Deployment) ([]string, error) {
	out, err := apply.WGHelper{T: t, Iface: d.Interface()}.Show("peers")
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s's peers through the WireGuard helper: %w", site, d.Interface(), err)
	}
	return apply.ReadMeshPeers(site, out)
}

// AsRunning is secrets as the mesh runs them: a copy in which every site
// with a next key whose host already runs it has that key as its current
// one, and the names of those sites, sorted. A site with a next key and no
// transport keeps the key the file names, since its host is not written to.
// The copy is for rendering; it is never written back.
func AsRunning(secrets *config.Secrets, d deployment.Deployment, transports map[string]apply.Transport) (*config.Secrets, []string, error) {
	if secrets == nil {
		return nil, nil, nil
	}
	out := *secrets
	out.Sites = make(map[string]config.SiteSecrets, len(secrets.Sites))
	var swapped []string
	for name, site := range secrets.Sites {
		out.Sites[name] = site
	}
	for _, name := range secrets.RekeyPending() {
		t, ok := transports[name]
		if !ok {
			continue
		}
		site := secrets.Sites[name]
		next, err := render.PublicKey(site.WireGuardNextPrivateKey)
		if err != nil {
			return nil, nil, fmt.Errorf("secrets sites.%s.wireguard_next_private_key: %w", name, err)
		}
		runs, err := RunningKey(name, t, d)
		if err != nil {
			return nil, nil, err
		}
		if runs == next {
			site.WireGuardPrivateKey = site.WireGuardNextPrivateKey
			out.Sites[name] = site
			swapped = append(swapped, name)
		}
	}
	sort.Strings(swapped)
	return &out, swapped, nil
}

// Keys is every public key site may be known by on a peer: its current one,
// and its next one while a rekey is under way.
func Keys(secrets *config.Secrets, site string) ([]string, error) {
	s := secrets.Sites[site]
	current, err := render.PublicKey(s.WireGuardPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("secrets sites.%s.wireguard_private_key: %w", site, err)
	}
	out := []string{current}
	if s.WireGuardNextPrivateKey != "" {
		next, err := render.PublicKey(s.WireGuardNextPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("secrets sites.%s.wireguard_next_private_key: %w", site, err)
		}
		out = append(out, next)
	}
	return out, nil
}

// NoAnswer is a host that could not be read while looking for joining
// sites: whether its site is new cannot be told.
type NoAnswer struct {
	Site string
	Err  error
}

func (e *NoAnswer) Error() string { return e.Site + ": " + e.Err.Error() }
func (e *NoAnswer) Unwrap() error { return e.Err }

// Joining is the sites cfg declares, but skip, that are new to this
// deployment, Eg: a replacement declared in the same edit as an eviction.
// Their hosts hold no claim of this deployment in their registry, and
// nothing else says they ever ran in it: no remaining site lists their key as
// a WireGuard peer, and no gateway's deployment record lists them. A missing
// registry entry alone is not enough, since a claim can be lost, or a site's
// ssh section can reach the wrong host; a site that ran and is taken for new
// would be left out of a cut or a rekey while its peers still talk to it, so
// that is refused, naming the site and the evidence.
func Joining(cfg *config.Config, secrets *config.Secrets, skip string, transports map[string]apply.Transport) ([]string, error) {
	var unclaimed, claimed []string
	for _, name := range cfg.SiteNames() {
		if name == skip {
			continue
		}
		t, ok := transports[name]
		if !ok {
			return nil, &NoAnswer{Site: name, Err: fmt.Errorf("no way to reach it")}
		}
		reg, err := registry.Read(t)
		if err != nil {
			return nil, &NoAnswer{Site: name, Err: err}
		}
		if _, ok := reg.Deployments[cfg.ID]; ok {
			claimed = append(claimed, name)
		} else {
			unclaimed = append(unclaimed, name)
		}
	}
	if len(unclaimed) == 0 {
		return nil, nil
	}
	evidence := map[string][]string{}
	for _, name := range claimed {
		// A remaining site that does not answer is refused by the command
		// for its own reasons; here it only gives no evidence.
		peers, err := Peers(name, transports[name], cfg.Deployment())
		if err != nil {
			continue
		}
		for _, site := range unclaimed {
			if secrets == nil || secrets.Sites[site].WireGuardPrivateKey == "" {
				continue
			}
			keys, err := Keys(secrets, site)
			if err != nil {
				continue
			}
			for _, key := range keys {
				if contains(peers, key) {
					evidence[site] = append(evidence[site], name+" lists its WireGuard key as a peer")
					break
				}
			}
		}
	}
	gateways := map[string]registry.Runner{}
	for _, name := range claimed {
		if cfg.Sites[name].Has(config.RoleGateway) {
			gateways[name] = transports[name]
		}
	}
	if len(gateways) > 0 {
		// A record that cannot be read gives no evidence either way.
		rec, found, _ := deployrecord.Gather(gateways, cfg.Deployment())
		if found > 0 {
			for _, site := range unclaimed {
				if rec.Lists("sites", site) {
					evidence[site] = append(evidence[site], "the deployment record on the gateway lists it as deployed")
				}
			}
		}
	}
	for _, site := range unclaimed {
		if len(evidence[site]) > 0 {
			return nil, fmt.Errorf("%s's host holds no claim of this deployment in %s, yet %s, so it has run in this deployment and is not a new site. Check that its ssh section reaches the host it runs on, and that the host's registry still holds this deployment's entry. Nothing was changed", site, registry.Path, strings.Join(evidence[site], ", and "))
		}
	}
	return unclaimed, nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
