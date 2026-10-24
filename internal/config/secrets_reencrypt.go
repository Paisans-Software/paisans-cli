package config

import "fmt"

// ReencryptSecretsBytes is data, the encrypted secrets file read from path,
// encrypted again to recipients with its plaintext exactly as it decrypts,
// as sops updatekeys leaves it: no key is dropped, and neither the version
// nor the id is stamped. It decrypts with this operator's own key, looked
// for as LoadDeploymentSecrets looks, and makes the checks that makes of
// the file first. Nothing is written.
func ReencryptSecretsBytes(path string, data []byte, id string, recipients []string) ([]byte, error) {
	if !looksEncrypted(data) {
		return nil, PlaintextProblem(path)
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
