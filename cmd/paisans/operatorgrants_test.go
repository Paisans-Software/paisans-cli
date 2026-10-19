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
			"site home-a of " + neighbour + " (apps, data), as community, admitted 2026-10-15T00:00:00Z",
			"watchdog: yes (--watchdog)\n",
			"data directory: /srv/my data (--data-dir)\n",
			"mesh LAN: yes, 192.168.1.0/24 recorded; admit reads the LAN again from the host (--mesh-lan)\n",
			"ufw enabled by admit: yes (--enable-ufw)\n",
			"admit again: paisans site admit --site 'home-a' --watchdog --data-dir '/srv/my data' --bridge-netfilter --mesh-lan --enable-ufw\n",
			"site vm of " + monitorID + " (gateway), as paisans-f2a9, admitted 2026-10-15T00:00:00Z",
			"watchdog: no (--watchdog)\n",
			"public ports: yes (--public-ports)\n",
			"data directory: the user's home (no --data-dir)\n",
			"admit again: paisans site admit --site 'vm' --public-ports\n",
			"2 admission record(s) read",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("--ssh %s: no %q in:\n%s", ssh, want, out)
			}
		}
		if strings.Contains(out, "--public-ports --enable-ufw") {
			t.Errorf("--ssh %s: --enable-ufw for a site whose admit did not enable ufw:\n%s", ssh, out)
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

// A record with no grant or flag is the bare command, and a data directory
// is quoted for the shell whatever it holds.
func TestAdmitFlagsOfBareAndQuotedRecords(t *testing.T) {
	if got := admitFlags(admission.Record{Site: "x"}); len(got) != 0 {
		t.Errorf("bare record: %q", got)
	}
	got := strings.Join(admitFlags(admission.Record{DataDir: "/srv/o'brien/paisans-f2a9"}), " ")
	if want := `--data-dir '/srv/o'\''brien'`; got != want {
		t.Errorf("quoted: %s, want %s", got, want)
	}
}

// Every flag admitFlags prints is one site admit takes, and means what the
// record holds: site admit's own flag set reads them back.
func TestAdmitFlagsAreSiteAdmitFlags(t *testing.T) {
	rec := admission.Record{Grants: admission.Grants{Watchdog: true, PublicPorts: true}, DataDir: "/srv/data/paisans-f2a9",
		BridgeNetfilter: true, MeshLAN: "192.168.1.0/24", UFWEnabled: true}
	var args []string
	for _, a := range admitFlags(rec) {
		args = append(args, strings.Trim(a, "'"))
	}
	// --site with --request stops site admit once its flags are read, before
	// any host is reached: a flag it does not take stops it at the parse.
	err := runSiteAdmit(append(args, "--site", "vm", "--request", "x.admit"), strings.NewReader(""), new(strings.Builder))
	if err == nil || !strings.Contains(err.Error(), "--request or --site, not both") {
		t.Fatalf("site admit read %q as: %v", args, err)
	}
	if want := []string{"--watchdog", "--public-ports", "--data-dir", "/srv/data", "--bridge-netfilter", "--mesh-lan", "--enable-ufw"}; strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("flags %q, want %q", args, want)
	}
}

// A site admitted with no grant or flag lists each as no, and its web
// server where the configuration handed it one, rather than printing
// nothing.
func TestOperatorGrantsListsEveryGrantOfABareSite(t *testing.T) {
	rec := admission.Record{
		Version: admission.RecordVersion, Deployment: monitorID, User: "paisans-f2a9", Site: "saw-gerrera", Roles: []string{"monitor"},
		Ports: admission.Ports{SSH: 22, WireGuard: 51820}, AdmittedAt: "2026-10-18T02:17:18Z", MeshSubnet: "10.44.0.0/24",
		WebServer: &admission.WebServer{Server: "caddy", Container: "caddy", Config: "/opt/beszel/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic", AdminTCP: "localhost:2019"},
		Build:     version.Build{Version: "0.3.0-dev", Commit: "59ad52727c82242550899efd520b6007b56509ca"},
	}
	b, err := admission.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := admission.Path("f2a9")
	out, _, err := grantsHost(t, &listedHost{t: t, records: map[string]string{p: string(b)}}, "--ssh", "saw-gerrera")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"watchdog: no (--watchdog)", "public ports: no (--public-ports)", "data directory: the user's home (no --data-dir)",
		"bridge netfilter: no (--bridge-netfilter)", "mesh LAN: no", "ufw enabled by admit: no (--enable-ufw)",
		"web server: caddy, in container caddy, the owner's, from the configuration's web_server",
		"admit again: paisans site admit --site 'saw-gerrera'\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}
