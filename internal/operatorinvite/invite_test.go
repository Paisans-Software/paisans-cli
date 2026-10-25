package operatorinvite_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
)

const testID = "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"

func testInvite(inviter, invited testKey) operatorinvite.Invite {
	return operatorinvite.Invite{
		Version:    operatorinvite.Version,
		Deployment: testID,
		Domain:     "paisans.example",
		Root:       strings.Repeat("0a", 20),
		Inviter:    operatorinvite.Inviter{Name: "alice", Fingerprint: inviter.fingerprint},
		Username:   "justin",
		Key:        invited.pub,
		Sites: []operatorinvite.Site{
			{Name: "home-a", Host: "home-a.paisans.example", User: "paisans-f2a9", Port: 22},
			{Name: "vm", Host: "203.0.113.10", User: "ubuntu", Port: 2222},
		},
	}
}

// signedInvite is i encoded and signed by k under the invite namespace.
func signedInvite(t *testing.T, i operatorinvite.Invite, k testKey) []byte {
	t.Helper()
	body := operatorinvite.EncodeInvite(i)
	return operatorinvite.Signed(body, sign(t, k, operatorinvite.InviteNamespace, body))
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("not refused, want a refusal naming %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal %q does not name %q", err, want)
	}
}

func TestInviteRoundTrip(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	if p := i.Check(); len(p) > 0 {
		t.Fatalf("Check: %q", p)
	}
	if !bytes.Equal(operatorinvite.EncodeInvite(i), operatorinvite.EncodeInvite(i)) {
		t.Fatal("EncodeInvite is not stable")
	}
	got, err := operatorinvite.ParseInvite(signedInvite(t, i, alice), alice.pub+" alice@laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, i) {
		t.Fatalf("got %+v\nwant %+v", got, i)
	}
}

func TestInviteRefusesWrongInviterKey(t *testing.T) {
	alice, justin, mallory := newKey(t), newKey(t), newKey(t)
	i := testInvite(alice, justin)
	// Signed by mallory, checked against alice's key: the fingerprints match,
	// the signature does not.
	_, err := operatorinvite.ParseInvite(signedInvite(t, i, mallory), alice.pub)
	refused(t, err, "signature")
}

// A signature valid for the key given is still refused when that key is not
// the one the invite names as its inviter's.
func TestInviteRefusesInviterKeyOtherThanNamed(t *testing.T) {
	alice, justin, mallory := newKey(t), newKey(t), newKey(t)
	i := testInvite(alice, justin)
	_, err := operatorinvite.ParseInvite(signedInvite(t, i, mallory), mallory.pub)
	refused(t, err, "inviter.fingerprint")
}

func TestInviteRefusesTamperedByte(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	signed := signedInvite(t, i, alice)
	body := len(operatorinvite.EncodeInvite(i))
	for _, at := range []int{0, body / 2, body - 2} {
		tampered := bytes.Clone(signed)
		tampered[at] ^= 0x01
		if _, err := operatorinvite.ParseInvite(tampered, alice.pub); err == nil {
			t.Errorf("byte %d changed, and the invite was accepted", at)
		}
	}
	// A change that still parses as a valid invite, so only the signature
	// can refuse it.
	tampered := bytes.Replace(signed, []byte(`"justin"`), []byte(`"justix"`), 1)
	_, err := operatorinvite.ParseInvite(tampered, alice.pub)
	refused(t, err, "signature")
}

func TestInviteRefusesRequestNamespace(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	body := operatorinvite.EncodeInvite(testInvite(alice, justin))
	signed := operatorinvite.Signed(body, sign(t, alice, operatorinvite.RequestNamespace, body))
	_, err := operatorinvite.ParseInvite(signed, alice.pub)
	refused(t, err, "signature")
}

func TestInviteRefusesTrailingData(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	signed := signedInvite(t, testInvite(alice, justin), alice)
	_, err := operatorinvite.ParseInvite(append(bytes.Clone(signed), "username: mallory\n"...), alice.pub)
	refused(t, err, "after the signature")
}

func TestInviteRefusesNoSignature(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	_, err := operatorinvite.ParseInvite(operatorinvite.EncodeInvite(testInvite(alice, justin)), alice.pub)
	refused(t, err, "signature")
}

func TestInviteRefusesVersion2(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	i.Version = 2
	_, err := operatorinvite.ParseInvite(signedInvite(t, i, alice), alice.pub)
	refused(t, err, "version")
}

func TestInviteRefusesUnknownField(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	body := append(operatorinvite.EncodeInvite(testInvite(alice, justin)), "extra: \"x\"\n"...)
	signed := operatorinvite.Signed(body, sign(t, alice, operatorinvite.InviteNamespace, body))
	_, err := operatorinvite.ParseInvite(signed, alice.pub)
	refused(t, err, `"extra"`)
}

func TestInviteRefusesKeyWithComment(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	i.Key = justin.pub + " justin@laptop"
	refused(t, joined(i.Check()), "key:")
	_, err := operatorinvite.ParseInvite(signedInvite(t, i, alice), alice.pub)
	refused(t, err, "key:")
}

func TestInviteRefusesKeyWithOptions(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	i.Key = `command="sh" ` + justin.pub
	refused(t, joined(i.Check()), "key: carries options")
	_, err := operatorinvite.ParseInvite(signedInvite(t, i, alice), alice.pub)
	refused(t, err, "key:")
}

func TestInviteCheckNamesEachField(t *testing.T) {
	alice, justin := newKey(t), newKey(t)
	i := testInvite(alice, justin)
	i.Deployment = "not-an-id"
	i.Root = "abc123"
	i.Username = "Justin Smith"
	i.Sites = nil
	want := []string{"deployment: ", "root: ", "username: ", "sites: "}
	problems := i.Check()
	if len(problems) != len(want) {
		t.Fatalf("problems %q, want one for each of %q", problems, want)
	}
	for n, p := range problems {
		if !strings.HasPrefix(p, want[n]) {
			t.Errorf("problem %q does not start with %q", p, want[n])
		}
	}

	i = testInvite(alice, justin)
	i.Sites[1].Port = 0
	problems = i.Check()
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "sites[1].port: ") {
		t.Fatalf("problems %q, want one naming sites[1].port", problems)
	}

	i = testInvite(alice, justin)
	i.Domain, i.Inviter.Name, i.Inviter.Fingerprint = "", "", "SHA256:short"
	i.Sites[0].Name, i.Sites[0].Host, i.Sites[0].User = "Home A", "-oProxyCommand=x", "root"
	want = []string{"domain: ", "inviter.name: ", "inviter.fingerprint: ", "sites[0].name: ", "sites[0].host: ", "sites[0].user: "}
	problems = i.Check()
	if len(problems) != len(want) {
		t.Fatalf("problems %q, want one for each of %q", problems, want)
	}
	for n, p := range problems {
		if !strings.HasPrefix(p, want[n]) {
			t.Errorf("problem %q does not start with %q", p, want[n])
		}
	}
}

func TestInviteSigner(t *testing.T) {
	alice, justin, mallory := newKey(t), newKey(t), newKey(t)
	i := testInvite(alice, justin)
	got, key, err := operatorinvite.InviteSigner(signedInvite(t, i, alice))
	if err != nil {
		t.Fatal(err)
	}
	if key != alice.pub || !reflect.DeepEqual(got, i) {
		t.Fatalf("signer %q, invite %+v", key, got)
	}
	// Mallory's signature is genuine, but mallory is not the inviter it names.
	_, _, err = operatorinvite.InviteSigner(signedInvite(t, i, mallory))
	refused(t, err, "inviter.fingerprint")
}

func joined(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "\n"))
}
