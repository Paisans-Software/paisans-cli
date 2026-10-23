package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// PlaintextSecretsVariable is the one way to have a plaintext secrets file
// read or written: set to 1, for a test or a fixture that must not need an
// age key. Without it, a plaintext secrets.enc.yaml is refused wherever a
// deployment reads it, and nothing is ever written in plaintext. It never
// lets an encrypted file be written back in plaintext.
const PlaintextSecretsVariable = "PAISANS_TEST_PLAINTEXT_SECRETS"

// PlaintextSecretsAllowed reports whether PlaintextSecretsVariable is set to 1.
func PlaintextSecretsAllowed() bool { return os.Getenv(PlaintextSecretsVariable) == "1" }

// IsEncrypted reports whether data, a secrets file's bytes, carries sops
// metadata.
func IsEncrypted(data []byte) bool { return looksEncrypted(data) }

// FileRecipients reads the age recipients a sops file is encrypted to from
// its own metadata, in the order the file lists them. A file with key groups
// gives every group's recipients. Nothing is decrypted.
func FileRecipients(data []byte) []string {
	type ageKey struct {
		Recipient string `yaml:"recipient"`
	}
	var probe struct {
		SOPS struct {
			Age       []ageKey `yaml:"age"`
			KeyGroups []struct {
				Age []ageKey `yaml:"age"`
			} `yaml:"key_groups"`
		} `yaml:"sops"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil
	}
	keys := probe.SOPS.Age
	for _, g := range probe.SOPS.KeyGroups {
		keys = append(keys, g.Age...)
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range keys {
		r := strings.TrimSpace(k.Recipient)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// NoRecipientProblem is the refusal to write the encrypted secrets file at
// path while no .sops.yaml beside it names an age recipient: writing would
// leave it in plaintext. It names the recipients the file is encrypted to,
// read from its sops metadata, and the command that restores .sops.yaml with
// them. Nothing was written.
func NoRecipientProblem(path string) error {
	data, _ := os.ReadFile(path)
	return noRecipientProblem(path, FileRecipients(data))
}

func noRecipientProblem(path string, recipients []string) error {
	name := ui.ShortPath(path)
	sopsPath := filepath.Join(filepath.Dir(path), SOPSConfigName)
	_, statErr := os.Stat(sopsPath)
	exists := statErr == nil
	valid, invalid := ageRecipientsOnly(recipients)

	var b strings.Builder
	if len(valid) > 0 {
		fmt.Fprintf(&b, "%s is encrypted to %s", name, strings.Join(valid, ", "))
	} else {
		fmt.Fprintf(&b, "%s is encrypted", name)
	}
	if exists {
		fmt.Fprintf(&b, ", and %s beside it names no age recipient Paisans reads: it reads the age field of each entry under creation_rules.", SOPSConfigName)
	} else {
		fmt.Fprintf(&b, ", and %s, which names who it is encrypted to, is missing beside it.", SOPSConfigName)
	}
	b.WriteString(" Paisans never writes an encrypted secrets file back in plaintext, so nothing was written.")
	if invalid > 0 {
		fmt.Fprintf(&b, " Its sops metadata also names %d recipient(s) that are not age public keys, which are not repeated here.", invalid)
	}
	switch {
	case exists:
		fmt.Fprintf(&b, " Name the recipients in %s as `- age: <public key>,...` under creation_rules, then run the command again.", SOPSConfigName)
	case len(valid) > 0 && invalid == 0 && quotePath(sopsPath) == sopsPath:
		// set -C makes the redirection refuse a file that exists, so the
		// command never replaces a .sops.yaml written in the meantime. It
		// runs under sh, whatever the operator's own shell is.
		fmt.Fprintf(&b, " Restore %s from where it is kept, such as git, or write it from the recipients the file records:\n  sh -c \"set -C; printf 'creation_rules:\\n  - age: %s\\n' > %s\"\nthen run the command again.", SOPSConfigName, strings.Join(valid, ","), sopsPath)
	default:
		fmt.Fprintf(&b, " Restore %s from where it is kept, such as git, or write it naming your age public key as `- age: <public key>` under creation_rules, then run the command again.", SOPSConfigName)
	}
	hint := "no " + SOPSConfigName + " beside " + name
	if exists {
		hint = SOPSConfigName + " beside " + name + " names no age recipient"
	}
	return &ui.Problem{
		Hint:    hint,
		Explain: b.String(),
		Cause:   fmt.Errorf("%s: encrypted, and %s names no age recipient", path, sopsPath),
	}
}

// ageRecipient is an age public key: bech32, under the age1 prefix, which
// covers a plugin's recipient (age1yubikey1...) as well.
var ageRecipient = regexp.MustCompile(`^age1[0-9a-z]+$`)

// ageRecipientsOnly is every recipient that is an age public key, and how
// many were not. Only those are ever printed into a command: the rest come
// from a file's metadata and could break out of its quoting.
func ageRecipientsOnly(recipients []string) (valid []string, invalid int) {
	for _, r := range recipients {
		if ageRecipient.MatchString(r) {
			valid = append(valid, r)
		} else {
			invalid++
		}
	}
	return valid, invalid
}

// PlaintextProblem is the refusal of a secrets file at path that is not
// encrypted, outside the explicit test mode.
func PlaintextProblem(path string) error {
	name := ui.ShortPath(path)
	return &ui.Problem{
		Hint:    name + " is not encrypted",
		Explain: fmt.Sprintf("A deployment keeps its secrets under sops, and %s carries no sops metadata, so it is refused. With your age public key named in %s beside it, encrypt it in place:\n  sops --encrypt --in-place %s\nthen run the command again, and check that no plaintext copy of it was committed anywhere.", name, SOPSConfigName, quotePath(path)),
		Cause:   fmt.Errorf("%s: no sops metadata", path),
	}
}

// noRecipientPlainProblem is the refusal to write a secrets file in
// plaintext outside the explicit test mode.
func noRecipientPlainProblem(path string) error {
	name := ui.ShortPath(path)
	return &ui.Problem{
		Hint:    "no " + SOPSConfigName + " beside " + name + " names an age recipient",
		Explain: fmt.Sprintf("Secrets are written only encrypted, and %s names who they are encrypted to, so nothing was written. Name your age public key in %s, or run %s init in a directory with neither file to have a key made.", SOPSConfigName, quotePath(filepath.Join(filepath.Dir(path), SOPSConfigName)), progname.Cmd()),
		Cause:   fmt.Errorf("%s: no age recipient to encrypt to", path),
	}
}

// CheckSecretsFile is what a command that writes or publishes the secrets
// file at path checks before it changes anything: an encrypted file has an
// age recipient in the .sops.yaml beside it, and a plaintext one is refused
// outside the explicit test mode. A file that does not exist passes; it is
// the caller's to say whether one must.
func CheckSecretsFile(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if !looksEncrypted(data) {
		if PlaintextSecretsAllowed() {
			return nil
		}
		return PlaintextProblem(path)
	}
	recipients, err := Recipients(filepath.Dir(path))
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return noRecipientProblem(path, FileRecipients(data))
	}
	return nil
}

// CheckSecretsBytes refuses data, the secrets file at path as it is about to
// be published, when it is not encrypted, outside the explicit test mode.
func CheckSecretsBytes(path string, data []byte) error {
	if looksEncrypted(data) || PlaintextSecretsAllowed() {
		return nil
	}
	return PlaintextProblem(path)
}

func quotePath(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
