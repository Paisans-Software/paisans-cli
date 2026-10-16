package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

const keysMarker = "paisans host admit: keys, read as the user"

var recordCases = addCases(
	goldenCase{name: "record-fresh", site: "vm", host: home},
	goldenCase{name: "record-mode-restored", site: "vm", host: func() *fakeHost {
		h := admittedHost(requestFor("vm"), hostadmit.Flags{})
		return h.withFile("/etc/paisans/admitted.f2a9.json", h.files["/etc/paisans/admitted.f2a9.json"], "600", 0)
	}},
	goldenCase{name: "record-etc-paisans-link-refused", site: "vm", host: func() *fakeHost { return home().withDir("/etc/paisans", "lrwxrwxrwx", 0, 0) }},
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
	plan, err := hostadmit.Build(recordCases[1].host(), input(t, "vm", hostadmit.Flags{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Text(), "write "+path+" mode 0644") {
		t.Errorf("a record found mode 600 is not rewritten 0644:\n%s", plan.Text())
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
