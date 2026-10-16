package hostprep

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// What follows keeps the deployment's user's authorized_keys holding the
// keys of the admins a site's ssh.keys names. Host admit first wrote the
// operators' keys there; host prepare, run as that user, maintains them from
// ssh.keys. The user is this deployment's alone, so no other deployment
// shares the file.
//
// A key someone else put there, an operator by hand, with options or
// without, is never removed. host prepare only ever adds a listed key, and
// only ever removes a line marked added in the sidecar,
// /srv/paisans/<token>/.paisans/authorized_keys.owned, which records what
// host prepare and host admit added: one `<mark> <fingerprint> <name>` line
// per key, `added` for a line paisans appended. A line of any other shape is
// no claim, so its key's line is never removed. A listed key that was already there is left as it is and never recorded:
// it is often the key the operator logs in with. Every write to the sidecar
// or to authorized_keys runs under the deployment's lock.
//
// The commands use GNU coreutils (`chmod --reference`) and util-linux's
// `flock`, which every Linux profile registered here has. authorized_keys
// itself is OpenSSH's format everywhere, which is why this is not behind
// Profile.

// markAdded starts a sidecar line for a key paisans appended.
const markAdded = "added"

// OwnedKeysPath is the sidecar recording which keys of the deployment
// user's authorized_keys host admit and host prepare added.
func OwnedKeysPath(d deployment.Deployment) string { return d.StatePath("authorized_keys.owned") }

// Locked runs command under the deployment's lock, in its state directory.
func Locked(d deployment.Deployment, command string) string {
	return d.StateLockCommand("the keys") + "; " + command
}

// hostKeyLine is one key line of a host's authorized_keys.
type hostKeyLine struct {
	raw     string // verbatim, so a removal names exactly this line
	key     config.AuthorizedKey
	options bool
}

// ownedEntry is one line of the sidecar: a key this deployment claims, how,
// and what it calls the key.
type ownedEntry struct {
	mark        string
	fingerprint string
	name        string
}

// parseOwned reads a sidecar's `<mark> <fingerprint> <name>` lines. A line of
// any other shape is not a claim this deployment can act on.
func parseOwned(content string) []ownedEntry {
	var out []ownedEntry
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != markAdded || !strings.HasPrefix(f[1], "SHA256:") {
			continue
		}
		out = append(out, ownedEntry{mark: f[0], fingerprint: f[1], name: f[2]})
	}
	return out
}

// userKeys is the deployment user's authorized_keys as host prepare reads
// it: where it is, and every key line it understands.
type userKeys struct {
	dir, file string
	found     bool
	lines     []hostKeyLine
}

// readUserKeys reads the authorized_keys under home on the host t reaches.
func readUserKeys(t Transport, home string) (userKeys, error) {
	k := userKeys{dir: home + "/.ssh"}
	k.file = k.dir + "/authorized_keys"
	content, found, err := t.ReadFile(k.file)
	if err != nil {
		return userKeys{}, err
	}
	k.found = found
	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, options, err := config.ParseKeyLine(trimmed)
		if err != nil {
			continue // not a key this package understands, so never touched
		}
		k.lines = append(k.lines, hostKeyLine{raw: strings.TrimSuffix(raw, "\r"), key: key, options: len(options) > 0})
	}
	return k, nil
}

// planAuthorizedKeys compares the keys s lists with the authorized_keys under
// home and the deployment's sidecar. It returns the additions and the changes
// to the sidecar, which run with the rest of the plan, and the removals,
// which run last: a key is only taken away once every listed key is in place.
func planAuthorizedKeys(t Transport, d deployment.Deployment, home string, s config.SSH) (out Section, removals []Step, err error) {
	listed, problems := s.AuthorizedKeys()
	if len(problems) > 0 {
		// Load refuses these; reaching here is a bug, not an operator error.
		return out, nil, fmt.Errorf("ssh.keys: %s", strings.Join(problems, "; "))
	}

	keys, err := readUserKeys(t, home)
	if err != nil {
		return out, nil, err
	}
	dir, file, found, lines := keys.dir, keys.file, keys.found, keys.lines

	sidecar, _, err := t.ReadFile(OwnedKeysPath(d))
	if err != nil {
		return out, nil, err
	}
	owned := parseOwned(sidecar)

	find := func(fp string) int {
		for i, o := range owned {
			if o.fingerprint == fp {
				return i
			}
		}
		return -1
	}
	drop := func(fp string) {
		if i := find(fp); i >= 0 {
			owned = append(owned[:i:i], owned[i+1:]...)
		}
	}
	onHost := func(fp string) (plain, restricted []hostKeyLine) {
		for _, l := range lines {
			if l.key.Fingerprint == fp {
				if l.options {
					restricted = append(restricted, l)
				} else {
					plain = append(plain, l)
				}
			}
		}
		return plain, restricted
	}

	if !found {
		out.Steps = append(out.Steps, Step{
			Kinds:    []string{KindSSHKeys},
			Title:    "create authorized_keys",
			Describe: fmt.Sprintf("ssh: create %s (0600) in %s (0700), as the deployment's user", file, dir),
			Command:  fmt.Sprintf("set -e; umask 077; [ -d %[1]s ] || install -d -m 700 %[1]s; [ -f %[2]s ] || install -m 600 /dev/null %[2]s", shellQuote(dir), shellQuote(file)),
		})
	}

	isListed := map[string]bool{}
	remaining := 0 // listed keys in the file once additions are done
	for _, k := range listed {
		isListed[k.Fingerprint] = true
		label := keyLabel(k.Fingerprint, k.Name)
		plain, restricted := onHost(k.Fingerprint)
		i := find(k.Fingerprint)
		switch {
		case len(plain) > 0 && i >= 0:
			remaining++
			out.Present = append(out.Present, fmt.Sprintf("ssh: key %s authorized for the deployment's user", label))
			if owned[i].name != k.Name {
				was := owned[i].name
				owned[i].name = k.Name
				out.Steps = append(out.Steps, Step{
					Kinds:    []string{KindSSHKeys},
					Label:    "rename",
					Title:    "rename ssh key",
					Describe: fmt.Sprintf("ssh: record key %s as %s's, which was recorded as %s's", k.Fingerprint, k.Name, was),
					Command:  Locked(d, writeOwned(d, owned)),
				})
			}
		case len(plain) > 0:
			// Already there, put there by someone else (cloud-init, most
			// often), and often the key this run logs in with. Neither added
			// again nor recorded, so host prepare never removes it.
			remaining++
			out.Present = append(out.Present, fmt.Sprintf("ssh: key %s authorized for the deployment's user, not by host prepare, which never removes it", label))
		case len(restricted) > 0:
			// The key is there with options someone chose. It is theirs:
			// adding the plain key beside it would undo the restriction.
			remaining++
			out.Foreign = append(out.Foreign, fmt.Sprintf("ssh: key %s authorized for the deployment's user with options, by `%s`; left as it is", label, restricted[0].raw))
		default:
			remaining++
			drop(k.Fingerprint)
			owned = append(owned, ownedEntry{mark: markAdded, fingerprint: k.Fingerprint, name: k.Name})
			out.Steps = append(out.Steps, Step{
				Kinds:    []string{KindSSHKeys},
				Label:    "add",
				Title:    "authorize ssh key",
				Describe: fmt.Sprintf("ssh: authorize key %s for the deployment's user", label),
				Command:  Locked(d, appendKey(file, k.Line)+"; "+writeOwned(d, owned)),
			})
		}
	}

	// Keys the sidecar records that are no longer listed. Snapshot first:
	// the loop changes owned.
	previous := append([]ownedEntry(nil), owned...)
	for _, o := range previous {
		if isListed[o.fingerprint] {
			continue
		}
		label := keyLabel(o.fingerprint, o.name)
		plain, restricted := onHost(o.fingerprint)
		drop(o.fingerprint)
		switch {
		case len(plain) == 0:
			// Gone from the file already, or kept only with options someone
			// added since. Either way it is no longer host prepare's line.
			describe := fmt.Sprintf("ssh: forget key %s, which is no longer in %s", label, file)
			if len(restricted) > 0 {
				describe = fmt.Sprintf("ssh: forget key %s, which is now authorized with options by `%s` and so is no longer host prepare's", label, restricted[0].raw)
			}
			out.Steps = append(out.Steps, Step{Kinds: []string{KindSSHKeys}, Title: "forget ssh key", Describe: describe, Command: Locked(d, writeOwned(d, owned))})
		default:
			var raws []string
			for _, l := range plain {
				raws = append(raws, l.raw)
			}
			removals = append(removals, Step{
				Kinds:    []string{KindSSHKeys},
				Label:    "remove",
				Title:    "remove ssh key",
				Describe: fmt.Sprintf("ssh: remove key %s from %s, which host prepare or host admit added and ssh.keys no longer lists", label, file),
				Command:  Locked(d, removeLines(file, raws)+"; "+writeOwned(d, owned)),
			})
		}
	}

	// The safety property, checked rather than assumed. Load guarantees a
	// listed key and every listed key is added above, so this cannot fire
	// today; it is here so that a later change to either cannot quietly plan
	// a file with no listed key left in it.
	if len(removals) > 0 && remaining == 0 {
		return out, nil, fmt.Errorf("ssh: removing %d key(s) from %s would leave the deployment's user with none of the keys ssh.keys lists. Nothing was planned", len(removals), file)
	}
	return out, removals, nil
}

func keyLabel(fingerprint, name string) string {
	if name == "" {
		return fingerprint
	}
	return fingerprint + " (" + name + ")"
}

// appendKey adds one line to authorized_keys, first ending the last line if
// someone left it without a newline, so the key is never glued onto it.
// Appending as the file's owner keeps its owner and mode.
func appendKey(file, line string) string {
	f := shellQuote(file)
	return fmt.Sprintf("set -e; if [ -s %[1]s ] && [ -n \"$(tail -c1 %[1]s)\" ]; then echo >> %[1]s; fi; printf '%%s\\n' %[2]s >> %[1]s",
		f, shellQuote(line))
}

// removeLines drops exactly these lines from authorized_keys and nothing else,
// through a temporary file given the original's mode and renamed over it, so
// sshd never reads a half written file. The user owns both, so the owner
// needs no copying. grep exits 1 when no line is left, which here is an empty
// file rather than a failure.
func removeLines(file string, lines []string) string {
	f := shellQuote(file)
	tmp := shellQuote(file + ".paisans-tmp")
	var patterns []string
	for _, l := range lines {
		patterns = append(patterns, "-e "+shellQuote(l))
	}
	return fmt.Sprintf("set -e; grep -vxF %[3]s %[1]s > %[2]s || [ $? -eq 1 ]; chmod --reference=%[1]s %[2]s; mv %[2]s %[1]s",
		f, tmp, strings.Join(patterns, " "))
}

// writeOwned replaces the sidecar with entries, through a temporary file in
// the state directory renamed into place. Fingerprints and comments are
// public, so a command line is no leak.
func writeOwned(d deployment.Deployment, entries []ownedEntry) string {
	path := OwnedKeysPath(d)
	tmp := shellQuote(path + ".paisans-tmp")
	return fmt.Sprintf("printf '%%s' %s > %s && chmod 600 %s && mv %s %s",
		shellQuote(ownedBody(entries)), tmp, tmp, tmp, shellQuote(path))
}

// ownedBody is a sidecar's content: its header, then entries sorted so that
// the file does not change when only the order of the configuration did.
func ownedBody(entries []ownedEntry) string {
	sorted := append([]ownedEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].fingerprint < sorted[j].fingerprint })
	body := "# Keys host admit and host prepare added to this deployment's user's authorized_keys: <mark> <fingerprint> <name>. host prepare removes a line only when it is marked added here and ssh.keys no longer lists it.\n"
	for _, e := range sorted {
		body += e.mark + " " + e.fingerprint + " " + e.name + "\n"
	}
	return body
}

// shellQuote wraps a value in single quotes for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
