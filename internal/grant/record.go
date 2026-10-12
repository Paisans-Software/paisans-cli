package grant

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/registry"
)

// Version is the record's layout.
const Version = 1

// Record is the grant a deployment last applied on one host, each part
// written by the command that applies it once its steps for the site have
// all succeeded: the keys part by host prepare, the images and web server
// parts by apply. A part that was never written is nil.
type Record struct {
	Version   int            `json:"version"`
	Keys      *KeysPart      `json:"keys,omitempty"`
	Images    *ImagesPart    `json:"images,omitempty"`
	WebServer *WebServerPart `json:"web_server,omitempty"`
}

// KeysPart is the login user and the keys host prepare last made its
// authorized keys.
type KeysPart struct {
	AppliedAt string `json:"applied_at"`
	// ApprovedBy is the admin whose key the operator's ssh agent held when
	// the latest addition or change was approved, when exactly one admin's
	// was. A label for the next reader, not a check.
	ApprovedBy string `json:"approved_by,omitempty"`
	Login      string `json:"login"`
	Entries    []Key  `json:"entries"`
}

// ImagesPart is the images apply last ran.
type ImagesPart struct {
	AppliedAt  string  `json:"applied_at"`
	ApprovedBy string  `json:"approved_by,omitempty"`
	Entries    []Image `json:"entries"`
}

// WebServerPart is the include directory apply last wrote into.
type WebServerPart struct {
	AppliedAt  string `json:"applied_at"`
	ApprovedBy string `json:"approved_by,omitempty"`
	IncludeDir string `json:"include_dir"`
}

// ErrMalformed is a record that cannot be read as one: corrupt, or written
// by a toolkit with a newer layout. It is not rewritten, since what it held
// is unknown; deleting it by hand makes the next run review everything
// again, which is safe.
var ErrMalformed = errors.New("not a grant record this toolkit reads")

// ErrChanged is a write refused because another command changed the record
// between the read and the write.
var ErrChanged = errors.New(changedMarker)

const changedMarker = "paisans: the grant record changed while it was read"

// Path is the record on a host, named by the deployment's token, beside the
// registry. It is root's and 0644: it holds fingerprints and image references,
// nothing secret, so it is readable without sudo, and only root writes it,
// so the login user alone cannot rewrite the baseline a review compares with.
func Path(d deployment.Deployment) string {
	return registry.Dir + "/granted." + d.Token() + ".json"
}

// RemoveCommand deletes the record, for site remove's stage 3, which deletes
// the deployment's registry entry beside it.
func RemoveCommand(d deployment.Deployment) string { return "rm -f -- " + quote(Path(d)) }

// Encode is the record as written.
func Encode(rec Record) string {
	rec.Version = Version
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rec)
	return b.String()
}

// Parse reads a record.
func Parse(content string) (Record, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		return Record{}, err
	}
	if rec.Version != Version {
		return Record{}, fmt.Errorf("version %d, and this toolkit reads version %d", rec.Version, Version)
	}
	return rec, nil
}

// Read reads the record on the host t reaches. A host with none has an empty
// baseline, against which every entry of a grant is new: that is how the
// record is first made, on every host of a deployment built before it existed.
func Read(t registry.Runner, d deployment.Deployment) (Record, string, bool, error) {
	content, found, err := t.ReadFile(Path(d))
	if err != nil {
		return Record{}, "", false, fmt.Errorf("%s: reading %s: %w", t.Describe(), Path(d), err)
	}
	if !found {
		return Record{Version: Version}, "", false, nil
	}
	rec, err := Parse(content)
	if err != nil {
		return Record{}, "", false, fmt.Errorf("%s: %s is %w (%v). Delete it with `sudo rm %s`; the next run then reviews every key and image again", t.Describe(), Path(d), ErrMalformed, err, Path(d))
	}
	return rec, content, true, nil
}

// Write changes the record on the host t reaches by change, under the
// registry's lock, through a temporary file and a rename. The record is read
// first and written only if the file still holds what was read, so a part
// another command wrote meanwhile is never lost: a write that finds the file
// changed reads it again and makes the change again, a few times. A change
// that leaves the record as it was writes nothing. It reports whether it
// wrote.
func Write(t registry.Runner, d deployment.Deployment, change func(Record) Record) (bool, error) {
	var err error
	for range 3 {
		var rec Record
		var raw string
		var found bool
		rec, raw, found, err = Read(t, d)
		if err != nil {
			return false, err
		}
		next := change(rec)
		if found && Encode(next) == Encode(rec) {
			return false, nil
		}
		if !found && Encode(next) == Encode(Record{}) {
			return false, nil
		}
		want := "none"
		if found {
			sum := sha256.Sum256([]byte(raw))
			want = hex.EncodeToString(sum[:])
		}
		out, runErr := t.Run(writeCommand(d, want, Encode(next)))
		if runErr == nil {
			return true, nil
		}
		if !strings.Contains(out, changedMarker) {
			return false, fmt.Errorf("%s: writing %s: %w", t.Describe(), Path(d), runErr)
		}
		err = fmt.Errorf("%s: %w", t.Describe(), ErrChanged)
	}
	return false, err
}

// writeCommand replaces the record with content if the file still hashes to
// want ("none" for no file), under the lock the registry and the deployment
// record take.
func writeCommand(d deployment.Deployment, want, content string) string {
	return strings.Join([]string{
		"set -e",
		"umask 077",
		"mkdir -p " + registry.Dir,
		"chmod 711 " + registry.Dir,
		"exec 9>>" + registry.LockPath,
		fmt.Sprintf("flock -w 60 9 || { echo 'paisans: another command holds %s'; exit 1; }", registry.LockPath),
		"f=" + quote(Path(d)),
		"cur=none",
		`if [ -f "$f" ]; then cur=$(sha256sum "$f" | cut -d' ' -f1); fi`,
		`[ "$cur" = ` + quote(want) + ` ] || { echo ` + quote(changedMarker) + `; exit 1; }`,
		"tmp=$(mktemp " + registry.Dir + "/.granted.XXXXXX)",
		`trap 'rm -f "$tmp"' EXIT`,
		"printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(content))) + ` | base64 -d > "$tmp"`,
		`chmod 644 "$tmp"`,
		`mv "$tmp" "$f"`,
	}, "; ")
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Scope is the parts of a grant one command reviews and records: host
// prepare's keys, apply's images and web server, or all of them for apply
// with no --site.
type Scope struct {
	Keys, Images, WebServer bool
	// Stacks, when set, narrows the images to these stacks, as apply --only
	// does: a stack the run leaves alone is neither reviewed nor recorded.
	Stacks []string
}

// holds reports whether rec holds any part s covers.
func (rec Record) holds(s Scope) bool {
	return (s.Keys && rec.Keys != nil) || (s.Images && rec.Images != nil) || (s.WebServer && rec.WebServer != nil)
}

// PrepareScope is host prepare's.
var PrepareScope = Scope{Keys: true}

// ApplyScope is apply --site's, narrowed to stacks when it names any.
func ApplyScope(stacks ...string) Scope {
	return Scope{Images: true, WebServer: true, Stacks: stacks}
}

// AllScope is every part, for apply with no --site.
var AllScope = Scope{Keys: true, Images: true, WebServer: true}

func (s Scope) stack(name string) bool {
	return len(s.Stacks) == 0 || slices.Contains(s.Stacks, name)
}

func (s Scope) images(list []Image) []Image {
	var out []Image
	for _, i := range list {
		if s.stack(i.Stack) {
			out = append(out, i)
		}
	}
	return out
}

// Update is rec with the parts s covers set to g, as applied at now. A part
// whose content is unchanged is kept as it was, applied_at and all, so a run
// that changed nothing leaves the record as it is. approvedBy is recorded on
// a part that adds or changes an entry, since it names who approved that; a
// part that only lost entries keeps the name it had.
func Update(rec Record, g Grant, s Scope, now time.Time, approvedBy string) Record {
	at := now.UTC().Format(time.RFC3339)
	out := rec
	out.Version = Version
	if s.Keys {
		keys := append([]Key{}, g.Keys...)
		if rec.Keys == nil || rec.Keys.Login != g.Login || !slices.Equal(rec.Keys.Entries, keys) {
			part := &KeysPart{AppliedAt: at, ApprovedBy: approvedBy, Login: g.Login, Entries: keys}
			if rec.Keys != nil && rec.Keys.Login == g.Login && subset(keys, rec.Keys.Entries) {
				part.ApprovedBy = rec.Keys.ApprovedBy
			}
			out.Keys = part
		}
	}
	if s.Images {
		var kept []Image
		if rec.Images != nil {
			for _, i := range rec.Images.Entries {
				if !s.stack(i.Stack) {
					kept = append(kept, i)
				}
			}
		}
		entries := append(kept, s.images(g.Images)...)
		sortImages(entries)
		if entries == nil {
			entries = []Image{}
		}
		if rec.Images == nil || !slices.Equal(rec.Images.Entries, entries) {
			part := &ImagesPart{AppliedAt: at, ApprovedBy: approvedBy, Entries: entries}
			if rec.Images != nil && subset(entries, rec.Images.Entries) {
				part.ApprovedBy = rec.Images.ApprovedBy
			}
			out.Images = part
		}
	}
	if s.WebServer {
		switch {
		case g.WebServer == nil:
			out.WebServer = nil
		case rec.WebServer == nil || rec.WebServer.IncludeDir != g.WebServer.IncludeDir:
			out.WebServer = &WebServerPart{AppliedAt: at, ApprovedBy: approvedBy, IncludeDir: g.WebServer.IncludeDir}
		}
	}
	return out
}

// subset reports whether every entry of list is in of, unchanged.
func subset[T comparable](list, of []T) bool {
	for _, e := range list {
		if !slices.Contains(of, e) {
			return false
		}
	}
	return true
}
