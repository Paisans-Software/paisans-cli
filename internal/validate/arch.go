package validate

import (
	"fmt"
	"sort"
	"strings"
)

// dataSitesShareAnArch refuses data sites that record different
// architectures. Patroni's replication, failover and site add's
// pg_basebackup copy the data directory and its WAL byte for byte, and both
// are written in the host's byte order and alignment, so a replica on
// another architecture cannot read what it receives. Which architecture does
// not matter. A site with no arch recorded yet is skipped: apply records it
// on the first run and checks again.
func (c *checker) dataSitesShareAnArch() {
	byArch := map[string][]string{}
	for _, name := range c.cfg.Cluster.Sites {
		if arch := c.cfg.Sites[name].Arch; arch != "" {
			byArch[arch] = append(byArch[arch], name)
		}
	}
	if len(byArch) < 2 {
		return
	}
	var parts []string
	for arch, names := range byArch {
		sort.Strings(names)
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s is %s", name, arch))
		}
	}
	sort.Strings(parts)
	c.refuse("data-sites-differ-in-arch", "cluster.sites",
		"data sites have different architectures",
		"%s. Patroni replicates the data directory byte for byte, so every data site needs the same architecture.",
		strings.Join(parts, " and "))
}
