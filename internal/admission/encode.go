package admission

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// header opens every request, for the owner who opens the file first.
const header = "# A request to admit one site of a paisans deployment to this host.\n" +
	"# Compare its digest with the operator who sent it before you admit it:\n" +
	"#   sudo paisans host admit --request <this file>\n"

// Encode is r as the request's file: one form for each request, written by
// hand rather than by a YAML library, so the bytes, and so the digest,
// depend on the request alone. Every string is double quoted, so no value
// reads as another type. Encode is for a request Check accepts.
func Encode(r Request) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	fmt.Fprintf(&b, "version: %d\n", r.Version)
	fmt.Fprintf(&b, "deployment: %s\n", quote(r.Deployment))
	fmt.Fprintf(&b, "user: %s\n", quote(r.User))
	fmt.Fprintf(&b, "ssh_port: %d\n", r.SSHPort)
	fmt.Fprintf(&b, "listen_port: %d\n", r.ListenPort)
	fmt.Fprintf(&b, "mesh_subnet: %s\n", quote(r.MeshSubnet))
	fmt.Fprintf(&b, "address: %s\n", quote(r.Address))
	fmt.Fprintf(&b, "endpoint: %t\n", r.Endpoint)
	b.WriteString("grants:\n")
	fmt.Fprintf(&b, "  watchdog: %t\n", r.Grants.Watchdog)
	fmt.Fprintf(&b, "  public_ports: %t\n", r.Grants.PublicPorts)
	if len(r.WebServer) == 0 {
		b.WriteString("web_server: []\n")
	} else {
		b.WriteString("web_server:\n")
		for _, w := range r.WebServer {
			fmt.Fprintf(&b, "  - app: %s\n", quote(w.App))
			fmt.Fprintf(&b, "    hostname: %s\n", quote(w.Hostname))
			fmt.Fprintf(&b, "    listen: %s\n", quote(w.Listen))
		}
	}
	if len(r.Operators) == 0 {
		b.WriteString("operators: []\n")
	} else {
		b.WriteString("operators:\n")
		for _, o := range r.Operators {
			fmt.Fprintf(&b, "  - name: %s\n", quote(o.Name))
			if len(o.Keys) == 0 {
				b.WriteString("    keys: []\n")
				continue
			}
			b.WriteString("    keys:\n")
			for _, k := range o.Keys {
				fmt.Fprintf(&b, "      - %s\n", quote(k))
			}
		}
	}
	return b.Bytes()
}

// quote is s as a YAML double-quoted scalar. JSON's string form is one:
// every escape it writes, \u00XX included, means the same in YAML.
func quote(s string) string {
	out, _ := json.Marshal(s)
	return string(out)
}

// fields is every key a request's mappings hold, by where the mapping is.
var fields = map[string][]string{
	"":           {"version", "deployment", "user", "ssh_port", "listen_port", "mesh_subnet", "address", "endpoint", "grants", "web_server", "operators"},
	"grants":     {"watchdog", "public_ports"},
	"web_server": {"app", "hostname", "listen"},
	"operators":  {"name", "keys"},
}

// Parse reads a request as host admit does, refusing anything it would
// have to guess at: a version other than RequestVersion, a field missing
// or unknown, any problem Check finds, and a file that is not exactly the
// bytes Encode writes for what it holds. That last rule means nothing in
// the file goes unread (no comment, anchor, tag, duplicate key or second
// document), and that the digest of the file is the digest of the request
// the owner is shown.
func Parse(data []byte) (Request, error) {
	refuse := func(problems ...string) (Request, error) {
		return Request{}, &Error{What: "the request", Problems: problems}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return refuse(fmt.Sprintf("it is not YAML (%v)", err))
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return refuse("it is not a request: a request is one YAML mapping, as paisans host request writes it")
	}
	root := doc.Content[0]
	if v := value(root, "version"); v == nil {
		return refuse("version: missing")
	} else if n, err := strconv.Atoi(v.Value); err != nil || v.Kind != yaml.ScalarNode || n != RequestVersion {
		return refuse(fmt.Sprintf("version: %s, and this paisans reads version %d requests. Install the paisans release the operator runs, or ask them for a version %d request", v.Value, RequestVersion, RequestVersion))
	}
	if problems := shape(root, ""); len(problems) > 0 {
		return refuse(problems...)
	}
	var r Request
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return refuse(fmt.Sprintf("a field holds the wrong kind of value (%v)", err))
	}
	if problems := r.Check(); len(problems) > 0 {
		return refuse(problems...)
	}
	if !bytes.Equal(Encode(r), data) {
		return refuse("it is not exactly as paisans host request wrote it: something changed it after it was written (a comment, spacing, quoting or line endings). Ask the operator to send the file again, as an attachment, and compare the digest again")
	}
	return r, nil
}

// value is the value of key in mapping m, nil when m has no such key.
func value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// shape is each field missing from or unknown to the mapping m found at
// path, and to the mappings inside it.
func shape(m *yaml.Node, path string) []string {
	var problems []string
	known := map[string]bool{}
	for _, k := range fields[path] {
		known[k] = true
	}
	at := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i].Value; !known[k] {
			problems = append(problems, at(k)+": not a field of a request")
		}
	}
	for _, k := range fields[path] {
		v := value(m, k)
		switch {
		case v == nil:
			problems = append(problems, at(k)+": missing")
		case path == "" && k == "grants" && v.Kind == yaml.MappingNode:
			problems = append(problems, shape(v, "grants")...)
		case path == "" && (k == "web_server" || k == "operators") && v.Kind == yaml.SequenceNode:
			for _, item := range v.Content {
				if item.Kind == yaml.MappingNode {
					problems = append(problems, shape(item, k)...)
				}
			}
		}
	}
	return problems
}

// The digest is 80 bits of the SHA-256, written in Crockford base32 as
// sixteen characters in four groups of four.
//
// 80 bits because the digest has to survive being read aloud or typed on a
// call, and cannot be longer than that, yet a forger who can change a
// request on its way gains by searching for another that matches: at 80
// bits that search is about 2^80 hashes for a chosen target, far beyond
// anyone's reach, where twelve hex digits (48 bits) would not be.
//
// Crockford's alphabet because a person reads it: it has no i, l, o or u,
// so nothing is confused with 1 or 0 and no word is spelled by accident,
// and ParseDigest accepts the common misreadings instead of refusing them.
const (
	DigestBits  = 80
	DigestGroup = 4

	crockford = "0123456789abcdefghjkmnpqrstvwxyz"
	// digestChars is sixteen symbols of five bits each.
	digestChars = DigestBits / 5
)

// Digest is the owner's check that a request is the one its operator sent:
// the first 80 bits of the SHA-256 of the file in Crockford base32, four
// groups of four, Eg: 0a1b-2c3d-4e5f-6g7h. host request prints it, host
// admit prints it again, and the two are compared over a channel both trust.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	var out strings.Builder
	var acc, bits uint
	n := 0
	for _, by := range sum[:DigestBits/8] {
		acc = acc<<8 | uint(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			if n > 0 && n%DigestGroup == 0 {
				out.WriteByte('-')
			}
			out.WriteByte(crockford[(acc>>bits)&31])
			n++
		}
	}
	return out.String()
}

// ParseDigest reads a digest as an owner types it and returns it as Digest
// writes it, so phase 5 compares ParseDigest(typed) == Digest(file). It
// drops hyphens and spaces, lower-cases, reads i and l as 1 and o as 0, and
// refuses u, any other character outside the alphabet, and any length but
// sixteen.
func ParseDigest(typed string) (string, error) {
	var out strings.Builder
	for _, r := range strings.ToLower(typed) {
		switch r {
		case '-', ' ', '\t', '\r', '\n':
			continue
		case 'i', 'l':
			r = '1'
		case 'o':
			r = '0'
		}
		if r >= 0x80 || !strings.ContainsRune(crockford, r) {
			return "", fmt.Errorf("%q is not a digest: %q is not one of the characters a digest is written in, 0-9 and a-z without i, l, o and u", typed, string(r))
		}
		out.WriteRune(r)
	}
	chars := out.String()
	if len(chars) != digestChars {
		return "", fmt.Errorf("%q is not a digest: a digest is sixteen characters in four groups of four, and this has %d", typed, len(chars))
	}
	groups := make([]string, 0, digestChars/DigestGroup)
	for i := 0; i < digestChars; i += DigestGroup {
		groups = append(groups, chars[i:i+DigestGroup])
	}
	return strings.Join(groups, "-"), nil
}

// Fields is every field a request holds, by its path: each top level key,
// and each key of grants, of a web_server entry and of an operator, as
// <parent>.<key>.
func Fields() []string {
	out := slices.Clone(fields[""])
	for _, parent := range []string{"grants", "web_server", "operators"} {
		for _, k := range fields[parent] {
			out = append(out, parent+"."+k)
		}
	}
	return out
}
