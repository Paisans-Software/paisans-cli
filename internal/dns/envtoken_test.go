package dns

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/configmerge"
)

// The host's reading of caddy.env undoes the quoting render wrote it with,
// so the token sent to the provider is the one issued, whatever it holds.
// It runs the command in /bin/sh here, and in dash, Ubuntu's /bin/sh, where
// this machine has it, against a file written the way render writes one.
func TestTheHostReadsTheTokenRenderQuoted(t *testing.T) {
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}
	for _, sh := range shells {
		t.Run(filepath.Base(sh), func(t *testing.T) { readsBack(t, sh) })
	}
}

func readsBack(t *testing.T, sh string) {
	for _, token := range []string{
		"Ab0_-Cd1Ef2Gh3Ij4Kl5Mn6Op7Qr8St9Uv0Wx1Yz2",
		"cfat_Ab0-Cd1.Ef2_Gh3",
		`a$b`,
		"a #b",
		`say "hi"`,
		`back\slash\\twice\`,
		`\n is not a newline`,
		"  edge spaces  ",
		`"quoted"`,
		"tab\there",
		"line\nbreak",
		`$$ \$ \" \\ "$"`,
	} {
		line, err := configmerge.EnvValue("acme_dns_token", token)
		if err != nil {
			t.Fatalf("%q: %v", token, err)
		}
		f := filepath.Join(t.TempDir(), "caddy.env")
		if err := os.WriteFile(f, []byte("# a comment\nACME_DNS_TOKEN="+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sh, "-c", "f="+quote(f)+"; "+envTokenRead+`printf '%s|' "$t"`).Output()
		if err != nil {
			t.Fatalf("%q: %v", token, err)
		}
		if got := string(out); got != token+"|" {
			t.Errorf("rendered as %s, read back as %q, want %q", line, got, token+"|")
		}
	}
}

// The token reaches the provider as issued: remoteGet, run here in sh and in
// dash against a local listener, sends the Authorization header curl builds
// from its configuration on stdin, and a backslash or a double quote in the
// token survives curl's own quoting.
func TestTheHostSendsTheTokenItRead(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl on this machine")
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}
	for _, sh := range shells {
		for _, token := range []string{
			"cfat_Ab0-Cd1.Ef2_Gh3",
			`back\slash`,
			`say "hi"`,
			`both \" and \\ "\"`,
			`a$b #c`,
			"  edge spaces  ",
		} {
			line, err := configmerge.EnvValue("acme_dns_token", token)
			if err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(t.TempDir(), "caddy.env")
			if err := os.WriteFile(f, []byte("ACME_DNS_TOKEN="+line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got = ""
			out, err := exec.Command(sh, "-c", remoteGet(f, srv.URL+"/user/tokens/verify")).CombinedOutput()
			if err != nil {
				t.Fatalf("%s %q: %v\n%s", sh, token, err, out)
			}
			// A header value's trailing spaces are dropped by any server.
			if want := "Bearer " + strings.TrimRight(token, " "); got != want {
				t.Errorf("%s: %q arrived as %q, want %q", filepath.Base(sh), token, got, want)
			}
		}
	}
}
