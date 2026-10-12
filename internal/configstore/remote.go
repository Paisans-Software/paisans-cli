package configstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
)

// emptyMark is what the gather command prints for a site that holds no
// version: no repository yet, or one with nothing under refs/versions. It is
// an answer, not a failure, because a site that was never published to is the
// ordinary state before the first publish.
const emptyMark = "__PAISANS_EMPTY__"

// SyncReport is what a site's last sync reached, as the sync script logged it.
type SyncReport struct {
	At    time.Time
	Peers []struct {
		Peer string
		OK   bool
	}
	// SyncErr is why the sync a Pull asked for did not complete, or why the
	// sync log could not be read. The fields above are then the previous run's
	// or empty, and it is reported beside the gathered versions, not instead.
	SyncErr string
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// incomingPath is where a pushed bundle lands for the length of one push. It
// sits beside the repository, in the directory hostprep already owns, so the
// path is derived from ConfigRepo rather than spelled a second time.
func incomingPath(d deployment.Deployment) string {
	return path.Dir(hostprep.ConfigRepo(d)) + "/" + d.Token() + ".incoming.bundle"
}

// Push puts a bundle's versions into the site's repository. The bundle is
// verified first and fetched without a plus in the refspec and without
// pruning, so a push can add versions and can never move or remove one.
func Push(t apply.Transport, d deployment.Deployment, bundle []byte) error {
	tmp, repo := incomingPath(d), hostprep.ConfigRepo(d)
	if err := t.WriteFile(tmp, string(bundle), 0o600); err != nil {
		return fmt.Errorf("write the bundle to %s: %w", t.Describe(), err)
	}
	cmd := fmt.Sprintf("git -C %[1]s bundle verify %[2]s >/dev/null && git -C %[1]s fetch --no-prune --quiet %[2]s 'refs/versions/*:refs/versions/*'; rc=$?; rm -f %[2]s; exit $rc",
		quote(repo), quote(tmp))
	if out, err := t.Run(cmd); err != nil {
		return fmt.Errorf("take the bundle into %s: %w: %s", t.Describe(), err, strings.TrimSpace(out))
	}
	return nil
}

// Pull gathers every version a site holds as one bundle. A site with none
// returns a nil bundle and no error. With syncFirst the site first fetches
// from its peers, so the answer includes what they hold; a sync that fails is
// recorded in the report and the site's own versions are still gathered.
func Pull(t apply.Transport, d deployment.Deployment, syncFirst bool) ([]byte, *SyncReport, error) {
	var syncErr string
	if syncFirst {
		if out, err := t.Run("systemctl start " + d.Prefix() + "-config-sync.service"); err != nil {
			syncErr = strings.TrimSpace(err.Error() + " " + out)
		}
	}
	repo := hostprep.ConfigRepo(d)
	cmd := fmt.Sprintf("set -e; r=%s; if [ ! -d \"$r\" ] || [ -z \"$(git -C \"$r\" for-each-ref --count=1 'refs/versions/*')\" ]; then echo %s; exit 0; fi; "+
		"tmp=$(mktemp); trap 'rm -f \"$tmp\"' EXIT; "+
		"git -C \"$r\" for-each-ref --format='%%(refname)' 'refs/versions/*' | xargs git -C \"$r\" bundle create \"$tmp\" >/dev/null 2>&1; "+
		"base64 -w0 \"$tmp\"; echo",
		quote(repo), emptyMark)
	out, err := t.Run(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("gather the versions on %s: %w: %s", t.Describe(), err, strings.TrimSpace(out))
	}
	line := lastLine(out)
	if line == "" {
		return nil, nil, fmt.Errorf("%s printed neither a bundle nor %s", t.Describe(), emptyMark)
	}
	var bundle []byte
	if line != emptyMark {
		bundle, err = base64.StdEncoding.DecodeString(line)
		if err != nil {
			return nil, nil, fmt.Errorf("the bundle from %s is not base64: %w", t.Describe(), err)
		}
		if len(bundle) == 0 {
			return nil, nil, fmt.Errorf("the bundle from %s is empty", t.Describe())
		}
	}
	// The log only describes the sync. A log that cannot be read or parsed is
	// reported beside the versions, never in place of them: recovery has to
	// work on a site whose log is damaged.
	report, logErr := readSyncLog(t, d)
	if logErr != nil {
		syncErr = strings.TrimSpace(syncErr + " " + logErr.Error())
	}
	if syncErr != "" {
		if report == nil {
			report = &SyncReport{}
		}
		report.SyncErr = syncErr
	}
	return bundle, report, nil
}

// lastLine is the last non-empty line of out. SSHTransport merges stderr into
// stdout, so ssh or sudo may print notices before the command's own answer,
// which is always its final line.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// readSyncLog parses the log the sync script writes, nil when there is none.
func readSyncLog(t apply.Transport, d deployment.Deployment) (*SyncReport, error) {
	body, found, err := t.ReadFile(hostprep.ConfigSyncLog(d))
	if err != nil {
		return nil, fmt.Errorf("read the sync log on %s: %w", t.Describe(), err)
	}
	if !found {
		return nil, nil
	}
	var raw struct {
		At    string `json:"at"`
		Peers []struct {
			Peer string `json:"peer"`
			OK   bool   `json:"ok"`
		} `json:"peers"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("the sync log on %s is not JSON: %w", t.Describe(), err)
	}
	at, err := time.Parse(time.RFC3339, raw.At)
	if err != nil {
		return nil, fmt.Errorf("the sync log on %s has no valid time: %w", t.Describe(), err)
	}
	r := &SyncReport{At: at}
	for _, p := range raw.Peers {
		r.Peers = append(r.Peers, struct {
			Peer string
			OK   bool
		}{p.Peer, p.OK})
	}
	return r, nil
}
