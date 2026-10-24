package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/operatorinvite"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

const createCarolKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgIC carol@example.org"

// createdDeployment is a paisans-stack.yaml create wrote, with alice and bob
// as its operators and one machine.
func createdDeployment(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	plainReporter(t)
	in := answersFile(t, createLines(createCommunity, createAdmin,
		[]string{"1", "", "203.0.113.10", "", ""}, createNoApps, createMail)...)
	var out bytes.Buffer
	if err := runCreate([]string{"--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return path
}

// notATerminal is stdin that is a file isTerminal says is not a terminal.
func notATerminal(t *testing.T) *os.File {
	t.Helper()
	f := answersFile(t)
	isTerminal = func(*os.File) bool { return false }
	return f
}

func loadOperators(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if validate.Check(cfg).Refused() {
		t.Error("the file is refused")
	}
	return cfg
}

// onlyAdded checks that taking carol's entry and her name in each flow list
// out of after gives before back: the addition changed nothing else.
func onlyAdded(t *testing.T, before, after []byte) {
	t.Helper()
	entry := "  carol:\n    name: Carol Example\n    email: carol@example.org\n    keys: |\n      " + createCarolKey + "\n"
	if !bytes.Contains(after, []byte(entry)) {
		t.Fatalf("no entry for carol:\n%s", after)
	}
	got := strings.Replace(string(after), entry, "", 1)
	got = strings.ReplaceAll(got, ", carol]", "]")
	if got != string(before) {
		t.Errorf("more than carol changed:\n%s", after)
	}
}

func TestOperatorAddFromFlags(t *testing.T) {
	path := createdDeployment(t)
	before, _ := os.ReadFile(path)
	var out bytes.Buffer
	err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org",
		"--key", createCarolKey, "--all-sites", "--config", path}, notATerminal(t), &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	after, _ := os.ReadFile(path)
	onlyAdded(t, before, after)
	cfg := loadOperators(t, path)
	carol := cfg.Operators["carol"]
	if carol.Name != "Carol Example" || carol.Email != "carol@example.org" || carol.Keys != createCarolKey+"\n" {
		t.Errorf("operators.carol = %+v", carol)
	}
	for name, site := range cfg.Sites {
		if !siteNames(site, "carol") {
			t.Errorf("sites.%s.ssh.keys does not name carol:\n%s", name, after)
		}
	}
	for _, want := range []string{"sops updatekeys", "deployment apply --execute", "operator register --execute"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("next steps lack %q:\n%s", want, out.String())
		}
	}
}

func siteNames(site config.Site, id string) bool {
	for _, k := range site.SSH.Keys {
		if k.Name == id {
			return true
		}
	}
	return false
}

// A key may be a .pub file, and --site gives the sites one by one.
func TestOperatorAddKeyFileAndSite(t *testing.T) {
	path := createdDeployment(t)
	cfg := loadOperators(t, path)
	var site string
	for name := range cfg.Sites {
		site = name
	}
	pub := filepath.Join(t.TempDir(), "carol.pub")
	if err := os.WriteFile(pub, []byte("# carol's laptop\n"+createCarolKey+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org",
		"--key", pub, "--site", site, "--config", path}, notATerminal(t), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	cfg = loadOperators(t, path)
	if cfg.Operators["carol"].Keys != createCarolKey+"\n" || !siteNames(cfg.Sites[site], "carol") {
		t.Errorf("got %+v", cfg.Operators["carol"])
	}
}

// --site takes several sites separated by commas.
func TestOperatorAddCommaSites(t *testing.T) {
	path := createdDeployment(t)
	var site string
	for name := range loadOperators(t, path).Sites {
		site = name
	}
	err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org",
		"--key", createCarolKey, "--site", site + "," + site, "--config", path}, notATerminal(t), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "given twice") {
		t.Errorf("err = %v, want the site given twice refused", err)
	}
}

// Without a terminal nothing is asked, so a missing answer is refused, by the
// flags that give it, and nothing is written.
func TestOperatorAddRefusesMissingFlags(t *testing.T) {
	path := createdDeployment(t)
	before, _ := os.ReadFile(path)
	err := runOperatorAdd([]string{"carol", "--email", "carol@example.org", "--config", path}, notATerminal(t), &bytes.Buffer{})
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"--name", "--key", "--site or --all-sites"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %s", want, err.Error())
		}
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("the file changed")
	}
}

func TestOperatorAddRefusesBadFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"taken name":   {[]string{"alice", "--name", "Carol Example", "--email", "carol@example.org", "--key", createCarolKey, "--all-sites"}, "already called alice"},
		"bad email":    {[]string{"carol", "--name", "Carol Example", "--email", "Carol <carol@example.org>", "--key", createCarolKey, "--all-sites"}, "--email"},
		"one name":     {[]string{"carol", "--name", "Carol", "--email", "carol@example.org", "--key", createCarolKey, "--all-sites"}, "--name"},
		"alice's key":  {[]string{"carol", "--name", "Carol Example", "--email", "carol@example.org", "--key", createKey, "--all-sites"}, "this is alice's key"},
		"no such site": {[]string{"carol", "--name", "Carol Example", "--email", "carol@example.org", "--key", createCarolKey, "--site", "nowhere"}, "not a declared site"},
		"both sites":   {[]string{"carol", "--name", "Carol Example", "--email", "carol@example.org", "--key", createCarolKey, "--site", "x", "--all-sites"}, "--all-sites and --site"},
		"not a key":    {[]string{"carol", "--name", "Carol Example", "--email", "carol@example.org", "--key", "/no/such.pub", "--all-sites"}, "neither a public key nor a file"},
	} {
		t.Run(name, func(t *testing.T) {
			path := createdDeployment(t)
			before, _ := os.ReadFile(path)
			err := runOperatorAdd(append(tc.args, "--config", path), notATerminal(t), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Error("the file changed")
			}
		})
	}
}

// At a terminal every answer the flags did not give is asked for, and Enter
// gives the operator every site.
func TestOperatorAddAsks(t *testing.T) {
	path := createdDeployment(t)
	in := answersFile(t, "carol", "Carol Example", createCarolKey, "", "")
	var out bytes.Buffer
	if err := runOperatorAdd([]string{"--email", "carol@example.org", "--config", path}, in, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "Email") {
		t.Errorf("asked for the email --email gave:\n%s", out.String())
	}
	cfg := loadOperators(t, path)
	if cfg.Operators["carol"].Email != "carol@example.org" {
		t.Errorf("operators.carol = %+v", cfg.Operators["carol"])
	}
	for name, site := range cfg.Sites {
		if !siteNames(site, "carol") {
			t.Errorf("sites.%s.ssh.keys does not name carol", name)
		}
	}
}

// A flag the prompt would refuse is asked for again, with the reason.
func TestOperatorAddAsksAgainForARefusedFlag(t *testing.T) {
	path := createdDeployment(t)
	in := answersFile(t, "carol@example.org", "")
	var out bytes.Buffer
	err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "not an address",
		"--key", createCarolKey, "--all-sites", "--config", path}, in, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "an email address alone") {
		t.Errorf("no reason given:\n%s", out.String())
	}
	if loadOperators(t, path).Operators["carol"].Email != "carol@example.org" {
		t.Error("the email asked for was not written")
	}
}

// Input that ends before every question is answered writes nothing.
func TestOperatorAddWritesNothingWhenInputEnds(t *testing.T) {
	path := createdDeployment(t)
	before, _ := os.ReadFile(path)
	in := answersFile(t, "carol")
	if err := runOperatorAdd([]string{"--config", path}, in, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("the file changed")
	}
}

// A refused --key at a terminal is said and dropped; the keys that pass are
// kept, and Enter keeps them.
func TestOperatorAddKeepsTheKeysThatPass(t *testing.T) {
	path := createdDeployment(t)
	in := answersFile(t, "", "")
	var out bytes.Buffer
	err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org",
		"--key", createCarolKey, "--key", createKey, "--config", path}, in, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "this is alice's key") {
		t.Errorf("alice's key not refused:\n%s", out.String())
	}
	if got := loadOperators(t, path).Operators["carol"].Keys; got != createCarolKey+"\n" {
		t.Errorf("keys = %q", got)
	}
}

// An operator must be given a site: answering no to every one asks again.
func TestOperatorAddAsksAgainForNoSite(t *testing.T) {
	path := createdDeployment(t)
	in := answersFile(t, "n", "n", "n", "y")
	var out bytes.Buffer
	err := runOperatorAdd([]string{"carol", "--name", "Carol Example", "--email", "carol@example.org",
		"--key", createCarolKey, "--config", path}, in, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "can run nothing") {
		t.Errorf("no reason given:\n%s", out.String())
	}
	for name, site := range loadOperators(t, path).Sites {
		if !siteNames(site, "carol") {
			t.Errorf("sites.%s.ssh.keys does not name carol", name)
		}
	}
}

// requestDeployment is an invite workstation whose secrets are encrypted to
// alice's age key, which the keystore holds for the deployment, and that
// .sops.yaml names: a deployment alice can accept a request on.
type requestDeployment struct {
	l          configstore.Local
	cfg        *config.Config
	root       string
	invite     operatorinvite.Invite // the invite alice wrote for carol
	alicePriv  string
	carolPriv  string
	carolKey   string
	alice      *age.X25519Identity
	carol      *age.X25519Identity
	ks         *keystore.Memory
	configPath string
	report     *bytes.Buffer // what the reporter printed
}

func newRequestDeployment(t *testing.T) *requestDeployment {
	t.Helper()
	withoutAgeKey(t)
	shas, answer, files, alicePriv, alicePub := storedHistoryKey(t, 1, 0)
	l := registerWorkstation(t, answer, files[0], nil, alicePub)
	report := plainReporter(t)
	if err := l.SetSigningKey(alicePub); err != nil {
		t.Fatal(err)
	}
	if err := l.SetRoot(shas[0]); err != nil {
		t.Fatal(err)
	}
	signFromFile(t, alicePriv)
	d := &requestDeployment{l: l, root: shas[0], alicePriv: alicePriv, configPath: filepath.Join(l.Dir, config.FileName), report: report}
	var err error
	if d.cfg, err = config.Load(d.configPath); err != nil {
		t.Fatal(err)
	}
	d.alice, _ = age.GenerateX25519Identity()
	d.carol, _ = age.GenerateX25519Identity()
	d.ks = keystore.NewMemory()
	withKeystore(t, d.ks)
	if err := d.ks.Put(d.cfg.ID, d.alice.String(), d.alice.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	sops := "creation_rules:\n  - age: " + d.alice.Recipient().String() + "\n"
	if err := os.WriteFile(filepath.Join(l.Dir, config.SOPSConfigName), []byte(sops), 0o644); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.LoadSecrets(fixtureSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteSecrets(d.secretsPath(), secrets, []string{d.alice.Recipient().String()}); err != nil {
		t.Fatal(err)
	}
	// From here on a plaintext secrets file is refused, as it is outside tests.
	realSecrets(t)
	d.carolPriv, d.carolKey = testKey(t)
	var out bytes.Buffer
	if err := runOperatorInvite(inviteArgs(l, "carol", "--key", d.carolKey), &out); err != nil {
		t.Fatal(err)
	}
	if d.invite, err = operatorinvite.ParseInvite(out.Bytes(), alicePub); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *requestDeployment) secretsPath() string { return filepath.Join(d.l.Dir, "secrets.enc.yaml") }

// signWith is the armored signature of data by the private key file priv.
func signWith(t *testing.T, priv, namespace string, data []byte) []byte {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", namespace, "-f", priv)
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v: %s", err, stderr.String())
	}
	return out.Bytes()
}

// request is carol's signed request answering inv, signed by inviterPriv,
// written to a file whose path it returns.
func (d *requestDeployment) request(t *testing.T, inv operatorinvite.Invite, inviterPriv string) string {
	t.Helper()
	body := operatorinvite.EncodeInvite(inv)
	signedInvite := operatorinvite.Signed(body, signWith(t, inviterPriv, operatorinvite.InviteNamespace, body))
	q := operatorinvite.EncodeRequest(operatorinvite.Request{
		Version: operatorinvite.Version,
		Invite:  string(signedInvite),
		Name:    "Carol Example",
		Email:   "carol@example.org",
		Age:     d.carol.Recipient().String(),
	})
	path := filepath.Join(t.TempDir(), "carol.request")
	if err := os.WriteFile(path, operatorinvite.Signed(q, signWith(t, d.carolPriv, operatorinvite.RequestNamespace, q)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// add runs operator add --request, and returns what it printed, the
// reporter's lines and then stdout's.
func (d *requestDeployment) add(t *testing.T, request string, rest ...string) (string, error) {
	t.Helper()
	d.report.Reset()
	var out bytes.Buffer
	err := runOperatorAdd(append([]string{"--request", request, "--config", d.configPath}, rest...), notATerminal(t), &out)
	return d.report.String() + out.String(), err
}

// dirNames is the names in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// files is the three files' bytes.
func (d *requestDeployment) files(t *testing.T) [3][]byte {
	t.Helper()
	var out [3][]byte
	for i, name := range []string{config.FileName, config.SOPSConfigName, "secrets.enc.yaml"} {
		b, err := os.ReadFile(filepath.Join(d.l.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

// opensWith checks the secrets open with identity alone.
func (d *requestDeployment) opensWith(t *testing.T, identity *age.X25519Identity) error {
	t.Helper()
	ks := keystore.NewMemory()
	if err := ks.Put(d.cfg.ID, identity.String(), identity.Recipient().String()); err != nil {
		t.Fatal(err)
	}
	saved := config.AgeKeystore
	config.AgeKeystore = ks
	defer func() { config.AgeKeystore = saved }()
	_, err := config.LoadDeploymentSecrets(d.secretsPath(), d.cfg.ID)
	return err
}

func TestOperatorAddRequestDeclaresAndReencrypts(t *testing.T) {
	d := newRequestDeployment(t)
	names := dirNames(t, d.l.Dir)
	out, err := d.add(t, d.request(t, d.invite, d.alicePriv), "--all-sites")
	if err != nil {
		t.Fatalf("%v\n%s", problemText(err), out)
	}
	cfg := loadOperators(t, d.configPath)
	carol := cfg.Operators["carol"]
	if carol.Name != "Carol Example" || carol.Email != "carol@example.org" || carol.Keys != bare(d.carolKey)+"\n" {
		t.Errorf("operators.carol = %+v", carol)
	}
	for name, site := range cfg.Sites {
		if !siteNames(site, "carol") {
			t.Errorf("sites.%s.ssh.keys does not name carol", name)
		}
	}
	recipients, err := config.Recipients(d.l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recipients, []string{d.alice.Recipient().String(), d.carol.Recipient().String()}) {
		t.Errorf("recipients %v", recipients)
	}
	for who, id := range map[string]*age.X25519Identity{"alice": d.alice, "carol": d.carol} {
		if err := d.opensWith(t, id); err != nil {
			t.Errorf("%s cannot open the secrets: %v", who, err)
		}
	}
	k, _, _ := config.ParseKeyLine(d.carolKey)
	for _, want := range []string{"Carol Example", "carol@example.org", k.Fingerprint, d.carol.Recipient().String(), "alice",
		"deployment apply --execute", "operator register --execute"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sops updatekeys") {
		t.Errorf("output still sends the age key by hand:\n%s", out)
	}
	if strings.Contains(out, d.carol.String()) || strings.Contains(out, d.alice.String()) {
		t.Error("a private key was printed")
	}
	if got := dirNames(t, d.l.Dir); !slices.Equal(got, names) {
		t.Errorf("the directory holds %v, and held %v", got, names)
	}
}

func TestOperatorAddRequestTwiceIsNoOp(t *testing.T) {
	d := newRequestDeployment(t)
	req := d.request(t, d.invite, d.alicePriv)
	if out, err := d.add(t, req, "--all-sites"); err != nil {
		t.Fatalf("%v\n%s", problemText(err), out)
	}
	before := d.files(t)
	out, err := d.add(t, req, "--all-sites")
	if err != nil {
		t.Fatalf("%v\n%s", problemText(err), out)
	}
	if !sameFiles(d.files(t), before) {
		t.Error("the second run changed a file")
	}
	if !strings.Contains(out, "nothing to change") {
		t.Errorf("the second run does not say it changed nothing:\n%s", out)
	}
}

func TestOperatorAddRequestReencryptsOnly(t *testing.T) {
	d := newRequestDeployment(t)
	data, _ := os.ReadFile(d.configPath)
	entry := config.Operator{Name: "Carol Earlier", Email: "carol@example.org", Keys: bare(d.carolKey) + "\n"}
	declared, err := config.AddOperatorBytes(data, "carol", entry, d.cfg.SiteNames())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteDeclaration(d.configPath, declared); err != nil {
		t.Fatal(err)
	}
	before := d.files(t)
	out, err := d.add(t, d.request(t, d.invite, d.alicePriv))
	if err != nil {
		t.Fatalf("%v\n%s", problemText(err), out)
	}
	after := d.files(t)
	if !bytes.Equal(before[0], after[0]) {
		t.Error(config.FileName + " changed")
	}
	if bytes.Equal(before[1], after[1]) || bytes.Equal(before[2], after[2]) {
		t.Error(".sops.yaml or the secrets did not change")
	}
	if err := d.opensWith(t, d.carol); err != nil {
		t.Errorf("carol cannot open the secrets: %v", err)
	}
}

// refused runs the request and checks it is refused, naming want, with no
// file changed.
func (d *requestDeployment) refused(t *testing.T, request, want string, rest ...string) {
	t.Helper()
	before := d.files(t)
	out, err := d.add(t, request, rest...)
	if err == nil {
		t.Fatalf("accepted:\n%s", out)
	}
	if !strings.Contains(problemText(err), want) {
		t.Errorf("got %v, want %q", problemText(err), want)
	}
	if !sameFiles(d.files(t), before) {
		t.Error("a file changed")
	}
}

func TestOperatorAddRequestRefusesInviteFromNonOperator(t *testing.T) {
	d := newRequestDeployment(t)
	malloryPriv, malloryPub := testKey(t)
	k, _, _ := config.ParseKeyLine(malloryPub)
	forged := d.invite
	forged.Inviter = operatorinvite.Inviter{Name: "alice", Fingerprint: k.Fingerprint}
	d.refused(t, d.request(t, forged, malloryPriv), "no operator", "--all-sites")
}

func TestOperatorAddRequestRefusesOtherDeployment(t *testing.T) {
	d := newRequestDeployment(t)
	other := d.invite
	other.Deployment = "0b7d4c3a-f2a9-4e1c-9e2f-5a6b7c8d9e01"
	d.refused(t, d.request(t, other, d.alicePriv), other.Deployment, "--all-sites")
}

func TestOperatorAddRequestRefusesOtherRoot(t *testing.T) {
	d := newRequestDeployment(t)
	other := d.invite
	other.Root = strings.Repeat("ab", 20)
	d.refused(t, d.request(t, other, d.alicePriv), "root", "--all-sites")
}

func TestOperatorAddRequestRefusesDeclaredWithOtherKey(t *testing.T) {
	d := newRequestDeployment(t)
	data, _ := os.ReadFile(d.configPath)
	_, otherKey := testKey(t)
	declared, err := config.AddOperatorBytes(data, "carol", config.Operator{Name: "Carol Example", Email: "carol@example.org", Keys: otherKey + "\n"}, d.cfg.SiteNames())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteDeclaration(d.configPath, declared); err != nil {
		t.Fatal(err)
	}
	d.refused(t, d.request(t, d.invite, d.alicePriv), "carol", "--all-sites")
}

func TestOperatorAddRequestRefusesWithNameFlag(t *testing.T) {
	d := newRequestDeployment(t)
	req := d.request(t, d.invite, d.alicePriv)
	d.refused(t, req, "--name", "--name", "Carol Other", "--all-sites")
	d.refused(t, req, "--email", "--email", "other@example.org", "--all-sites")
	d.refused(t, req, "--key", "--key", d.carolKey, "--all-sites")
	d.refused(t, req, "username", "carol", "--all-sites")
}

func TestOperatorAddRequestRefusesTamperedRequest(t *testing.T) {
	d := newRequestDeployment(t)
	req := d.request(t, d.invite, d.alicePriv)
	data, _ := os.ReadFile(req)
	tampered := bytes.Replace(data, []byte("Carol Example"), []byte("Carol Exampla"), 1)
	if err := os.WriteFile(req, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	d.refused(t, req, "request", "--all-sites")
}

// Review Focus 3: a workstation that cannot open the secrets refuses before
// anything is written.
func TestOperatorAddRequestCannotDecrypt(t *testing.T) {
	d := newRequestDeployment(t)
	withKeystore(t, keystore.NewMemory())
	d.refused(t, d.request(t, d.invite, d.alicePriv), "secrets", "--all-sites")
}

// A write that fails after the first leaves every file as it was.
func TestOperatorAddRequestRestoresOnFailure(t *testing.T) {
	d := newRequestDeployment(t)
	saved := writeDeclaration
	writeDeclaration = func(string, []byte) error { return errors.New("disk full") }
	t.Cleanup(func() { writeDeclaration = saved })
	d.refused(t, d.request(t, d.invite, d.alicePriv), "as it was", "--all-sites")
}

func sameFiles(a, b [3][]byte) bool {
	return bytes.Equal(a[0], b[0]) && bytes.Equal(a[1], b[1]) && bytes.Equal(a[2], b[2])
}
