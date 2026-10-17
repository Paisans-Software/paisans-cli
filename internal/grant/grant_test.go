package grant_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/grant"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/version"
	"golang.org/x/crypto/ssh"
)

func fixture(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Every site logs in with alice's key alone: the fixture's second
	// operator, bob, is there because validate requires two.
	for name, s := range cfg.Sites {
		var keys []config.NamedKey
		for _, k := range s.SSH.Keys {
			if k.Name == "alice" {
				keys = append(keys, k)
			}
		}
		s.SSH.Keys = keys
		cfg.Sites[name] = s
	}
	return cfg
}

func rendered(t *testing.T, cfg *config.Config) *render.Plan {
	t.Helper()
	secrets, err := config.LoadSecrets(filepath.Join("..", "render", "testdata", "secrets.fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// key is a public key line made from seed, so each test key is its own.
func key(t *testing.T, seed byte, comment string) (line, fingerprint string) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(append(make([]byte, 31), seed))
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(pub.Marshal()) + " " + comment, ssh.FingerprintSHA256(pub)
}

// withKeys makes site's ssh.keys exactly these admins' lines.
func withKeys(cfg *config.Config, site string, keys ...config.NamedKey) {
	s := cfg.Sites[site]
	s.SSH.Keys = keys
	cfg.Sites[site] = s
}

func TestKeysComeFromTheSitesAdmins(t *testing.T) {
	cfg := fixture(t)
	g, err := grant.For(cfg, nil, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if g.Login != "ubuntu" || len(g.Keys) != 1 || g.Keys[0].Admin != "alice" || g.Keys[0].Comment != "alice@example.org" || !strings.HasPrefix(g.Keys[0].Fingerprint, "SHA256:") {
		t.Errorf("grant %+v", g)
	}
	if g.Images != nil {
		t.Errorf("images without a render: %+v", g.Images)
	}
}

func TestEmptyBaselineReviewsEveryEntry(t *testing.T) {
	cfg := fixture(t)
	g, err := grant.For(cfg, rendered(t, cfg), "vm")
	if err != nil {
		t.Fatal(err)
	}
	r := grant.Compare("vm", g, grant.Record{}, grant.AllScope)
	// Every entry is in it; a foundation image is shown and not approved.
	foundation := 0
	for _, i := range g.Images {
		if i.Foundation {
			foundation++
		}
	}
	if len(r.Changes) != len(g.Keys)+len(g.Images)+len(g.Blueprints) || r.Pending() != len(r.Changes)-foundation || r.Pending() == 0 || foundation == 0 {
		t.Errorf("%d pending of %d keys, %d images (%d foundation) and %d blueprints", r.Pending(), len(g.Keys), len(g.Images), foundation, len(g.Blueprints))
	}
	if r.Changes[0].Line() != fmt.Sprintf("+ key %s (alice, alice@example.org) may log in as ubuntu", g.Keys[0].Fingerprint) {
		t.Errorf("keys first: %q", r.Changes[0].Line())
	}
	if !r.First {
		t.Error("a review against no record is not marked as the first")
	}
}

// A review is the first when the record holds none of the parts it covers:
// a host prepared since the record existed still has its images reviewed
// for the first time by its first apply.
func TestFirstIsNoRecordedPartTheReviewCovers(t *testing.T) {
	cfg := fixture(t)
	g, err := grant.For(cfg, rendered(t, cfg), "vm")
	if err != nil {
		t.Fatal(err)
	}
	prepared := grant.Update(grant.Record{}, g, grant.PrepareScope, time.Unix(0, 0), "")
	if grant.Compare("vm", g, prepared, grant.PrepareScope).First {
		t.Error("site prepare's review of recorded keys is marked first")
	}
	if !grant.Compare("vm", g, prepared, grant.ApplyScope()).First {
		t.Error("the first review of the images is not marked first")
	}
}

// Every change of a stack's images is together, whatever it is, so a long
// first review reads stack by stack.
func TestImageChangesAreGroupedByStack(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{{Stack: "b", Service: "x", Reference: "b:2"}, {Stack: "c", Service: "x", Reference: "c:1"}}}
	rec := grant.Record{Images: &grant.ImagesPart{Entries: []grant.Image{{Stack: "a", Service: "x", Reference: "a:1"}, {Stack: "b", Service: "x", Reference: "b:1"}, {Stack: "b", Service: "y", Reference: "b:1"}}}}
	var lines []string
	for _, c := range grant.Compare("home-a", g, rec, grant.ApplyScope()).Changes {
		lines = append(lines, c.Line())
	}
	want := []string{"- image a/x: a:1", "~ image b/x: b:1 → b:2", "- image b/y: b:1", "+ image c/x: c:1"}
	if !slices.Equal(lines, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestReviewRows(t *testing.T) {
	_, bob := key(t, 1, "bob@laptop")
	_, carol := key(t, 2, "carol@desk")
	g := grant.Grant{
		Login: "ubuntu",
		Keys:  []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a", Comment: "a"}, {Admin: "bob", Fingerprint: bob, Comment: "bob@laptop"}},
		Images: []grant.Image{
			{Stack: "infra", Service: "caddy", Reference: "caddy:2"},
			{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.15"},
		},
	}
	rec := grant.Record{
		Keys: &grant.KeysPart{Login: "ubuntu", Entries: []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a", Comment: "a"}, {Admin: "carol", Fingerprint: carol, Comment: "carol@desk"}}},
		Images: &grant.ImagesPart{Entries: []grant.Image{
			{Stack: "infra", Service: "caddy", Reference: "caddy:2"},
			{Stack: "talk", Service: "app", Reference: "ghcr.io/example/mbin:1.14"},
			{Stack: "old", Service: "app", Reference: "ghcr.io/example/old:1"},
		}},
	}
	r := grant.Compare("home-a", g, rec, grant.AllScope)
	var lines []string
	for _, c := range r.Changes {
		lines = append(lines, c.Line())
	}
	want := []string{
		"+ key " + bob + " (bob, bob@laptop) may log in as ubuntu",
		"- key " + carol + " (carol, carol@desk)",
		"- image old/app: ghcr.io/example/old:1",
		"~ image talk/app: ghcr.io/example/mbin:1.14 → ghcr.io/example/mbin:1.15",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("review:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if r.Pending() != 2 {
		t.Errorf("%d pending, want the addition and the change", r.Pending())
	}
}

func TestAChangedCommentOrAMovedKeyIsAChange(t *testing.T) {
	was := grant.Record{Keys: &grant.KeysPart{Login: "ubuntu", Entries: []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a", Comment: "alice@old"}}}}
	for name, now := range map[string]grant.Key{
		"comment": {Admin: "alice", Fingerprint: "SHA256:a", Comment: "alice@new"},
		"moved":   {Admin: "mallory", Fingerprint: "SHA256:a", Comment: "alice@old"},
	} {
		r := grant.Compare("home-a", grant.Grant{Login: "ubuntu", Keys: []grant.Key{now}}, was, grant.PrepareScope)
		if len(r.Changes) != 1 || r.Changes[0].Op != grant.Changed || r.Pending() != 1 {
			t.Errorf("%s: %+v", name, r.Changes)
		}
	}
	r := grant.Compare("home-a", grant.Grant{Login: "admin", Keys: was.Keys.Entries}, was, grant.PrepareScope)
	if len(r.Changes) != 1 || r.Changes[0].Line() != "~ login: ubuntu → admin; every key below may log in as admin" {
		t.Errorf("login: %+v", r.Changes)
	}
}

func TestImagesComeFromEveryComposeFile(t *testing.T) {
	cfg := fixture(t)
	plan := rendered(t, cfg)
	g, err := grant.For(cfg, plan, "vm")
	if err != nil {
		t.Fatal(err)
	}
	var caddy string
	for _, i := range g.Images {
		if i.Stack == "infra" && i.Service == "caddy" {
			caddy = i.Reference
		}
	}
	if caddy == "" {
		t.Fatalf("no infra/caddy on the gateway: %+v", g.Images)
	}
	// Every service of every compose file, not only the ones the
	// configuration names an image for: the infrastructure runs with
	// network_mode: host, so its images are reviewed too.
	stacks := map[string]bool{}
	for _, i := range g.Images {
		stacks[i.Stack] = true
	}
	if len(g.Images) < 2 || !stacks["infra"] {
		t.Errorf("images %+v", g.Images)
	}
	a, _ := grant.For(cfg, plan, "home-a")
	appStack := false
	for _, i := range a.Images {
		if i.Stack != "infra" {
			appStack = true
		}
	}
	if !appStack {
		t.Errorf("no app stack on home-a: %+v", a.Images)
	}
}

func TestAPinnedCaddyDigestIsTheReference(t *testing.T) {
	cfg := fixture(t)
	const pinned = "ghcr.io/example-org/caddy-desec@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.ACME.Image = pinned
	g, err := grant.For(cfg, rendered(t, cfg), "vm")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(g.Images, grant.Image{Stack: "infra", Service: "caddy", Reference: pinned}) {
		t.Errorf("caddy not pinned: %+v", g.Images)
	}
	// The same reference string is the same image; a different digest is
	// a change.
	was := grant.Record{Images: &grant.ImagesPart{Entries: g.Images}, Blueprints: &grant.BlueprintsPart{Entries: g.Blueprints}}
	if r := grant.Compare("vm", g, was, grant.ApplyScope()); len(r.Changes) != 0 {
		t.Errorf("unchanged: %+v", r.Changes)
	}
	cfg.ACME.Image = strings.Replace(pinned, "0123", "4567", 1)
	moved, _ := grant.For(cfg, rendered(t, cfg), "vm")
	if r := grant.Compare("vm", moved, was, grant.ApplyScope()); len(r.Changes) != 1 || r.Changes[0].Op != grant.Changed {
		t.Errorf("a new digest: %+v", r.Changes)
	}
}

func TestOnlyNarrowsTheImages(t *testing.T) {
	g := grant.Grant{Images: []grant.Image{{Stack: "talk", Service: "app", Reference: "b"}, {Stack: "blog", Service: "app", Reference: "b"}}}
	rec := grant.Record{Images: &grant.ImagesPart{Entries: []grant.Image{{Stack: "blog", Service: "app", Reference: "a"}, {Stack: "chat", Service: "app", Reference: "a"}}}}
	r := grant.Compare("home-a", g, rec, grant.ApplyScope("talk"))
	if len(r.Changes) != 1 || r.Changes[0].Subject != "image talk/app" {
		t.Errorf("only talk: %+v", r.Changes)
	}
	next := grant.Update(rec, g, grant.ApplyScope("talk"), time.Unix(0, 0), "")
	want := []grant.Image{{Stack: "blog", Service: "app", Reference: "a"}, {Stack: "chat", Service: "app", Reference: "a"}, {Stack: "talk", Service: "app", Reference: "b"}}
	if !slices.Equal(next.Images.Entries, want) {
		t.Errorf("stacks outside --only were not kept: %+v", next.Images.Entries)
	}
}

func TestUpdateKeepsAnUnchangedPart(t *testing.T) {
	g := grant.Grant{Login: "ubuntu", Keys: []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a"}}}
	rec := grant.Update(grant.Record{}, g, grant.PrepareScope, time.Unix(100, 0), "alice")
	if rec.Keys == nil || rec.Keys.ApprovedBy != "alice" || rec.Keys.AppliedAt != "1970-01-01T00:01:40Z" {
		t.Fatalf("%+v", rec.Keys)
	}
	again := grant.Update(rec, g, grant.PrepareScope, time.Unix(200, 0), "bob")
	if grant.Encode(again) != grant.Encode(rec) {
		t.Errorf("an unchanged grant rewrote the record:\n%s", grant.Encode(again))
	}
	// A removal keeps who approved what is left.
	fewer := grant.Update(rec, grant.Grant{Login: "ubuntu", Keys: []grant.Key{}}, grant.PrepareScope, time.Unix(300, 0), "")
	if fewer.Keys.ApprovedBy != "alice" || len(fewer.Keys.Entries) != 0 {
		t.Errorf("%+v", fewer.Keys)
	}
	if rec.Images != nil {
		t.Errorf("site prepare wrote images: %+v", rec.Images)
	}
}

func TestUnapproved(t *testing.T) {
	approved := grant.Review{Changes: []grant.Change{{Op: grant.Added, Subject: "image talk/app", To: "b"}}}
	same := grant.Review{Changes: []grant.Change{{Op: grant.Added, Subject: "image talk/app", To: "b"}, {Op: grant.Removed, Subject: "image x/y"}}}
	if u := same.Unapproved(approved); len(u) != 0 {
		t.Errorf("%+v", u)
	}
	since := grant.Review{Changes: []grant.Change{{Op: grant.Added, Subject: "image talk/app", To: "c"}}}
	if u := since.Unapproved(approved); len(u) != 1 {
		t.Errorf("a change made after the approval passed: %+v", u)
	}
}

// fakeHost holds one file, and runs a write the way the remote shell would:
// only if the file still hashes to what the command expects.
type fakeHost struct {
	content  string
	found    bool
	commands []string
	// race, when set, is written over the file before the next write runs,
	// as another command would.
	race string
}

func (h *fakeHost) Describe() string { return "ubuntu@home-a.example.org" }

func (h *fakeHost) ReadFile(string) (string, bool, error) { return h.content, h.found, nil }

var payload = regexp.MustCompile(`printf %s '([A-Za-z0-9+/=]+)' \| base64 -d`)
var expected = regexp.MustCompile(`\[ "\$cur" = '([0-9a-f]+|none)' \]`)

func (h *fakeHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	if h.race != "" {
		h.content, h.found, h.race = h.race, true, ""
	}
	want := expected.FindStringSubmatch(command)[1]
	cur := "none"
	if h.found {
		cur = sha(h.content)
	}
	if cur != want {
		return "paisans: the grant record changed while it was read\n", fmt.Errorf("exit status 1")
	}
	data, err := base64.StdEncoding.DecodeString(payload.FindStringSubmatch(command)[1])
	if err != nil {
		return "", err
	}
	h.content, h.found = string(data), true
	return "", nil
}

func TestWriteIsUnderTheLockAndKeepsTheOtherParts(t *testing.T) {
	images := grant.Record{Version: grant.Version, Images: &grant.ImagesPart{AppliedAt: "x", Entries: []grant.Image{{Stack: "talk", Service: "app", Reference: "a"}}}}
	h := &fakeHost{content: grant.Encode(images), found: true}
	g := grant.Grant{Login: "ubuntu", Keys: []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a"}}}
	wrote, err := grant.Write(h, testDeployment(t), func(r grant.Record) grant.Record {
		return grant.Update(r, g, grant.PrepareScope, time.Unix(0, 0), "")
	})
	if err != nil || !wrote {
		t.Fatalf("wrote %v, %v", wrote, err)
	}
	cmd := h.commands[0]
	lock, mv := strings.Index(cmd, "flock -w 60 9"), strings.Index(cmd, `mv "$tmp" "$f"`)
	if !strings.Contains(cmd, testDeployment(t).StateLockCommand("the grant record")) || lock < 0 || mv < lock || !strings.Contains(cmd, `chmod 644 "$tmp"`) {
		t.Errorf("not written under the deployment's lock by a rename: %s", cmd)
	}
	rec, err := grant.Parse(h.content)
	if err != nil || rec.Images == nil || rec.Keys == nil {
		t.Errorf("parts: %+v %v", rec, err)
	}
	if rec.Build != version.Current() {
		t.Errorf("the write records %+v, not this build", rec.Build)
	}
	// Written again with nothing changed, it writes nothing.
	if wrote, err := grant.Write(h, testDeployment(t), func(r grant.Record) grant.Record {
		return grant.Update(r, g, grant.PrepareScope, time.Unix(9, 0), "")
	}); wrote || err != nil || len(h.commands) != 1 {
		t.Errorf("rewrote an unchanged record: %v %v", wrote, err)
	}
}

func TestWriteReadsAgainWhenAnotherCommandWrote(t *testing.T) {
	other := grant.Encode(grant.Record{Images: &grant.ImagesPart{AppliedAt: "y", Entries: []grant.Image{{Stack: "blog", Service: "app", Reference: "b"}}}})
	h := &fakeHost{race: other}
	g := grant.Grant{Login: "ubuntu", Keys: []grant.Key{{Admin: "alice", Fingerprint: "SHA256:a"}}}
	if _, err := grant.Write(h, testDeployment(t), func(r grant.Record) grant.Record {
		return grant.Update(r, g, grant.PrepareScope, time.Unix(0, 0), "")
	}); err != nil {
		t.Fatal(err)
	}
	rec, _ := grant.Parse(h.content)
	if len(h.commands) != 2 || rec.Images == nil || rec.Keys == nil {
		t.Errorf("the other command's part was lost after %d writes: %s", len(h.commands), h.content)
	}
}

func TestAMalformedRecordIsNotRewritten(t *testing.T) {
	h := &fakeHost{content: `{"version": 9}`, found: true}
	if _, err := grant.Write(h, testDeployment(t), func(r grant.Record) grant.Record { return r }); err == nil || !strings.Contains(err.Error(), "`rm /srv/paisans/f2a9/.paisans/granted.json`") || len(h.commands) != 0 {
		t.Errorf("err %v after %d writes", err, len(h.commands))
	}
}

func TestRemoveCommandNamesTheRecord(t *testing.T) {
	d := testDeployment(t)
	if got := grant.RemoveCommand(d); got != "rm -f -- '/srv/paisans/f2a9/.paisans/granted.json'" {
		t.Errorf("%s", got)
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func testDeployment(t *testing.T) deployment.Deployment {
	t.Helper()
	return deployment.Deployment{ID: "f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01"}
}

// A control character in what the review prints, from the configuration or
// from the record a host holds, is shown as an escape: a comment carrying
// ESC cannot redraw the review's lines.
func TestReviewLinesShowControlCharactersEscaped(t *testing.T) {
	esc := "\x1b[1A\x1b[2K~ key"
	g := grant.Grant{Login: "ubuntu",
		Keys:   []grant.Key{{Admin: "bob", Fingerprint: "SHA256:b", Comment: "bob" + esc}},
		Images: []grant.Image{{Stack: "talk", Service: "app", Reference: "ghcr.io/x/mbin:1.1" + esc}},
	}
	rec := grant.Record{
		Keys:   &grant.KeysPart{Login: "ubuntu", Entries: []grant.Key{{Admin: "carol", Fingerprint: "SHA256:c", Comment: "carol" + esc}}},
		Images: &grant.ImagesPart{Entries: []grant.Image{{Stack: "talk", Service: "app", Reference: "ghcr.io/x/mbin:1.0" + esc}}},
	}
	rev := grant.Compare("home-a", g, rec, grant.AllScope)
	if len(rev.Changes) != 3 {
		t.Fatalf("changes: %+v", rev.Changes)
	}
	for _, c := range rev.Changes {
		if strings.ContainsRune(c.Line(), 0x1b) || !strings.Contains(c.Line(), `\x1b[1A`) {
			t.Errorf("printed raw: %q", c.Line())
		}
	}
}

// A site that leaves ssh.user out grants its keys to the user it is reached
// as, the deployment's own.
func TestTheLoginIsTheDefaultUserWhenSSHUserIsLeftOut(t *testing.T) {
	cfg := fixture(t)
	s := cfg.Sites["home-a"]
	s.SSH.User = ""
	cfg.Sites["home-a"] = s
	g, err := grant.For(cfg, nil, "home-a")
	if err != nil {
		t.Fatal(err)
	}
	if g.Login != "paisans-f2a9" {
		t.Errorf("login is %q, want paisans-f2a9", g.Login)
	}
}

func TestTheGrantRecordIsInTheDeploymentsStateDir(t *testing.T) {
	if got := grant.Path(testDeployment(t)); got != "/srv/paisans/f2a9/.paisans/granted.json" {
		t.Errorf("got %s", got)
	}
}
