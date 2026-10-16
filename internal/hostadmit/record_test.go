package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
	"github.com/paisans-software/paisans-cli/internal/registry"
)

const keysMarker = "paisans host admit: keys, read as the user"

var recordCases = addCases(
	goldenCase{name: "record-fresh", site: "vm", host: home},
	goldenCase{name: "record-mode-restored", site: "vm", host: func() *fakeHost {
		h := admittedHost(requestFor("vm"), hostadmit.Flags{})
		return h.withFile("/etc/paisans/admitted.f2a9.json", h.files["/etc/paisans/admitted.f2a9.json"], "600", 0)
	}},
	goldenCase{name: "record-etc-paisans-link-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/paisans", "lrwxrwxrwx", 0, 0) }},
	goldenCase{name: "record-etc-paisans-private", site: "vm", host: func() *fakeHost {
		return admittedHost(requestFor("vm"), hostadmit.Flags{}).withDir("/etc/paisans", "drwx------", 0, 0)
	}},
	goldenCase{name: "record-etc-paisans-wrong-owner", site: "vm", host: func() *fakeHost {
		return admittedHost(requestFor("vm"), hostadmit.Flags{}).withDir("/etc/paisans", "drwxr-xr-x", 1000, 1000)
	}},
	goldenCase{name: "claim-another-site-of-this-deployment-refused", site: "vm", host: func() *fakeHost {
		return home().file("/var/lib/paisans/registry.json", registryWith("f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", "10.44.0.1"))
	}},
	goldenCase{name: "claim-public-ports-held-refused", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: func() *fakeHost {
		return home().file("/var/lib/paisans/registry.json", `{"version":1,"deployments":{`+"\n"+
			`"0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01":{"token":"0c0d","root":"/srv/paisans/0c0d","domain":"","site":"edge","claimed_at":"2026-10-01T09:00:00Z","interface":"psns-0c0d","listen_port":51899,"subnet":"10.50.0.0/24","address":"10.50.0.1","roles":"gateway"}`+"\n}}\n")
	}},
	goldenCase{name: "keys-some-present", site: "home-a", flags: gateway, host: func() *fakeHost {
		return home().answer(keysMarker, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA owner@laptop\npaisans-sidecar\n")
	}},
)

func TestRecordPlans(t *testing.T) { runGolden(t, recordCases) }

// recordCase is the record case named name.
func recordCase(t *testing.T, name string) goldenCase {
	t.Helper()
	for _, c := range recordCases {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no record case %s", name)
	return goldenCase{}
}

// A claim from before entries named their site holds only an address: the
// refusal names that address and no empty site.
func TestAnotherSiteWithNoNameIsRefusedByAddress(t *testing.T) {
	_, e := registry.ForRequest(requestFor("home-a"), admitted)
	e.Address, e.Site = "10.44.0.1", ""
	h := home().file("/var/lib/paisans/registry.json", encodeRegistry("f2a9c4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", e))
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || strings.Contains(err.Error(), "site  ") || !strings.Contains(err.Error(), "this machine already runs this deployment, at 10.44.0.1") {
		t.Errorf("Build = %v", err)
	}
}

// The keys go in with ~/.ssh no one but the user can write: sshd's
// StrictModes ignores authorized_keys under a group- or other-writable
// directory, so an existing ~/.ssh loses those bits.
func TestTheKeysScriptKeepsSSHPrivate(t *testing.T) {
	cmd := hostadmit.KeysCommand("paisans-f2a9", "/home/paisans-f2a9", "f2a9")
	mkdir, chmod := strings.Index(cmd, `mkdir -p "$HOME/.ssh"`), strings.Index(cmd, `chmod go-w "$HOME/.ssh"`)
	if mkdir < 0 || chmod < mkdir {
		t.Errorf("the keys script does not make ~/.ssh private:\n%s", cmd)
	}
}

// Every plan that writes the record writes it root's, mode 0644, at its
// path, so the deployment's user reads it without sudo; and a record found
// with another mode is rewritten.
func TestRecordIsWrittenMode0644(t *testing.T) {
	path, _ := admission.Path("f2a9")
	for _, c := range allCases {
		_, plan := golden(t, c)
		if plan == nil {
			continue
		}
		for _, s := range plan.Steps {
			if s.File != nil && s.File.Path == path && s.File.Mode != 0o644 {
				t.Errorf("%s: the record is written mode %o", c.name, s.File.Mode)
			}
		}
	}
	plan, err := hostadmit.Build(recordCase(t, "record-mode-restored").host(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Text(), "write "+path+" mode 0644") {
		t.Errorf("a record found mode 600 is not rewritten 0644:\n%s", plan.Text())
	}
}

// /etc/paisans is made, or made root's 755, before any part writes into it,
// so a directory the user cannot traverse is mended before egress and the
// sysctls put files there, and a re-run of a host with it right plans nothing.
func TestEtcPaisansIsSettledBeforeItsWriters(t *testing.T) {
	index := func(plan *hostadmit.Plan, title string) int {
		for i, s := range plan.Steps {
			if s.Title == title {
				return i
			}
		}
		return -1
	}
	for name, c := range map[string]struct {
		host  *fakeHost
		title string
	}{
		"absent":      {home(), "create /etc/paisans"},
		"private":     {admittedHost(requestFor("vm"), hostadmit.Flags{}).withDir("/etc/paisans", "drwx------", 0, 0), "make /etc/paisans root's alone"},
		"wrong owner": {admittedHost(requestFor("vm"), hostadmit.Flags{}).withDir("/etc/paisans", "drwxr-xr-x", 1000, 1000), "make /etc/paisans root's alone"},
	} {
		plan, err := hostadmit.Build(c.host, input(t, "vm", hostadmit.Flags{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := index(plan, c.title)
		if dir < 0 {
			t.Fatalf("%s: no %q step:\n%s", name, c.title, plan.Text())
		}
		for _, w := range []string{"write the egress table", "write the sysctls"} {
			if at := index(plan, w); at >= 0 && at < dir {
				t.Errorf("%s: %q (step %d) runs before %q (step %d)", name, w, at, c.title, dir)
			}
		}
	}
	plan, err := hostadmit.Build(admittedHost(requestFor("vm"), hostadmit.Flags{}), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("an admitted host plans steps:\n%s", plan.Text())
	}
}

// The same site planned from its request and from the configuration is
// the same plan, but for the digest the record keeps.
func TestRequestAndSitePlanTheSame(t *testing.T) {
	for _, site := range []string{"home-a", "home-b", "vm", "status", "watch", "store"} {
		flags := hostadmit.Flags{Watchdog: requestFor(site).Grants.Watchdog, PublicPorts: requestFor(site).Grants.PublicPorts}
		fromFile := input(t, site, flags)
		built, err := admission.Build(fixture(t), site)
		if err != nil {
			t.Fatal(err)
		}
		fromConfig := hostadmit.Input{Request: built, Flags: flags, Now: admitted}
		host := home
		if site == "home-b" {
			host = communityHome
		}
		a, errA := hostadmit.Build(host(), fromFile)
		b, errB := hostadmit.Build(host(), fromConfig)
		if errA != nil || errB != nil {
			t.Fatalf("%s: %v, %v", site, errA, errB)
		}
		if !strings.Contains(a.Text(), fromFile.Digest) {
			t.Fatalf("%s: the plan from the request does not carry its digest", site)
		}
		if strings.ReplaceAll(a.Text(), fromFile.Digest, "") != b.Text() {
			t.Errorf("%s: the plan from the request differs from the plan from the configuration:\n%s", site, diff(b.Text(), a.Text()))
		}
	}
}

// The check runs docker version as the user, with an environment of its
// own, on its own socket, and reports the Engine's version.
func TestCheckUser(t *testing.T) {
	h := home().answer("docker version", "29.9.1\n")
	v, err := hostadmit.CheckUser(h, requestFor("vm"))
	if err != nil || v != "29.9.1" {
		t.Fatalf("CheckUser = %q, %v", v, err)
	}
	ran := h.ran[len(h.ran)-1]
	for _, want := range []string{"runuser -u paisans-f2a9 -- env -i ", "DOCKER_HOST=unix:///run/user/$(id -u 'paisans-f2a9')/docker.sock", "docker version"} {
		if !strings.Contains(ran, want) {
			t.Errorf("the check does not run %q: %s", want, ran)
		}
	}
	if !strings.HasPrefix(ran, "runuser -u ") {
		t.Errorf("the check does not run as the user: %s", ran)
	}
}
