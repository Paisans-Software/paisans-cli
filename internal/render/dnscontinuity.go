package render

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// The DNS continuity check: the admin reconciler holds every Pocket ID
// administrator to being a Super Administrator of the Cloudflare account that
// holds the zone, and the admins heartbeat pusher passes its two answers to
// two more monitors. docs/specs/2026-10-13-dns-admin-continuity.md has the
// design. Rendered only for a deployment on Cloudflare that has pasted the
// members token: the check needs an account with members an API can list,
// and a credential that reads them and nothing else.

// dnsMembersToken is the pasted secret the reconciler reads the account's
// members with. It is not acme_dns_token, which edits the zone and lives
// only beside Caddy: this one is rendered into the Pocket ID stack on every
// apps site, and a leak there must not be control of every hostname.
const dnsMembersToken = "dns_members_token"

// dnsContinuityProvider is the one provider whose account has members an
// API lists.
const dnsContinuityProvider = "cloudflare"

// dnsValues is what a Pocket ID app's .env renders for the check.
type dnsValues struct {
	Enabled bool
	Token   string
	Domain  string
	HashKey string
	// Map is space separated <hash of a pocket id email>=<hash of the
	// address at the provider>, sorted, for each admin the secrets map.
	Map string
}

// dnsContinuity reports whether the deployment renders the check.
func (p *planner) dnsContinuity() bool {
	return p.cfg.ACME.Provider == dnsContinuityProvider && strings.TrimSpace(p.secrets.External[dnsMembersToken]) != ""
}

// dnsValuesFor is a Pocket ID app's check, or the zero value when the
// deployment renders none. It refuses a mapping that would let one
// membership satisfy two administrators, or that names no declared admin:
// validate reads no secrets, so the refusal is here, at render, as a
// heartbeat token's is.
func (p *planner) dnsValuesFor(app string) (dnsValues, error) {
	if !p.dnsContinuity() {
		return dnsValues{}, nil
	}
	key := p.appSecret(app, "dns_email_hash_key")
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) < 32 {
		return dnsValues{}, fmt.Errorf("secrets apps.%s.dns_email_hash_key: required, as 32 bytes of hex, for a deployment with external.%s. Run `paisans init` to generate it", app, dnsMembersToken)
	}
	if err := p.checkDNSMapping(); err != nil {
		return dnsValues{}, err
	}
	hash := func(address string) string {
		mac := hmac.New(sha256.New, raw)
		mac.Write([]byte(strings.ToLower(strings.TrimSpace(address))))
		return hex.EncodeToString(mac.Sum(nil))
	}
	var pairs []string
	for _, id := range sortedKeys(p.secrets.Admins) {
		to := p.secrets.Admins[id].DNSProviderEmail
		if strings.TrimSpace(to) == "" {
			continue
		}
		pairs = append(pairs, hash(p.cfg.Admins[id].Email)+"="+hash(to))
	}
	sort.Strings(pairs)
	return dnsValues{
		Enabled: true,
		Token:   p.secrets.External[dnsMembersToken],
		Domain:  p.cfg.Community.Domain,
		HashKey: key,
		Map:     strings.Join(pairs, " "),
	}, nil
}

// checkDNSMapping refuses an entry under the secrets' admins that names no
// admin paisans-stack.yaml declares, and an address that two admins would be
// expected at: two mapped to one address, or one mapped to another admin's
// email. Either lets one membership satisfy two administrators. Errors name
// the admins by key, never the address.
func (p *planner) checkDNSMapping() error {
	expected := map[string]string{} // address to the admin expected there
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	for _, id := range sortedKeys(p.secrets.Admins) {
		if _, ok := p.cfg.Admins[id]; !ok {
			return fmt.Errorf("secrets admins.%s: names no admin %s declares. Its dns_provider_email maps an admin there by key", id, config.FileName)
		}
	}
	for _, id := range sortedKeys(p.cfg.Admins) {
		address := p.cfg.Admins[id].Email
		if mapped := p.secrets.Admins[id].DNSProviderEmail; strings.TrimSpace(mapped) != "" {
			address = mapped
		}
		if other, ok := expected[norm(address)]; ok && norm(address) != "" {
			return fmt.Errorf("secrets admins: %s and %s would be expected at the same address at the DNS provider, so one membership would count for both. Each admin's dns_provider_email, or email when it has none, must be their own", other, id)
		}
		expected[norm(address)] = id
	}
	return nil
}

// continuityName is the name of one of a Pocket ID app's continuity
// monitors: the admins heartbeat when function is empty, else one of the
// DNS check's. They are named for the community rather than the app, since
// every instance of the app pushes to the one token; only a deployment with
// more than one Pocket ID app prefixes the app key, because the seed tells
// monitors apart by name.
func (p *planner) continuityName(app, function string) string {
	name := "admin continuity"
	if function != "" {
		name += ": " + function
	}
	n := 0
	for _, a := range p.cfg.AppNames() {
		if p.cfg.Apps[a].Kind == config.KindPocketID {
			n++
		}
	}
	if n > 1 {
		name = app + ": " + name
	}
	return name
}

// dnsMonitor is one of the DNS check's heartbeats: the function in its
// monitor's name, the secret its token is, the reconciler path it reads and
// the pusher's variable its URLs are in.
type dnsMonitor struct {
	Function, Secret, Path, Var string
}

var dnsMonitors = []dnsMonitor{
	{Function: "dns super admins", Secret: "dns_super_admins_heartbeat_token", Path: "/healthz/dns-super-admins", Var: "DNS_SUPER_ADMINS_HEARTBEAT_URLS"},
	{Function: "dns 2fa", Secret: "dns_2fa_heartbeat_token", Path: "/healthz/dns-2fa", Var: "DNS_2FA_HEARTBEAT_URLS"},
}
