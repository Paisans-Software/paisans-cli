package dns

import "context"

// No test in this package resolves a name through public DNS: a CNAME's
// target has no mail unless a test says so.
func init() {
	lookupMail = func(context.Context, string) bool { return false }
}
