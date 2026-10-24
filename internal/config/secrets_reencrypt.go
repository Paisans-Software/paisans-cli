package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// handSetMetadata is the sops metadata a secrets file can carry that this
// toolkit never writes, and that re-encrypting here would not carry over:
// which values are encrypted, how the MAC is taken, and keys and key groups
// other than one group of age recipients.
var handSetMetadata = []string{
	"encrypted_regex", "unencrypted_regex", "encrypted_suffix", "unencrypted_suffix",
	"encrypted_comment_regex", "unencrypted_comment_regex", "mac_only_encrypted",
	"shamir_threshold", "key_groups", "pgp", "kms", "gcp_kms", "hckms", "azure_kv", "hc_vault",
}

// HandSetMetadata is the sops metadata fields data, a secrets file's bytes,
// sets that this toolkit never writes, in the order handSetMetadata lists
// them. A field present but empty, false or zero is not set.
func HandSetMetadata(data []byte) []string {
	var probe struct {
		SOPS map[string]any `yaml:"sops"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil
	}
	var out []string
	for _, f := range handSetMetadata {
		switch v := probe.SOPS[f].(type) {
		case nil:
		case string:
			if v != "" {
				out = append(out, f)
			}
		case bool:
			if v {
				out = append(out, f)
			}
		case int:
			if v != 0 {
				out = append(out, f)
			}
		case []any:
			if len(v) > 0 {
				out = append(out, f)
			}
		default:
			out = append(out, f)
		}
	}
	return out
}

// HandSetMetadataProblem is the refusal to re-encrypt the secrets file at
// path, whose sops metadata sets fields, since the file would be written
// without them. Nothing was written.
func HandSetMetadataProblem(path string, fields []string) error {
	name := ui.ShortPath(path)
	return &ui.Problem{
		Hint:    fmt.Sprintf("%s sets sops metadata this toolkit does not carry over: %s", name, strings.Join(fields, ", ")),
		Explain: fmt.Sprintf("Re-encrypting it here would write it without them, since the toolkit never writes them. Add the age key to %s by hand and run sops updatekeys %s, which keeps them. Nothing was written.", SOPSConfigName, name),
	}
}

// ReencryptSecretsBytes is data, the encrypted secrets file read from path,
// encrypted again to recipients with its plaintext exactly as it decrypts,
// as sops updatekeys leaves it: no key is dropped, and neither the version
// nor the id is stamped. It decrypts with this operator's own key, looked
// for as LoadDeploymentSecrets looks, and makes the checks that makes of
// the file first, and refuses one whose sops metadata sets a field
// HandSetMetadata names. Nothing is written.
func ReencryptSecretsBytes(path string, data []byte, id string, recipients []string) ([]byte, error) {
	if !looksEncrypted(data) {
		return nil, PlaintextProblem(path)
	}
	if fields := HandSetMetadata(data); len(fields) > 0 {
		return nil, HandSetMetadataProblem(path, fields)
	}
	if len(recipients) == 0 {
		return nil, noRecipientProblem(path, FileRecipients(data))
	}
	plain, err := decryptSOPS(data, id)
	if err != nil {
		return nil, decryptProblem(path, err)
	}
	s, err := parseSecrets(plain, path, id, "")
	if err != nil {
		return nil, err
	}
	if _, err := deploymentSecrets(s, path, id); err != nil {
		return nil, err
	}
	out, err := encryptSOPS(plain, recipients)
	if err != nil {
		return nil, fmt.Errorf("encrypting %s: %w", path, err)
	}
	return out, nil
}

// WriteSecretsFile writes data, a secrets file's bytes, to path as
// WriteSecrets writes one: through a synced temporary file beside it
// renamed over it, at 0600.
func WriteSecretsFile(path string, data []byte) error {
	if err := writeSecretsFile(path, data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
