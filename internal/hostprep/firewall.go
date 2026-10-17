package hostprep

import (
	"fmt"
)

// Rule is one inbound allowance. Either Port and Proto are set, or Interface
// is, never both: "everything arriving on psns-f2a9" is a rule about where traffic
// comes from, not which port it is for.
type Rule struct {
	Port      int
	Proto     string
	Interface string
	// To narrows an interface rule to one destination address and Port, for
	// traffic that should reach one service and nothing else.
	To string
	// Why is printed beside the rule in the plan.
	Why string
	// SSH marks the allow for the site's ssh.port. It is the one rule never
	// removed, and a deny on it is refused rather than warned about; see
	// planRules.
	SSH bool
}

// sshWhy is the SSH rule's reason, and so part of its ownership comment on the
// host. It must not change: it is how planRules recognises an SSH allow site
// admit added for an earlier ssh.port, and keeps it.
const sshWhy = "ssh, the bootstrap route"

func (r Rule) String() string {
	if r.Interface != "" && r.To != "" {
		return fmt.Sprintf("%d/%s on %s to %s", r.Port, r.Proto, r.Interface, r.To)
	}
	if r.Interface != "" {
		return "all inbound on " + r.Interface
	}
	return fmt.Sprintf("%d/%s", r.Port, r.Proto)
}
