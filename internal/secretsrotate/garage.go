package secretsrotate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/acme"
	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/garage"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/storageadd"
)

// Both of Garage's secrets are checked by Garage itself, from garage.toml,
// which a node reads only when it starts: rendering the next value changes
// nothing until the node restarts. So there is no checking side to change
// before the users, and stage 3 is the whole of the rotation: garage.toml is
// written with the next value through a scoped apply (apply.Scope, which
// writes the file and runs no action), and garage is restarted where it does
// not run with it yet.
//
// rpc_secret is the key every node's RPC handshake is made with, so a node on
// the old value and one on the next cannot talk: it cannot be held in two
// values at once. Every node's file is written, then every node is restarted,
// and only then is any of them waited for. Storage is unavailable for that
// restart (Founder decision). With one node, that node still restarts.
//
// admin_token guards each node's admin API, which nothing in the toolkit
// uses: it drives Garage through its command line inside the container. A
// node takes it alone, one at a time, each waited for before the next.
//
// Whether a node already runs with the next value is read on the host, where
// garage.toml is bind mounted and Docker records when the container last
// started: a container started after the file was last written runs with what
// the file says. The value in the file is hashed on the host, so no secret
// crosses the ssh connection in either direction.

// How long every Garage node has to list every other as healthy after a
// restart, and how often to look.
var (
	garageWait = 3 * time.Minute
	garagePoll = 3 * time.Second
)

// garageRPCDowntime is what the plan says rotating rpc_secret costs.
const garageRPCDowntime = "storage, while every Garage node restarts together"

// garageAdmin rotates storage.garage.admin_token.
type garageAdmin struct{ Generated }

// GarageAdminToken is the rotation of storage.garage.admin_token: each Garage
// site's garage.toml is written with the next value and its node restarted,
// one site at a time, every node healthy before the next. Nothing in the
// toolkit presents the token, so it costs nothing.
func GarageAdminToken() Rotator {
	return garageAdmin{Generated{SecretPath: "storage.garage.admin_token"}}
}

func (r garageAdmin) Cost() Cost { return Cost{} }

func (r garageAdmin) Precheck(e Env) error { return garageDeclared(e, r.SecretPath) }

// Checked has nothing to do: each node checks the token from its own
// garage.toml, which stage 3 writes.
func (r garageAdmin) Checked(e Env, next string) error { return nil }

func (r garageAdmin) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is each Garage site's infra stack, in the order storage.garage.sites
// lists them.
func (r garageAdmin) Users(e Env) []Use {
	var out []Use
	for _, site := range e.Cfg.Storage.Garage.Sites {
		out = append(out, Use{Site: site, Stacks: []string{"infra"}, Do: r.present(site)})
	}
	return out
}

// present writes site's garage.toml with the next token, restarts its node
// when it does not run with it yet, and waits for every node to list every
// other as healthy. A node that already runs with it is only waited for.
func (r garageAdmin) present(site string) func(Env, *config.Secrets) error {
	return func(e Env, s *config.Secrets) error {
		next, _ := s.Get(r.SecretPath)
		if next == "" {
			return fmt.Errorf("%s: the secrets handed to it hold no value for %s", site, r.SecretPath)
		}
		if err := writeGarageConfig(e, s, []string{site}); err != nil {
			return err
		}
		has, err := garageRunsWith(e, site, "admin_token", next)
		if err != nil {
			return err
		}
		if !has {
			if err := restartGarage(e, site); err != nil {
				return err
			}
		}
		return everyGarageHealthy(e)
	}
}

// garageRPC rotates storage.garage.rpc_secret.
type garageRPC struct{ Generated }

// GarageRPC is the rotation of storage.garage.rpc_secret: every Garage site's
// garage.toml is written with the next value, every node is restarted
// together, and then every node must list every other as healthy. Storage is
// unavailable for that restart, and the plan says so (Founder decision).
func GarageRPC() Rotator {
	return garageRPC{Generated{SecretPath: "storage.garage.rpc_secret"}}
}

func (r garageRPC) Cost() Cost { return Cost{Downtime: garageRPCDowntime} }

func (r garageRPC) Precheck(e Env) error { return garageDeclared(e, r.SecretPath) }

// Checked has nothing to do: every node checks the secret from its own
// garage.toml, which stage 3 writes.
func (r garageRPC) Checked(e Env, next string) error { return nil }

func (r garageRPC) Accepts(e Env, next string) (bool, error) { return true, nil }

// Users is a single step for every Garage node, since they take the next
// value together. It is named for the first Garage site, whose infra stack
// the health gate checks after it; the step itself waits for every node.
func (r garageRPC) Users(e Env) []Use {
	sites := e.Cfg.Storage.Garage.Sites
	if len(sites) == 0 {
		return nil
	}
	return []Use{{Site: sites[0], Stacks: []string{"infra"}, Do: r.present}}
}

// present brings every Garage node to the next value, and is safe to run
// again:
//
//  1. Every Garage site's garage.toml is written with the next value, each
//     compared against what the last apply recorded before any is written.
//  2. When any node does not run with the next value, garage is restarted on
//     every Garage site, one straight after another and none waited for in
//     between: a node restarted alone could not talk to the others. A restart
//     that fails does not stop the rest.
//  3. It waits for `garage status` on every node to list every node healthy.
//
// Where every node already runs with the next value, it only waits.
func (r garageRPC) present(e Env, s *config.Secrets) error {
	next, _ := s.Get(r.SecretPath)
	if next == "" {
		return fmt.Errorf("the secrets handed to it hold no value for %s", r.SecretPath)
	}
	sites := e.Cfg.Storage.Garage.Sites
	if err := writeGarageConfig(e, s, sites); err != nil {
		return err
	}
	stale := false
	for _, site := range sites {
		has, err := garageRunsWith(e, site, "rpc_secret", next)
		if err != nil {
			return err
		}
		stale = stale || !has
	}
	if stale {
		var errs []error
		for _, site := range sites {
			if err := restartGarage(e, site); err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return fmt.Errorf("%w. Storage is unavailable until every Garage node runs with the next value; run again to restart them", err)
		}
	}
	if err := everyGarageHealthy(e); err != nil {
		return fmt.Errorf("%w. Storage is unavailable until every Garage node lists every other as healthy", err)
	}
	return nil
}

// garageDeclared refuses a rotation of path on a deployment with no Garage
// node, where no garage.toml would be handed the next value.
func garageDeclared(e Env, path string) error {
	if len(e.Cfg.Storage.Garage.Sites) == 0 {
		return fmt.Errorf("storage.garage.sites lists no site, so no Garage node holds %s", path)
	}
	return nil
}

// writeGarageConfig renders s and writes garage.toml on each of sites through
// a scoped apply, which writes the file, records it in the site's manifest and
// runs no action. Every site is compared before any is written, so a file
// somebody edited on one host changes nothing anywhere. A site whose file
// already says what s renders is not written.
func writeGarageConfig(e Env, s *config.Secrets, sites []string) error {
	rendered, err := render.Build(e.Cfg, s)
	if err != nil {
		return err
	}
	rel := apply.GarageConfig(e.Cfg.Deployment())
	plans := make([]*apply.Plan, len(sites))
	for i, site := range sites {
		t, ok := e.Sites[site]
		if !ok {
			return fmt.Errorf("%s: no way to reach it", site)
		}
		sp, err := apply.Build(site, rendered, acme.Module(e.Cfg.ACME.Provider), t, apply.Scope(rel))
		if err != nil {
			return fmt.Errorf("%s: %w", site, err)
		}
		if c := sp.Conflicts(); len(c) > 0 {
			return fmt.Errorf("%s's %s differs from what the last apply recorded, so somebody edited it on the host. Nothing was written. Restore it, or copy what is wanted into the configuration, and run again", site, c[0].Path)
		}
		plans[i] = sp
	}
	for i, sp := range plans {
		if len(sp.Writes()) == 0 {
			continue
		}
		if err := apply.Execute(sp, e.Sites[sites[i]]); err != nil {
			return fmt.Errorf("%s: %w", sites[i], err)
		}
	}
	return nil
}

// garageKeys are the garage.toml settings garageRunsWith reads, as
// garage.toml.tmpl renders them.
var garageKeys = map[string]bool{"rpc_secret": true, "admin_token": true}

// garageRunsWith reports whether site's Garage container runs with next as
// key: garage.toml says next, and the container is running and last started
// after the file was last written. A site with no Garage container does not.
func garageRunsWith(e Env, site, key, next string) (bool, error) {
	if !garageKeys[key] {
		return false, fmt.Errorf("garage.toml renders no %q to read", key)
	}
	t, ok := e.Sites[site]
	if !ok {
		return false, fmt.Errorf("%s: no way to reach it", site)
	}
	out, err := t.Run(garageStateCommand(e.Cfg.Deployment(), key))
	if err != nil {
		return false, fmt.Errorf("%s: reading what its Garage runs with: %w: %s", site, err, lastLine(out))
	}
	if strings.TrimSpace(out) == "absent" {
		return false, nil
	}
	var fields []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 4 {
			fields = f
		}
	}
	if fields == nil {
		return false, fmt.Errorf("%s: reading what its Garage runs with: unexpected answer %q", site, lastLine(out))
	}
	started, err1 := strconv.ParseInt(fields[2], 10, 64)
	written, err2 := strconv.ParseInt(fields[3], 10, 64)
	if err1 != nil || err2 != nil {
		return false, fmt.Errorf("%s: reading what its Garage runs with: unexpected answer %q", site, lastLine(out))
	}
	h := sha256.Sum256([]byte(next))
	return fields[0] == hex.EncodeToString(h[:]) && fields[1] == "true" && started > written, nil
}

// garageStateCommand prints, on the host, the sha256 of key's value in
// garage.toml, whether the garage container runs, when it last started and
// when garage.toml was last written, both in nanoseconds since the epoch; or
// "absent" when the stack has no garage container. The value goes from the
// file to sha256sum through a pipe, never through an argument.
func garageStateCommand(d deployment.Deployment, key string) string {
	file := "/" + apply.GarageConfig(d)
	return fmt.Sprintf(`f=%s; id=$(%s ps -aq garage) || exit 1; [ -n "$id" ] || { echo absent; exit 0; }; state=$(docker inspect --format '{{.State.Running}} {{.State.StartedAt}}' "$id") || exit 1; set -- $state; h=$(sed -n 's/^%s = "\(.*\)"$/\1/p' "$f" | tr -d '\n' | sha256sum | cut -d' ' -f1); printf '%%s %%s %%s %%s\n' "$h" "$1" "$(date -d "$2" +%%s%%N)" "$(date -r "$f" +%%s%%N)"`,
		file, d.ComposeCmd("infra"), key)
}

// restartGarage restarts the garage service of site's infra stack, which
// reads garage.toml again as it starts.
func restartGarage(e Env, site string) error {
	t, ok := e.Sites[site]
	if !ok {
		return fmt.Errorf("%s: no way to reach it", site)
	}
	if out, err := t.Run(e.Cfg.Deployment().ComposeCmd("infra") + " restart garage"); err != nil {
		return fmt.Errorf("%s: restarting Garage: %w: %s", site, err, lastLine(out))
	}
	return nil
}

// garageNodeID is the shape of a full node ID, as `garage node id -q`
// prints it before its address.
var garageNodeID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// everyGarageHealthy waits until `garage status` on every Garage node lists
// every Garage node as healthy, as `storage add` waits after it connects them.
// The command line in the container connects to its own node with the
// rpc_secret in garage.toml, so a node that answers runs with the value its
// file has.
func everyGarageHealthy(e Env) error {
	d := e.Cfg.Deployment()
	sites := e.Cfg.Storage.Garage.Sites
	ids := map[string]string{}
	err := pollWithin("every Garage node listing every other as healthy", garageWait, garagePoll, func() error {
		for _, site := range sites {
			if ids[site] != "" {
				continue
			}
			t, ok := e.Sites[site]
			if !ok {
				return fmt.Errorf("%s: no way to reach it", site)
			}
			out, err := t.Run(garage.Command(d) + " node id -q")
			if err != nil {
				return fmt.Errorf("%s: `garage node id` failed: %s", site, lastLine(out))
			}
			for _, line := range strings.Split(out, "\n") {
				id, _, _ := strings.Cut(strings.TrimSpace(line), "@")
				if garageNodeID.MatchString(id) {
					ids[site] = id
				}
			}
			if ids[site] == "" {
				return fmt.Errorf("%s: `garage node id` printed no node ID: %s", site, lastLine(out))
			}
		}
		for _, site := range sites {
			out, err := e.Sites[site].Run(garage.Command(d) + " status")
			if err != nil {
				return fmt.Errorf("%s: `garage status` failed: %s", site, lastLine(out))
			}
			healthy, _ := storageadd.HealthyNodes(out)
			for _, other := range sites {
				if !healthy[ids[other][:16]] {
					return fmt.Errorf("%s does not list %s (%s) as healthy", site, other, ids[other][:16])
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w. Fix the node named and run again, which resumes here", err)
	}
	return nil
}
