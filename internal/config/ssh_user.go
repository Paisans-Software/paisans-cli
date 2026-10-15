package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// SetSSHUser writes sites.<site>.ssh.user into the declaration at path, and
// leaves every other byte as it was. It records the user a command has
// connected as, so that the name in the file is the name used from then on;
// a name already there is that record and is never overwritten, and the
// same name already there writes nothing.
//
// The line goes ahead of the ssh section's first key, indented as that key
// is, as SetArch places arch: the first key is the one place in a block
// mapping where a new line is valid whatever the keys after it hold. In a
// flow mapping it goes in front of the first key, followed by `, `. A user
// written empty (an empty double quoted string, an empty single quoted
// string, or ~) names nobody, so the name is written where that value
// stands, in its quoting.
func SetSSHUser(path, site, user string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := setSSHUser(data, site, user)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if bytes.Equal(out, data) {
		return nil
	}
	return replaceFile(path, out)
}

func setSSHUser(data []byte, site, user string) ([]byte, error) {
	if !unixUser.MatchString(user) {
		return nil, fmt.Errorf("%q is not a user name this toolkit accepts", user)
	}
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	_, sites := lookup(top, "sites")
	_, s := lookup(sites, site)
	if s == nil || s.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("sites.%s is not declared as a section this command can add a user to", site)
	}
	_, ssh := lookup(s, "ssh")
	if ssh == nil || ssh.Kind != yaml.MappingNode || len(ssh.Content) == 0 {
		return nil, fmt.Errorf("sites.%s.ssh is not a section this command can add a user to. Give the site its ssh section, with its keys, first", site)
	}
	lines := lineStarts(data)
	_, value := lookup(ssh, "user")
	switch {
	case value == nil:
	case value.Kind != yaml.ScalarNode:
		return nil, fmt.Errorf("sites.%s.ssh.user is not a single value", site)
	case value.Tag == "!!str" && value.Value == user:
		return data, nil
	case value.Tag == "!!null" && value.Value == "":
		return nil, fmt.Errorf("sites.%s.ssh.user is written with no value, in a form this command cannot edit in place. Write the name, Eg: user: %s, or remove the line", site, user)
	case value.Tag == "!!null" || value.Value == "":
		text := user
		if value.Style == 0 {
			text = plainOrQuoted(user)
		}
		e, err := replaceScalar(data, lines, value, text)
		if err != nil {
			return nil, fmt.Errorf("sites.%s.ssh.user: %w", site, err)
		}
		return splice(data, e), nil
	default:
		return nil, fmt.Errorf("sites.%s.ssh.user already names %s, which is never overwritten", site, value.Value)
	}
	first := ssh.Content[0]
	if ssh.Style&yaml.FlowStyle != 0 {
		runes := []rune(lines.text(data, first.Line))
		if first.Column-1 > len(runes) {
			return nil, fmt.Errorf("sites.%s.ssh could not be found on line %d", site, first.Line)
		}
		at := lines.start(first.Line) + len(string(runes[:first.Column-1]))
		return splice(data, edit{at, at, "user: " + plainOrQuoted(user) + ", "}), nil
	}
	at := lines.start(first.Line)
	return splice(data, edit{at, at, fmt.Sprintf("%*suser: %s\n", first.Column-1, "", plainOrQuoted(user))}), nil
}

// plainOrQuoted is value as YAML is to read it back: plain when the plain
// text is read as the same string, double quoted when it would be read as
// something else, Eg: null or true.
func plainOrQuoted(value string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(value), &doc); err == nil && len(doc.Content) == 1 {
		if n := doc.Content[0]; n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Value == value {
			return value
		}
	}
	return strconv.Quote(value)
}
