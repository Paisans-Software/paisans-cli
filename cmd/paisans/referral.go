package main

import (
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/diagnosis"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// referUnreachable adds doctor's referral to an error that a site ssh could
// not reach caused, after what the error already says: doctor's reach check
// for that site says what the deployment is without while it is gone. Any
// other error, and one that already refers to doctor, is returned as it is,
// so a referral is said once however many commands an error passes through.
func referUnreachable(err error) error {
	site := apply.UnreachableSite(err)
	if site == "" {
		return err
	}
	hint, explain := ui.Describe(err)
	if diagnosis.Mentions(explain) {
		return err
	}
	if explain != "" {
		explain += "\n"
	}
	return &ui.Problem{Hint: hint, Explain: explain + diagnosis.Unreachable(site).Sentence, Cause: err}
}

// referProvider adds doctor's referral to an error that is the DNS
// provider refusing the token from this workstation, worded as the token
// check words it. gateway move calls the provider without that check, so
// its refusal is otherwise a bare HTTP 403. Any other error, and one that
// already refers to doctor, is returned as it is.
func referProvider(err error) error {
	r, ok := diagnosis.Provider(err, "")
	if !ok {
		return err
	}
	hint, explain := ui.Describe(err)
	if diagnosis.Mentions(explain) {
		return err
	}
	if explain != "" {
		explain += "\n"
	}
	return &ui.Problem{Hint: hint, Explain: explain + r.Sentence, Cause: err}
}
