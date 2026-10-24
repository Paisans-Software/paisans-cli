package operatorinvite

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paisans-software/paisans-cli/internal/progname"
)

// Request is the invited operator's answer to an invite: the invite itself,
// whole and as signed, and what the invite cannot know, their full name,
// email and age public key. It holds nothing secret, and it is signed with
// the key the invite names.
type Request struct {
	Version int `yaml:"version"`
	// Invite is the signed invite, whole, as text.
	Invite string `yaml:"invite"`
	Name   string `yaml:"name"`
	Email  string `yaml:"email"`
	// Age is the new operator's age public key, age1...
	Age string `yaml:"age"`
}

// requestHeader opens every request, for the operator who opens it first.
const requestHeader = "# A request to become an operator of a paisans deployment, answering an invite.\n" +
	"# Accept it beside the deployment's files with:\n" +
	"#   " + progname.Default + " operator add --request <this file>\n"

// The longest name and email a request may hold: a name in characters, and
// an address in bytes, the most a mail path carries.
const (
	MaxNameRunes   = 128
	MaxEmailLength = 254
)

// ageRecipient is an age public key: bech32, under the age1 prefix.
var ageRecipient = regexp.MustCompile(`^age1[0-9a-z]+$`)

// requestFields is every key a request holds.
var requestFields = map[string][]string{
	"": {"version", "invite", "name", "email", "age"},
}

// Check is every problem with q, each led by its field. The invite is
// checked by ParseRequest, which reads it; Check only requires one.
func (q Request) Check() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if q.Version != Version {
		add("version: %d, and this paisans reads version %d requests", q.Version, Version)
	}
	if q.Invite == "" {
		add("invite: missing")
	}
	// The name and email come from the person invited, are shown to the
	// operator accepting them and are written into paisans-stack.yaml, so a
	// control character, which could rewrite what that operator sees, is
	// refused, and so is a value longer than a person's name or an address.
	first, last, _ := strings.Cut(q.Name, " ")
	switch {
	case strings.IndexFunc(q.Name, unicode.IsControl) >= 0:
		add("name: %q holds a control character", q.Name)
	case utf8.RuneCountInString(q.Name) > MaxNameRunes:
		add("name: %d characters, and a name is at most %d", utf8.RuneCountInString(q.Name), MaxNameRunes)
	case first == "" || strings.TrimSpace(last) == "":
		add("name: %q is not a first and a last name, separated by a space", q.Name)
	}
	switch addr, err := mail.ParseAddress(q.Email); {
	case strings.IndexFunc(q.Email, unicode.IsControl) >= 0:
		add("email: %q holds a control character", q.Email)
	case len(q.Email) > MaxEmailLength:
		add("email: %d characters, and an email address is at most %d", len(q.Email), MaxEmailLength)
	case err != nil || addr.Address != q.Email:
		add("email: %q is not an email address alone, Eg: alice@example.org", q.Email)
	}
	if !ageRecipient.MatchString(q.Age) {
		add("age: %q is not an age public key, age1...", q.Age)
	}
	return problems
}

// EncodeRequest is q as the request's YAML: each field in a fixed order,
// every string double quoted, so the same request is always the same bytes.
// The invite is one quoted string, its newlines escaped.
func EncodeRequest(q Request) []byte {
	var b bytes.Buffer
	b.WriteString(requestHeader)
	fmt.Fprintf(&b, "version: %d\n", q.Version)
	fmt.Fprintf(&b, "invite: %s\n", quote(q.Invite))
	fmt.Fprintf(&b, "name: %s\n", quote(q.Name))
	fmt.Fprintf(&b, "email: %s\n", quote(q.Email))
	fmt.Fprintf(&b, "age: %s\n", quote(q.Age))
	return b.Bytes()
}

// ParseRequest reads a signed request as operator add --request does: its
// shape, its version, the invite inside it for shape, and that the request
// is signed by the invite's key under RequestNamespace. The invite's own
// signature is not checked here: InviteSigner on q.Invite is the check that
// an operator signed it.
func ParseRequest(signed []byte) (Request, Invite, error) {
	refuse := func(problems ...string) (Request, Invite, error) {
		return Request{}, Invite{}, &Error{What: "the request", Problems: problems}
	}
	if len(signed) > MaxSize {
		return refuse(fmt.Sprintf("it is %d bytes, and a request is at most %d", len(signed), MaxSize))
	}
	body, sig, err := split(signed)
	if err != nil {
		return refuse(err.Error())
	}
	var q Request
	if problems := decode(body, "a request", progname.Default+" operator register --invite", requestFields, &q); len(problems) > 0 {
		return refuse(problems...)
	}
	if problems := q.Check(); len(problems) > 0 {
		return refuse(problems...)
	}
	if !bytes.Equal(EncodeRequest(q), body) {
		return refuse("it is not exactly as " + progname.Default + " operator register wrote it: something changed it after it was written (a comment, spacing, quoting or line endings). Ask for the request again, as an attachment")
	}
	i, _, _, err := readInvite([]byte(q.Invite))
	if err != nil {
		problems := []string{err.Error()}
		var e *Error
		if errors.As(err, &e) {
			problems = e.Problems
		}
		for n, p := range problems {
			problems[n] = "invite: " + p
		}
		return refuse(problems...)
	}
	if err := Verify(i.Key, RequestNamespace, body, sig); err != nil {
		return refuse("signature: " + err.Error() + ", the key the invite names")
	}
	return q, i, nil
}
