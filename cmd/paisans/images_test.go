package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// trackRegistry names one build for every tag and puts a track on its
// pattern with each * as 0.
type trackRegistry string

func (r trackRegistry) Digest(string) (string, error) { return string(r), nil }
func (r trackRegistry) Track(ref string) (string, error) {
	return strings.ReplaceAll(ref, "*", "0"), nil
}

// downHost is a site that does not answer.
type downHost struct{ emptyHost }

func (downHost) Run(string) (string, error) {
	return "", errors.New("ssh: connect to host: no route to host")
}
func (downHost) ReadFile(string) (string, bool, error) {
	return "", false, errors.New("ssh: connect to host: no route to host")
}

func imagesFixture(t *testing.T, edit func(*config.Config)) (*config.Config, *config.Secrets) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
	if err := os.WriteFile(path, []byte(freshSite), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(cfg)
	}
	secrets := &config.Secrets{Version: 1}
	if _, err := secretsgen.Fill(cfg, secrets); err != nil {
		t.Fatal(err)
	}
	return cfg, secrets
}

func withImagesTransport(t *testing.T, f func(string, config.Site) apply.Transport) {
	t.Helper()
	saved, savedDigests := imagesTransport, imageDigests
	imagesTransport, imageDigests = f, trackRegistry(testCaddyDigest)
	t.Cleanup(func() { imagesTransport, imageDigests = saved, savedDigests })
}

func TestImagesListsEveryImageAndWhereItCameFrom(t *testing.T) {
	withImagesTransport(t, func(string, config.Site) apply.Transport { return emptyHost{} })
	cfg, secrets := imagesFixture(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.4.30"}
	})
	rows, lags, _, err := imageRows(&ui.Recorder{}, cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !lags {
		t.Error("an override behind its track did not count")
	}
	var etcd, haproxy *imageRow
	for i := range rows {
		switch {
		case rows[i].stack == "infra" && rows[i].service == "etcd":
			etcd = &rows[i]
		case rows[i].stack == "infra" && rows[i].service == "haproxy":
			haproxy = &rows[i]
		}
	}
	if etcd == nil || haproxy == nil {
		t.Fatalf("rows: %+v", rows)
	}
	if etcd.origin != "infra.images.etcd" || !strings.Contains(etcd.note, "v3.5.0") || etcd.running != "-" {
		t.Errorf("etcd row: %+v", *etcd)
	}
	if haproxy.origin != "foundation" || !strings.HasPrefix(haproxy.newest, "3.0.0-alpine (") {
		t.Errorf("haproxy row: %+v", *haproxy)
	}
}

func TestImagesKeepsGoingPastASiteItCannotReach(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	if len(cfg.SiteNames()) < 1 {
		t.Fatal("the fixture declares no site")
	}
	withImagesTransport(t, func(string, config.Site) apply.Transport { return downHost{} })
	rec := &ui.Recorder{Verbose_: true}
	rows, _, unread, err := imageRows(rec, cfg, secrets)
	if err != nil {
		t.Fatalf("one site down stopped the listing: %v", err)
	}
	if len(unread) != 1 || unread[0] != "home-a" {
		t.Errorf("unread sites: %v", unread)
	}
	if len(rows) == 0 {
		t.Fatal("a site that cannot be reached listed nothing")
	}
	for _, row := range rows {
		if row.running != "unknown" {
			t.Errorf("a site that cannot be reached has a running build: %+v", row)
		}
	}
	if !strings.Contains(rec.Lines(), "no route to host") {
		t.Errorf("the reason was not reported:\n%s", rec.Lines())
	}
}

func TestImagesCheckExitsThreeWhenSomethingWouldMove(t *testing.T) {
	var out strings.Builder
	if got := reportError(&out, errImagesMove, false); got != 3 {
		t.Errorf("errImagesMove exits %d", got)
	}
	if out.Len() != 0 {
		t.Errorf("--check printed %q", out.String())
	}
}

func TestImagesPrintsTheTableThenFailsForASiteItCannotRead(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	withImagesTransport(t, func(string, config.Site) apply.Transport { return downHost{} })
	var out strings.Builder
	err := writeImages(&ui.Recorder{}, cfg, secrets, &out, false)
	var problem *ui.Problem
	if !errors.As(err, &problem) || !strings.Contains(problem.Hint, "home-a") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "SITE") || !strings.Contains(out.String(), "unknown") {
		t.Errorf("the table was not written:\n%s", out.String())
	}
}

// downRegistry is a registry this workstation cannot reach.
type downRegistry struct{}

func (downRegistry) Digest(string) (string, error) {
	return "", errors.New("dial tcp: lookup registry: no such host")
}
func (downRegistry) Track(string) (string, error) {
	return "", errors.New("dial tcp: lookup registry: no such host")
}

// deployedHost is a site an apply has run on: it reads back what the apply
// wrote, so its compose files name the builds that apply pinned.
type deployedHost struct {
	runningHost
	files map[string]string
}

func (h deployedHost) ReadFile(path string) (string, bool, error) {
	content, ok := h.files[path]
	return content, ok, nil
}

// Run answers the WireGuard helper's show reads, which a plan of an up mesh
// sends in one command, from the mesh file the apply wrote: the interface
// runs what its file says, so planning finds nothing to sync. Every other
// command is the running host's.
func (h deployedHost) Run(command string) (string, error) {
	if !strings.HasPrefix(command, "echo paisans-show:") {
		return h.runningHost.Run(command)
	}
	var conf string
	for path, content := range h.files {
		if strings.Contains(path, "/psns-") && strings.HasSuffix(path, ".conf") {
			conf = content
		}
	}
	var out strings.Builder
	for _, part := range strings.Split(command, " && ") {
		if field, ok := strings.CutPrefix(part, "echo paisans-show:"); ok {
			out.WriteString("paisans-show:" + field + "\n")
			continue
		}
		_, field, _ := strings.Cut(part, " show ")
		switch field {
		case "public-key":
			m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)`).FindStringSubmatch(conf)
			if m == nil {
				return "", fmt.Errorf("the mesh file has no PrivateKey")
			}
			key, err := render.PublicKey(m[1])
			if err != nil {
				return "", err
			}
			out.WriteString(key + "\n")
		case "listen-port":
			port := "40123"
			if m := regexp.MustCompile(`(?m)^ListenPort = (\S+)`).FindStringSubmatch(conf); m != nil {
				port = m[1]
			}
			out.WriteString(port + "\n")
		case "peers":
			for _, m := range regexp.MustCompile(`(?m)^PublicKey = (\S+)`).FindAllStringSubmatch(conf, -1) {
				out.WriteString(m[1] + "\n")
			}
		case "allowed-ips", "persistent-keepalive":
			for _, peer := range regexp.MustCompile(`(?m)^\[Peer\]`).Split(conf, -1)[1:] {
				key := regexp.MustCompile(`(?m)^PublicKey = (\S+)`).FindStringSubmatch(peer)
				if key == nil {
					continue
				}
				value := "(none)"
				if field == "persistent-keepalive" {
					value = "off"
				}
				attr := map[string]string{"allowed-ips": "AllowedIPs", "persistent-keepalive": "PersistentKeepalive"}[field]
				if m := regexp.MustCompile(`(?m)^` + attr + ` = (.+)$`).FindStringSubmatch(peer); m != nil && strings.TrimSpace(m[1]) != "0" {
					value = strings.ReplaceAll(strings.TrimSpace(m[1]), ", ", " ")
				}
				out.WriteString(key[1] + "\t" + value + "\n")
			}
		}
	}
	return out.String(), nil
}

func (h deployedHost) WriteFile(path, content string, _ uint32) error {
	h.files[path] = content
	return nil
}

// deployedSite applies cfg to home-a through imageDigests and returns the
// host it leaves, running exactly the builds that apply pinned.
func deployedSite(t *testing.T, cfg *config.Config, secrets *config.Secrets) deployedHost {
	t.Helper()
	host := deployedHost{runningHost: runningHost{failingHost{match: "\x00"}}, files: map[string]string{}}
	plan, err := planSiteApply(cfg, secrets, "home-a", host)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply.Execute(plan, host); err != nil {
		t.Fatal(err)
	}
	return host
}

// A registry that cannot be reached, on a site that can, is unknown under
// newest for each image it could not resolve. The site is read, every other
// column prints, and the command does not fail.
func TestImagesShowsAnImageItCannotResolveAsUnknown(t *testing.T) {
	withImagesTransport(t, func(string, config.Site) apply.Transport { return emptyHost{} })
	imageDigests = downRegistry{}
	cfg, secrets := imagesFixture(t, nil)
	rows, _, unread, err := imageRows(&ui.Recorder{}, cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 0 {
		t.Errorf("a site that answered is unread: %v", unread)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if row.newest != "unknown" || row.running != "-" || row.origin == "" || row.moves || !strings.HasPrefix(row.note, "unresolved: no such host") {
			t.Errorf("row %+v", row)
		}
	}
	var out strings.Builder
	if err := writeImages(&ui.Recorder{}, cfg, secrets, &out, false); err != nil {
		t.Errorf("a registry down failed the command: %v", err)
	}
}

// A site that cannot be read fails the command, after the table, and says
// where to look for why.
func TestImagesFailsForASiteItCannotRead(t *testing.T) {
	withImagesTransport(t, func(string, config.Site) apply.Transport { return downHost{} })
	cfg, secrets := imagesFixture(t, nil)
	err := writeImages(&ui.Recorder{}, cfg, secrets, &strings.Builder{}, false)
	var problem *ui.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("err = %v", err)
	}
	if problem.Hint != "could not read home-a" || !strings.Contains(problem.Explain, "the warnings above say why") || strings.Contains(problem.Explain, "ssh") {
		t.Errorf("problem: %+v", problem)
	}
}

// Past three sites, the hint counts them and the explanation names them.
func TestImagesCountsManySitesItCannotRead(t *testing.T) {
	problem := unreadProblem([]string{"a", "b", "c", "d"})
	if problem.Hint != "could not read 4 sites" || !strings.Contains(problem.Explain, "a, b, c, d") {
		t.Errorf("problem: %+v", problem)
	}
	if problem := unreadProblem([]string{"a", "b", "c"}); problem.Hint != "could not read a, b, c" {
		t.Errorf("problem: %+v", problem)
	}
}

// A site that runs exactly what the next apply would, with no override
// behind, passes --check.
func TestImagesCheckPassesASiteThatRunsTheNewestBuilds(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	withImagesTransport(t, nil)
	host := deployedSite(t, cfg, secrets)
	imagesTransport = func(string, config.Site) apply.Transport { return host }
	var out strings.Builder
	if err := writeImages(&ui.Recorder{}, cfg, secrets, &out, true); err != nil {
		t.Fatalf("--check: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "moves on next apply") {
		t.Errorf("a build moves:\n%s", out.String())
	}
}

// The same site with an override behind its track fails --check with
// errImagesMove, though no build moves.
func TestImagesCheckFailsAnOverrideBehind(t *testing.T) {
	cfg, secrets := imagesFixture(t, func(c *config.Config) {
		c.Infra.Images = map[string]string{"etcd": "gcr.io/etcd-development/etcd:v3.4.30"}
	})
	withImagesTransport(t, nil)
	host := deployedSite(t, cfg, secrets)
	imagesTransport = func(string, config.Site) apply.Transport { return host }
	var out strings.Builder
	if err := writeImages(&ui.Recorder{}, cfg, secrets, &out, true); !errors.Is(err, errImagesMove) {
		t.Fatalf("--check: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "moves on next apply") || !strings.Contains(out.String(), "behind gcr.io/etcd-development/etcd:v3.5.0") {
		t.Errorf("table:\n%s", out.String())
	}
}

// A registry that cannot be reached keeps the builds a site runs, which
// print under running, with newest unknown.
func TestImagesShowsNewestUnknownForADeployedBuild(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	withImagesTransport(t, nil)
	host := deployedSite(t, cfg, secrets)
	imagesTransport = func(string, config.Site) apply.Transport { return host }
	imageDigests = downRegistry{}
	rows, _, unread, err := imageRows(&ui.Recorder{}, cfg, secrets)
	if err != nil || len(unread) != 0 {
		t.Fatalf("err %v, unread %v", err, unread)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if row.newest != "unknown" || row.running == "-" || row.running == "unknown" || row.moves {
			t.Errorf("row %+v", row)
		}
	}
}

// A site whose builds were rebuilt under the same tags fails --check on the
// move alone: no override lags.
func TestImagesCheckFailsABuildThatMoves(t *testing.T) {
	cfg, secrets := imagesFixture(t, nil)
	withImagesTransport(t, nil)
	host := deployedSite(t, cfg, secrets)
	imagesTransport = func(string, config.Site) apply.Transport { return host }
	imageDigests = trackRegistry("sha256:" + strings.Repeat("ab", 32))
	var out strings.Builder
	if err := writeImages(&ui.Recorder{}, cfg, secrets, &out, true); !errors.Is(err, errImagesMove) {
		t.Fatalf("--check: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "moves on next apply") || strings.Contains(out.String(), "behind ") {
		t.Errorf("table:\n%s", out.String())
	}
}
