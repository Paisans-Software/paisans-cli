package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// SetArch writes sites.<site>.arch for each site in archs into the
// declaration at path, and leaves every other byte as it was. The line goes
// ahead of the site's first key, indented as that key is, because the first
// key is the one place in a block mapping where a new line is valid whatever
// the keys after it hold. In a flow mapping it goes in front of the first key,
// followed by `, `. It is apply's record of what each host reported, and it
// refuses a site that already records one: a recorded value is compared with
// the host, never overwritten.
func SetArch(path string, archs map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setArch(data, archs)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

func setArch(data []byte, archs map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	_, sites := lookup(doc.Content[0], "sites")
	lines := lineStarts(data)
	names := make([]string, 0, len(archs))
	for name := range archs {
		names = append(names, name)
	}
	sort.Strings(names)
	var edits []edit
	for _, name := range names {
		_, site := lookup(sites, name)
		if site == nil || site.Kind != yaml.MappingNode || len(site.Content) == 0 {
			return nil, fmt.Errorf("sites.%s is not a mapping this command can add arch to", name)
		}
		if _, recorded := lookup(site, "arch"); recorded != nil {
			return nil, fmt.Errorf("sites.%s already records arch %s", name, recorded.Value)
		}
		first := site.Content[0]
		if site.Style&yaml.FlowStyle != 0 {
			runes := []rune(lines.text(data, first.Line))
			if first.Column-1 > len(runes) {
				return nil, fmt.Errorf("sites.%s could not be found on line %d", name, first.Line)
			}
			at := lines.start(first.Line) + len(string(runes[:first.Column-1]))
			edits = append(edits, edit{at, at, "arch: " + archs[name] + ", "})
			continue
		}
		at := lines.start(first.Line)
		edits = append(edits, edit{at, at, fmt.Sprintf("%*sarch: %s\n", first.Column-1, "", archs[name])})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), data...)
	for _, e := range edits {
		var b bytes.Buffer
		b.Write(out[:e.start])
		b.WriteString(e.text)
		b.Write(out[e.end:])
		out = b.Bytes()
	}
	return out, nil
}
