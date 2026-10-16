package hostadmit_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostadmit"
)

// other is the record of a second deployment on the host, 0c0d, with the
// grants and forwarding given. The fixture always encodes, so an error is
// a broken test and panics.
func other(public, forwarding bool) string {
	rec := admission.Record{Version: admission.RecordVersion, Deployment: "0c0dc4e1-0b7d-4c3a-9e2f-5a6b7c8d9e01", User: "paisans-0c0d",
		Grants: admission.Grants{PublicPorts: public}, Forwarding: forwarding,
		Ports: admission.Ports{SSH: 22, WireGuard: 51821, Public: []int{}}, AdmittedAt: "2026-10-01T09:00:00Z"}
	if public {
		rec.Ports.Public = []int{80, 443}
	}
	data, err := admission.EncodeRecord(rec)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// withOther is h with deployment 0c0d admitted too.
func withOther(h *fakeHost, public, forwarding bool) *fakeHost {
	return h.add("record", "/etc/paisans/admitted.0c0d.json").file("/etc/paisans/admitted.0c0d.json", other(public, forwarding))
}

var sysctlCases = addCases(
	goldenCase{name: "sysctl-endpoint-and-low-ports", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: home},
	goldenCase{name: "sysctl-no-endpoint", site: "vm", host: home},
	goldenCase{name: "sysctl-forwarding-on-already", site: "watch", flags: hostadmit.Flags{PublicPorts: true}, host: func() *fakeHost { return home().with("ip-forward", "1") }},
	goldenCase{name: "sysctl-shared-with-another", site: "vm", host: func() *fakeHost {
		return withOther(home(), true, true).with("port-start", "80").with("ip-forward", "1").
			withFile("/etc/sysctl.d/60-paisans.conf", hostadmit.SysctlFile(true, true), "644", 0)
	}},
)

func TestSysctlPlans(t *testing.T) { runGolden(t, sysctlCases) }

// The low ports are announced as what they are: every local user may bind
// them; forwarding as routing between every interface.
func TestSysctlWarningsSayWhatTheyGrant(t *testing.T) {
	plan, err := hostadmit.Build(home(), input(t, "watch", hostadmit.Flags{PublicPorts: true}))
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Text()
	for _, want := range []string{"every local user", "ports 80 to 1023", "routing between every interface"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan does not say %q", want)
		}
	}
}

// A before file is read as root and its values reach a command line, so one
// that holds anything but digits under a known key is refused.
func TestSysctlBeforeFileMustHoldNumbers(t *testing.T) {
	h := home().file("/etc/paisans/sysctl.before", "net.ipv4.ip_forward = 0; reboot\n")
	_, err := hostadmit.Build(h, input(t, "vm", hostadmit.Flags{}))
	if err == nil || !strings.Contains(err.Error(), "not a value admit wrote") {
		t.Fatalf("a before file with a command in it was accepted: %v", err)
	}
}
