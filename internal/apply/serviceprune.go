package apply

import (
	"fmt"
	"strings"
)

// RemoveContainers stops and removes each named container, as site prune does
// for a left over service. Its bind mounts stay, and so does any volume, which
// the volume prune after it judges. A failure is collected and reported
// rather than stopping the rest.
func RemoveContainers(site string, names []string, t Transport) error {
	var failed []string
	for _, name := range names {
		if out, err := t.Run("docker rm -f " + shellQuote(name)); err != nil {
			failed = append(failed, name+": "+firstLine(strings.TrimSpace(out)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s: %d container(s) were not removed:\n  %s", site, len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}
