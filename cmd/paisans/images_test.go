package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
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
	rows, lags, err := imageRows(&ui.Recorder{}, cfg, secrets)
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
	rows, _, err := imageRows(rec, cfg, secrets)
	if err != nil {
		t.Fatalf("one site down stopped the listing: %v", err)
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
	if got := reportError(&strings.Builder{}, errImagesMove, false); got != 3 {
		t.Errorf("errImagesMove exits %d", got)
	}
}
