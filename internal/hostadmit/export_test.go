package hostadmit

import (
	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
)

var CaddyAdmin = caddyAdmin

// EgressRuleCount is how many rules an egress table file holds.
var EgressRuleCount = ruleCount

// RecordOf is the admission record the parts have filled so far.
func RecordOf(t apply.Transport, in Input) (admission.Record, error) {
	p, err := build(t, in)
	if err != nil {
		return admission.Record{}, err
	}
	return p.record, nil
}

// KeysCommand is the keys step's command for user, whose home is home,
// with its UID read on the host when it runs.
var KeysCommand = keysCommand

// FollowLinkAt names path as a system file the local transport reads
// through a link, for the length of the test.
func FollowLinkAt(t interface{ Cleanup(func()) }, path string) {
	before := linkedSystemFiles
	linkedSystemFiles = append(append([]string(nil), before...), path)
	t.Cleanup(func() { linkedSystemFiles = before })
}

// LinkedSystemFiles is the list of system files read through a link.
func LinkedSystemFiles() []string { return append([]string(nil), linkedSystemFiles...) }
