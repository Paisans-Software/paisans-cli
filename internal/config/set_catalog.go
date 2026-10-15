package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetCatalog writes catalogs.<name> into the declaration at path, replacing
// the entry when there is one and adding it otherwise, and leaves every
// other byte as it was: comments above the entry stay, and a file with no
// catalogs: gets the section just before apps:. It is how `catalog add` and
// `catalog update` record a pin. A layout it cannot edit in place, such as a
// flow mapping, is refused rather than reformatted.
func SetCatalog(path, name string, cat Catalog) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setCatalog(data, name, cat)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

// RemoveCatalog takes catalogs.<name> out of the declaration at path, with the
// comment lines directly above it, and catalogs: itself when that was its
// only entry.
func RemoveCatalog(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := removeCatalog(data, name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

// catalogEntry is an entry's lines at an indentation.
func catalogEntry(name string, cat Catalog, indent string) string {
	var b strings.Builder
	scalar := func(v string) string {
		out, _ := yaml.Marshal(v)
		return strings.TrimSpace(string(out))
	}
	fmt.Fprintf(&b, "%s%s:\n", indent, name)
	inner := indent + "  "
	if cat.URL != "" {
		fmt.Fprintf(&b, "%surl: %s\n", inner, scalar(cat.URL))
	}
	if cat.Tag != "" {
		fmt.Fprintf(&b, "%stag: %s\n", inner, scalar(cat.Tag))
	}
	if cat.Commit != "" {
		fmt.Fprintf(&b, "%scommit: %s\n", inner, cat.Commit)
	}
	return b.String()
}

func topMapping(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	return doc.Content[0], nil
}

func setCatalog(data []byte, name string, cat Catalog) ([]byte, error) {
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	lines := lineStarts(data)
	catalogsKey, catalogs := lookup(top, "catalogs")
	if catalogsKey == nil {
		// No section: it goes just before apps:, with the comment lines
		// directly above apps: left introducing apps:.
		at := len(data)
		if appsKey, _ := lookup(top, "apps"); appsKey != nil {
			start := appsKey.Line
			for start > 1 && commentAt(lines.text(data, start-1), appsKey.Column) {
				start--
			}
			at = lines.start(start)
		} else if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
			at = len(data)
		}
		text := "catalogs:\n" + catalogEntry(name, cat, "  ") + "\n"
		return splice(data, edit{at, at, text}), nil
	}
	if catalogs.Kind != yaml.MappingNode || catalogs.Style&yaml.FlowStyle != 0 || len(catalogs.Content) == 0 {
		return nil, fmt.Errorf("catalogs: is not a block mapping this command can edit in place")
	}
	indent := strings.Repeat(" ", catalogs.Content[0].Column-1)
	if key, _ := lookup(catalogs, name); key != nil {
		whole, err := removeEntry(data, lines, top, catalogs, key)
		if err != nil {
			return nil, err
		}
		// Replace from the key itself, so the comments above it stay.
		return splice(data, edit{lines.start(key.Line), whole.end, catalogEntry(name, cat, indent)}), nil
	}
	last := catalogs.Content[len(catalogs.Content)-2]
	whole, err := removeEntry(data, lines, top, catalogs, last)
	if err != nil {
		return nil, err
	}
	return splice(data, edit{whole.end, whole.end, catalogEntry(name, cat, indent)}), nil
}

func removeCatalog(data []byte, name string) ([]byte, error) {
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	lines := lineStarts(data)
	catalogsKey, catalogs := lookup(top, "catalogs")
	if catalogs == nil || catalogs.Kind != yaml.MappingNode || catalogs.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("catalogs.%s is not declared", name)
	}
	key, _ := lookup(catalogs, name)
	if key == nil {
		return nil, fmt.Errorf("catalogs.%s is not declared", name)
	}
	parent, target := catalogs, key
	if len(catalogs.Content) == 2 {
		parent, target = top, catalogsKey
	}
	e, err := removeEntry(data, lines, top, parent, target)
	if err != nil {
		return nil, err
	}
	return splice(data, e), nil
}

func splice(data []byte, e edit) []byte {
	var b bytes.Buffer
	b.Write(data[:e.start])
	b.WriteString(e.text)
	b.Write(data[e.end:])
	return b.Bytes()
}
