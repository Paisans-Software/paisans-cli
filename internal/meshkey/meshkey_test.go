package meshkey_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/apply/meshfake"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/meshkey"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

var dep = deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}

// sudoWarning is what sudo prints, and then runs the command anyway, on a
// host whose own name does not resolve, which is common on a fresh VM. The
// transport merges it into the output ahead of what the helper printed.
const sudoWarning = "sudo: unable to resolve host vm: Name or service not known\n"

// key is a private key of 32 bytes of b, as the secrets file holds one.
func key(b byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(rune(b)), 32)))
}

func public(t *testing.T, private string) string {
	t.Helper()
	pub, err := render.PublicKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// host runs conf on its interface and answers the helper's show as wg show
// would, behind whatever warn says sudo printed first.
type host struct {
	conf, warn string
	refuse     string
}

func (h host) Run(command string) (string, error) {
	if h.refuse != "" {
		return h.refuse, fmt.Errorf("exit status 1")
	}
	_, field, ok := strings.Cut(command, " show ")
	if !ok {
		return "", fmt.Errorf("unexpected command %q", command)
	}
	out, ok := meshfake.ShowRunning(field, h.conf)
	if !ok {
		return "", fmt.Errorf("unexpected field %q", field)
	}
	return h.warn + out, nil
}

func (host) ReadFile(string) (string, bool, error)   { return "", false, nil }
func (host) WriteFile(string, string, uint32) error  { return nil }
func (host) RunInput(string, string) (string, error) { return "", fmt.Errorf("no input") }
func (host) Describe() string                        { return "vm" }

func conf(private string, peers ...string) string {
	var b strings.Builder
	b.WriteString("[Interface]\nPrivateKey = " + private + "\nListenPort = 51820\n")
	for _, p := range peers {
		b.WriteString("\n[Peer]\nPublicKey = " + p + "\nAllowedIPs = 10.44.0.2/32\n")
	}
	return b.String()
}

// sudo's warning is not the key: the read is the helper's line alone.
func TestRunningKeyIgnoresASudoWarning(t *testing.T) {
	private := key(1)
	got, err := meshkey.RunningKey("home-a", host{conf: conf(private), warn: sudoWarning}, dep)
	if err != nil {
		t.Fatal(err)
	}
	if want := public(t, private); got != want {
		t.Errorf("RunningKey = %q, want %q", got, want)
	}
}

// sudo's warning is not a peer.
func TestPeersIgnoresASudoWarning(t *testing.T) {
	peer := public(t, key(2))
	got, err := meshkey.Peers("home-a", host{conf: conf(key(1), peer), warn: sudoWarning}, dep)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != peer {
		t.Errorf("Peers = %q, want only %q", got, peer)
	}
}

// What the helper printed that is not a key is an error that quotes it,
// never a key that matches nothing.
func TestRunningKeyRefusesWhatIsNotAKey(t *testing.T) {
	_, err := meshkey.RunningKey("home-a", host{conf: "", warn: "something else\n"}, dep)
	if err == nil || !strings.Contains(err.Error(), "something else") {
		t.Errorf("RunningKey = %v, want an error quoting the line", err)
	}
	_, err = meshkey.Peers("home-a", host{conf: conf(key(1), "not-a-key")}, dep)
	if err == nil || !strings.Contains(err.Error(), "not-a-key") {
		t.Errorf("Peers = %v, want an error quoting the line", err)
	}
}

// A site whose host already runs its next key is rendered with that key,
// even where sudo warns before the helper prints it: otherwise the old key
// would be handed back to every peer mid rekey.
func TestAsRunningSeesTheNextKeyBehindASudoWarning(t *testing.T) {
	current, next := key(1), key(3)
	secrets := &config.Secrets{Sites: map[string]config.SiteSecrets{
		"home-a": {WireGuardPrivateKey: current, WireGuardNextPrivateKey: next},
	}}
	transports := map[string]apply.Transport{"home-a": host{conf: conf(next), warn: sudoWarning}}
	out, swapped, err := meshkey.AsRunning(secrets, dep, transports)
	if err != nil {
		t.Fatal(err)
	}
	if len(swapped) != 1 || swapped[0] != "home-a" || out.Sites["home-a"].WireGuardPrivateKey != next {
		t.Errorf("swapped = %q, key %q; want home-a rendered with its next key", swapped, out.Sites["home-a"].WireGuardPrivateKey)
	}
}

// sudo refusing the helper is the host's owner's problem, said as such.
func TestASudoRefusalIsTheOwnersProblem(t *testing.T) {
	refused := host{refuse: "sudo: a password is required\n"}
	_, err := meshkey.RunningKey("home-a", refused, dep)
	var problem *ui.Problem
	if !errors.As(err, &problem) || !strings.Contains(problem.Hint, "could not be run on home-a") || !strings.Contains(problem.Explain, "site admit") {
		t.Errorf("RunningKey = %v, want the helper's sudo refusal as the owner's problem", err)
	}
	_, err = meshkey.Peers("home-a", refused, dep)
	if !errors.As(err, &problem) || !strings.Contains(problem.Hint, "could not be run on home-a") {
		t.Errorf("Peers = %v, want the helper's sudo refusal as the owner's problem", err)
	}
}

// recordHost serves files, an admission record among them, and fails
// everything with err when it is set.
type recordHost struct {
	host
	files map[string]string
	err   error
}

func (h recordHost) Run(command string) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	return h.host.Run(command)
}

func (h recordHost) ReadFile(path string) (string, bool, error) {
	if h.err != nil {
		return "", false, h.err
	}
	c, ok := h.files[path]
	return c, ok, nil
}

// A site is joining when its host holds no admission record of this
// deployment, or refuses the deployment's login, and nothing else says it
// ran here; one that holds a record is not, and one that does not answer
// cannot be told.
func TestJoiningReadsAdmissionRecords(t *testing.T) {
	cfg, err := config.Load("../render/testdata/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	record := func(site string) map[string]string {
		req, err := admission.Build(cfg, site)
		if err != nil {
			t.Fatal(err)
		}
		b, err := admission.EncodeRecord(admission.NewRecord(req, req.Grants, "", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)))
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"/etc/paisans/admitted.f2a9.json": string(b)}
	}
	transports := map[string]apply.Transport{
		"home-a": recordHost{files: record("home-a")},
		"home-b": recordHost{files: record("home-b")},
		"vm":     recordHost{},
		"watch":  recordHost{err: fmt.Errorf("ssh: %w", apply.ErrLoginRefused)},
	}
	got, err := meshkey.Joining(cfg, nil, "", transports)
	if err != nil || strings.Join(got, ",") != "vm,watch" {
		t.Fatalf("Joining = %v, %v", got, err)
	}
	transports["watch"] = recordHost{err: errors.New("exit status 1")}
	var none *meshkey.NoAnswer
	if _, err := meshkey.Joining(cfg, nil, "", transports); !errors.As(err, &none) || none.Site != "watch" {
		t.Errorf("a host that failed otherwise: %v", err)
	}
}
