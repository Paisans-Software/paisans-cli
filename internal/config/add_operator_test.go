package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const operatorDoc = `version: 1
operators:
  # The first.
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAA alice@laptop

# The machines.
sites:
  a:
    ssh:
      keys: [alice]
  b:
    ssh:
      user: paisans-1a2b
      keys:
        - alice
    roles: [data]
`

var carol = Operator{Name: "Carol Example", Email: "carol@example.org", Keys: "ssh-ed25519 BBBB carol@laptop\nssh-ed25519 CCCC carol@desk\n"}

// The entry goes after the last operator, before the blank line and the
// comment that introduce sites:, and each site's list gains the name in the
// style it is written in.
func TestAddOperatorBytes(t *testing.T) {
	out, err := AddOperatorBytes([]byte(operatorDoc), "carol", carol, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := `version: 1
operators:
  # The first.
  alice:
    name: Alice Example
    email: alice@example.org
    keys: |
      ssh-ed25519 AAAA alice@laptop
  carol:
    name: Carol Example
    email: carol@example.org
    keys: |
      ssh-ed25519 BBBB carol@laptop
      ssh-ed25519 CCCC carol@desk

# The machines.
sites:
  a:
    ssh:
      keys: [alice, carol]
  b:
    ssh:
      user: paisans-1a2b
      keys:
        - alice
        - carol
    roles: [data]
`
	if string(out) != want {
		t.Errorf("got:\n%s", out)
	}
}

// Only the sites named gain the operator, and a site already naming them is
// left as it is.
func TestAddOperatorSomeSites(t *testing.T) {
	doc := strings.Replace(operatorDoc, "keys: [alice]", "keys: [alice, carol ]", 1)
	out, err := AddOperatorBytes([]byte(doc), "carol", carol, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "keys: [alice, carol ]\n") || strings.Contains(s, "- carol") {
		t.Errorf("got:\n%s", s)
	}
}

// A file without operators: gets the section just before sites:, leaving the
// comment that introduces sites: where it was.
func TestAddOperatorAddsTheSection(t *testing.T) {
	doc := "version: 1\n# The machines.\nsites:\n  a:\n    ssh:\n      keys: [x]\n"
	out, err := AddOperatorBytes([]byte(doc), "carol", carol, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "version: 1\noperators:\n  carol:\n    name: Carol Example\n") || !strings.Contains(string(out), "\n\n# The machines.\nsites:\n") {
		t.Errorf("got:\n%s", out)
	}
}

// A file ending without a newline, and one written with CRLF, get lines
// that end as theirs do.
func TestAddOperatorLineEndings(t *testing.T) {
	doc := "operators:\n  alice:\n    name: A B\n    email: a@example.org\n    keys: |\n      ssh-ed25519 AAAA\nsites:\n  a:\n    ssh:\n      keys:\n        - alice"
	out, err := AddOperatorBytes([]byte(doc), "carol", carol, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(out), "        - alice\n        - carol\n") || !strings.Contains(string(out), "      ssh-ed25519 AAAA\n  carol:\n") {
		t.Errorf("got:\n%s", out)
	}
	crlf := strings.ReplaceAll(operatorDoc, "\n", "\r\n")
	out, err = AddOperatorBytes([]byte(crlf), "carol", carol, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(string(out), "\r\n", ""), "\n") {
		t.Errorf("a bare LF in:\n%q", out)
	}
}

func TestAddOperatorRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		doc   string
		id    string
		sites []string
		want  string
	}{
		"declared":        {operatorDoc, "alice", nil, "operators.alice is already declared"},
		"bad name":        {operatorDoc, "Carol", nil, "is not an operator name"},
		"no site":         {operatorDoc, "carol", []string{"c"}, "sites.c is not declared"},
		"flow operators":  {"operators: {alice: {name: A B}}\nsites: {}\n", "carol", nil, "not a block mapping"},
		"no ssh keys":     {"operators:\n  alice: {}\nsites:\n  a:\n    roles: [data]\n", "carol", []string{"a"}, "sites.a.ssh.keys is not a list"},
		"flow over lines": {"operators:\n  alice: {}\nsites:\n  a:\n    ssh:\n      keys: [\n        alice]\n", "carol", []string{"a"}, "over several lines"},
		"two on a line":   {"operators:\n  alice: {}\nsites:\n  a:\n    ssh:\n      keys:\n        - alice\n        - bob\n", "carol", []string{"a"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := AddOperatorBytes([]byte(tc.doc), tc.id, carol, tc.sites)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Keys holding a control character are refused, since they would be written
// into a literal block.
func TestAddOperatorRefusesControlRunes(t *testing.T) {
	bad := carol
	bad.Keys = "ssh-ed25519 BBBB\x1b[2J"
	if _, err := AddOperatorBytes([]byte(operatorDoc), "carol", bad, nil); err == nil {
		t.Error("accepted a control character")
	}
}

// What is written loads back as the operator given.
func TestAddOperatorParses(t *testing.T) {
	out, err := AddOperatorBytes([]byte(operatorDoc), "carol", carol, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Operators map[string]Operator `yaml:"operators"`
		Sites     map[string]struct {
			SSH struct {
				Keys []string `yaml:"keys"`
			} `yaml:"ssh"`
		} `yaml:"sites"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Operators["carol"]; got != carol {
		t.Errorf("parsed %+v", got)
	}
	for _, s := range []string{"a", "b"} {
		if keys := cfg.Sites[s].SSH.Keys; len(keys) != 2 || keys[1] != "carol" {
			t.Errorf("sites.%s.ssh.keys = %v", s, keys)
		}
	}
}

// A flow list with a trailing comma, or none at all, gains the name without
// a stray comma; operators: with nothing under it gains the entry beneath.
func TestAddOperatorFlowAndEmpty(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"trailing comma": {"operators:\n  alice: {}\nsites:\n  a:\n    ssh:\n      keys: [alice,]\n", "keys: [alice, carol]\n"},
		"empty flow":     {"operators:\n  alice: {}\nsites:\n  a:\n    ssh:\n      keys: [ ]\n", "keys: [carol ]\n"},
		"bare operators": {"operators:\nsites:\n  a:\n    ssh:\n      keys: []\n", "operators:\n  carol:\n    name: Carol Example\n"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := AddOperatorBytes([]byte(tc.doc), "carol", carol, []string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("got:\n%s", out)
			}
			var doc struct {
				Operators map[string]Operator `yaml:"operators"`
			}
			if err := yaml.Unmarshal(out, &doc); err != nil || doc.Operators["carol"] != carol {
				t.Errorf("parsed %+v, %v:\n%s", doc.Operators, err, out)
			}
		})
	}
}

// A name YAML reads as something else is refused, since it is written plain.
func TestAddOperatorRefusesNonStringNames(t *testing.T) {
	for _, id := range []string{"null", "true", "false", "123", "1e3"} {
		if _, err := AddOperatorBytes([]byte(operatorDoc), id, carol, nil); err == nil {
			t.Errorf("%s accepted", id)
		}
	}
}
