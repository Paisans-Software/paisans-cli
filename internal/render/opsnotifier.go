package render

import (
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// opsNotifierValues is what the ops-notifier blueprint's templates need
// beyond any app's: who the operators are.
type opsNotifierValues struct {
	// OperatorEmails is every operator's email, lower case, comma separated,
	// in operator name order. The service deletes the Beszel account of a
	// user signed in through Pocket ID whose email is not among them.
	OperatorEmails string
}

// opsNotifierValuesFor is the ops-notifier values of cfg.
func opsNotifierValuesFor(cfg *config.Config) opsNotifierValues {
	var emails []string
	for _, name := range sortedKeys(cfg.Operators) {
		if e := strings.ToLower(strings.TrimSpace(cfg.Operators[name].Email)); e != "" {
			emails = append(emails, e)
		}
	}
	return opsNotifierValues{OperatorEmails: strings.Join(emails, ",")}
}
