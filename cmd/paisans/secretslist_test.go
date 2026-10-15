package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// listRows runs secrets list and returns its table, keyed by path, each row
// its fields after the path, and the report beside it.
func listRows(t *testing.T, configPath, secretsPath string) (map[string][]string, string, string) {
	t.Helper()
	var report bytes.Buffer
	reporterOverride = ui.NewPlain(&report, false)
	t.Cleanup(func() { reporterOverride = nil })
	var table bytes.Buffer
	if err := runSecretsList([]string{"--config", configPath, "--secrets", secretsPath}, &table); err != nil {
		t.Fatal(err)
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(table.String()), "\n")[1:] {
		f := strings.Fields(line)
		rows[f[0]] = f[1:]
	}
	return rows, table.String(), report.String()
}

// Every secret the configuration needs is listed by path with whether it is
// set and where it comes from, and no value reaches either stream: not the
// value, and not a part of one.
func TestSecretsListPrintsPathsAndStateNeverValues(t *testing.T) {
	const (
		dnsToken   = "list-value-dns-0001"
		standby    = "list-value-standby-0002"
		extra      = "list-value-extra-0003"
		goneKey    = "list-value-gone-0004"
		clientSec  = "list-value-client-0005"
		heartbeat  = "list-value-heartbeat-0006"
		wireguard  = "list-value-wireguard-0007"
		valkeyPass = "list-value-valkey-0008"
	)
	configPath, secretsPath := secretsDir(t, `version: 2
cluster:
  standby_password: `+standby+`
external:
  acme_dns_token: `+dnsToken+`
  extra_token: `+extra+`
sites:
  home-a:
    heartbeat_token: `+heartbeat+`
    wireguard_private_key: `+wireguard+`
  gone-site:
    wireguard_private_key: `+goneKey+`
apps:
  talk:
    valkey_password: `+valkeyPass+`
oidc_clients:
  wiki:
    client_id: wiki-client
    client_secret: `+clientSec+`
`)
	rows, table, report := listRows(t, configPath, secretsPath)

	for _, value := range []string{dnsToken, standby, extra, goneKey, clientSec, heartbeat, wireguard, valkeyPass, "wiki-client", "list-value"} {
		if strings.Contains(table+report, value) {
			t.Errorf("secrets list printed a value (%s):\n%s\n%s", value, table, report)
		}
	}

	for path, want := range map[string]string{
		"external.acme_dns_token":                   "set pasted",
		"external.smtp_password":                    "unset pasted",
		"external.extra_token":                      "set pasted",
		"cluster.standby_password":                  "set generated",
		"cluster.admin_password":                    "unset generated",
		"sites.home-a.heartbeat_token":              "set generated",
		"apps.talk.valkey_password":                 "set generated",
		"oidc_clients.wiki.client_secret":           "set captured",
		"oidc_clients.talk.client_id":               "unset captured",
		"backup.destinations.offsite.access_key_id": "unset pasted",
	} {
		row, ok := rows[path]
		if !ok {
			t.Errorf("%s is not listed:\n%s", path, table)
			continue
		}
		if got := strings.Join(row[:2], " "); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}

	// A path naming something the configuration no longer declares is
	// counted and left to secrets prune, not listed.
	if _, ok := rows["sites.gone-site.wireguard_private_key"]; ok {
		t.Errorf("an orphan is listed:\n%s", table)
	}
	if !strings.Contains(report, "1 more path(s)") || !strings.Contains(report, "paisans secrets prune") {
		t.Errorf("the orphan is not counted:\n%s", report)
	}
}

// The last column is what secrets rotate takes for each set secret: its own
// path, a unit's for a secret rotated with its unit, kept or mesh rekey for
// one rotate leaves alone, and nothing for an unset one.
func TestSecretsListSaysHowRotateTakesEachSecret(t *testing.T) {
	configPath, secretsPath := secretsDir(t, `version: 2
cluster:
  standby_password: a
sites:
  home-a:
    heartbeat_token: b
    wireguard_private_key: c
backup:
  restic_password: d
apps:
  talk:
    oauth_passphrase: e
    oauth_private_key: f
    oauth_public_key: g
oidc_clients:
  wiki:
    client_id: h
    client_secret: i
`)
	rows, table, _ := listRows(t, configPath, secretsPath)
	for path, want := range map[string]string{
		"cluster.standby_password":           "cluster.standby_password",
		"cluster.admin_password":             "-",
		"sites.home-a.heartbeat_token":       "sites.home-a.heartbeat_token",
		"sites.home-a.wireguard_private_key": "mesh",
		"backup.restic_password":             "kept",
		"oidc_clients.wiki.client_secret":    "oidc_clients.wiki.client_secret",
		"oidc_clients.wiki.client_id":        "-",
		"apps.talk.oauth_private_key":        "apps.talk.oauth",
		"apps.talk.oauth_public_key":         "apps.talk.oauth",
	} {
		row, ok := rows[path]
		if !ok {
			t.Errorf("%s is not listed:\n%s", path, table)
			continue
		}
		if row[2] != want {
			t.Errorf("%s rotates as %q, want %q", path, row[2], want)
		}
	}
}

func TestSecretsListTakesNoArgument(t *testing.T) {
	configPath, secretsPath := secretsDir(t, "version: 2\n")
	err := runSecretsList([]string{"cluster.standby_password", "--config", configPath, "--secrets", secretsPath}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "takes no argument") {
		t.Fatalf("got %v", err)
	}
}
