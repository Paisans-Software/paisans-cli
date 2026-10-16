package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

func byID(t *testing.T, w *world, site, ref string, o siteremove.Options) *siteremove.Plan {
	t.Helper()
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	p, err := siteremove.BuildForcedByID(dest, w.hosts[site], ref, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func hostTexts(p *siteremove.Plan) []string {
	var out []string
	for _, s := range stageNamed(p, "clean the host").Steps {
		if s.Verb != "note" {
			out = append(out, s.Site+": "+s.Text)
		}
	}
	return out
}

// A monitor-only deployment is cleaned with no configuration at all: the
// same host stage the forced removal plans with one, by its token or its
// full id, and the host is left with nothing of it.
func TestByIDCleansAMonitorWithNoConfiguration(t *testing.T) {
	w := setup(t)
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	want := hostTexts(forced(t, w, w.cfg.WithoutSite("watch"), "watch", dest, siteremove.Options{}))
	for _, ref := range []string{"f2a9", ourID} {
		p := byID(t, w, "watch", ref, siteremove.Options{})
		if p.Site != "watch" || p.Current {
			t.Errorf("%s: site %q, current %v", ref, p.Site, p.Current)
		}
		got := hostTexts(p)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: host stage differs from the forced removal's\nwant:\n%s\ngot:\n%s", ref, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
	}
	p := byID(t, w, "watch", "f2a9", siteremove.Options{DeleteData: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	h := w.hosts["watch"]
	reg, _ := registry.Parse([]byte(h.files[registry.Path]))
	if _, ok := reg.Deployments[ourID]; !ok {
		t.Error("the registry entry is gone, which is host evict's to remove")
	}
	for path := range h.files {
		if strings.HasPrefix(path, root+"/") {
			t.Errorf("%s is still there", path)
		}
	}
	if len(h.containers) != 0 {
		t.Errorf("containers left: %+v", h.containers)
	}
}

// A removal without --delete-data keeps the data, and the registry entry,
// which is host evict's, stays as it was, so a later run by --id finds the
// deployment and deletes the data; a run without --delete-data in between
// keeps the data again and names no registry.
func TestByIDDeletesDataARemovalWithoutDeleteDataKept(t *testing.T) {
	w := setup(t)
	h := w.hosts["home-b"]
	entry := h.files[registry.Path]
	if err := siteremove.Execute(byID(t, w, "home-b", "f2a9", siteremove.Options{})); err != nil {
		t.Fatal(err)
	}
	if h.files[registry.Path] != entry {
		t.Fatalf("the registry changed:\n%s", h.files[registry.Path])
	}
	if _, ok := h.files[root+"/infra/postgres/PG_VERSION"]; !ok {
		t.Fatal("data was deleted without --delete-data")
	}

	again := byID(t, w, "home-b", "f2a9", siteremove.Options{})
	if !again.DataKept || strings.Contains(printed(again), registry.Path) {
		t.Errorf("a second run without --delete-data:\n%s", printed(again))
	}

	if err := siteremove.Execute(byID(t, w, "home-b", "f2a9", siteremove.Options{DeleteData: true})); err != nil {
		t.Fatal(err)
	}
	if h.files[registry.Path] != entry {
		t.Errorf("the registry changed:\n%s", h.files[registry.Path])
	}
	for path := range h.files {
		if strings.HasPrefix(path, root+"/") {
			t.Errorf("%s is still there", path)
		}
	}
	for _, v := range h.volumes {
		if v.Deployment == ourID {
			t.Errorf("volume %s is still there", v.Name)
		}
	}
}

// DNS is the one thing the configuration would have said that is reported as
// unchecked; neither the secrets file nor Pocket ID is mentioned.
func TestByIDSaysWhatItDidNotRead(t *testing.T) {
	w := setup(t)
	remains := strings.Join(byID(t, w, "watch", "f2a9", siteremove.Options{}).Remains(), "\n")
	if !strings.Contains(remains, "DNS records for this deployment, if any exist, were not modified") {
		t.Errorf("no DNS line:\n%s", remains)
	}
	if strings.Contains(remains, "secrets") {
		t.Errorf("an --id run mentions secrets:\n%s", remains)
	}
	remains = strings.Join(byID(t, w, "home-a", "f2a9", siteremove.Options{}).Remains(), "\n")
	if strings.Contains(remains, "Pocket ID") {
		t.Errorf("an apps site is told about Pocket ID:\n%s", remains)
	}
}

// On a host shared with another deployment, nothing of that deployment's is
// planned or touched: its container, unit, ufw rule and registry entry stay.
func TestByIDLeavesAnotherDeploymentAlone(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	p := byID(t, w, "home-b", ourID, siteremove.Options{DeleteData: true})
	if plan := printed(p); strings.Contains(plan, "0c1d") {
		t.Errorf("the plan names the other deployment:\n%s", plan)
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	reg, _ := registry.Parse([]byte(b.files[registry.Path]))
	if _, ok := reg.Deployments[otherID]; !ok {
		t.Error("the other deployment's registry entry is gone")
	}
	if _, ok := b.files[userUnits+"/paisans-0c1d-config-daemon.service"]; !ok {
		t.Error("the other deployment's unit is gone")
	}
	if !contains(b.rules, "allow 51821/udp comment 'paisans-0c1d: wireguard'") {
		t.Error("the other deployment's ufw rule is gone")
	}
	found := false
	for _, c := range b.containers {
		found = found || c.Deployment == otherID
	}
	if !found {
		t.Error("the other deployment's container is gone")
	}
	for _, c := range b.commands {
		if strings.Contains(c, otherID) || strings.Contains(c, "paisans-0c1d") {
			t.Errorf("a command names the other deployment: %s", c)
		}
	}
}

// The deployment is the one the login's own admission record names, read
// with no registry: the registry is root's, and the deployment's user cannot
// read it. Its token and its full id both find it, as the site and roles the
// record names.
func TestByIDFindsTheDeploymentByTheLoginsRecord(t *testing.T) {
	for _, ref := range []string{"f2a9", ourID} {
		w := setup(t)
		h := w.hosts["home-b"]
		delete(h.files, registry.Path)
		p := byID(t, w, "home-b", ref, siteremove.Options{})
		if p.Site != "home-b" || p.DeploymentID() != ourID {
			t.Errorf("%s: site %q, deployment %s", ref, p.Site, p.DeploymentID())
		}
		for _, r := range h.reads {
			if r == registry.Path {
				t.Errorf("%s: read the registry", ref)
			}
		}
	}
}

// --id naming another deployment than the login's record is refused, naming
// the deployment the record is for, before anything is planned.
func TestByIDRefusesAnotherDeploymentThanTheLoginsRecord(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	_, err := siteremove.BuildForcedByID(dest, b, "0c1d", siteremove.Options{})
	if err == nil {
		t.Fatal("--id 0c1d was not refused")
	}
	for _, want := range []string{"site remove --id 0c1d: this login's admission record on ubuntu@192.0.2.30:22 is for deployment " + ourID + " (home-b)", "Log in as that deployment's user", "Nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no %q in: %v", want, err)
		}
	}
	if n := b.ran("docker"); n != 0 {
		t.Errorf("%d docker command(s) ran", n)
	}
}

// A login with no admission record of its own, on a host with none for
// --id either, is refused naming why.
func TestByIDRefusesALoginWithNoRecord(t *testing.T) {
	w := setup(t)
	b := w.hosts["home-b"]
	recordPath, _ := admission.Path(dep.Token())
	delete(b.files, recordPath)
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	_, err := siteremove.BuildForcedByID(dest, b, "f2a9", siteremove.Options{})
	if err == nil || !strings.Contains(err.Error(), admission.ErrNotMine.Error()) || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("err = %v", err)
	}
	if n := b.ran("docker"); n != 0 {
		t.Errorf("%d docker command(s) ran", n)
	}
}

func TestByIDRefusesHostGone(t *testing.T) {
	w := setup(t)
	dest, _ := config.ParseDestination("ubuntu@192.0.2.30")
	if _, err := siteremove.BuildForcedByID(dest, w.hosts["watch"], "f2a9", siteremove.Options{HostGone: true}); err == nil || !strings.Contains(err.Error(), "Drop one of them") {
		t.Errorf("err = %v", err)
	}
}
