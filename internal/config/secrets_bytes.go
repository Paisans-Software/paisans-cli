package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecryptSecretsBytes decrypts a secrets file held in memory, as a version in
// the configuration store holds it, and refuses one whose MAC does not verify.
// A pasted secret holding a control character is not refused here: a stored
// version cannot be repaired, so a pull merges past it, and the local file it
// writes is checked by the next load (checkPasted).
func DecryptSecretsBytes(data []byte, id string) (*Secrets, error) {
	plain, err := decryptSOPS(data, id)
	if err != nil {
		return nil, err
	}
	var s Secrets
	dec := yaml.NewDecoder(bytes.NewReader(plain))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("secrets in a stored version: %w", err)
	}
	s.Encrypted = true
	return &s, nil
}
