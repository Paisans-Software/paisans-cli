package hostprep

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/ui"
)

// What follows is the host's key allowlist: the key fingerprints a host's
// owner allows host prepare to add to a login user's authorized_keys
// (docs/specs/2026-10-11-config-review.md).
//
// paisans-stack.yaml is one file every admin of a deployment edits, and whoever
// runs host prepare runs it with their own access, so any admin can get a key
// of theirs installed through someone else's run. The list is the host
// owner's answer to that, kept on the host where no edit to paisans-stack.yaml
// reaches it: /etc/paisans/ssh-allow.<user>, root's and 0644, one per login
// user and shared by every deployment on the host. It holds fingerprints,
// not admin names, because a name is whatever paisans-stack.yaml says it is, and
// replacing the key under an admin must not carry that admin's permission to
// the new key.
//
// No file means every key is allowed, so a host behaves as it did before
// until its owner makes a list (founder decision). An empty file allows
// nothing.

// AllowPath is the allowlist for user's authorized_keys.
func AllowPath(user string) string { return ownedDir + "/ssh-allow." + user }

// AllowLine is one key the list allows, and what the line says of it.
type AllowLine struct {
	Fingerprint string
	Comment     string
}

var fingerprintShape = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// ParseAllow reads a list: one fingerprint per line, anything after it a
// comment, and `#` starting a comment line. A line is split at its first run
// of whitespace, as the guard's awk splits it, so the two never disagree on
// what a line allows. A line that is not a fingerprint allows nothing, so a
// mistyped line can only refuse a key, never allow one.
func ParseAllow(content string) []AllowLine {
	var out []AllowLine
	for _, line := range strings.Split(content, "\n") {
		if l, ok := parseAllowLine(line); ok {
			out = append(out, l)
		}
	}
	return out
}

// parseAllowLine reads one line of a list. ok is false for a blank line, a
// comment, and a line whose first field is not a fingerprint.
func parseAllowLine(line string) (AllowLine, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return AllowLine{}, false
	}
	fp := strings.Fields(line)[0]
	if !fingerprintShape.MatchString(fp) {
		return AllowLine{}, false
	}
	return AllowLine{Fingerprint: fp, Comment: strings.TrimSpace(strings.TrimPrefix(line, fp))}, true
}

// AllowFile is a list as its owner wrote it, line by line, for host allow
// and host disallow to change. The file is the owner's: a comment they wrote
// or a line the command does not change is kept as it is, and only the
// lines a command adds or takes off differ.
type AllowFile struct {
	lines []string
}

// ParseAllowFile reads a list to rewrite it. A line that is neither blank,
// a comment, nor a fingerprint is refused, naming it: ParseAllow ignores
// such a line, which allows nothing, but a rewrite would have to keep or
// drop it without knowing what the owner meant, so the owner fixes it by
// hand.
func ParseAllowFile(content string) (AllowFile, error) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for n, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if _, ok := parseAllowLine(line); !ok {
			return AllowFile{}, fmt.Errorf("line %d, %s, is not a fingerprint (SHA256: and 43 characters) and a comment", n+1, strconv.Quote(t))
		}
	}
	return AllowFile{lines: lines}, nil
}

// Lines is every key the file allows.
func (f AllowFile) Lines() []AllowLine { return ParseAllow(f.content()) }

func (f AllowFile) content() string {
	if len(f.lines) == 0 {
		return ""
	}
	return strings.Join(f.lines, "\n") + "\n"
}

// Add is the file with lines appended, each as EncodeAllow writes it.
func (f AllowFile) Add(lines []AllowLine) string {
	var b strings.Builder
	b.WriteString(f.content())
	for _, l := range lines {
		b.WriteString(encodeAllowLine(l))
	}
	return b.String()
}

// Remove is the file without each line allowing a fingerprint drop holds,
// and the lines it took off.
func (f AllowFile) Remove(drop []AllowLine) (string, []AllowLine) {
	var kept []string
	var removed []AllowLine
	for _, line := range f.lines {
		if l, ok := parseAllowLine(line); ok && Allows(drop, l.Fingerprint) {
			removed = append(removed, l)
			continue
		}
		kept = append(kept, line)
	}
	return AllowFile{lines: kept}.content(), removed
}

func encodeAllowLine(l AllowLine) string {
	if l.Comment == "" {
		return l.Fingerprint + "\n"
	}
	return l.Fingerprint + " " + l.Comment + "\n"
}

// Allows reports whether lines hold fingerprint.
func Allows(lines []AllowLine, fingerprint string) bool {
	return slices.ContainsFunc(lines, func(l AllowLine) bool { return l.Fingerprint == fingerprint })
}

// EncodeAllow is the list for user holding lines, with a header saying what it
// is and how it is changed.
func EncodeAllow(user string, lines []AllowLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Keys host prepare may add to %s's authorized_keys, one per line.\n", user)
	b.WriteString("# Changed with paisans host allow and host disallow. An empty list\n")
	b.WriteString("# allows no key; deleting this file allows every key.\n")
	for _, l := range lines {
		b.WriteString(encodeAllowLine(l))
	}
	return b.String()
}

// AllowChangedMarker is what WriteAllowCommand prints when the list changed
// between the read and the write.
const AllowChangedMarker = "paisans: the allowlist changed while it was read"

// WriteAllowCommand replaces user's list with content, under the lock every
// write to authorized_keys and the sidecars takes, through a temporary file
// and a rename, and only if the file is still what was read: wasFound and
// was. A list another command or the owner changed meanwhile is refused
// rather than overwritten.
func WriteAllowCommand(user string, wasFound bool, was, content string) string {
	path := AllowPath(user)
	check := fmt.Sprintf("[ ! -e %s ]", shellQuote(path))
	if wasFound {
		sum := sha256.Sum256([]byte(was))
		check = fmt.Sprintf("[ -f %[1]s ] && [ \"$(sha256sum %[1]s | cut -d' ' -f1)\" = %[2]s ]", shellQuote(path), shellQuote(hex.EncodeToString(sum[:])))
	}
	tmp := shellQuote(path + ".paisans-tmp")
	return Locked(fmt.Sprintf("set -e; %s || { echo %s; exit 1; }; printf %%s %s | base64 -d > %s; chmod 644 %s; mv %s %s",
		check, shellQuote(AllowChangedMarker), shellQuote(base64.StdEncoding.EncodeToString([]byte(content))), tmp, tmp, tmp, shellQuote(path)))
}

// allowGuard is a shell test that fails, naming the key, when user's list
// exists and does not hold fingerprint. Every command that appends a key runs
// it under the lock, so a list made between the plan and the run still
// holds.
func allowGuard(user, fingerprint string) string {
	path := shellQuote(AllowPath(user))
	return fmt.Sprintf("if [ -e %[1]s ]; then awk -v fp=%[2]s '$1 == fp { found = 1 } END { exit !found }' %[1]s || { echo %[3]s; exit 1; }; fi",
		path, shellQuote(fingerprint), shellQuote("paisans: "+fingerprint+" is not in "+AllowPath(user)+", so it is not added"))
}

// KeyRefusal is a key host prepare would add that the host's list does not
// hold.
type KeyRefusal struct {
	Site, User, Admin, Fingerprint string
}

// Refusal is what Execute refuses before any step runs: a key to add that
// the host's allowlist does not hold. Nothing else the list touches is
// refused: sharing or renaming a key already in authorized_keys lets nothing
// new log in, and releasing, forgetting and removing only take away.
func Refusal(plan *Plan) error {
	if len(plan.Refused) == 0 {
		return nil
	}
	var lines, admins []string
	for _, k := range plan.Refused {
		lines = append(lines, fmt.Sprintf("- %s (%s)", k.Fingerprint, k.Admin))
		if !slices.Contains(admins, k.Admin) {
			admins = append(admins, k.Admin)
		}
	}
	first := plan.Refused[0]
	keys := "a key"
	if len(plan.Refused) > 1 {
		keys = fmt.Sprintf("%d keys", len(plan.Refused))
	}
	return &ui.Problem{
		Hint: fmt.Sprintf("%s not on %s's allowlist on %s", keys, first.User, first.Site),
		Explain: strings.Join(lines, "\n") + fmt.Sprintf("\n%s lists the keys host prepare may add for %s, and these are not on it, whoever runs this. The host's owner allows them, after checking each fingerprint with its admin, with: paisans host allow %s %s. Nothing was changed.",
			AllowPath(first.User), first.User, first.Site, strings.Join(admins, " ")),
	}
}

// Reader is what reading a host's lists needs.
type Reader interface {
	Run(command string) (string, error)
	ReadFile(path string) (content string, found bool, err error)
}

var claimedFingerprint = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)

// ClaimedKeys is every key some deployment's sidecar claims in user's
// authorized_keys, each once, with the deployment and the name the sidecar
// gives it as its comment. A sidecar claims a key when its fingerprint
// appears anywhere in it, as planAuthorizedKeys reads them, so a format
// this version does not parse still counts.
func ClaimedKeys(t Reader, user string) ([]AllowLine, error) {
	listing, err := t.Run(fmt.Sprintf(`for f in %s; do [ -f "$f" ] && echo "$f"; done; true`, OwnedKeysGlob(user)))
	if err != nil {
		return nil, err
	}
	var out []AllowLine
	for _, path := range strings.Fields(listing) {
		content, _, err := t.ReadFile(path)
		if err != nil {
			return nil, err
		}
		owner := strings.TrimSuffix(strings.TrimPrefix(path, ownedDir+"/authorized_keys."+user+"."), ".owned")
		names := map[string]string{}
		for _, e := range parseOwned(content) {
			names[e.fingerprint] = e.name
		}
		for _, fp := range claimedFingerprint.FindAllString(content, -1) {
			if Allows(out, fp) {
				continue
			}
			comment := "claimed by " + owner
			if names[fp] != "" {
				comment += " for " + names[fp]
			}
			out = append(out, AllowLine{Fingerprint: fp, Comment: comment})
		}
	}
	return out, nil
}
