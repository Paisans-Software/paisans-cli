package operatorinvite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Version is the invite and request format this toolkit writes and reads.
// A file of any other version is refused, so an older CLI never acts on a
// partial reading of a newer one.
const Version = 1

// Invite is what an operator hands someone they invite: which deployment,
// how to reach its sites, and the name and key the new operator will have.
// It holds nothing secret.
type Invite struct {
	Version    int    `yaml:"version"`
	Deployment string `yaml:"deployment"`
	// Domain is community.domain, so the new operator sees what they join.
	Domain string `yaml:"domain"`
	// Root is the full hash of the configuration history's root version, as
	// the inviter's workstation trusts it.
	Root    string  `yaml:"root"`
	Inviter Inviter `yaml:"inviter"`
	// Username is the operator name the new operator will have.
	Username string `yaml:"username"`
	// Key is the new operator's SSH public key, as type and base64 alone.
	Key   string `yaml:"key"`
	Sites []Site `yaml:"sites"`
}

// Inviter is the operator who signed the invite: their operator name and the
// SHA256 fingerprint of the key that signed it.
type Inviter struct {
	Name        string `yaml:"name"`
	Fingerprint string `yaml:"fingerprint"`
}

// Site is one site of the deployment, as deployment recover reaches it.
type Site struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Port int    `yaml:"port"`
}

// inviteHeader opens every invite, for the person who opens the file first.
const inviteHeader = "# An invite to become an operator of a paisans deployment.\n" +
	"# Answer it with the key of the operator who sent it:\n" +
	"#   " + progname.Default + " operator register --invite <this file> --inviter-key <their key>\n"

var (
	commitID         = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	fingerprintShape = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	hostLabel        = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// Check is every problem with i, each led by its field. ParseInvite refuses
// an invite with any.
func (i Invite) Check() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if i.Version != Version {
		add("version: %d, and this paisans reads version %d invites", i.Version, Version)
	}
	if !deployment.ValidID(i.Deployment) {
		add("deployment: %q is not a deployment id", i.Deployment)
	}
	if !config.ValidHostname(i.Domain) {
		add("domain: %q is not a hostname", i.Domain)
	}
	if !commitID.MatchString(i.Root) {
		add("root: %q is not the full hash of a version", i.Root)
	}
	if !config.ValidOperatorName(i.Inviter.Name) {
		add("inviter.name: %q is not an operator name", i.Inviter.Name)
	}
	if !fingerprintShape.MatchString(i.Inviter.Fingerprint) {
		add("inviter.fingerprint: %q is not a SHA256 key fingerprint", i.Inviter.Fingerprint)
	}
	switch {
	case !config.ValidOperatorName(i.Username):
		add("username: %q is not an operator name", i.Username)
	case i.Username == i.Inviter.Name:
		add("username: %s is the inviter's own name", i.Username)
	}
	switch key, err := canonicalKey(i.Key); {
	case err != nil:
		add("key: %v", err)
	case key != i.Key:
		add("key: not written as type and base64 alone, %s", key)
	case fingerprint(key) == i.Inviter.Fingerprint:
		add("key: the inviter's own key")
	}
	if len(i.Sites) == 0 {
		add("sites: none, so the deployment could not be reached")
	}
	names := map[string]bool{}
	for n, s := range i.Sites {
		switch {
		case !config.ValidSiteName(s.Name):
			add("sites[%d].name: %q is not a site name", n, s.Name)
		case names[s.Name]:
			add("sites[%d].name: %s is listed twice", n, s.Name)
		}
		names[s.Name] = true
		if net.ParseIP(s.Host) == nil && !isHostname(s.Host) {
			add("sites[%d].host: %q is neither a hostname nor an IP address", n, s.Host)
		}
		if p := config.DeploymentUserProblem(s.User, i.Deployment); p != "" {
			add("sites[%d].user: %s", n, p)
		}
		if s.Port < 1 || s.Port > 65535 {
			add("sites[%d].port: %d is not a port", n, s.Port)
		}
	}
	return problems
}

// isHostname is the rule ssh.host is held to: dot-separated labels of
// letters, digits and hyphens, a single label allowed.
func isHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// EncodeInvite is i as the invite's YAML: each field in a fixed order and
// layout, every string double quoted, so the same invite is always the same
// bytes, which is what the signature covers.
func EncodeInvite(i Invite) []byte {
	var b bytes.Buffer
	b.WriteString(inviteHeader)
	fmt.Fprintf(&b, "version: %d\n", i.Version)
	fmt.Fprintf(&b, "deployment: %s\n", quote(i.Deployment))
	fmt.Fprintf(&b, "domain: %s\n", quote(i.Domain))
	fmt.Fprintf(&b, "root: %s\n", quote(i.Root))
	b.WriteString("inviter:\n")
	fmt.Fprintf(&b, "  name: %s\n", quote(i.Inviter.Name))
	fmt.Fprintf(&b, "  fingerprint: %s\n", quote(i.Inviter.Fingerprint))
	fmt.Fprintf(&b, "username: %s\n", quote(i.Username))
	fmt.Fprintf(&b, "key: %s\n", quote(i.Key))
	if len(i.Sites) == 0 {
		b.WriteString("sites: []\n")
	} else {
		b.WriteString("sites:\n")
		for _, s := range i.Sites {
			fmt.Fprintf(&b, "  - name: %s\n", quote(s.Name))
			fmt.Fprintf(&b, "    host: %s\n", quote(s.Host))
			fmt.Fprintf(&b, "    port: %d\n", s.Port)
			fmt.Fprintf(&b, "    user: %s\n", quote(s.User))
		}
	}
	return b.Bytes()
}

// inviteFields is every key an invite's mappings hold, by where the mapping is.
var inviteFields = map[string][]string{
	"":        {"version", "deployment", "domain", "root", "inviter", "username", "key", "sites"},
	"inviter": {"name", "fingerprint"},
	"sites":   {"name", "host", "port", "user"},
}

// ParseInvite reads a signed invite as operator register does: its shape,
// its version, that inviterKey's fingerprint is the one the invite names as
// its inviter's, and that the signature is inviterKey's under
// InviteNamespace. inviterKey is a public key line, its comment allowed.
func ParseInvite(signed []byte, inviterKey string) (Invite, error) {
	refuse := func(problems ...string) (Invite, error) {
		return Invite{}, &Error{What: "the invite", Problems: problems}
	}
	i, body, sig, err := readInvite(signed)
	if err != nil {
		return Invite{}, err
	}
	key, err := canonicalKey(inviterKey)
	if err != nil {
		return refuse("the inviter's key is " + err.Error())
	}
	if fp := fingerprint(key); fp != i.Inviter.Fingerprint {
		return refuse(fmt.Sprintf("inviter.fingerprint: %s, and the inviter's key given is %s", i.Inviter.Fingerprint, fp))
	}
	if err := Verify(key, InviteNamespace, body, sig); err != nil {
		return refuse("signature: " + err.Error())
	}
	return i, nil
}

// InviteSigner is the invite, checked for shape, and the public key line
// that signed it, as type and base64 alone: the key the signature names,
// once the signature verifies with it under InviteNamespace and its
// fingerprint is the invite's inviter.fingerprint. Whether that key is an
// operator's is the caller's to check.
func InviteSigner(signed []byte) (Invite, string, error) {
	refuse := func(problems ...string) (Invite, string, error) {
		return Invite{}, "", &Error{What: "the invite", Problems: problems}
	}
	i, body, sig, err := readInvite(signed)
	if err != nil {
		return Invite{}, "", err
	}
	key, err := signer(sig)
	if err != nil {
		return refuse(err.Error())
	}
	if fp := fingerprint(key); fp != i.Inviter.Fingerprint {
		return refuse(fmt.Sprintf("inviter.fingerprint: %s, and the invite is signed by %s", i.Inviter.Fingerprint, fp))
	}
	if err := Verify(key, InviteNamespace, body, sig); err != nil {
		return refuse("signature: " + err.Error())
	}
	return i, key, nil
}

// readInvite is a signed invite split into the invite, the bytes signed and
// the signature, checked for shape alone: the signature is not verified.
func readInvite(signed []byte) (Invite, []byte, []byte, error) {
	refuse := func(problems ...string) (Invite, []byte, []byte, error) {
		return Invite{}, nil, nil, &Error{What: "the invite", Problems: problems}
	}
	if len(signed) > MaxSize {
		return refuse(fmt.Sprintf("it is %d bytes, and an invite is at most %d", len(signed), MaxSize))
	}
	body, sig, err := split(signed)
	if err != nil {
		return refuse(err.Error())
	}
	var i Invite
	if problems := decode(body, "an invite", progname.Default+" operator invite", inviteFields, &i); len(problems) > 0 {
		return refuse(problems...)
	}
	if problems := i.Check(); len(problems) > 0 {
		return refuse(problems...)
	}
	if !bytes.Equal(EncodeInvite(i), body) {
		return refuse("it is not exactly as " + progname.Default + " operator invite wrote it: something changed it after it was written (a comment, spacing, quoting or line endings). Ask for the invite again, as an attachment")
	}
	return i, body, sig, nil
}

// decode reads data, a YAML document of the kind what written by writer,
// into out: refusing a version other than Version, a field missing or one
// fields does not name, and a value of the wrong kind.
func decode(data []byte, what, writer string, fields map[string][]string, out any) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []string{fmt.Sprintf("it is not YAML (%s)", strconv.Quote(err.Error()))}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return []string{fmt.Sprintf("it is not %s: %s is one YAML mapping, as %s writes it", what, what, writer)}
	}
	root := doc.Content[0]
	if v := value(root, "version"); v == nil {
		return []string{"version: missing"}
	} else if n, err := strconv.Atoi(v.Value); err != nil || v.Kind != yaml.ScalarNode || n != Version {
		return []string{fmt.Sprintf("version: %s, and this paisans reads version %d. Install the paisans release the sender runs, or ask them for a version %d file", strconv.Quote(v.Value), Version, Version)}
	}
	if problems := shape(root, "", what, fields); len(problems) > 0 {
		return problems
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return []string{fmt.Sprintf("a field holds the wrong kind of value (%s)", strconv.Quote(err.Error()))}
	}
	return nil
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
func shape(m *yaml.Node, path, what string, fields map[string][]string) []string {
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
			problems = append(problems, at(strconv.Quote(k))+": not a field of "+what)
		}
	}
	for _, k := range fields[path] {
		v := value(m, k)
		_, nested := fields[k]
		switch {
		case v == nil:
			problems = append(problems, at(k)+": missing")
		case path != "" || !nested:
		case v.Kind == yaml.MappingNode:
			problems = append(problems, shape(v, k, what, fields)...)
		case v.Kind == yaml.SequenceNode:
			for _, item := range v.Content {
				if item.Kind == yaml.MappingNode {
					problems = append(problems, shape(item, k, what, fields)...)
				}
			}
		}
	}
	return problems
}

// quote is s as a YAML double-quoted scalar. JSON's string form is one:
// every escape it writes, \u00XX included, means the same in YAML. JSON
// leaves the C1 controls as they are, and YAML reads U+0085 as a line break,
// so those are escaped too, and the value reads back as it was written.
func quote(s string) string {
	out, _ := json.Marshal(s)
	var b strings.Builder
	for _, r := range string(out) {
		if r >= 0x80 && unicode.IsControl(r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
