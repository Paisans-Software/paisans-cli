package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// An operator's key is held, where the configuration declares it, to the key
// types a request accepts: a real 2048-bit RSA key and a certificate are each
// refused naming the operator's key line, so validate accepts no key host
// request would refuse.
func TestOperatorKeyTypes(t *testing.T) {
	line := func(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, err := ssh.NewPublicKey(&rsaPriv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPub, err := ssh.NewPublicKey(&ecPriv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(ecPriv)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: ecPub, CertType: ssh.UserCert, ValidPrincipals: []string{"alice"}, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	alice := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA alice@example.org"
	for _, c := range []struct{ name, key, want string }{
		{"rsa 2048", line(rsaPub), "operators.alice.keys: line 1 is an ssh-rsa key of 2048 bits, under 3072"},
		{"an ecdsa certificate", line(cert), `operators.alice.keys: line 1 is "ecdsa-sha2-nistp256-cert-v01@openssh.com", not a key type`},
		{"ecdsa", line(ecPub), ""},
	} {
		got := loadErr(t, strings.Replace(validConfig, alice, c.key+" alice@example.org", 1))
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q\nwant it to say %q", c.name, got, c.want)
		}
	}
}
