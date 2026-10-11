package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SetIncludeDir writes sites.<site>.web_server.include_dir into the
// declaration at path, and leaves every other byte as it was: in a block
// mapping a line of its own after the block's last, indented as its first;
// in a flow mapping, `, include_dir: <dir>` after its last value. One already
// written is replaced where it stands. It is apply's write of the directory
// it derived, and refuses a layout it cannot edit in place rather than
// reformatting the file, as SetMesh does.
func SetIncludeDir(path, site, dir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setIncludeDir(data, site, dir)
	if err != nil {
		return fmt.Errorf("%s: sites.%s.web_server.include_dir: %w", path, site, err)
	}
	return replaceFile(path, out)
}

func setIncludeDir(data []byte, site, dir string) ([]byte, error) {
	if p := IncludeDirProblem(dir); p != "" {
		return nil, fmt.Errorf("%s %s", dir, p)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	in := walk(doc.Content[0], "sites", site, "web_server")
	if in == nil || in.Kind != yaml.MappingNode || len(in.Content) == 0 {
		return nil, fmt.Errorf("sites.%s.web_server is not a mapping this command can add to", site)
	}
	lines := lineStarts(data)
	var e edit
	if _, value := lookup(in, "include_dir"); value != nil {
		var err error
		if e, err = replaceScalar(data, lines, value, dir); err != nil {
			return nil, err
		}
	} else {
		last := in.Content[len(in.Content)-1]
		if last.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("sites.%s.web_server ends in a value written in a form this command cannot add after", site)
		}
		if in.Style&yaml.FlowStyle != 0 {
			// The end of the last value, found as replaceScalar finds it.
			end, err := replaceScalar(data, lines, last, last.Value)
			if err != nil {
				return nil, err
			}
			e = edit{end.end, end.end, ", include_dir: " + dir}
		} else {
			if last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				return nil, fmt.Errorf("sites.%s.web_server ends in a multi-line value this command cannot add after", site)
			}
			at := len(data)
			if last.Line < len(lines) {
				at = lines.next(last.Line)
			}
			text := fmt.Sprintf("%*sinclude_dir: %s\n", in.Content[0].Column-1, "", dir)
			if at == len(data) && len(data) > 0 && data[len(data)-1] != '\n' {
				text = "\n" + text
			}
			e = edit{at, at, text}
		}
	}
	out := append([]byte(nil), data[:e.start]...)
	out = append(out, e.text...)
	return append(out, data[e.end:]...), nil
}
