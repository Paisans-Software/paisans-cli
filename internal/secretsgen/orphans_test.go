package secretsgen_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployrecord"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
)

func TestOrphans(t *testing.T) {
	cfg := &config.Config{
		Sites: map[string]config.Site{"home-a": {}},
		Apps: map[string]config.App{
			"talk":  {Blueprint: config.Blueprint("mbin")},
			"auth":  {Blueprint: config.BlueprintPocketID, Settings: map[string]any{"signup_default_groups": []any{"provisional"}}},
			"auth2": {Blueprint: config.BlueprintPocketID, Settings: map[string]any{"signup_default_groups": []any{"members"}}},
		},
	}
	s := &config.Secrets{
		Sites:          map[string]config.SiteSecrets{"home-a": {}, "monitor-a": {WireGuardPrivateKey: "x"}},
		Apps:           map[string]map[string]any{"talk": {}, "uptime": {"database_password": "x"}},
		OIDCClients:    map[string]config.OIDCClient{"talk": {}, "uptime": {ClientID: "c-1d2e", ClientSecret: "s3cr3t"}},
		PocketIDGroups: map[string]string{"provisional": "1", "members": "2", "old": "3"},
	}
	var keys []string
	leaves := 0
	for _, o := range secretsgen.Orphans(cfg, s, nil) {
		keys = append(keys, o.Key)
		if o.Leaves != "" {
			leaves++
		}
		for _, value := range []string{"x", "c-1d2e", "s3cr3t"} {
			if strings.Contains(o.Why+" "+o.Leaves, value+" ") {
				t.Errorf("%s reports a value: %q %q", o.Key, o.Why, o.Leaves)
			}
		}
	}
	want := []string{"apps.uptime", "oidc_clients.uptime", "pocket_id_groups.old", "sites.monitor-a"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", keys, want)
	}
	if leaves != 1 {
		t.Errorf("%d orphan(s) say they leave something, want only the OIDC client", leaves)
	}

	secretsgen.Prune(s, secretsgen.Orphans(cfg, s, nil))
	if len(secretsgen.Orphans(cfg, s, nil)) != 0 {
		t.Error("orphans left after Prune")
	}
	if _, ok := s.Sites["home-a"]; !ok {
		t.Error("Prune removed a declared site's secrets")
	}
	if s.PocketIDGroups["members"] != "2" {
		t.Error("Prune removed a group the second Pocket ID app names")
	}
}

func TestNoOrphansWhenTheSecretsMatch(t *testing.T) {
	cfg := &config.Config{Sites: map[string]config.Site{"home-a": {}}, Apps: map[string]config.App{"talk": {Blueprint: config.Blueprint("mbin")}}}
	s := &config.Secrets{
		Sites:       map[string]config.SiteSecrets{"home-a": {}},
		Apps:        map[string]map[string]any{"talk": {}},
		OIDCClients: map[string]config.OIDCClient{"talk": {}},
	}
	if o := secretsgen.Orphans(cfg, s, nil); len(o) != 0 {
		t.Errorf("got %v", o)
	}
}

func TestOrphansKeepWhatAnyRecordLists(t *testing.T) {
	cfg := &config.Config{Sites: map[string]config.Site{"home-a": {}}, Apps: map[string]config.App{}}
	s := &config.Secrets{
		Sites:          map[string]config.SiteSecrets{"home-a": {}, "monitor-a": {}, "monitor-b": {}},
		Apps:           map[string]map[string]any{"uptime": {}},
		OIDCClients:    map[string]config.OIDCClient{"uptime": {}},
		PocketIDGroups: map[string]string{"old": "1"},
	}
	one := deployrecord.Record{Sites: []string{"home-a", "monitor-a"}, Apps: []string{"uptime"}}
	two := deployrecord.Record{Sites: []string{"home-a"}, PocketIDGroups: []string{"old"}}
	u := deployrecord.Record{Sites: append(one.Sites, two.Sites...), Apps: append(one.Apps, two.Apps...), PocketIDGroups: append(one.PocketIDGroups, two.PocketIDGroups...)}
	var keys []string
	for _, o := range secretsgen.Orphans(cfg, s, &u) {
		keys = append(keys, o.Key)
	}
	if strings.Join(keys, ",") != "sites.monitor-b" {
		t.Errorf("got %v, want only sites.monitor-b", keys)
	}
	if n := len(secretsgen.Orphans(cfg, s, nil)); n != 5 {
		t.Errorf("without a record: %d orphans, want 5", n)
	}
}

func TestDroppedIsRegardlessOfSecrets(t *testing.T) {
	cfg := &config.Config{Sites: map[string]config.Site{"home-a": {}}, Apps: map[string]config.App{}}
	rec := deployrecord.Record{Sites: []string{"home-a", "monitor-a"}, Apps: []string{"uptime"}, PocketIDGroups: []string{"old"}}
	var keys []string
	for _, o := range secretsgen.Dropped(cfg, &rec) {
		keys = append(keys, o.Key)
		if !strings.Contains(o.Why, "still deployed") && !strings.Contains(o.Why, "is deployed") {
			t.Errorf("%s: %q", o.Key, o.Why)
		}
		if o.Leaves == "" {
			t.Errorf("%s says nothing about what to do", o.Key)
		}
	}
	if strings.Join(keys, ",") != "apps.uptime,pocket_id_groups.old,sites.monitor-a" {
		t.Errorf("got %v", keys)
	}
	if len(secretsgen.Dropped(cfg, nil)) != 0 {
		t.Error("no record dropped something")
	}
}

// A destination renamed or taken out of paisans-stack.yaml leaves its credentials
// as an orphan, which prune removes. The generated keys never are: they
// decrypt every backup already taken.
func TestARemovedDestinationsCredentialsAreOrphans(t *testing.T) {
	cfg := load(t) // destinations offsite and home
	secrets := &config.Secrets{Backup: config.BackupSecrets{
		ResticPassword:   "r",
		WALGLibsodiumKey: "w",
		Destinations: map[string]config.DestinationSecrets{
			"offsite": {AccessKeyID: "a", SecretAccessKey: "b"},
			"old-b2":  {AccessKeyID: "AKIAOLDKEY", SecretAccessKey: "oldsecret0"},
		},
	}}
	orphans := secretsgen.Orphans(cfg, secrets, nil)
	var keys []string
	for _, o := range orphans {
		keys = append(keys, o.Key)
		if strings.Contains(o.Why+o.Leaves, "AKIAOLDKEY") || strings.Contains(o.Why+o.Leaves, "oldsecret0") {
			t.Errorf("%s reports a value", o.Key)
		}
	}
	if got := strings.Join(keys, " "); !strings.Contains(got, "backup.destinations.old-b2") || strings.Contains(got, "offsite") || strings.Contains(got, "restic") || strings.Contains(got, "walg") {
		t.Fatalf("orphans = %s", got)
	}
	secretsgen.Prune(secrets, orphans)
	if _, ok := secrets.Backup.Destinations["old-b2"]; ok {
		t.Error("prune kept old-b2's credentials")
	}
	if _, ok := secrets.Backup.Destinations["offsite"]; !ok || secrets.Backup.ResticPassword == "" {
		t.Error("prune removed what is still declared")
	}

	// With the section gone, every destination's credentials are orphans,
	// and the keys still are not.
	cfg.Backup = nil
	keys = nil
	for _, o := range secretsgen.Orphans(cfg, secrets, nil) {
		keys = append(keys, o.Key)
	}
	if got := strings.Join(keys, " "); !strings.Contains(got, "backup.destinations.offsite") || strings.Contains(got, "restic") {
		t.Errorf("orphans with no backup section = %s", got)
	}
}
