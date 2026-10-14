package secretsgen_test

import (
	"encoding/pem"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/secretsgen"
)

func rotatable() *config.Secrets {
	return &config.Secrets{
		Cluster:  config.ClusterSecrets{SuperuserPassword: "old-superuser-0001", AdminPassword: "old-admin-0000001", StandbyPassword: "old-standby-000001"},
		Storage:  config.StorageSecrets{Garage: config.GarageSecrets{AdminToken: "old-token-000000001", RPCSecret: "old-rpc"}},
		External: map[string]string{"acme_dns_token": "old-acme-0000001"},
		Sites:    map[string]config.SiteSecrets{"vm": {WireGuardPrivateKey: "old-wg", HeartbeatToken: "old-heartbeat"}},
		Apps: map[string]map[string]any{
			"id":      {"encryption_key": "old-enc-00000000001", "static_api_key": "old-api-0000000001", "operators_heartbeat_token": "old-operators-heartbeat"},
			"mas":     {"mas_encryption_secret": "old-mas-enc-00001", "mas_signing_key": "old-pem", "mas_matrix_secret": "old-matrix-0001"},
			"mbin":    {"app_secret": "old-app-00000000001", "oauth_passphrase": "old-pass-0000000001", "oauth_encryption_key": "old-oek-0000000001", "oauth_private_key": "old-private"},
			"account": {"email_hash_key": "old-hash-000000001", "session_key": "old-session-0000001"},
		},
	}
}

// declared is a deployment whose apps' blueprints say how their secrets are
// made, as Replacement reads them through ReplacementIn.
var declared = &config.Config{Apps: map[string]config.App{
	"id": {Blueprint: config.BlueprintPocketID}, "mas": {Blueprint: "synapse"}, "mbin": {Blueprint: "mbin"}, "account": {Blueprint: config.BlueprintAccount},
}}

func replacement(path string, s *config.Secrets) (string, error) {
	return secretsgen.ReplacementIn(declared, path, s)
}

func TestReplacementShapes(t *testing.T) {
	s := rotatable()
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)

	rpc, err := replacement("storage.garage.rpc_secret", s)
	if err != nil || !hex64.MatchString(rpc) {
		t.Errorf("rpc_secret = %q, %v; want 64 hex", rpc, err)
	}
	hb, err := replacement("sites.vm.heartbeat_token", s)
	if err != nil || !hex32.MatchString(hb) {
		t.Errorf("heartbeat_token = %q, %v; want 32 hex", hb, err)
	}
	ahb, err := replacement("apps.id.operators_heartbeat_token", s)
	if err != nil || !hex32.MatchString(ahb) {
		t.Errorf("operators_heartbeat_token = %q, %v; want 32 hex", ahb, err)
	}
	key, err := replacement("apps.mas.mas_signing_key", s)
	if err != nil {
		t.Fatal(err)
	}
	if block, _ := pem.Decode([]byte(key)); block == nil || block.Type != "PRIVATE KEY" {
		t.Errorf("mas_signing_key is not a PRIVATE KEY PEM: %.40q", key)
	}
}

func TestReplacementPasswordsDiffer(t *testing.T) {
	s := rotatable()
	for _, path := range []string{
		"cluster.superuser_password", "cluster.admin_password", "cluster.standby_password",
		"storage.garage.admin_token", "apps.id.static_api_key", "apps.mbin.app_secret",
		"apps.mbin.oauth_passphrase", "apps.mbin.oauth_encryption_key", "apps.mas.mas_matrix_secret",
		"apps.account.session_key",
	} {
		a, err := replacement(path, s)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		b, err := replacement(path, s)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if a == b || len(a) < 32 {
			t.Errorf("%s: replacements %q and %q are not distinct generated passwords", path, a, b)
		}
	}
}

func TestReplacementRefusesWhatRotationBreaks(t *testing.T) {
	s := rotatable()
	for _, tc := range []struct{ path, want string }{
		{"apps.id.encryption_key", "unreadable"},
		{"apps.mas.mas_encryption_secret", "unreadable"},
		{"apps.account.email_hash_key", "forgets every declined address"},
		{"apps.mbin.oauth_private_key", "replaced with the rest of apps.mbin.oauth"},
		{"sites.vm.wireguard_private_key", "paisans mesh rekey"},
		{"apps.id.nothing", "no secret"},
		{"external.acme_dns_token", "no replacement"},
	} {
		got, err := replacement(tc.path, s)
		if err == nil || got != "" {
			t.Errorf("%s: got %q, %v; want a refusal", tc.path, got, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not say %q", tc.path, err, tc.want)
		}
	}
}

func TestReplacementKeypairOpensWithItsPassphrase(t *testing.T) {
	priv, pub, err := secretsgen.ReplacementKeypair("a-passphrase-for-the-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secretsgen.OpenPrivateKey(priv, "a-passphrase-for-the-test"); err != nil {
		t.Errorf("the private key does not open with its passphrase: %v", err)
	}
	if !strings.Contains(pub, "PUBLIC KEY") {
		t.Errorf("public key is not PEM: %.40q", pub)
	}
}
