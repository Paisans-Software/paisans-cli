package configstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// noStoreMark is what the push's first command prints for a site with no
// repository.
const noStoreMark = "__PAISANS_NO_STORE__"

// ErrNoStore is a site with no configuration store yet: one host prepare
// has not run on since the store was added. paisans host prepare makes it.
var ErrNoStore = errors.New("no configuration store; paisans host prepare makes it")

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

// checkID refuses a deployment whose id is not one before any name derived
// from it reaches a command line. A recovery reads the id from a host's
// registry, which whoever has root on that host can write.
func checkID(d deployment.Deployment) error {
	if !deployment.ValidID(d.ID) {
		return fmt.Errorf("%q is not a deployment id", d.ID)
	}
	return nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Push puts a bundle's versions into the site's repository. The bundle is
// verified first and taken in by hostprep.ConfigReceiveShell's paisans_receive,
// which creates each version the bundle names correctly where it is absent
// and ignores every other ref, so a push can add versions and can never move
// or remove one.
func Push(t apply.Transport, d deployment.Deployment, bundle []byte) error {
	if err := checkID(d); err != nil {
		return err
	}
	tmp, repo := hostprep.ConfigIncoming(d), hostprep.ConfigRepo(d)
	// A site host prepare has not given a store has nowhere to take the
	// bundle, and is told apart so the refusal can name what makes one.
	out, err := t.Run(fmt.Sprintf("if [ -d %s ]; then echo ok; else echo %s; fi", quote(repo), noStoreMark))
	if err != nil {
		return fmt.Errorf("look for the store on %s: %w: %s", t.Describe(), err, strings.TrimSpace(out))
	}
	if lastLine(out) == noStoreMark {
		return fmt.Errorf("%s: %w", t.Describe(), ErrNoStore)
	}
	if err := t.WriteFile(tmp, string(bundle), 0o600); err != nil {
		return fmt.Errorf("write the bundle to %s: %w", t.Describe(), err)
	}
	// The comment names the command in an error, which quotes its first line.
	cmd := "# take the pushed versions into the store\n" + hostprep.ConfigReceiveShell + fmt.Sprintf("git -C %[1]s bundle verify %[2]s >/dev/null && paisans_receive %[1]s %[2]s; rc=$?; rm -f %[2]s; exit $rc",
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
	if err := checkID(d); err != nil {
		return nil, nil, err
	}
	var syncErr string
	if syncFirst {
		if out, err := t.Run("systemctl start " + quote(d.Prefix()+"-config-sync.service")); err != nil {
			syncErr = strings.TrimSpace(err.Error() + " " + out)
		}
	}
	// Only correctly named versions are listed, by paisans_versions, and
	// given to bundle create on its stdin, so no ref name reaches a command
	// line. bundle create's stderr is kept for the error; the base64 line is
	// always the last.
	repo := hostprep.ConfigRepo(d)
	cmd := "# bundle the versions the store holds\n" + hostprep.ConfigVersionsShell + fmt.Sprintf("set -e; r=%s; v=''; if [ -d \"$r\" ]; then v=$(paisans_versions \"$r\" refs/versions/); fi; "+
		"if [ -z \"$v\" ]; then echo %s; exit 0; fi; "+
		"tmp=$(mktemp); trap 'rm -f \"$tmp\"' EXIT; "+
		"printf '%%s\\n' $v | sed 's|^|refs/versions/|' | git -C \"$r\" bundle create --quiet \"$tmp\" --stdin >/dev/null; "+
		"base64 -w0 <\"$tmp\"; echo",
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
