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
	"sort"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

// Version is the record's layout. Version 2 added the configuration part
// and version 3 the approvals; an older record is read as one with neither
// yet, and written back as version 3.
const Version = 3

// Record is the grant a deployment last applied on one host, each part
// written by the command that applies it once its steps for the site have
// all succeeded: the keys part by host prepare, the images and
// configuration parts by apply. Approved is the one written before any step:
// what an operator approved there and no command has applied yet. A part
// that was never written is nil.
type Record struct {
	Version    int             `json:"version"`
	Keys       *KeysPart       `json:"keys,omitempty"`
	Images     *ImagesPart     `json:"images,omitempty"`
	Blueprints *BlueprintsPart `json:"blueprints,omitempty"`
	Config     *ConfigPart     `json:"config,omitempty"`
	Approved   *ApprovedPart   `json:"approved,omitempty"`
}

// ApprovedPart is each entry an operator approved on the host that the
// command applying it has not recorded yet. It is written as soon as the
// operator types approve, before the first change, so a run that stops part
// way, at a step that fails or an interrupt, does not ask for the same entries
// again: the operator approved them, and the host has not changed since.
// Applying an entry moves it to the part it belongs to (Update).
type ApprovedPart struct {
	Entries []Approval `json:"entries"`
}

// Approval is one entry approved: a review change's subject and what it
// becomes, which together are what the operator saw and approved.
type Approval struct {
	Subject    string `json:"subject"`
	To         string `json:"to"`
	ApprovedAt string `json:"approved_at"`
	ApprovedBy string `json:"approved_by,omitempty"`
}

// ConfigPart is the version of the configuration apply last applied: its
// hash and revision in the store every site keeps
// (docs/specs/2026-10-12-config-store.md). It makes the host a witness: a
// site that deletes the newer versions and serves an old one cannot hide
// from a host that has applied a newer one, and apply refuses to put an
// older revision on it unless the operator types rollback.
type ConfigPart struct {
	Hash      string    `json:"hash"`
	Revision  int       `json:"revision"`
	AppliedAt time.Time `json:"applied_at"`
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

// BlueprintsPart is the catalog blueprints apply last rendered.
type BlueprintsPart struct {
	AppliedAt  string      `json:"applied_at"`
	ApprovedBy string      `json:"approved_by,omitempty"`
	Entries    []Blueprint `json:"entries"`
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

// Path is the record on a host, the deployment user's, in its state
// directory. It holds fingerprints and image references, nothing secret.
func Path(d deployment.Deployment) string { return d.StatePath("granted.json") }

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
	if rec.Version < 1 || rec.Version > Version {
		return Record{}, fmt.Errorf("version %d, and this toolkit reads versions 1 to %d", rec.Version, Version)
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
		return Record{}, "", false, fmt.Errorf("%s: %s is %w (%v). Delete it with `rm %s`; the next run then reviews every key and image again", t.Describe(), Path(d), ErrMalformed, err, Path(d))
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
// want ("none" for no file), under the deployment's lock.
func writeCommand(d deployment.Deployment, want, content string) string {
	return strings.Join([]string{
		"set -e",
		d.StateLockCommand("the grant record"),
		"f=" + quote(Path(d)),
		"cur=none",
		`if [ -f "$f" ]; then cur=$(sha256sum "$f" | cut -d' ' -f1); fi`,
		`[ "$cur" = ` + quote(want) + ` ] || { echo ` + quote(changedMarker) + `; exit 1; }`,
		"tmp=$(mktemp " + quote(d.StatePath(".granted.XXXXXX")) + ")",
		`trap 'rm -f "$tmp"' EXIT`,
		"printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(content))) + ` | base64 -d > "$tmp"`,
		`chmod 644 "$tmp"`,
		`mv "$tmp" "$f"`,
	}, "; ")
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Scope is the parts of a grant one command reviews and records: host
// prepare's keys, apply's images, or all of them for apply
// with no --site.
type Scope struct {
	Keys, Images bool
	// Stacks, when set, narrows the images to these stacks, as apply --only
	// does: a stack the run leaves alone is neither reviewed nor recorded.
	Stacks []string
}

// holds reports whether rec holds any part s covers.
func (rec Record) holds(s Scope) bool {
	return (s.Keys && rec.Keys != nil) || (s.Images && rec.Images != nil)
}

// PrepareScope is host prepare's.
var PrepareScope = Scope{Keys: true}

// ApplyScope is apply --site's, narrowed to stacks when it names any.
func ApplyScope(stacks ...string) Scope {
	return Scope{Images: true, Stacks: stacks}
}

// AllScope is every part, for apply with no --site.
var AllScope = Scope{Keys: true, Images: true}

func (s Scope) stack(name string) bool {
	return len(s.Stacks) == 0 || slices.Contains(s.Stacks, name)
}

// covers reports whether subject, a review change's, is in a part s
// covers.
func (s Scope) covers(subject string) bool {
	kind, rest, _ := strings.Cut(subject, " ")
	switch kind {
	case "login", "key":
		return s.Keys
	case "image":
		stack, _, _ := strings.Cut(rest, "/")
		return s.Images && s.stack(stack)
	case "blueprint":
		return s.Images && s.stack(rest)
	}
	return false
}

// Approve is rec with each change needing approval recorded as approved at
// now, by approvedBy. An entry approved before as something else is
// replaced, so each holds the latest approval.
func (rec Record) Approve(changes []Change, now time.Time, approvedBy string) Record {
	out := rec
	out.Version = Version
	var entries []Approval
	if rec.Approved != nil {
		entries = append(entries, rec.Approved.Entries...)
	}
	at := now.UTC().Format(time.RFC3339)
	for _, c := range changes {
		if !c.NeedsApproval() {
			continue
		}
		i := slices.IndexFunc(entries, func(a Approval) bool { return a.Subject == c.Subject })
		switch {
		case i < 0:
			entries = append(entries, Approval{Subject: c.Subject, To: c.To, ApprovedAt: at, ApprovedBy: approvedBy})
		case entries[i].To != c.To:
			entries[i] = Approval{Subject: c.Subject, To: c.To, ApprovedAt: at, ApprovedBy: approvedBy}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Subject < entries[j].Subject })
	if len(entries) > 0 {
		out.Approved = &ApprovedPart{Entries: entries}
	}
	return out
}

// approved reports whether c is what rec holds approved.
func (rec Record) approved(c Change) bool {
	if rec.Approved == nil || !c.NeedsApproval() {
		return false
	}
	return slices.ContainsFunc(rec.Approved.Entries, func(a Approval) bool { return a.Subject == c.Subject && a.To == c.To })
}

// applied is rec without the approvals s covers, which the command applying
// s has just applied or replaced, and who approved them when one admin did.
func (rec Record) applied(s Scope) (Record, string) {
	if rec.Approved == nil {
		return rec, ""
	}
	var kept []Approval
	var by []string
	for _, a := range rec.Approved.Entries {
		if !s.covers(a.Subject) {
			kept = append(kept, a)
		} else if a.ApprovedBy != "" && !slices.Contains(by, a.ApprovedBy) {
			by = append(by, a.ApprovedBy)
		}
	}
	out := rec
	out.Approved = nil
	if len(kept) > 0 {
		out.Approved = &ApprovedPart{Entries: kept}
	}
	if len(by) != 1 {
		return out, ""
	}
	return out, by[0]
}

func (s Scope) blueprints(list []Blueprint) []Blueprint {
	var out []Blueprint
	for _, b := range list {
		if s.stack(b.App) {
			out = append(out, b)
		}
	}
	return out
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
// part that only lost entries keeps the name it had. The approvals s covers
// are dropped, since what s covers is now the record itself, and when
// approvedBy is empty, because the run applied entries approved by an
// earlier one, the name those approvals hold is recorded instead.
func Update(rec Record, g Grant, s Scope, now time.Time, approvedBy string) Record {
	at := now.UTC().Format(time.RFC3339)
	rec, earlier := rec.applied(s)
	if approvedBy == "" {
		approvedBy = earlier
	}
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
		// An image is the same image when its stack, service and reference
		// are: a part differing only in the foundation mark is unchanged,
		// and left as it is. When it is written, the mark is written with it.
		if rec.Images == nil || !slices.Equal(imageKeys(rec.Images.Entries), imageKeys(entries)) {
			part := &ImagesPart{AppliedAt: at, ApprovedBy: approvedBy, Entries: entries}
			if rec.Images != nil && approvedNothing(entries, rec.Images.Entries) {
				part.ApprovedBy = rec.Images.ApprovedBy
			}
			out.Images = part
		}
	}
	if s.Images {
		var kept []Blueprint
		if rec.Blueprints != nil {
			for _, b := range rec.Blueprints.Entries {
				if !s.stack(b.App) {
					kept = append(kept, b)
				}
			}
		}
		entries := append(kept, s.blueprints(g.Blueprints)...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].App < entries[j].App })
		switch {
		case len(entries) == 0:
			out.Blueprints = nil
		case rec.Blueprints == nil || !slices.Equal(rec.Blueprints.Entries, entries):
			part := &BlueprintsPart{AppliedAt: at, ApprovedBy: approvedBy, Entries: entries}
			if rec.Blueprints != nil && subset(entries, rec.Blueprints.Entries) {
				part.ApprovedBy = rec.Blueprints.ApprovedBy
			}
			out.Blueprints = part
		}
	}
	return out
}

// WithConfig is rec with the configuration version hash, at revision,
// recorded as applied at now. The version already recorded keeps the time
// it was first applied, so a run that applies it again leaves the record as
// it is.
func (rec Record) WithConfig(hash string, revision int, now time.Time) Record {
	out := rec
	out.Version = Version
	if rec.Config != nil && rec.Config.Hash == hash && rec.Config.Revision == revision {
		return out
	}
	out.Config = &ConfigPart{Hash: hash, Revision: revision, AppliedAt: now.UTC().Truncate(time.Second)}
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

// imageKey is what makes an image the same image: its place and reference.
type imageKey struct{ stack, service, reference string }

func imageKeys(list []Image) []imageKey {
	out := make([]imageKey, len(list))
	for i, image := range list {
		out[i] = imageKey{image.Stack, image.Service, image.Reference}
	}
	return out
}

// approvedNothing reports whether every image of list that of does not hold
// is a foundation image, which nobody approves: a part that only lost
// entries or moved foundation images keeps the name of who approved the rest.
func approvedNothing(list, of []Image) bool {
	held := imageKeys(of)
	for _, image := range list {
		if !image.Foundation && !slices.Contains(held, imageKey{image.Stack, image.Service, image.Reference}) {
			return false
		}
	}
	return true
}
