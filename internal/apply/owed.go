package apply

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// Owed is what the host's pending record says an apply still owes it: the
// stacks it wrote and did not act on, or held back, the gateway checks it
// did not run, and the database grants still waiting for their source's
// tables. The configuration store holds the version an apply publishes
// before it changes a host, so a published version is not a deployed one;
// this, beside the version the host's grant record says it applied, is what
// tells them apart.
type Owed struct {
	Stacks  []string
	Gateway bool
	Grants  []string
}

// None is whether the host is owed nothing.
func (o Owed) None() bool { return len(o.Stacks) == 0 && !o.Gateway && len(o.Grants) == 0 }

// String names what is owed, Eg: "chat, the gateway's checks and 1 database
// grant".
func (o Owed) String() string {
	parts := slices.Clone(o.Stacks)
	if o.Gateway {
		parts = append(parts, "the gateway's checks")
	}
	if n := len(o.Grants); n == 1 {
		parts = append(parts, "1 database grant")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("%d database grants", n))
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// ReadOwed reads what the host's pending record owes, each stack once and
// sorted. A host with no record owes nothing.
func ReadOwed(t Transport, d deployment.Deployment) (Owed, error) {
	p, err := readPending(t, d)
	if err != nil {
		return Owed{}, err
	}
	o := Owed{Gateway: p.GatewayChanging || p.GatewayReload, Grants: slices.Clone(p.Grants)}
	for _, a := range p.Actions {
		o.Stacks = append(o.Stacks, a.Stack)
	}
	slices.Sort(o.Stacks)
	o.Stacks = slices.Compact(o.Stacks)
	return o, nil
}
