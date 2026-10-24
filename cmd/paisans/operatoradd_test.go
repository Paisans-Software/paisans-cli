package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
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
