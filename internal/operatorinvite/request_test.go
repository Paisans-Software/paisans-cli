package operatorinvite_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
)

const testAge = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"

func testRequest(invite []byte) operatorinvite.Request {
	return operatorinvite.Request{
		Version: operatorinvite.Version,
		Invite:  string(invite),
		Name:    "Justin Case",
		Email:   "justin@example.org",
		Age:     testAge,
	}
}

// signedRequest is q encoded and signed by k under the request namespace.
func signedRequest(t *testing.T, q operatorinvite.Request, k testKey) []byte {
	t.Helper()
	body := operatorinvite.EncodeRequest(q)
	return operatorinvite.Signed(body, sign(t, k, operatorinvite.RequestNamespace, body))
}

func TestRequestRoundTrip(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	q := testRequest(signedInvite(t, i, alice))
	if p := q.Check(); len(p) > 0 {
		t.Fatalf("Check: %q", p)
	}
	gotQ, gotI, err := operatorinvite.ParseRequest(signedRequest(t, q, justin))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotQ, q) || !reflect.DeepEqual(gotI, i) {
		t.Fatalf("got %+v and %+v", gotQ, gotI)
	}
	// The invite inside it is still the inviter's, signed.
	if _, key, err := operatorinvite.InviteSigner([]byte(gotQ.Invite)); err != nil || key != alice.pub {
		t.Fatalf("InviteSigner: %q %v", key, err)
	}
}

// The request is signed with Sign, by the key the invite names.
func TestRequestSignsWithSign(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	useKeys(t, justin)
	q := testRequest(signedInvite(t, testInvite(alice, justin), alice))
	body := operatorinvite.EncodeRequest(q)
	sig, err := operatorinvite.Sign(justin.pub, operatorinvite.RequestNamespace, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := operatorinvite.ParseRequest(operatorinvite.Signed(body, sig)); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRefusesSignatureByOtherKey(t *testing.T) {
	alice, justin, mallory := newKey(t), newKey(t), newKey(t)
	q := testRequest(signedInvite(t, testInvite(alice, justin), alice))
	_, _, err := operatorinvite.ParseRequest(signedRequest(t, q, mallory))
	refused(t, err, "signature")
	// Nor by the inviter.
	_, _, err = operatorinvite.ParseRequest(signedRequest(t, q, alice))
	refused(t, err, "signature")
}

func TestRequestRefusesInviteNamespace(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	q := testRequest(signedInvite(t, testInvite(alice, justin), alice))
	body := operatorinvite.EncodeRequest(q)
	_, _, err := operatorinvite.ParseRequest(operatorinvite.Signed(body, sign(t, justin, operatorinvite.InviteNamespace, body)))
	refused(t, err, "signature")
}

// One byte of the embedded invite changed, and the request re-signed by the
// invited key: the request's own signature is good, the invite's is not.
func TestRequestRefusesTamperedInvite(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	invite := signedInvite(t, testInvite(alice, justin), alice)
	tampered := bytes.Replace(invite, []byte(`"paisans.example"`), []byte(`"paisans.exampld"`), 1)
	if bytes.Equal(tampered, invite) {
		t.Fatal("nothing changed")
	}
	q, i, err := operatorinvite.ParseRequest(signedRequest(t, testRequest(tampered), justin))
	if err != nil {
		t.Fatalf("ParseRequest checks the invite for shape only: %v", err)
	}
	if i.Domain != "paisans.exampld" {
		t.Fatalf("invite %+v", i)
	}
	_, _, err = operatorinvite.InviteSigner([]byte(q.Invite))
	refused(t, err, "signature")
	_, err = operatorinvite.ParseInvite([]byte(q.Invite), alice.pub)
	refused(t, err, "signature")
}

func TestRequestRefusesTamperedByte(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	signed := signedRequest(t, testRequest(signedInvite(t, testInvite(alice, justin), alice)), justin)
	tampered := bytes.Replace(signed, []byte(`"Justin Case"`), []byte(`"Justin Cast"`), 1)
	_, _, err := operatorinvite.ParseRequest(tampered)
	refused(t, err, "signature")
}

func TestRequestRefusesTrailingData(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	signed := signedRequest(t, testRequest(signedInvite(t, testInvite(alice, justin), alice)), justin)
	_, _, err := operatorinvite.ParseRequest(append(bytes.Clone(signed), '\n', 'x'))
	refused(t, err, "after the signature")
}

func TestRequestRefusesBadAge(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	for _, age := range []string{"", "AGE1QL3Z", "age1", "age1abc def", "AGE-SECRET-KEY-1ABC", "ssh-ed25519 AAAA"} {
		q := testRequest(signedInvite(t, testInvite(alice, justin), alice))
		q.Age = age
		refused(t, joined(q.Check()), "age: ")
		_, _, err := operatorinvite.ParseRequest(signedRequest(t, q, justin))
		refused(t, err, "age: ")
	}
}

func TestRequestRefusesBadEmail(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	for _, email := range []string{"", "justin", "Justin <justin@example.org>", "justin@example.org, mallory@example.org"} {
		q := testRequest(signedInvite(t, testInvite(alice, justin), alice))
		q.Email = email
		refused(t, joined(q.Check()), "email: ")
		_, _, err := operatorinvite.ParseRequest(signedRequest(t, q, justin))
		refused(t, err, "email: ")
	}
}

func TestRequestCheckNamesEachField(t *testing.T) {
	q := operatorinvite.Request{Version: 2, Name: "Justin", Email: "justin", Age: "x"}
	want := []string{"version: ", "invite: ", "name: ", "email: ", "age: "}
	problems := q.Check()
	if len(problems) != len(want) {
		t.Fatalf("problems %q, want one for each of %q", problems, want)
	}
	for n, p := range problems {
		if !strings.HasPrefix(p, want[n]) {
			t.Errorf("problem %q does not start with %q", p, want[n])
		}
	}
}

func TestRequestRefusesInviteThatIsNotOne(t *testing.T) {
	justin := newKey(t)
	q := testRequest([]byte("version: 1\n"))
	_, _, err := operatorinvite.ParseRequest(signedRequest(t, q, justin))
	refused(t, err, "invite")
}

// The name and email come from the person invited and are shown to the
// operator who accepts them, so nothing in them may move the cursor, clear
// a line or start a new one.
func TestRequestRefusesControlCharacters(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	invite := signedInvite(t, testInvite(alice, justin), alice)
	for _, c := range []struct{ field, name, email string }{
		{"name: ", "\x1b[2K\rAlice Smith", "justin@example.org"},
		{"name: ", "Mallory\nOperator: x y", "justin@example.org"},
		{"name: ", "Justin Case\u0085", "justin@example.org"},
		{"name: ", "Justin " + strings.Repeat("a", 128), "justin@example.org"},
		{"email: ", "Justin Case", "justin@example.org\x1b[2K"},
		{"email: ", "Justin Case", strings.Repeat("a", 250) + "@example.org"},
	} {
		q := testRequest(invite)
		q.Name, q.Email = c.name, c.email
		refused(t, joined(q.Check()), c.field)
		_, _, err := operatorinvite.ParseRequest(signedRequest(t, q, justin))
		refused(t, err, c.field)
	}
	q := testRequest(invite)
	q.Name = "Justin " + strings.Repeat("a", 121)
	if p := q.Check(); len(p) > 0 {
		t.Fatalf("a name of 128 runes is refused: %q", p)
	}
}
