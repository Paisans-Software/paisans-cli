package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AddOperator writes operators.<id> into the declaration at path and names
// id in the ssh.keys of each of sites, and leaves every other byte as it was.
// It is how `operator add` declares an operator. The entry goes after the
// last operator, at its indentation; a site already naming id is left as it
// is. A layout it cannot edit in place, such as a flow mapping, is refused
// rather than reformatted, and nothing is written.
func AddOperator(path, id string, op Operator, sites []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := AddOperatorBytes(data, id, op, sites)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(path, out)
}

// AddOperatorBytes is AddOperator on a declaration held in memory, so a
// command can check the result before writing it.
func AddOperatorBytes(data []byte, id string, op Operator, sites []string) ([]byte, error) {
	if !keyName.MatchString(id) {
		return nil, fmt.Errorf("%q is not an operator name this toolkit accepts", id)
	}
	out, err := addOperatorEntry(data, id, op)
	if err != nil {
		return nil, err
	}
	for _, site := range sites {
		if out, err = addSiteKey(out, site, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// operatorEntry is an operator's lines at an indentation, written as create
// writes them, with the keys as a literal block, one per line.
func operatorEntry(id string, op Operator, indent, nl string) string {
	var b strings.Builder
	inner := indent + "  "
	fmt.Fprintf(&b, "%s%s:%s", indent, id, nl)
	fmt.Fprintf(&b, "%sname: %s%s", inner, plainOrQuoted(op.Name), nl)
	fmt.Fprintf(&b, "%semail: %s%s", inner, plainOrQuoted(op.Email), nl)
	fmt.Fprintf(&b, "%skeys: |%s", inner, nl)
	for _, line := range strings.Split(strings.TrimRight(op.Keys, "\n"), "\n") {
		fmt.Fprintf(&b, "%s  %s%s", inner, strings.TrimSpace(line), nl)
	}
	return b.String()
}

func addOperatorEntry(data []byte, id string, op Operator) ([]byte, error) {
	for _, line := range strings.Split(op.Keys, "\n") {
		if _, ok := ControlRune(line); ok {
			return nil, fmt.Errorf("operators.%s.keys holds a control character", id)
		}
	}
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	nl := lineEnding(data)
	lines := lineStarts(data)
	operatorsKey, operators := lookup(top, "operators")
	if operatorsKey == nil {
		// No section: it goes just before sites:, with the comment lines
		// directly above sites: left introducing sites:.
		at := len(data)
		if sitesKey, _ := lookup(top, "sites"); sitesKey != nil {
			start := sitesKey.Line
			for start > 1 && commentAt(lines.text(data, start-1), sitesKey.Column) {
				start--
			}
			at = lines.start(start)
		} else if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, nl...)
			at = len(data)
		}
		text := "operators:" + nl + operatorEntry(id, op, "  ", nl) + nl
		return splice(data, edit{at, at, text}), nil
	}
	if operators.Kind != yaml.MappingNode || operators.Style&yaml.FlowStyle != 0 || len(operators.Content) == 0 {
		return nil, fmt.Errorf("operators: is not a block mapping this command can edit in place")
	}
	if key, _ := lookup(operators, id); key != nil {
		return nil, fmt.Errorf("operators.%s is already declared", id)
	}
	indent := strings.Repeat(" ", operators.Content[0].Column-1)
	last := operators.Content[len(operators.Content)-2]
	whole, err := removeEntry(data, lines, top, operators, last)
	if err != nil {
		return nil, err
	}
	at := whole.end
	// The last line of a file need not end in a newline.
	if at == len(data) && len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, nl...)
		at = len(data)
	}
	return splice(data, edit{at, at, operatorEntry(id, op, indent, nl)}), nil
}

// addSiteKey names id in sites.<site>.ssh.keys: a line after the last item
// of a block list, at its indentation, or a word before the ] of a flow list
// written on one line.
func addSiteKey(data []byte, site, id string) ([]byte, error) {
	top, err := topMapping(data)
	if err != nil {
		return nil, err
	}
	list := walk(top, "sites", site, "ssh", "keys")
	if walk(top, "sites", site) == nil {
		return nil, fmt.Errorf("sites.%s is not declared", site)
	}
	if list == nil || list.Kind != yaml.SequenceNode || len(list.Content) == 0 {
		return nil, fmt.Errorf("sites.%s.ssh.keys is not a list this command can add to. Give the site its ssh section, with its keys, first", site)
	}
	for _, item := range list.Content {
		if item.Kind == yaml.ScalarNode && item.Value == id {
			return data, nil
		}
	}
	lines := lineStarts(data)
	final := list.Content[len(list.Content)-1]
	if list.Style&yaml.FlowStyle == 0 {
		for _, other := range list.Content {
			if other != final && other.Line == final.Line {
				return nil, fmt.Errorf("sites.%s.ssh.keys has more than one item on line %d; write one per line", site, final.Line)
			}
		}
		if final.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("sites.%s.ssh.keys holds an item that is not a name", site)
		}
		// The new item copies the last one's dash and spacing.
		runes := []rune(lines.text(data, final.Line))
		if final.Column-1 > len(runes) {
			return nil, fmt.Errorf("sites.%s.ssh.keys could not be found on line %d", site, final.Line)
		}
		dash := string(runes[:final.Column-1])
		if strings.TrimSpace(dash) != "-" {
			return nil, fmt.Errorf("sites.%s.ssh.keys on line %d is not written as - <name>; write one name per line", site, final.Line)
		}
		at := lines.next(final.Line)
		nl := lineEnding(data)
		if final.Line == len(lines) && (len(data) == 0 || data[len(data)-1] != '\n') {
			data = append(data, nl...)
			at = len(data)
		}
		return splice(data, edit{at, at, dash + id + nl}), nil
	}
	if list.Line != final.Line {
		return nil, fmt.Errorf("sites.%s.ssh.keys is a flow list over several lines; write it on one line", site)
	}
	line := lines.text(data, list.Line)
	open := lines.start(list.Line) + len(string([]rune(line)[:list.Column-1]))
	if data[open] != '[' {
		return nil, fmt.Errorf("sites.%s.ssh.keys on line %d is not written as a [ ] list", site, list.Line)
	}
	closing := bytes.IndexByte(data[open:], ']')
	if closing < 0 || open+closing >= lines.next(list.Line) {
		return nil, fmt.Errorf("sites.%s.ssh.keys on line %d has no closing ] on the same line", site, list.Line)
	}
	at := open + closing
	for at > open && data[at-1] == ' ' {
		at--
	}
	return splice(data, edit{at, at, ", " + id}), nil
}
