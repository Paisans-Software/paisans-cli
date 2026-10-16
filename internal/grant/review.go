package grant

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// Op is how an entry of the grant compares with the record.
type Op byte

const (
	// Added is an entry in the grant only. It needs approval, unless it is
	// a foundation image.
	Added Op = '+'
	// Changed is an entry in both, different. It needs approval, unless it
	// is a foundation image.
	Changed Op = '~'
	// Removed is an entry in the record only. It is shown and not approved,
	// because it only takes away.
	Removed Op = '-'
)

// Change is one line of a review.
type Change struct {
	Op Op
	// Subject is what the entry is about, Eg: "key SHA256:...", "image
	// talk/app", and To what it becomes, together what an approval covers.
	Subject, To string
	// Text is the line the operator reads, without its mark.
	Text string
	// Foundation is an addition or a change of a foundation image (Image's
	// Foundation): shown, and not approved here.
	Foundation bool
}

// NeedsApproval reports whether the change adds or changes something other
// than a foundation image. A foundation image is the toolkit's own, listed in
// the core catalog's foundation.yaml, which the team reviews in that
// catalog's pull requests, so approving it again at every apply would repeat
// a check already made.
func (c Change) NeedsApproval() bool { return c.Op != Removed && !c.Foundation }

// Line is the change as the review prints it. Its text comes from the
// configuration and from the host's record, so any control character in it
// is shown as an escape (ui.Visible): a comment carrying ESC cannot redraw
// the review.
func (c Change) Line() string { return string(c.Op) + " " + ui.Visible(c.Text) }

// Review is what one site's grant changes from its record: keys first, then
// images, then catalog blueprints. An entry in both and the same is not in it.
type Review struct {
	Site    string
	Changes []Change
	// First is a review against a record holding none of the parts it
	// covers: the host has no record of them yet, every entry is new, and
	// approving it is the record being made.
	First bool
	// Approved is each change an operator approved on the host earlier and
	// no command has applied yet. It is not asked about again, and not
	// listed: the operator saw it when they approved it.
	Approved []Change
}

// Pending is how many changes need approval.
func (r Review) Pending() int {
	n := 0
	for _, c := range r.Changes {
		if c.NeedsApproval() {
			n++
		}
	}
	return n
}

// Unapproved is each change needing approval that approved does not hold
// exactly: apply with no --site approves every site's review once, and each
// step it then runs checks that it is about to apply what was approved and
// nothing since.
func (r Review) Unapproved(approved Review) []Change {
	held := map[string]string{}
	for _, c := range approved.Changes {
		if c.NeedsApproval() {
			held[c.Subject] = c.To
		}
	}
	var out []Change
	for _, c := range r.Changes {
		if to, ok := held[c.Subject]; c.NeedsApproval() && (!ok || to != c.To) {
			out = append(out, c)
		}
	}
	return out
}

// Compare is site's review: the parts of g that s covers against rec.
func Compare(site string, g Grant, rec Record, s Scope) Review {
	r := Review{Site: site, First: !rec.holds(s)}
	if s.Keys {
		r.Changes = append(r.Changes, compareKeys(g, rec.Keys)...)
	}
	if s.Images {
		var was []Image
		if rec.Images != nil {
			was = s.images(rec.Images.Entries)
		}
		r.Changes = append(r.Changes, compareImages(s.images(g.Images), was)...)
	}
	if s.Images {
		var was []Blueprint
		if rec.Blueprints != nil {
			was = s.blueprints(rec.Blueprints.Entries)
		}
		r.Changes = append(r.Changes, compareBlueprints(s.blueprints(g.Blueprints), was)...)
	}
	var asked []Change
	for _, c := range r.Changes {
		if rec.approved(c) {
			r.Approved = append(r.Approved, c)
		} else {
			asked = append(asked, c)
		}
	}
	r.Changes = asked
	return r
}

// blueprintLabel is how a review names a catalog blueprint's release.
func blueprintLabel(b Blueprint) string {
	release := b.Commit
	if len(release) > 12 {
		release = release[:12]
	}
	if b.Tag != "" {
		release = b.Tag + " (" + release + ")"
	}
	return b.Blueprint + " at " + release
}

// compareBlueprints is each app's catalog blueprint against the record. A
// blueprint whose files changed is a change even at the same release name,
// since the files are what the render used.
func compareBlueprints(now, was []Blueprint) []Change {
	prior := map[string]Blueprint{}
	for _, b := range was {
		prior[b.App] = b
	}
	var out []Change
	for _, b := range now {
		subject, to := "blueprint "+b.App, b.Blueprint+"@"+b.SHA256
		p, ok := prior[b.App]
		switch {
		case !ok:
			out = append(out, Change{Op: Added, Subject: subject, To: to, Text: "blueprint " + b.App + ": " + blueprintLabel(b)})
		case p.Blueprint != b.Blueprint || p.SHA256 != b.SHA256:
			out = append(out, Change{Op: Changed, Subject: subject, To: to, Text: "blueprint " + b.App + ": " + blueprintLabel(p) + " → " + blueprintLabel(b)})
		}
		delete(prior, b.App)
	}
	for _, b := range was {
		if _, gone := prior[b.App]; gone {
			out = append(out, Change{Op: Removed, Subject: "blueprint " + b.App, Text: "blueprint " + b.App + ": " + blueprintLabel(b)})
		}
	}
	return out
}

func keyLabel(k Key) string {
	if k.Comment == "" {
		return k.Admin
	}
	return k.Admin + ", " + k.Comment
}

func compareKeys(g Grant, was *KeysPart) []Change {
	var out []Change
	var old []Key
	if was != nil {
		old = was.Entries
		// A login that changed moves every key to another account, so it
		// is a change of its own, ahead of the keys.
		if was.Login != g.Login {
			out = append(out, Change{Op: Changed, Subject: "login", To: g.Login,
				Text: fmt.Sprintf("login: %s → %s; every key below may log in as %s", was.Login, g.Login, g.Login)})
		}
	}
	find := func(list []Key, fp string) (Key, bool) {
		for _, k := range list {
			if k.Fingerprint == fp {
				return k, true
			}
		}
		return Key{}, false
	}
	for _, k := range g.Keys {
		to := g.Login + "\x00" + k.Admin + "\x00" + k.Comment
		prior, ok := find(old, k.Fingerprint)
		switch {
		case !ok:
			out = append(out, Change{Op: Added, Subject: "key " + k.Fingerprint, To: to,
				Text: fmt.Sprintf("key %s (%s) may log in as %s", k.Fingerprint, keyLabel(k), g.Login)})
		case prior != k:
			out = append(out, Change{Op: Changed, Subject: "key " + k.Fingerprint, To: to,
				Text: fmt.Sprintf("key %s: (%s) → (%s)", k.Fingerprint, keyLabel(prior), keyLabel(k))})
		}
	}
	for _, k := range old {
		if _, ok := find(g.Keys, k.Fingerprint); !ok {
			out = append(out, Change{Op: Removed, Subject: "key " + k.Fingerprint, Text: fmt.Sprintf("key %s (%s)", k.Fingerprint, keyLabel(k))})
		}
	}
	return out
}

// compareImages lists each stack's changes together, by stack and then
// service, whatever each change is, so a long review reads stack by stack.
func compareImages(now, was []Image) []Change {
	type entry struct {
		stack, service string
		change         Change
	}
	var all []entry
	for _, c := range imageChanges(now, was) {
		stack, service, _ := strings.Cut(strings.TrimPrefix(c.Subject, "image "), "/")
		all = append(all, entry{stack, service, c})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].stack != all[j].stack {
			return all[i].stack < all[j].stack
		}
		return all[i].service < all[j].service
	})
	out := make([]Change, 0, len(all))
	for _, e := range all {
		out = append(out, e.change)
	}
	return out
}

func imageChanges(now, was []Image) []Change {
	var out []Change
	find := func(list []Image, stack, service string) (Image, bool) {
		for _, i := range list {
			if i.Stack == stack && i.Service == service {
				return i, true
			}
		}
		return Image{}, false
	}
	for _, i := range now {
		name := i.Stack + "/" + i.Service
		prior, ok := find(was, i.Stack, i.Service)
		switch {
		case !ok:
			out = append(out, Change{Op: Added, Subject: "image " + name, To: i.Reference, Text: "image " + name + ": " + i.Reference, Foundation: i.Foundation})
		case prior.Reference != i.Reference:
			out = append(out, Change{Op: Changed, Subject: "image " + name, To: i.Reference, Text: "image " + name + ": " + prior.Reference + " → " + i.Reference, Foundation: i.Foundation})
		}
	}
	for _, i := range was {
		if _, ok := find(now, i.Stack, i.Service); !ok {
			name := i.Stack + "/" + i.Service
			out = append(out, Change{Op: Removed, Subject: "image " + name, Text: "image " + name + ": " + i.Reference})
		}
	}
	return out
}

// Summary is what the reviews ask approval for, as a sentence: how many
// entries on how many sites.
func Summary(reviews []Review) string {
	n, sites := 0, []string{}
	for _, r := range reviews {
		if p := r.Pending(); p > 0 {
			n += p
			sites = append(sites, r.Site)
		}
	}
	entries := "entries"
	if n == 1 {
		entries = "entry"
	}
	return fmt.Sprintf("%d %s to approve on %s", n, entries, strings.Join(sites, ", "))
}
