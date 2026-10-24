package create

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// NewOperator is one operator `operator add` declares in an existing
// deployment: the entry under operators, and the sites whose ssh.keys name
// them.
type NewOperator struct {
	ID    string
	Name  string
	Email string
	Keys  []string
	Sites []string
}

// Declared is what a declaration already holds that a new operator is
// checked against: its operators, whose names and keys are taken, and its
// sites, which are the ones a new operator can be given.
type Declared struct {
	Operators map[string]config.Operator
	Sites     []string
}

// taken is every declared operator's name, and each of their keys'
// fingerprints to the operator listing it.
func (d Declared) taken() ([]string, map[string]string) {
	var ids []string
	keys := map[string]string{}
	for id, op := range d.Operators {
		ids = append(ids, id)
		for _, k := range op.KeyLines() {
			if fp, err := checkKey(k); err == nil {
				keys[fp] = id
			}
		}
	}
	slices.Sort(ids)
	return ids, keys
}

// FlagError is an answer given by a flag that its question would refuse.
type FlagError struct {
	// Flag is the flag that gave it, or "the username".
	Flag string
	Err  error
}

func (e *FlagError) Error() string { return e.Flag + ": " + e.Err.Error() }
func (e *FlagError) Unwrap() error { return e.Err }

// CheckNewOperator is the prompt's checks on an operator given whole, as
// `operator add` is given one by its flags: the first answer that would be
// asked again, as a *FlagError naming the flag that gives it.
func CheckNewOperator(op NewOperator, d Declared) error {
	ids, keys := d.taken()
	if err := notIn(ids, "operator", checkOperatorID)(op.ID); err != nil {
		return &FlagError{"the username", err}
	}
	if err := CheckFullName(op.Name); err != nil {
		return &FlagError{"--name", err}
	}
	if err := CheckEmail(op.Email); err != nil {
		return &FlagError{"--email", err}
	}
	if len(op.Keys) == 0 {
		return &FlagError{"--key", errors.New("give at least one")}
	}
	check := keyCheck(keys)
	for i, k := range op.Keys {
		if err := check(op.Keys[:i], k); err != nil {
			return &FlagError{"--key " + keyLabel(k), err}
		}
	}
	if len(op.Sites) == 0 {
		return &FlagError{"--site", errNoSite(d)}
	}
	for i, s := range op.Sites {
		if err := checkSite(d.Sites, op.Sites[:i], s); err != nil {
			return &FlagError{"--site " + s, err}
		}
	}
	return nil
}

// errNoSite is why an operator needs a site: one who can log in to none can
// run nothing.
func errNoSite(d Declared) error {
	if len(d.Sites) == 0 {
		return errors.New("the deployment declares no site. Declare one, with its ssh section, first")
	}
	return errors.New("give at least one: an operator who can log in to no site can run nothing")
}

// AskNewOperator asks for whatever of op is missing or would be refused,
// and keeps what was given and does. Sites are asked for when none is given:
// every site, or a choice of them.
func AskNewOperator(p *Prompter, op NewOperator, d Declared) (NewOperator, error) {
	ids, keys := d.taken()
	var err error
	p.Say("\nA new operator: someone who runs the machines. They log in to the sites you give them, hold the secrets once you add their age key, and become a Pocket ID user and a community admin on the next apply.")
	if op.ID, err = ask(p, "Username", op.ID, notIn(ids, "operator", checkOperatorID)); err != nil {
		return op, err
	}
	if op.Name, err = ask(p, "Full name (first and last)", op.Name, CheckFullName); err != nil {
		return op, err
	}
	if op.Email, err = ask(p, "Email", op.Email, CheckEmail); err != nil {
		return op, err
	}
	check := keyCheck(keys)
	// A refused key is said and dropped, and the rest are kept as the
	// default, so a key given once need not be pasted again.
	var kept []string
	for _, k := range op.Keys {
		if err := check(kept, k); err != nil {
			p.Say("SSH public key %s: %v", keyLabel(k), err)
			continue
		}
		kept = append(kept, k)
	}
	if len(kept) == 0 || len(kept) < len(op.Keys) {
		if kept, err = p.Lines("SSH public keys", kept, check); err != nil {
			return op, err
		}
	}
	op.Keys = kept
	if len(op.Sites) > 0 {
		for i, s := range op.Sites {
			if err := checkSite(d.Sites, op.Sites[:i], s); err != nil {
				return op, &FlagError{"--site " + s, err}
			}
		}
		return op, nil
	}
	if len(d.Sites) == 0 {
		return op, &FlagError{"--site", errNoSite(d)}
	}
	all, err := p.YesNo(fmt.Sprintf("Can %s log in to every site (%s)?", op.ID, strings.Join(d.Sites, ", ")), true)
	if err != nil {
		return op, err
	}
	if all {
		op.Sites = slices.Clone(d.Sites)
		return op, nil
	}
	for len(op.Sites) == 0 {
		for _, s := range d.Sites {
			yes, err := p.YesNo(fmt.Sprintf("Can %s log in to %s?", op.ID, s), false)
			if err != nil {
				return op, err
			}
			if yes {
				op.Sites = append(op.Sites, s)
			}
		}
		if len(op.Sites) == 0 {
			p.Say("%v.", errNoSite(d))
		}
	}
	return op, nil
}

// ask keeps a given answer the check accepts, and asks for one otherwise,
// with the refused answer as the default so the reason is shown for it.
func ask(p *Prompter, q, given string, check func(string) error) (string, error) {
	if given != "" && check(given) == nil {
		return given, nil
	}
	if given != "" {
		p.Say("%s %q: %v", q, given, check(given))
	}
	return p.Text(q, "", check)
}

func checkSite(sites, got []string, s string) error {
	switch {
	case !slices.Contains(sites, s):
		return fmt.Errorf("not a declared site. The sites are %s", strings.Join(sites, ", "))
	case slices.Contains(got, s):
		return fmt.Errorf("given twice")
	}
	return nil
}

// keyLabel is how a key is named in a refusal: its comment, or its type.
func keyLabel(line string) string {
	f := strings.Fields(line)
	switch {
	case len(f) >= 3:
		return f[2]
	case len(f) >= 1:
		return f[0]
	}
	return "(empty)"
}
