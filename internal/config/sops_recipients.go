package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AddSOPSRecipient is the .sops.yaml in dir with recipient appended to the
// age field of every creation rule, comma-separated: what adding an
// operator's age key to the recipients is. Every other rule, key and comment
// is kept. A file whose every rule already names recipient is returned
// unchanged, byte for byte. Nothing is written: the caller writes the result
// once everything else it changes is ready.
//
// A missing file is refused, and so is one with no creation rule, since the
// recipient would then be named nowhere, and one with a rule that uses
// key_groups or names no age recipient, which is left to the operator.
func AddSOPSRecipient(dir, recipient string) ([]byte, error) {
	path := filepath.Join(dir, SOPSConfigName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	rules, err := creationRules(&doc)
	if err != nil {
		return nil, fmt.Errorf("%s %w", path, err)
	}
	changed := false
	for n, rule := range rules {
		// A rule that splits the data key between key groups, or names no
		// age recipient at all, is one this toolkit does not write: an age
		// field added beside it would change what it means.
		if mappingValue(rule, "key_groups") != nil {
			return nil, fmt.Errorf("%s: creation rule %d uses key_groups. Add %s to it by hand", path, n+1, recipient)
		}
		age := mappingValue(rule, "age")
		if age == nil {
			return nil, fmt.Errorf("%s: creation rule %d names no age recipient. Add %s to it by hand", path, n+1, recipient)
		}
		if age.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s: creation rule %d has an age that is not a comma-separated list of recipients", path, n+1)
		}
		if namesRecipient(age.Value, recipient) {
			continue
		}
		if strings.TrimSpace(age.Value) == "" {
			age.Value = recipient
		} else {
			age.Value = strings.TrimRight(age.Value, " ,") + "," + recipient
		}
		changed = true
	}
	if !changed {
		return data, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return out.Bytes(), nil
}

// creationRules is each rule of a .sops.yaml document: the mappings of its
// creation_rules sequence, at least one.
func creationRules(doc *yaml.Node) ([]*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("is not a mapping of creation_rules")
	}
	seq := mappingValue(doc.Content[0], "creation_rules")
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return nil, errors.New("has no creation rule to add the recipient to")
	}
	for _, rule := range seq.Content {
		if rule.Kind != yaml.MappingNode {
			return nil, errors.New("has a creation rule that is not a mapping")
		}
	}
	return seq.Content, nil
}

// mappingValue is the value under key in the mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// namesRecipient is whether list, an age field's comma-separated
// recipients, names recipient.
func namesRecipient(list, recipient string) bool {
	for _, r := range strings.Split(list, ",") {
		if strings.TrimSpace(r) == recipient {
			return true
		}
	}
	return false
}
