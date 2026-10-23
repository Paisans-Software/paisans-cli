package apply

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// EtcdRemoved is one etcd member this deployment's tooling removed: the site
// it ran on, the cluster it was removed from and its member ID.
//
// It is what lets site add tell a site's old etcd data from anything else
// (siteadd, checkEtcdData): data whose write-ahead log names this cluster
// and a member this cluster removed for that site is a removed member's of
// this cluster. A cluster's ID alone is not that evidence, because etcd
// derives it from the founders' peer URLs and the cluster token, so two
// clusters founded on the same addresses with the same token share it.
type EtcdRemoved struct {
	Site    string
	Cluster uint64
	Member  uint64
}

// EtcdRemovedPath is where a host keeps the removals it was told of, in the
// deployment's state directory: one line each, the site, then the cluster
// and member IDs in hex. site remove and site add write it on the hosts of
// the members that stay, before each removal, and site add reads it from
// every host. It is not rendered and no apply touches it.
func EtcdRemovedPath(d deployment.Deployment) string { return d.StatePath("etcd-removed") }

// RecordEtcdRemovedCommand adds r to a host's list once, creating the state
// directory and the file if they are not there, and ending a last line that
// has no newline first so the new line is a line of its own.
func RecordEtcdRemovedCommand(d deployment.Deployment, r EtcdRemoved) string {
	line := fmt.Sprintf("%s %x %x", r.Site, r.Cluster, r.Member)
	return fmt.Sprintf(`set -e; mkdir -p %s; f=%s; touch "$f"; grep -qxF %s "$f" && exit 0; if [ -s "$f" ] && [ -n "$(tail -c 1 "$f")" ]; then printf '\n' >> "$f"; fi; printf '%%s\n' %s >> "$f"`,
		shellQuote(d.StateDir()), shellQuote(EtcdRemovedPath(d)), shellQuote(line), shellQuote(line))
}

// RecordEtcdRemoved runs RecordEtcdRemovedCommand on a host.
func RecordEtcdRemoved(t Transport, d deployment.Deployment, r EtcdRemoved) error {
	if out, err := t.Run(RecordEtcdRemovedCommand(d, r)); err != nil {
		return fmt.Errorf("recording the removal of %s's etcd member %x in %s: %w: %s", r.Site, r.Member, EtcdRemovedPath(d), err, strings.TrimSpace(out))
	}
	return nil
}

// ReadEtcdRemoved reads a host's list. A host with none has an empty list.
func ReadEtcdRemoved(t Transport, d deployment.Deployment) ([]EtcdRemoved, error) {
	content, found, err := t.ReadFile(EtcdRemovedPath(d))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", EtcdRemovedPath(d), err)
	}
	if !found {
		return nil, nil
	}
	out, err := ParseEtcdRemoved(content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EtcdRemovedPath(d), err)
	}
	return out, nil
}

// ParseEtcdRemoved reads a list. A line it cannot read is an error, never
// skipped: the list is evidence for a deletion.
func ParseEtcdRemoved(content string) ([]EtcdRemoved, error) {
	var out []EtcdRemoved
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("line %d is not a site, a cluster ID and a member ID: %q", i+1, line)
		}
		cluster, err1 := strconv.ParseUint(f[1], 16, 64)
		member, err2 := strconv.ParseUint(f[2], 16, 64)
		if err1 != nil || err2 != nil || cluster == 0 || member == 0 {
			return nil, fmt.Errorf("line %d has an ID that is not hex: %q", i+1, line)
		}
		out = append(out, EtcdRemoved{Site: f[0], Cluster: cluster, Member: member})
	}
	return out, nil
}
