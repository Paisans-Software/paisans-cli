package hostadmit

import (
	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
)

var CaddyAdmin = caddyAdmin

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
