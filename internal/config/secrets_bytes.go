package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecryptSecretsBytes decrypts a secrets file held in memory, as a version in
// the configuration store holds it, and refuses one whose MAC does not verify
// or that holds a pasted secret with a control character (checkPasted).
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
	if err := s.checkPasted(""); err != nil {
		return nil, err
	}
	return &s, nil
}
