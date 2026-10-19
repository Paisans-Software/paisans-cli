package main

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// --ssh takes user@host[:port], given to ssh as admit's commands give it,
// or an alias of the operator's ssh config, given as it is; anything ssh
// could read as an option, or that is neither, is refused.
func TestGrantsDestination(t *testing.T) {
	for in, want := range map[string]string{
		"admin@203.0.113.9":      "admin@203.0.113.9",
		"admin@203.0.113.9:2222": "ssh://admin@203.0.113.9:2222",
		"home-a":                 "home-a",
		"vm.example.org":         "vm.example.org",
	} {
		if got, err := grantsDestination(in); err != nil || got != want {
			t.Errorf("grantsDestination(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-oProxyCommand=x", "home a", "@host", "admin@", "admin@host:99999", "a;b", ""} {
		if got, err := grantsDestination(bad); err == nil {
			t.Errorf("grantsDestination(%q) = %q, not refused", bad, got)
		}
	}
}

// grantsHost runs operator grants against h, reached however --ssh named
// it, and returns what it printed and the destination it was given.
func grantsHost(t *testing.T, h apply.Transport, args ...string) (string, string, error) {
	t.Helper()
	var reached string
	saved := reachGrantsHost
	reachGrantsHost = func(dest string) apply.Transport { reached = dest; return h }
	t.Cleanup(func() { reachGrantsHost = saved })
	out := plainOutput(t)
	err := runOperatorGrants(args)
	return out.String(), reached, err
}

// Each record is the site admit command that admitted it, read without
// sudo through the operator's own ssh, alias or user@host alike.
func TestOperatorGrantsPrintsTheAdmitFlags(t *testing.T) {
	records := admittedRecords(t)
	rec := admission.Record{
		Version: admission.RecordVersion, Deployment: neighbour, User: "community", Site: "home-a", Roles: []string{"apps", "data"},
		Grants: admission.Grants{Watchdog: true}, Ports: admission.Ports{SSH: 22, WireGuard: 51820},
		AdmittedAt: "2026-10-15T00:00:00Z", MeshSubnet: "10.45.0.0/24", DataDir: "/srv/my data/paisans-0c1d",
		BridgeNetfilter: true, MeshLAN: "192.168.1.0/24", UFWEnabled: true,
		Build: version.Build{Version: "0.3.0", Commit: "519299d5cb009da4904cdf53c2025f1ad609f102"},
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := admission.Path("0c1d")
	records[p] = string(b)
	for _, ssh := range []string{"home-a", "owner@vm.example.org"} {
		h := &listedHost{t: t, records: records}
		out, reached, err := grantsHost(t, h, "--ssh", ssh)
		if err != nil {
			t.Fatal(err)
		}
		if reached != ssh {
			t.Errorf("--ssh %s reached %q", ssh, reached)
		}
		for _, want := range []string{
			"site home-a of " + neighbour + " (apps, data), as community: paisans site admit --site 'home-a' --watchdog --data-dir '/srv/my data' --bridge-netfilter --mesh-lan",
			"site vm of " + monitorID + " (gateway), as paisans-f2a9: paisans site admit --site 'vm' --public-ports",
			"2 admission record(s) read",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("--ssh %s: no %q in:\n%s", ssh, want, out)
			}
		}
		if strings.Contains(out, "--enable-ufw") {
			t.Errorf("--ssh %s: --enable-ufw for ufw that is on:\n%s", ssh, out)
		}
		if len(h.commands) != 1 || h.commands[0] != hostDeploymentsAdmitted || h.registryRead {
			t.Errorf("--ssh %s: ran %q, read the registry %v", ssh, h.commands, h.registryRead)
		}
	}
}

// A host with no record says so; a record that does not parse is a
// warning, and the rest is listed.
func TestOperatorGrantsOnAnEmptyHostAndABadRecord(t *testing.T) {
	out, _, err := grantsHost(t, &listedHost{t: t}, "--ssh", "home-a")
	if err != nil || !strings.Contains(out, "No admission record in /etc/paisans") {
		t.Errorf("empty host: %v\n%s", err, out)
	}
	records := admittedRecords(t)
	records["/etc/paisans/admitted.0c1d.json"] = "{}"
	out, _, err = grantsHost(t, &listedHost{t: t, records: records}, "--ssh", "home-a")
	if err != nil || !strings.Contains(out, "WARN /etc/paisans/admitted.0c1d.json:") || !strings.Contains(out, "--site 'vm' --public-ports") || !strings.Contains(out, "1 admission record(s) read") {
		t.Errorf("bad record: %v\n%s", err, out)
	}
}

// --ssh is required, and refused when it is not a destination, before any
// host is reached.
func TestOperatorGrantsRefusals(t *testing.T) {
	for _, args := range [][]string{{}, {"--ssh", "-oProxyCommand=x"}, {"--ssh", "home-a", "extra"}} {
		out, reached, err := grantsHost(t, &listedHost{t: t}, args...)
		if err == nil || reached != "" {
			t.Errorf("%q: %v, reached %q\n%s", args, err, reached, out)
		}
	}
	if err := runOperator([]string{"grants", "--ssh", ""}, nil, nil); err == nil || !strings.Contains(err.Error(), "name the host with --ssh") {
		t.Errorf("operator grants without a host: %v", err)
	}
}

// The host is reached as --ssh names it, through the operator's own ssh,
// with no sudo and nothing of a deployment's user.
func TestGrantsTransportIsPlainSSH(t *testing.T) {
	for _, dest := range []string{"home-a", "ssh://admin@203.0.113.9:2222"} {
		tr := grantsTransport(dest)
		if tr.Destination != dest || tr.Sudo || tr.Auth != nil || tr.Login != nil || tr.User != "" || tr.Host != "" {
			t.Errorf("%s: %+v", dest, tr)
		}
	}
}
