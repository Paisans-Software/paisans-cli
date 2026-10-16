package secretsrotate

import (
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// Cost is what a rotation costs the members, as the plan states it before
// anything runs.
type Cost struct {
	// SignsOut is a rotation that ends members' sessions. A run that holds
	// one asks the operator to type "sign out" first (Founder decision).
	SignsOut bool
	// Downtime is "" or what is briefly unavailable, Eg: "storage, for one
	// restart".
	Downtime string
	// Reconnects is a rotation whose users drop their connections and make
	// new ones with the new value, Eg: an app's database pool.
	Reconnects bool
}

// Use is one site's stacks that present a secret: stage 3 hands them the
// next value one Use at a time, and each must be healthy before the next.
type Use struct {
	Site   string
	Stacks []string
	// Do, when set, is how this Use takes the next value in place of the
	// scoped apply, Eg: a Patroni switchover, or every Garage node at once.
	// It is handed the secrets with the next value promoted, and the health
	// gate follows it as it follows an apply. It must be safe to run again.
	Do func(e Env, s *config.Secrets) error
}

// SiteTrier is a rotator whose next value is tried from every site that
// will be handed it (Env.MailFrom) at the end of stage 2, before stage 3
// hands it to any, Eg: an SMTP password, which this machine may not have
// reached the server to try.
type SiteTrier interface {
	TriedFromSites() bool
}

// Rotator is one secret's rotation: where its next value comes from, the side
// that checks it, the stacks that present it, and how the value is promoted.
type Rotator interface {
	// Path is the secret's dotted path in the secrets file, the key of its
	// next value under rotations.
	Path() string
	Cost() Cost
	// Next is a new value for the secret, made from s. Generated rotators
	// call secretsgen.Replacement; one whose next value is not a single
	// generated string returns whatever Promote reads back.
	Next(e Env, s *config.Secrets) (string, error)
	// Checked makes the side that verifies the secret accept next. It must be
	// safe to run again, and Accepts reports whether it already does.
	Checked(e Env, next string) error
	Accepts(e Env, next string) (bool, error)
	// Users lists, in order, the sites and stacks that present the secret.
	Users(e Env) []Use
	// Promote writes next into s as the secret's value. It is run on a copy
	// for stage 3, so the users are rendered with the next value while the
	// file still names the old one, and on the file itself at stage 4. It
	// must be safe to run twice.
	Promote(s *config.Secrets, next string) error
}

// Prechecker is a rotator with a refusal of its own, checked when the run is
// planned, before anything changes. Eg: an app's database password whose app
// runs nowhere.
type Prechecker interface {
	Precheck(e Env) error
}

// Asker is a rotator of a secret issued elsewhere, Eg: the DNS token, whose
// next value a person may have to give. The plan reads Asks; when it is true,
// --execute asks the operator for the next value before any host is claimed
// (Run.Asked, Run.Answer), and stage 1 writes the answer under rotations
// instead of calling Next.
type Asker interface {
	// Asks is whether the next value has to be asked for: the secrets file
	// holds nothing every host using it does not already run.
	Asks(e Env) (bool, error)
	// Issuer is where a person replaces the secret, Eg: the DNS provider.
	Issuer(cfg *config.Config) string
	// CheckText is what the plan says stage 2 does for it.
	CheckText(cfg *config.Config) string
}

// Whole is a rotation that keeps its own staging, Eg: an OIDC client secret
// or an app's S3 key, each of which an existing command already rotates in
// stages it resumes itself. The engine runs it in its place in the order,
// skips stages 1 to 4 for it, and takes its path off rotate_remaining once it
// succeeds.
type Whole interface {
	RunWhole(e Env) error
}

// Retirer is a rotation whose old value stays valid at its issuer until it
// is retired there, Eg: an OIDC client's other secrets at Pocket ID. The
// engine retires it after every user presents the next value and passed its
// gate, and before the next value is promoted, so a run stopped in between
// resumes and retires again. Retire must be safe to run again.
type Retirer interface {
	Retire(e Env) error
}

// Generated is the part of a rotator every generated secret shares, meant
// to be embedded: its path, a next value from secretsgen.Replacement in the
// shape that secret's consumer parses, and promotion by setting the path.
type Generated struct {
	SecretPath string
}

func (g Generated) Path() string { return g.SecretPath }

func (g Generated) Next(e Env, s *config.Secrets) (string, error) {
	return secretsgen.ReplacementIn(e.Cfg, g.SecretPath, s)
}

func (g Generated) Promote(s *config.Secrets, next string) error {
	return s.Set(g.SecretPath, next)
}

// kept is a rotator the run lists as kept, with why, in place of rotating
// it.
type kept struct {
	Rotator
	reason string
}

// Keep is r listed as kept for reason rather than rotated, Eg: a pasted
// secret the operator names as already replaced and applied. It keeps its
// place in an --all run, so a path an earlier run left is taken off the list
// rather than refused as one with no rotation.
func Keep(r Rotator, reason string) Rotator { return kept{Rotator: r, reason: reason} }

func (k kept) Precheck(e Env) error { return &KeptError{Path: k.Path(), Reason: k.reason} }
