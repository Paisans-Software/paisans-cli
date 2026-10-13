package kinds

import (
	"fmt"
	"slices"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// A blueprint's generated secrets say how each is made and what replacing it
// costs (docs/specs/2026-10-13-core-catalog-fetched.md, "Secrets say how they
// are made"), so `init` and `secrets rotate` treat a catalog's secrets as
// they treat the toolkit's own.

// Secret formats a blueprint may name. The set is closed, because each is a
// generator in internal/secretsgen.
const (
	// FormatPassword is a generated password, the default.
	FormatPassword = "password"
	// FormatHex16 and FormatHex32 are 16 and 32 random bytes, hex encoded.
	FormatHex16 = "hex16"
	FormatHex32 = "hex32"
	// FormatRSAPEM is an RSA private key, PEM encoded.
	FormatRSAPEM = "rsa-pem"
	// FormatRSAKeypair is an RSA private key encrypted under another of the
	// app's secrets, its Passphrase; FormatRSAPublic is the public half of
	// the keypair secret it is Of.
	FormatRSAKeypair = "rsa-keypair"
	FormatRSAPublic  = "rsa-public"
)

var secretFormats = map[string]bool{FormatPassword: true, FormatHex16: true, FormatHex32: true, FormatRSAPEM: true, FormatRSAKeypair: true, FormatRSAPublic: true}

// Rotation costs a blueprint may name.
const (
	// CostSignsOut is a secret whose replacement ends members' sign-ins.
	CostSignsOut = "signs-out"
	// CostRestart is one whose replacement restarts the app's stack on each
	// of its sites in turn.
	CostRestart = "restart"
)

// SecretSpec is one generated secret of a blueprint.
type SecretSpec struct {
	Name string
	// Format is one of the formats above, empty when the blueprint names
	// none, which is a password.
	Format string
	// Passphrase is the secret an rsa-keypair key is encrypted under, and Of
	// the rsa-keypair secret an rsa-public one is the half of.
	Passphrase, Of string
	// RotationCost is CostSignsOut, CostRestart or empty for neither.
	RotationCost string
	// NeverRotate is why the secret is never replaced, empty for one that
	// may be: an encryption key whose replacement makes stored data
	// unreadable.
	NeverRotate string
	// Group names the unit the secret rotates with, as apps.<app>.<group>:
	// secrets that must change together, as a keypair and its passphrase.
	Group string
	// Rotate is the in-app action a replacement needs, nil for a secret the
	// app's own stack checks.
	Rotate *InAppRotation
}

// InAppRotation is how a secret the running app keeps in its own data is
// changed there: Change sets it, Check reports whether it is accepted, each
// run in Service with the new value on stdin. Output holding Refused from
// Check means not yet accepted rather than a failure. When, a CEL condition
// over the app, says whether the app has the secret in use at all; Kept is
// why not, when it does not.
type InAppRotation struct {
	Service string
	Change  []string
	Check   []string
	Refused string
	Kept    string
	when    cel.Program
}

// Applies reports whether app keeps the secret in use, so a rotation has
// something to change. A condition that cannot be evaluated applies.
func (r InAppRotation) Applies(app config.App) bool {
	if r.when == nil {
		return true
	}
	v, _, err := r.when.Eval(celApp(app))
	return err != nil || v.Value() != false
}

// secretSpec is a generated secret as a blueprint writes it: its name alone,
// for a password nothing special is said of, or a map.
type secretSpec struct {
	Name         string      `yaml:"name"`
	Format       string      `yaml:"format"`
	Passphrase   string      `yaml:"passphrase"`
	Of           string      `yaml:"of"`
	RotationCost string      `yaml:"rotation_cost"`
	NeverRotate  string      `yaml:"never_rotate"`
	Group        string      `yaml:"group"`
	Rotate       *rotateSpec `yaml:"rotate"`
}

type rotateSpec struct {
	Service string   `yaml:"service"`
	Change  []string `yaml:"change"`
	Check   []string `yaml:"check"`
	Refused string   `yaml:"refused"`
	When    string   `yaml:"when"`
	Kept    string   `yaml:"kept"`
}

func (s *secretSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Name)
	}
	type plain secretSpec
	return n.Decode((*plain)(s))
}

// secretSpecs checks a blueprint's generated secrets against each other and
// its services.
func secretSpecs(specs []secretSpec, services []Service) ([]SecretSpec, error) {
	byName := map[string]secretSpec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	var out []SecretSpec
	for i, s := range specs {
		at := fmt.Sprintf("secrets.generate[%d]", i)
		if !snakeName(s.Name) {
			return nil, fmt.Errorf("%s: %q is not a secret name of lowercase letters, digits and underscores", at, s.Name)
		}
		format := s.Format
		if format == "" {
			format = FormatPassword
		}
		if !secretFormats[format] {
			return nil, fmt.Errorf("%s.format: is %q; it must be password, hex16, hex32, rsa-pem, rsa-keypair or rsa-public", at, s.Format)
		}
		switch {
		case format == FormatRSAKeypair && byName[s.Passphrase].Name == "":
			return nil, fmt.Errorf("%s.passphrase: an rsa-keypair secret names the secret it is encrypted under, one this blueprint generates", at)
		case format == FormatRSAPublic && (byName[s.Of].Name == "" || byName[s.Of].Format != FormatRSAKeypair):
			return nil, fmt.Errorf("%s.of: an rsa-public secret names the rsa-keypair secret it is the half of", at)
		case format != FormatRSAKeypair && s.Passphrase != "", format != FormatRSAPublic && s.Of != "":
			return nil, fmt.Errorf("%s: passphrase goes with rsa-keypair, and of with rsa-public", at)
		}
		// A keypair and what it depends on change together, or the app
		// holds a private key its passphrase does not open.
		if (s.Passphrase != "" && byName[s.Passphrase].Group != s.Group) || (s.Of != "" && byName[s.Of].Group != s.Group) || (s.Group == "" && (s.Passphrase != "" || s.Of != "")) {
			return nil, fmt.Errorf("%s.group: a keypair secret rotates in one group with the secret it depends on", at)
		}
		switch s.RotationCost {
		case "", CostSignsOut, CostRestart:
		default:
			return nil, fmt.Errorf("%s.rotation_cost: is %q; it must be signs-out or restart", at, s.RotationCost)
		}
		if s.NeverRotate != "" && (s.RotationCost != "" || s.Group != "" || s.Rotate != nil) {
			return nil, fmt.Errorf("%s.never_rotate: a secret never rotated has no cost, group or rotation", at)
		}
		if s.Group != "" && (!snakeName(s.Group) || byName[s.Group].Name != "") {
			return nil, fmt.Errorf("%s.group: %q names neither a unit of lowercase words nor one apart from every secret's name", at, s.Group)
		}
		spec := SecretSpec{Name: s.Name, Format: s.Format, Passphrase: s.Passphrase, Of: s.Of, RotationCost: s.RotationCost, NeverRotate: s.NeverRotate, Group: s.Group}
		if r := s.Rotate; r != nil {
			if s.Group != "" || format != FormatPassword {
				return nil, fmt.Errorf("%s.rotate: an in-app rotation is for a password of its own, not one in a group", at)
			}
			if !slices.ContainsFunc(services, func(svc Service) bool { return svc.Name == r.Service }) {
				return nil, fmt.Errorf("%s.rotate.service: %q is not one of the blueprint's services, and a rotation runs only in one of them", at, r.Service)
			}
			if len(r.Change) == 0 || len(r.Check) == 0 {
				return nil, fmt.Errorf("%s.rotate: needs change and check, each a command", at)
			}
			if (r.When == "") != (r.Kept == "") {
				return nil, fmt.Errorf("%s.rotate: when and kept go together: the condition, and why the secret is kept when it does not hold", at)
			}
			rot := &InAppRotation{Service: r.Service, Change: r.Change, Check: r.Check, Refused: r.Refused, Kept: r.Kept}
			if r.When != "" {
				prg, err := compile(r.When)
				if err != nil {
					return nil, fmt.Errorf("%s.rotate.when: %w", at, err)
				}
				rot.when = prg
			}
			spec.Rotate = rot
		}
		out = append(out, spec)
	}
	return out, nil
}

func snakeName(s string) bool {
	if s == "" || len(s) > 63 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// SecretOf is how a kind's blueprint generates one of its secrets, and false
// for a key it does not declare.
func SecretOf(kind config.Kind, key string) (SecretSpec, bool) {
	for _, s := range of(kind).Secrets {
		if s.Name == key {
			return s, true
		}
	}
	return SecretSpec{}, false
}

// SecretGroup is the secrets of a kind that rotate as the unit group, in the
// blueprint's order.
func SecretGroup(kind config.Kind, group string) []SecretSpec {
	var out []SecretSpec
	for _, s := range of(kind).Secrets {
		if s.Group == group {
			out = append(out, s)
		}
	}
	return out
}

// SecretGroups is the units a kind's secrets rotate as, in the order their
// first member appears.
func SecretGroups(kind config.Kind) []string {
	var out []string
	for _, s := range of(kind).Secrets {
		if s.Group != "" && !slices.Contains(out, s.Group) {
			out = append(out, s.Group)
		}
	}
	return out
}
