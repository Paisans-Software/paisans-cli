package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// RemoveConfigKey takes apps.<app>.config.<key> out of the declaration at
// path and leaves every other byte as it was: the key's line goes, with the
// comment lines directly above it at its indentation, and config: itself goes
// too when that was its only key. It is how `doctor --execute` drops a
// passthrough key that only repeats a value the toolkit renders. The key must
// be written on one line as a scalar; anything else is refused rather than
// reformatted.
func RemoveConfigKey(path, app, key string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := removeConfigKey(data, app, key)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

func removeConfigKey(data []byte, app, key string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	name := fmt.Sprintf("apps.%s.config.%s", app, key)
	appNode := walk(doc.Content[0], "apps", app)
	if appNode == nil || appNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("apps.%s is not declared", app)
	}
	configKey, configNode := lookup(appNode, "config")
	if configNode == nil || configNode.Kind != yaml.MappingNode || configNode.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("apps.%s.config is not a block mapping this command can edit in place", app)
	}
	k, v := lookup(configNode, key)
	if k == nil {
		return nil, fmt.Errorf("%s is not declared", name)
	}
	if v.Kind != yaml.ScalarNode || v.Line != k.Line {
		return nil, fmt.Errorf("%s is not a one line value; remove it by hand", name)
	}
	lines := lineStarts(data)
	// The only key: config: goes with it, so the file does not keep an
	// empty mapping that says nothing.
	target, column := k.Line, k.Column
	if len(configNode.Content) == 2 {
		target, column = configKey.Line, configKey.Column
	}
	start := target
	for start > 1 && commentAt(lines.text(data, start-1), column) {
		start--
	}
	end := len(data)
	if k.Line < len(lines) {
		end = lines.start(k.Line + 1)
	}
	var b bytes.Buffer
	b.Write(data[:lines.start(start)])
	b.Write(data[end:])
	return b.Bytes(), nil
}
