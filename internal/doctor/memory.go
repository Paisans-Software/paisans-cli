package doctor

import (
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostmem"
)

// Sections of the host's memory checks, read right after sudo: the host's
// own resources, under everything the deployment runs on it.
const (
	SectionSwap   = "swap"
	SectionMemory = "memory"
)

// MemoryCommand prints the host's RAM and swap, in every form, as site
// admit's probe reads them (hostmem.Probe). Every file it reads is
// readable by the login user.
const MemoryCommand = hostmem.Command

// MemoryProbe is MemoryCommand's answer on one site.
type MemoryProbe struct{ Site, Out, Err string }

func (p MemoryProbe) site() string { return p.Site }

// facts is what the probe read, or why it could not be read.
func (p MemoryProbe) facts() (hostmem.Facts, string) {
	if p.Err != "" {
		return hostmem.Facts{}, firstLine(p.Err)
	}
	f, err := hostmem.Parse(p.Out)
	if err != nil {
		return f, err.Error()
	}
	return f, ""
}

// Answered reports whether the probe was read: the memory check needs it.
func (p MemoryProbe) Answered() bool {
	_, why := p.facts()
	return why == ""
}

// Swap warns about a host with no swap on, in the words site admit uses:
// swap the host has and has not turned on is its owner's to turn on, and
// a host with none in any form is offered a swapfile by admit. A probe
// doctor could not read is a warning here, and the memory check is not
// made.
func Swap(probes []MemoryProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f, why := p.facts()
		switch configured := f.Configured(); {
		case why != "":
			out = append(out, Finding{Section: SectionSwap, Level: Warn, Line: fmt.Sprintf("%s: could not read the host's memory and swap", p.Site), More: []string{why}})
		case f.Active:
			line := fmt.Sprintf("%s: on, %s", p.Site, hostmem.Size(f.SwapTotal))
			if f.ZRAMOn > 0 {
				line += fmt.Sprintf(", %s of it zram", hostmem.Size(f.ZRAMOn))
			}
			out = append(out, Finding{Section: SectionSwap, Level: OK, Line: line})
		case len(configured) > 0:
			out = append(out, Finding{Section: SectionSwap, Level: Warn,
				Line: fmt.Sprintf("%s: configured but not on (%s)", p.Site, strings.Join(configured, ", ")),
				More: []string{hostmem.SwapNeeded, "The host's owner turns it on (swapon -a, or the zram service), or removes it and runs `paisans site admit` again to be offered a swapfile."}})
		default:
			out = append(out, Finding{Section: SectionSwap, Level: Warn,
				Line: fmt.Sprintf("%s: none on this host", p.Site),
				More: []string{hostmem.SwapNeeded, "The host's owner runs `paisans site admit` again, which offers a swapfile where it can make one (`--swapfile`)."}})
		}
	}
	return out
}

// Memory compares each site's RAM, and the swap it has on, with the least
// what the site runs in cfg needs (hostmem.ForSite): under it is a warning, since the site
// runs, but it is where the kernel starts killing processes. A probe
// doctor could not read has its warning under Swap and nothing here.
func Memory(cfg *config.Config, probes []MemoryProbe) []Finding {
	var out []Finding
	for _, p := range probes {
		f, why := p.facts()
		if why != "" {
			continue
		}
		if f.MemTotal == 0 {
			out = append(out, Finding{Section: SectionMemory, Level: Warn, Line: fmt.Sprintf("%s: could not read the host's RAM", p.Site),
				More: []string{"/proc/meminfo gave no MemTotal, so nothing says the host has the memory its roles need."}})
			continue
		}
		v := hostmem.Judge(f, cfg.Sites[p.Site].Roles, hostmem.ForSite(cfg, p.Site))
		if !v.Short() {
			out = append(out, Finding{Section: SectionMemory, Level: OK, Line: p.Site + ": " + v.Line()})
			continue
		}
		out = append(out, Finding{Section: SectionMemory, Level: Warn, Line: p.Site + ": " + v.Line(),
			More: append(v.Explain(), "Give the host more RAM, turn on or add swap (`paisans site admit --swapfile` on a host with none), or move a role to another host.")})
	}
	return out
}
