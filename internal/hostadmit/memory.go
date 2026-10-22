package hostadmit

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostmem"
)

// planMemory warns about a host with less memory than its roles need
// (hostmem.Floor), counting swap that is on and the swapfile this plan
// makes. It is a warning and never a refusal: a host below its floor runs,
// and may be what its owner has, but it is where the kernel starts killing
// processes. A host whose RAM the probe could not read is warned about too,
// since nothing then says it has enough.
func planMemory(p *planner) error {
	facts := p.f.Swap.Facts
	if facts.MemTotal == 0 {
		p.warn("memory: admit could not read the host's RAM", "It is compared with the least its roles need; check it with `free -m`, against the floors in the host owners' guide.")
		return nil
	}
	if p.swapMade > 0 {
		facts.Active, facts.SwapTotal = true, facts.SwapTotal+p.swapMade
	}
	roles := make([]config.Role, len(p.in.Request.Roles))
	for i, r := range p.in.Request.Roles {
		roles[i] = config.Role(r)
	}
	v := hostmem.Judge(facts, roles)
	if !v.Short() {
		return nil
	}
	advice := "Give the host more RAM, or move a role to another host."
	if p.plan.SwapOffer > 0 {
		advice = "Give the host more RAM, make a swapfile with --swapfile (swap makes up a quarter of the floor at most), or move a role to another host."
	}
	p.warn("memory: "+v.Line(), strings.Join(append(v.Explain(), advice), " "))
	return nil
}
