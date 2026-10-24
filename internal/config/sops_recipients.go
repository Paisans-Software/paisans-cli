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
// age field of every creation rule, comma-separated, and given an age field
// in a rule that has none: what adding an operator's age key to the
// recipients is. Every other rule, key and comment is kept. A file whose
// every rule already names recipient is returned unchanged, byte for byte.
// Nothing is written: the caller writes the result once everything else it
// changes is ready.
//
// A missing file is refused, and so is one with no creation rule, since the
// recipient would then be named nowhere.
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
	for _, rule := range rules {
		age := mappingValue(rule, "age")
		if age == nil {
			rule.Content = append(rule.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "age"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: recipient})
			changed = true
			continue
		}
		if age.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s has a creation rule whose age is not a comma-separated list of recipients", path)
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
