// Package validate applies the design's policy to a loaded configuration.
//
// The distinction between refusing and warning is load bearing and is not an
// implementation detail. A refusal means the configuration is incoherent: it
// describes something that cannot work, and rendering it would produce
// artifacts that damage a deployment. A warning means the configuration is
// legitimate but risky, and the risk is acceptable when it is chosen rather
// than stumbled into.
//
// Every rule here traces to a rule in README.md. Nothing is invented.
package validate

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/paisans-software/pocketid-go"

	"github.com/paisans-software/paisans-cli/internal/acme"
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
)

// Level says how much a finding costs.
type Level int

const (
	// Warn is legitimate but risky. Rendering proceeds.
	Warn Level = iota
	// Refuse is incoherent. Rendering must fail.
	Refuse
)

func (l Level) String() string {
	if l == Refuse {
		return "REFUSE"
	}
	return "WARN"
}

// Finding is one problem, named so it can be looked up in README.md.
type Finding struct {
	Level Level
	// Rule is a stable identifier, quoted in tests and in verbose output.
	Rule string
	// Key is the configuration key the finding is about.
	Key string
	// Hint is one line saying what is wrong, in an operator's words. It is
	// what a warning shows by default.
	Hint string
	// Message says why it matters and what to do instead.
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s  %s\n  %s: %s", f.Level, f.Rule, f.Key, f.Message)
}

// Result is every finding for one configuration, refusals first and each group
// sorted, so that two runs over the same file print the same thing.
type Result struct {
	Findings []Finding
}

// Refused reports whether anything in the result blocks rendering.
func (r Result) Refused() bool {
	for _, f := range r.Findings {
		if f.Level == Refuse {
			return true
		}
	}
	return false
}

// Refusals returns only the findings that block rendering.
func (r Result) Refusals() []Finding { return r.byLevel(Refuse) }

// Warnings returns only the findings that do not block rendering.
func (r Result) Warnings() []Finding { return r.byLevel(Warn) }

func (r Result) byLevel(l Level) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Level == l {
			out = append(out, f)
		}
	}
	return out
}

// Has reports whether a rule fired. Tests use it.
func (r Result) Has(rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

type checker struct {
	cfg      *config.Config
	findings []Finding
}

func (c *checker) refuse(rule, key, hint, format string, args ...any) {
	c.findings = append(c.findings, Finding{Refuse, rule, key, hint, fmt.Sprintf(format, args...)})
}

func (c *checker) warn(rule, key, hint, format string, args ...any) {
	c.findings = append(c.findings, Finding{Warn, rule, key, hint, fmt.Sprintf(format, args...)})
}

// Check applies every rule and returns all of them. It never stops at the
// first problem: fixing a configuration one error per run is miserable, and an
// operator wants the whole list.
func Check(cfg *config.Config) Result {
	c := &checker{cfg: cfg}

	c.witnessSharesFailureDomain()
	c.twoVoters()
	c.votersShareARelay()
	c.dataSiteNotInCluster()
	c.undeclaredSites()
	c.placementShape()
	c.dashboardLinkIsAPath()
	c.consentIsABoolean()
	c.clusterSiteWithoutData()
	c.dataSitesShareAnArch()
	c.clusterAppWithoutAppsSite()
	c.garageReplicationExceedsSites()
	c.garageConsistency()
	c.storageRoleWithoutGarage()
	c.garageCapacities()
	c.garageAvailability()
	c.siteOutsideMesh()
	c.duplicateSiteAddress()
	c.sitesShareAMachine()
	c.imageServices()
	c.floatingImages()
	c.infraImages()
	c.clusterPlacementWithoutACluster()
	c.acmeProviderHasAnImage()
	c.acmeImageIsNotStockCaddy()
	c.latestACMEImage()
	c.hostnameRoles()
	c.duplicateHostname()
	c.gatedOnUngateableBlueprint()
	c.gatedWithoutSignedFetch()
	c.autoLoginWithoutARule()
	c.usesGateOutsideTheDomain()
	c.adminGroupNotAdmins()
	c.oidcMemberGroupNotAName()
	c.oidcMemberGroupDisagreesWithGate()
	c.oidcClientUnrestricted()
	c.displayNameInvalid()
	c.duplicateClientName()
	c.displayNameWithoutAClient()
	c.monitorRoles()
	c.webServer()
	c.smtpOnABlueprintWithoutMail()
	c.smtpPlaintext()
	c.smtpPlaintextWithUsername()
	c.pocketIDSettingInvalid()
	c.pocketIDAccountEditOn()
	c.pocketIDConfigKeyHasASetting()
	c.pocketIDEmailWithoutSMTP()
	c.pocketIDOperatorUsername()
	c.pocketIDManagement()
	c.operatorsFewerThanTwo()
	c.pocketIDSMTPSender()
	c.storage()
	c.backup()
	c.mediaHostnameShape()
	c.mediaHostnameUnderAnAppHostname()
	c.mailDomains()
	c.configKeyIsNestedInAnEnvFile()
	c.configKeyLooksLikeASecret()
	c.configKeySteersCompose()
	c.configKeyAlreadyRendered()
	c.portCollision()
	c.accountMustBeClustered()
	c.accountSignup()
	c.accountIdentityProvider()
	c.accountWithoutSMTP()
	c.accountCodeOfConduct()
	c.accountSettingInvalid()
	c.accountSettingUnknown()

	c.evenVoters()
	c.oneVoterNoFailover()
	c.asyncAutomaticFailover()
	c.pinnedOntoWitness()
	c.gatewayOnDataSite()
	c.pocketIDFileBackend()
	c.uptimeWithoutSMTP()
	c.pocketIDSettingUnknown()
	c.pocketIDSMTPFromName()
	c.nothingWatches()
	c.blueprintRules()
	c.pocketIDStandbyMarkerUnknown()
	c.imageForAbsentPostgres()
	c.publicAddress()

	sort.SliceStable(c.findings, func(i, j int) bool {
		if c.findings[i].Level != c.findings[j].Level {
			return c.findings[i].Level > c.findings[j].Level
		}
		if c.findings[i].Rule != c.findings[j].Rule {
			return c.findings[i].Rule < c.findings[j].Rule
		}
		return c.findings[i].Key < c.findings[j].Key
	})
	return Result{Findings: c.findings}
}

// witnessSharesFailureDomain refuses a witness beside a voter.
//
// One site with one etcd member has quorum 1: it works, and the only thing
// that stops it is the machine itself, which would stop Postgres anyway. Add a
// witness on that same machine and quorum becomes 2 of 2, so the witness
// container crashing takes the database down while Postgres is healthy. A
// tiebreaker between two things that are always gone at once breaks no ties.
func (c *checker) witnessSharesFailureDomain() {
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if site.Has(config.RoleWitness) && site.Has(config.RoleData) {
			c.refuse("witness-shares-failure-domain", fmt.Sprintf("sites.%s.roles", name),
				"witness shares a failure domain with its only voter",
				"the witness role is on a site that also holds data. Quorum becomes 2 of 2, so losing the witness takes down a healthy database, and losing the machine loses both members at once. Move the witness to a location that fails independently, or drop it: one site correctly runs exactly one etcd member.",
			)
		}
	}
}

// twoVoters refuses a two member etcd cluster.
//
// Quorum is a majority: 1 member survives nothing but works, 3 survive one
// loss, and 2 survive nothing while requiring both. Two voters are strictly
// worse than one, so a join goes from one to three in a single operation and
// never rests at two.
//
// Either way the first way out is a witness: a site with the witness role in a
// third location, which makes three voters and gives automatic failover. The
// toolkit never demands one, so declaring one voter stays the other way out.
// A derived pair came from the roles, so the advice is in their terms: with
// one data site the witness has no tie to break and its role comes off.
func (c *checker) twoVoters() {
	const witness = "Add a witness: a site in a third location, one that fails independently of both, with the witness role (Eg: `roles: [witness]`)"
	const failover = ", which makes three voters and gives automatic failover."
	members := c.cfg.Etcd.Members
	if len(members) != 2 {
		return
	}
	if !c.cfg.Etcd.MembersDerived {
		c.refuse("two-etcd-voters", "etcd.members",
			"etcd has exactly two voters",
			"exactly two etcd voters (%s and %s). Two voters are strictly worse than one: a majority of two is two, so either failing stops the cluster. %s Or declare one member, which gives up automatic failover.",
			members[0], members[1], witness+" and listed in etcd.members"+failover)
		return
	}
	var data, witnesses []string
	for _, name := range members {
		if c.cfg.Sites[name].Has(config.RoleData) {
			data = append(data, name)
		} else {
			witnesses = append(witnesses, name)
		}
	}
	one := members[0]
	if len(data) > 0 {
		one = data[0]
	}
	fix := fmt.Sprintf("%s Or write `etcd.members: [%s]` to run one voter, which gives up automatic failover.", witness+failover, one)
	if len(data) == 1 && len(witnesses) == 1 {
		fix = fmt.Sprintf("With one data site the witness has no tie to break: take the witness role off %s, and %s is the one voter.", witnesses[0], data[0])
	}
	c.refuse("two-etcd-voters", c.cfg.EtcdMembersKey(),
		"etcd has exactly two voters",
		"is left out, so the voters come from the roles, every witness and then every data site, and here that is exactly two (%s and %s). Two voters are strictly worse than one: a majority of two is two, so either failing stops the cluster. %s",
		members[0], members[1], fix)
}

// votersVerb is how a finding says the file gives its voters.
func (c *checker) votersVerb() string {
	if c.cfg.Etcd.MembersDerived {
		return "has"
	}
	return "declares"
}

// votersShareARelay refuses a mesh in which losing one site cuts the voters
// that would otherwise hold quorum off from each other.
//
// Two sites that are never dialled (config.Site.DialEndpoint: no
// public_address and no endpoint) never peer directly
// (config.Site.PeersDirectly): their traffic relays through the first dialled
// site, and that path has no fallback. A dialled voter reaches every other
// voter, so the voters that survive losing any one site still reach each
// other exactly when at least one of them is dialled, which with three or
// more voters means at least two of them are. With fewer, there is a site whose loss
// leaves every surviving voter alone, 1 of n and no majority: the database
// goes read-only on every site, even when the lost site held no data and
// was "only" the relay. Two voters are refused on their own, and one has
// nobody to be partitioned from.
func (c *checker) votersShareARelay() {
	var voters, dialled []string
	for _, name := range c.cfg.Etcd.Members {
		site, ok := c.cfg.Sites[name]
		if !ok {
			continue // undeclaredSites reports this
		}
		voters = append(voters, name)
		if site.DialEndpoint() != "" {
			dialled = append(dialled, name)
		}
	}
	sort.Strings(voters)
	sort.Strings(dialled)
	if len(voters) < 3 || len(dialled) >= 2 {
		return
	}
	var relays []string
	for _, name := range c.cfg.SiteNames() {
		if c.cfg.Sites[name].DialEndpoint() != "" {
			relays = append(relays, name)
		}
	}
	stranded := without(voters, dialled)
	fix := fmt.Sprintf("Give a second voter a public_address, or a stable endpoint (sites.<name>.endpoint; for a home connection a dynamic DNS name and a forwarded UDP port is enough), so that the voters surviving any one loss dial each other directly. Here that is any of %s.", strings.Join(stranded, ", "))
	switch {
	case len(dialled) == 1:
		c.refuse("voters-share-a-relay", c.cfg.EtcdMembersKey(),
			"only one etcd voter is dialled",
			"%s %d voters, and only %s is dialled, by a public_address or an endpoint. %s are not, so they reach each other only through %s, and losing %s leaves each of them alone, 1 of %d and no majority: the database goes read-only on every site. %s",
			c.votersVerb(), len(voters), dialled[0], strings.Join(stranded, " and "), dialled[0], dialled[0], len(voters), fix)
	case len(relays) > 0:
		c.refuse("voters-share-a-relay", c.cfg.EtcdMembersKey(),
			"no etcd voter is dialled, so they peer through a relay",
			"%s %d voters and none of them is dialled, having neither a public_address nor an endpoint, so they reach each other only through %s, and losing %s leaves every voter alone, 1 of %d and no majority: the database goes read-only on every site. %s",
			c.votersVerb(), len(voters), relays[0], relays[0], len(voters), fix)
	default:
		c.refuse("voters-share-a-relay", c.cfg.EtcdMembersKey(),
			"no etcd voter is dialled and no relay exists",
			"%s %d voters and no site is dialled, having neither a public_address nor an endpoint, so no two of them can ever peer: WireGuard needs one side to know where to send the first packet. %s",
			c.votersVerb(), len(voters), fix)
	}
}

// without returns the names in all that are not in some, in all's order.
func without(all, some []string) []string {
	var out []string
	for _, name := range all {
		if !slices.Contains(some, name) {
			out = append(out, name)
		}
	}
	return out
}

// dataSiteNotInCluster refuses a data site the cluster list leaves out.
//
// The inverse of clusterSiteWithoutData, and incoherent for the same reason:
// the data role renders Patroni on the site, while cluster.sites is where
// every HAProxy's backends, apply's wait for a leader, site add's replica
// list and doctor's membership check come from. A data site missing from it
// runs a Patroni member that nothing routes to and nothing watches, so the
// file describes a replica the deployment does not have.
//
// A derived list is every data site, so only a written one can leave one out.
func (c *checker) dataSiteNotInCluster() {
	if c.cfg.Cluster.SitesDerived {
		return
	}
	for _, name := range c.cfg.DataSites() {
		if slices.Contains(c.cfg.Cluster.Sites, name) {
			continue
		}
		c.refuse("data-site-not-in-cluster", fmt.Sprintf("sites.%s.roles", name),
			"data site is missing from cluster.sites",
			"includes data, but cluster.sites does not list %s. The data role runs Patroni on the site, and cluster.sites is what HAProxy's backends, apply's leader wait and doctor are built from, so this would be a replica nothing routes to or watches. List %s in cluster.sites, at the end if the cluster is already running, or drop the role. Leaving cluster.sites out derives it from the roles.", name, name)
	}
}

// oneVoterNoFailover warns that a replicated cluster with one etcd member
// cannot fail over.
//
// One member elects nobody: when the voter's site is down, Patroni on every
// other site loses the DCS and stops serving writes, so the replica is a
// manual promote rather than a failover. That is the mode the design offers
// for a declined witness, so it is legitimate and a warning. It is only
// worth saying when a replica exists; one site with one voter is the plain
// case.
func (c *checker) oneVoterNoFailover() {
	if len(c.cfg.Etcd.Members) != 1 || len(c.cfg.Cluster.Sites) < 2 {
		return
	}
	voter := c.cfg.Etcd.Members[0]
	c.warn("one-voter-no-failover", c.cfg.EtcdMembersKey(),
		"one etcd voter means no automatic failover",
		"%s one voter, %s, while %d sites share the cluster. One member elects nobody: while %s is down every other site's Patroni loses etcd and stops taking writes, so the database stops rather than failing over, and promoting the replica is a manual step. A witness in a third failure domain makes three voters and restores automatic failover; without one, this is the manual promote mode and the runbook should say so.",
		c.votersVerb(), voter, len(c.cfg.Cluster.Sites), voter)
}

// asyncAutomaticFailover warns that automatic failover under asynchronous
// replication can lose acknowledged commits.
//
// With three voters Patroni promotes a replica on its own, and under
// asynchronous replication that replica may be behind: every commit the old
// leader acknowledged after the replica's last received WAL is gone with it,
// and Patroni promotes anyway as long as the lag is within
// maximum_lag_on_failover ("the maximum bytes a follower may lag to be able
// to participate in leader election"; "When using asynchronous replication a
// failover can cause lost transactions", Patroni's dynamic configuration
// reference). With one voter nothing promotes anyone, so there is nothing
// to warn about. It is a choice an operator may make for write latency over
// a slow link, which is why it warns rather than refuses.
func (c *checker) asyncAutomaticFailover() {
	if c.cfg.Cluster.Synchronous || len(c.cfg.Cluster.Sites) < 2 || len(c.cfg.Etcd.Members) < 3 {
		return
	}
	c.warn("async-automatic-failover", "cluster.synchronous",
		"asynchronous replication can lose commits on failover",
		"is false while %d sites share the cluster and %d voters can elect a new leader. Patroni then promotes a replica on its own, and under asynchronous replication that replica may be behind: every commit the old leader acknowledged after the replica's last received WAL is lost, up to maximum_lag_on_failover, the lag Patroni still promotes through. Set synchronous: true so a commit is acknowledged only once a standby holds it (synchronous_strict: false keeps a lone leader writing), or accept that a failover can lose acknowledged writes.",
		len(c.cfg.Cluster.Sites), len(c.cfg.Etcd.Members))
}

// clusterSiteWithoutData refuses a cluster member that does not hold the data
// role.
//
// The roles say what a site provides and the cluster list says which sites
// hold the database. A site named in the cluster without the data role
// declares both that it stores data and that it does not, and the two
// renderings disagree: it would receive HAProxy backends pointing at a Patroni
// that was never rendered for it. There is no reading of this that works, so
// it is a refusal rather than a warning.
//
// A derived list holds only data sites, so only a written one can name
// another.
func (c *checker) clusterSiteWithoutData() {
	if c.cfg.Cluster.SitesDerived {
		return
	}
	for i, name := range c.cfg.Cluster.Sites {
		site, ok := c.cfg.Sites[name]
		if !ok {
			continue // undeclaredSites reports this
		}
		if site.Has(config.RoleData) {
			continue
		}
		c.refuse("cluster-site-without-data-role", fmt.Sprintf("cluster.sites[%d]", i),
			"cluster site does not hold the data role",
			"names site %q, which does not hold the data role. A site in the cluster runs Patroni and stores the database. Add the data role to %s, or remove it from the cluster. Leaving cluster.sites out derives it from the roles.", name, name)
	}
}

// clusterAppWithoutAppsSite refuses cluster placement when nowhere can run it.
//
// Cluster placement means the app runs on the sites holding the apps role and
// connects to the local proxy. With no such site the placement names no
// location at all, so the app would be declared and never rendered anywhere,
// which is the quietest possible failure.
func (c *checker) clusterAppWithoutAppsSite() {
	if len(c.cfg.AppsSites()) > 0 {
		return
	}
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Placement.Mode != config.PlacementCluster {
			continue
		}
		c.refuse("cluster-app-without-apps-site", fmt.Sprintf("apps.%s.placement", name),
			"cluster app has no apps site to run on",
			"is `cluster`, but no site holds the apps role, so there is nowhere to run it. Give a site the apps role, or pin the app to a site.")
	}
}

// garageReplicationExceedsSites refuses a replication factor larger than the
// number of Garage nodes.
//
// Garage will not store an object it cannot place on the requested number of
// distinct nodes. The deployment comes up, the buckets exist, and every upload
// fails, which looks like an application bug rather than a configuration one.
func (c *checker) garageReplicationExceedsSites() {
	garage := c.cfg.Storage.Garage
	if garage.Replication == 0 || len(garage.Sites) == 0 {
		return
	}
	if garage.Replication <= len(garage.Sites) {
		return
	}
	c.refuse("garage-replication-exceeds-sites", "storage.garage.replication",
		"garage replication exceeds the number of garage sites",
		"is %d, but only %d site(s) run Garage. Garage cannot place a copy on a node that does not exist, so every upload fails while everything else looks healthy. Lower the factor, or add a Garage site.",
		garage.Replication, len(garage.Sites))
}

// garageConsistency refuses a consistency mode Garage does not know, and
// warns on the two that do not mean what they appear to.
//
// Garage v1.0.1 accepts exactly consistent, degraded and dangerous
// (src/rpc/replication_mode.rs) and refuses to start on anything else, so a
// typo would take object storage down at the next apply. dangerous confirms
// an upload once one copy exists; degraded relaxes only the read quorum, which
// is already one at replication 2, so there it changes nothing
// (doc/book/reference-manual/configuration.md, "consistency_mode").
func (c *checker) garageConsistency() {
	garage := c.cfg.Storage.Garage
	switch garage.Consistency {
	case "", config.GarageConsistent:
	case config.GarageDangerous:
		if len(garage.Sites) > 1 {
			// In dangerous mode the write quorum is one copy, whatever the
			// replication factor (src/rpc/replication_mode.rs).
			c.warn("garage-consistency-dangerous", "storage.garage.consistency",
				fmt.Sprintf("garage consistency is dangerous: an upload is confirmed even if only 1 of %d copies is written", c.replication()),
				"Garage confirms an upload once one copy exists and sends the rest in the background, so an upload can live on one disk until the other sites catch up, and a read can miss a recent change. Uploads continue while a site is down.")
		}
	case config.GarageDegraded:
		if c.replication() <= 2 {
			c.warn("garage-consistency-degraded-is-consistent", "storage.garage.consistency",
				"garage degraded consistency equals consistent here",
				"is degraded, which at replication %d is identical to consistent: it lowers only the read quorum, which is already one. Use dangerous if uploads must continue while a site is down.", c.replication())
		}
	default:
		c.refuse("garage-consistency-unknown", "storage.garage.consistency",
			"garage consistency is not a known value",
			"is %q. Garage accepts consistent, degraded or dangerous, and refuses to start on anything else.", garage.Consistency)
	}
}

// storageRoleWithoutGarage refuses the storage role on a site that
// storage.garage.sites does not list. The role says the host exists to run
// Garage, and the render places Garage only from that list, so the site would
// come up with nothing on it while its entry promised object storage.
func (c *checker) storageRoleWithoutGarage() {
	for _, name := range c.cfg.SiteNames() {
		if !c.cfg.Sites[name].Has(config.RoleStorage) || slices.Contains(c.cfg.Storage.Garage.Sites, name) {
			continue
		}
		c.refuse("storage-role-without-garage", fmt.Sprintf("sites.%s.roles", name),
			"storage site is missing from storage.garage.sites",
			"includes storage, but storage.garage.sites does not list %s, so it would run no Garage at all. Add it to storage.garage.sites, at the end if the cluster is already running, or drop the role.", name)
	}
}

// garageCapacities refuses a capacity Garage would refuse, and an override
// for a site that runs no Garage.
//
// Garage's `layout assign -c` takes a number with a unit and refuses a bare
// number or zero, so a bad value would surface as a failed layout stage on a
// live cluster rather than here. A capacities key that is not a Garage site
// is a typo or a leftover, and silently ignoring it would leave the node it
// meant at the default.
func (c *checker) garageCapacities() {
	garage := c.cfg.Storage.Garage
	if len(garage.Sites) > 0 {
		if _, err := config.ParseSize(garage.Capacity); err != nil {
			c.refuse("garage-capacity-not-a-size", "storage.garage.capacity", "garage capacity is not a size with a unit", "%v. Garage's layout needs a capacity with a unit.", err)
		}
	}
	keys := make([]string, 0, len(garage.Capacities))
	for site := range garage.Capacities {
		keys = append(keys, site)
	}
	sort.Strings(keys)
	for _, site := range keys {
		key := "storage.garage.capacities." + site
		if !slices.Contains(garage.Sites, site) {
			c.refuse("garage-capacity-for-no-garage-site", key,
				"garage capacity names a site that runs no garage",
				"names %s, which storage.garage.sites does not list, so no node would take it. Remove it, or list the site.", site)
			continue
		}
		if _, err := config.ParseSize(garage.Capacities[site]); err != nil {
			c.refuse("garage-capacity-not-a-size", key, "garage capacity is not a size with a unit", "%v. Garage's layout needs a capacity with a unit.", err)
		}
	}
}

// garageAvailability warns on layouts whose behaviour with one site down
// surprises. Each is a legitimate choice, so none is refused.
//
// Quorums are Garage v1.0.1's (src/rpc/replication_mode.rs): at replication 2
// a write needs both copies unless the mode is dangerous; at replication 2 on
// three or more sites each partition sits on two of them, so the partitions a
// down site holds stop taking writes.
func (c *checker) garageAvailability() {
	garage := c.cfg.Storage.Garage
	n := len(garage.Sites)
	rf := c.replication()
	switch {
	case n > 1 && rf == 1:
		c.warn("garage-single-copy", "storage.garage.replication",
			"garage keeps a single copy of each object",
			"is 1 across %d sites: each object lives on one site only, and is unreadable while that site is down. Raise it to 2 or more.", n)
	case rf == 2 && n == 2 && garage.Consistency != config.GarageDangerous:
		c.warn("garage-two-sites-stop-uploads", "storage.garage.replication",
			"garage uploads stop while either of two sites is down",
			"is 2 on two sites at consistency %s: every upload needs both, so uploads stop while either site is down; reads continue. A third Garage site at replication 3 keeps both, or consistency dangerous keeps uploads at a durability cost.", c.consistency())
	case rf == 2 && n > 2 && garage.Consistency != config.GarageDangerous:
		c.warn("garage-partial-uploads", "storage.garage.replication",
			"garage uploads partly fail while one site is down",
			"is 2 on %d sites: each object lives on two of them, so while any one is down the uploads that would land on it fail, roughly %d in %d. Replication 3 keeps every upload working with one site down.", n, 2, n)
	}
}

func (c *checker) replication() int {
	if c.cfg.Storage.Garage.Replication == 0 {
		return 1
	}
	return c.cfg.Storage.Garage.Replication
}

func (c *checker) consistency() string {
	if c.cfg.Storage.Garage.Consistency == "" {
		return config.GarageConsistent
	}
	return c.cfg.Storage.Garage.Consistency
}

// evenVoters warns about an even number of etcd members above two.
//
// Quorum is a majority, so four members need three and tolerate one loss:
// exactly what three members tolerate, with an extra machine that can fail and
// an extra vote to collect on every write. An even count buys nothing and adds
// a failure domain. Two is refused separately, because two is worse than one
// rather than merely pointless.
func (c *checker) evenVoters() {
	count := len(c.cfg.Etcd.Members)
	if count <= 2 || count%2 != 0 {
		return
	}
	fix := fmt.Sprintf("Prefer %d.", count-1)
	if c.cfg.Etcd.MembersDerived {
		witnesses := 0
		for _, name := range c.cfg.Etcd.Members {
			if !c.cfg.Sites[name].Has(config.RoleData) {
				witnesses++
			}
		}
		if witnesses > 0 {
			fix = fmt.Sprintf("Prefer %d: take the witness role off a site before the deployment is founded, or, once it runs, take a witness out with `paisans site remove`, which changes the live voters with the file; or write etcd.members with the %d voters wanted.", count-1, count-1)
		} else {
			fix = fmt.Sprintf("Give a site in a third location, one that fails independently, the witness role, which makes %d voters, or write etcd.members with the %d voters wanted.", count+1, count-1)
		}
	}
	c.warn("even-etcd-voters", c.cfg.EtcdMembersKey(),
		"etcd has an even number of voters",
		"%s %d voters. A majority of %d is %d, which is the same number of losses %d members tolerate, so the extra member adds a machine that can fail and a vote to collect without improving anything. %s",
		c.votersVerb(), count, count, count/2+1, count-1, fix)
}

// siteOutsideMesh refuses a site address that is not in the declared mesh.
//
// Applications trust the mesh subnet for forwarded client addresses, and every
// service binds to a mesh address. A site outside it is unreachable over the
// tunnel and its traffic would arrive from an untrusted source, so nothing
// about the deployment works as described.
func (c *checker) siteOutsideMesh() {
	if c.cfg.Mesh.Subnet == "" {
		return // the loader has already reported this
	}
	for _, name := range c.cfg.SiteNames() {
		address := c.cfg.Sites[name].Address
		if address == "" || c.cfg.Mesh.Contains(address) {
			continue
		}
		c.refuse("site-address-outside-mesh", fmt.Sprintf("sites.%s.address", name),
			"site address is outside the mesh subnet",
			"is %s, which is outside mesh.subnet %s. Every site binds its services to a mesh address, and applications trust that subnet for forwarded client addresses. Give the site an address inside it, or widen the mesh.",
			address, c.cfg.Mesh.Subnet)
	}
}

// duplicateSiteAddress refuses two sites with one mesh address.
//
// Every service on a site binds its mesh address, and every other site routes
// that address to it through its WireGuard peer. Two sites holding one address
// would each get the other's traffic and neither could tell, so there is no
// way to apply it that works.
func (c *checker) duplicateSiteAddress() {
	first := map[string]string{}
	for _, name := range c.cfg.SiteNames() {
		address := c.cfg.Sites[name].Address
		if address == "" {
			continue
		}
		if earlier, taken := first[address]; taken {
			c.refuse("duplicate-site-address", fmt.Sprintf("sites.%s.address", name),
				"two sites have the same mesh address",
				"is %s, which sites.%s.address already is. Every site binds its services to its own mesh address and every other site routes that address to it, so two sites on one address each receive the other's traffic. Give each site an address of its own inside mesh.subnet.",
				address, earlier)
			continue
		}
		first[address] = name
	}
}

// sitesShareAMachine refuses two sites that are certainly one machine.
//
// A site is a machine. Everything a deployment owns on a host is named for the
// deployment, not the site: its root /srv/paisans/<token>, its compose
// projects, its WireGuard interface psns-<token>, its entry in the host's
// registry. Two sites of one deployment on one machine would overwrite each
// other's files and containers. Roles combine on one site, and a second
// environment is a second deployment, so nothing needs two sites on one
// machine.
//
// The file can only see a machine through how it is reached, so two things
// are compared, each of which reaches exactly one machine:
//
//   - the ssh destination, host and port after their defaults (ssh.host or
//     public_address or the endpoint's host; port 22). The login user is
//     left out: two users on one host and port are still one machine.
//   - the endpoint other sites dial (config.Site.DialEndpoint). One address
//     and UDP port is delivered to one machine, behind NAT or not.
//
// A shared public_address alone is not compared. Two sites behind one router
// share the router's public address, each reached on a port forwarded to it,
// and that is a correct home lab, not one machine. The apply's claim on each
// host is what catches the rest (internal/registry), since two different
// names for one machine look different in a file.
func (c *checker) sitesShareAMachine() {
	advice := "A site is a machine: a deployment names its files, containers and WireGuard interface for itself and not for the site, so two of its sites on one machine overwrite each other. To combine roles, give one site several roles; a second environment is a second deployment."
	ssh := map[string]string{}
	dial := map[string]string{}
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if host := site.SSHHost(); host != "" {
			where := sshKey(host, site.SSH.PortOrDefault())
			if earlier, taken := ssh[where]; taken {
				c.refuse("sites-share-a-machine", fmt.Sprintf("sites.%s.ssh", name),
					"two sites are reached at one ssh destination",
					"reaches %s, as sites.%s.ssh does, so the two are one machine. %s",
					where, earlier, advice)
			} else {
				ssh[where] = name
			}
		}
		if endpoint := site.DialEndpoint(); endpoint != "" {
			where := strings.ToLower(endpoint)
			if earlier, taken := dial[where]; taken {
				c.refuse("sites-share-a-machine", fmt.Sprintf("sites.%s.endpoint", name),
					"two sites are dialled at one endpoint",
					"is dialled at %s, as sites.%s is, and one address and port reaches one machine. %s Behind one router, forward a port of its own to each site.",
					endpoint, earlier, advice)
			} else {
				dial[where] = name
			}
		}
	}
}

// sshKey is one ssh host and port, written the same way however the file
// spelt them: a hostname in lowercase, an address in its canonical form.
func sshKey(host string, port int) string {
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(strings.ToLower(host), strconv.Itoa(port))
}

// undeclaredSites refuses any reference to a site that does not exist. A
// typo here renders artifacts for a machine nobody owns.
func (c *checker) undeclaredSites() {
	check := func(key string, names []string) {
		for i, name := range names {
			if _, ok := c.cfg.Sites[name]; !ok {
				c.refuse("undeclared-site", fmt.Sprintf("%s[%d]", key, i),
					"site name is not declared under sites",
					"names site %q, which is not declared under sites. Add the site, or correct the name.", name)
			}
		}
	}
	// A derived list names only declared sites.
	if !c.cfg.Cluster.SitesDerived {
		check("cluster.sites", c.cfg.Cluster.Sites)
	}
	if !c.cfg.Etcd.MembersDerived {
		check("etcd.members", c.cfg.Etcd.Members)
	}
	check("storage.garage.sites", c.cfg.Storage.Garage.Sites)

	for _, appName := range c.cfg.AppNames() {
		app := c.cfg.Apps[appName]
		if app.Placement.Mode != config.PlacementPinned {
			continue
		}
		if _, ok := c.cfg.Sites[app.Placement.Site]; !ok {
			c.refuse("undeclared-site", fmt.Sprintf("apps.%s.placement", appName),
				"site name is not declared under sites",
				"pins the app to site %q, which is not declared under sites. Add the site, or correct the name.", app.Placement.Site)
		}
	}
}

// placementShape refuses a placement that is neither of the two the design
// allows. There are exactly two, and a third spelling is a silent
// misconfiguration rather than a new mode.
func (c *checker) placementShape() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Placement.Mode != config.PlacementInvalid {
			continue
		}
		c.refuse("invalid-placement", fmt.Sprintf("apps.%s.placement", name),
			"app placement is neither cluster nor pinned",
			"is %q. Placement is either `cluster` or `{ pinned: <site> }`, and nothing else.", app.Placement.Literal)
	}
}

// dashboardLinkIsAPath refuses a sso_dashboard_link that is not a path on
// the app's own hostname. `oidc client create` sends it to the identity
// provider as part of a launch URL, so a malformed one is caught here, before
// any client is planned, rather than at the provider.
func (c *checker) dashboardLinkIsAPath() {
	for _, name := range c.cfg.AppNames() {
		v, ok := c.cfg.Apps[name].Settings[blueprints.DashboardLinkSetting]
		if !ok {
			continue
		}
		if err := blueprints.CheckDashboardLink(v); err != nil {
			c.refuse("sso-dashboard-link-not-a-path", fmt.Sprintf("apps.%s.settings.%s", name, blueprints.DashboardLinkSetting), "dashboard link is not a valid path", "%s", err.Error())
		}
	}
}

// consentIsABoolean refuses an sso_consent that is not true or false. The
// client is made from it, and a string such as "yes" would read as off, so a
// member would never see the screen the operator asked for.
func (c *checker) consentIsABoolean() {
	for _, name := range c.cfg.AppNames() {
		v, ok := c.cfg.Apps[name].Settings[blueprints.ConsentSetting]
		if _, isBool := v.(bool); ok && !isBool {
			c.refuse("sso-consent-not-a-boolean", fmt.Sprintf("apps.%s.settings.%s", name, blueprints.ConsentSetting),
				"sso_consent is not true or false",
				"is %v. Write true to show Pocket ID's consent screen on a member's first sign in, or leave it out.", v)
		}
	}
}

// clusterPlacementWithoutACluster refuses cluster placement for an application
// that cannot join the cluster.
//
// Cluster placement means the app runs on every site holding the apps role and
// shares one database through the local proxy. An application that stores its
// data anywhere else gets neither half of that: it would be rendered onto each
// apps site with its own file, so one hostname would serve two deployments
// that diverge from the moment anybody writes to them, and a failover would
// move readers between them.
//
// Three blueprints are in this position: Element, which is a static client,
// oauth2-proxy, which keeps a session in a cookie, and uptime, which keeps
// its database in SQLite under its own data directory. It is refused rather
// than warned about because there is no reading of it that works, and pinning
// is not a downgrade: it is the plain case, and the app's availability
// becomes its site's, which is what it was always going to be.
func (c *checker) clusterPlacementWithoutACluster() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Placement.Mode != config.PlacementCluster || blueprints.UsesPostgres(app.Blueprint) {
			continue
		}
		c.refuse("cluster-placement-without-a-cluster", fmt.Sprintf("apps.%s.placement", name),
			"app has cluster placement but no shared database",
			"is `cluster`, but %s keeps its data outside the Postgres cluster, so there is nothing for it to join. It would be rendered onto every apps site with its own separate storage, which is two deployments behind one hostname. Pin it to a site instead.",
			app.Blueprint)
	}
}

// acmeProviderHasAnImage refuses a provider whose module cannot reach the
// gateway, or a monitor's own Caddy, which runs the same image.
//
// A DNS provider in Caddy is a Go module compiled into the binary, and nothing
// is built on a host, so a provider the toolkit publishes no image for needs an
// image declared that carries it. Without one the gateway would run a binary
// that cannot load its own configuration, and would hold no certificate for
// any hostname. That is incoherent rather than risky, so it is a refusal.
func (c *checker) acmeProviderHasAnImage() {
	if len(c.cfg.CaddySites()) == 0 || c.cfg.ACME.Provider == "" {
		return // no Caddy needs certificates; a missing provider is structural
	}
	if acme.Published(c.cfg.ACME.Provider) {
		return
	}
	if c.cfg.ACME.Image != "" {
		return
	}
	c.refuse("acme-provider-needs-an-image", "acme.provider",
		"acme provider has no published gateway image",
		"is %q, which this toolkit publishes no image for, and acme.image declares none. A DNS provider is a module compiled into Caddy rather than a setting, and nothing is built on a host, so the gateway would run a binary that cannot load its own configuration. Published providers are %s; for any other, declare acme.image with the module compiled in.",
		c.cfg.ACME.Provider, strings.Join(acme.Providers(), ", "))
}

// acmeImageIsNotStockCaddy refuses upstream's own image as an override.
//
// This is the one thing about an image that can be judged from its reference.
// Upstream's Caddy carries no DNS provider module, so it certainly cannot serve
// a configuration that names one. Every other reference is accepted here and
// verified at apply time by asking the binary, because what is compiled into a
// binary is not a property of its name.
func (c *checker) acmeImageIsNotStockCaddy() {
	if c.cfg.ACME.Image == "" || !acme.IsStockCaddy(c.cfg.ACME.Image) {
		return
	}
	c.refuse("acme-image-is-stock-caddy", "acme.image",
		"acme image is stock caddy with no dns module",
		"is %q, which is upstream's own Caddy image and carries no DNS provider module. Caddy would fail to load a configuration using `acme_dns %s`. Remove this key to take the image this toolkit publishes, or declare one with the module compiled in.",
		c.cfg.ACME.Image, c.cfg.ACME.Provider)
}

// imageServices refuses an image declared for a service the blueprint
// does not have.
//
// The map is keyed on compose service names precisely so this check can exist.
// An unknown key is a typo, and a typo that renders nothing is the worst
// outcome available: the operator believes they moved to their fork, the
// deployment keeps running the default, and nothing anywhere says so.
func (c *checker) imageServices() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		for _, service := range sortedKeys(app.Images) {
			if blueprints.Has(app.Blueprint, service) {
				continue
			}
			c.refuse("unknown-image-service", fmt.Sprintf("apps.%s.images.%s", name, service),
				"image names a service the template does not define",
				"names a service %q, which the %s template does not define. Its services are %s. A key that matches nothing renders nothing, so the deployment would keep running the default image while the file says otherwise.",
				service, app.Blueprint, strings.Join(blueprints.ServiceNames(app.Blueprint), ", "))
		}
	}
}

// floatingImages refuses a reference that does not name one build.
//
// `latest` makes two runs of `apply` produce different deployments from
// identical inputs, which contradicts the premise that the configuration plus
// the secrets reconstruct the stack. That is incoherent rather than risky, so
// it is a refusal. A digest is accepted and is the stronger form.
//
// What this does not do is judge whether a version is safe to move to. Many of
// these applications run schema migrations at boot against live member data,
// and the toolkit cannot know which release does. It must not imply it
// checked.
func (c *checker) floatingImages() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		for _, service := range sortedKeys(app.Images) {
			c.floatingImage(fmt.Sprintf("apps.%s.images.%s", name, service), app.Images[service])
		}
	}
	for _, key := range sortedKeys(c.cfg.Infra.Images) {
		c.floatingImage("infra.images."+key, c.cfg.Infra.Images[key])
	}
	for _, major := range sortedKeys(c.cfg.Infra.Postgres) {
		for _, key := range sortedKeys(c.cfg.Infra.Postgres[major]) {
			c.floatingImage(fmt.Sprintf("infra.postgres.%s.%s", major, key), c.cfg.Infra.Postgres[major][key])
		}
	}
}

// floatingImage refuses ref, found at key, when it does not name one build.
func (c *checker) floatingImage(key, ref string) {
	why := blueprints.ParseReference(ref).Floating()
	if why == "" {
		return
	}
	c.refuse("floating-image-tag", key,
		"image tag is floating",
		"is %q and %s. Two runs of `apply` would then deploy different builds from the same inputs, so the configuration and the secrets no longer reconstruct the stack. Name a version tag, or a digest, which is stronger.",
		ref, why)
}

// zalandoSpilo is the repository prefix of the Spilo the toolkit is written
// against, whose versions it can check.
const zalandoSpilo = "ghcr.io/zalando/spilo-"

// infraImages checks what infra overrides against what the toolkit's own
// code speaks to: the versions of etcd, HAProxy, Garage and Spilo it is
// written against, and the Postgres majors it runs.
func (c *checker) infraImages() {
	for _, key := range sortedKeys(c.cfg.Infra.Images) {
		c.supportedVersion("infra.images."+key, key, c.cfg.Infra.Images[key])
	}
	if c.cfg.Infra.Images["heartbeat"] != "" {
		c.warn("infra-heartbeat-covers-pocket-id", "infra.images.heartbeat",
			"the heartbeat override covers Pocket ID's too",
			"replaces the infrastructure stack's heartbeat and the operators heartbeat in every Pocket ID stack, which run the same image.")
	}
	majors := map[string]bool{}
	for _, m := range blueprints.PostgresMajors() {
		majors[m] = true
	}
	running := c.cfg.PostgresMajor()
	for _, major := range sortedKeys(c.cfg.Infra.Postgres) {
		key := "infra.postgres." + major
		switch {
		case !majors[major]:
			c.refuse("infra-postgres-unknown-major", key,
				"not a Postgres major the toolkit runs",
				"is Postgres %s, and the toolkit runs %s.", major, strings.Join(blueprints.PostgresMajors(), ", "))
			continue
		case major != running:
			c.warn("infra-postgres-unused-major", key,
				"applies to no Postgres the deployment runs",
				"is for Postgres %s, and cluster.postgres_version is %s, so nothing runs it. Remove it, or keep it only while a move to %s is planned.", major, running, major)
		}
		spilo := c.cfg.Infra.Postgres[major]["spilo"]
		if spilo == "" {
			continue
		}
		if strings.HasPrefix(spilo, zalandoSpilo) {
			c.supportedVersion(key+".spilo", "spilo", spilo)
			continue
		}
		c.warn("infra-spilo-unchecked", key+".spilo",
			"the toolkit cannot check this Spilo",
			"is %q, outside %s*. The toolkit is written against Spilo's and Patroni's environment and data path, and cannot check that this image follows them. apply's review asks for it.", spilo, zalandoSpilo)
	}
}

// supportedVersion refuses ref as the image of the foundation's name when
// its tag can leave the versions the toolkit's code is written against. A
// reference with no tag cannot be checked, and is refused naming the form
// that can. An image the toolkit's code does not speak to has no versions to
// leave.
func (c *checker) supportedVersion(key, name, ref string) {
	prefixes := blueprints.SupportedTags(name)
	r := blueprints.ParseReference(ref)
	if len(prefixes) == 0 || r.Floating() != "" {
		return // floating-image-tag already refuses a floating one
	}
	if blueprints.CheckFoundationImage(name, ref) == nil {
		return
	}
	if r.Digest != "" && blueprints.ParseReference(r.Name).Tag == "" {
		c.refuse("infra-image-unsupported-version", key,
			"outside the versions the toolkit is written against",
			"is %q, a digest with no tag, so its version cannot be checked. Write the tag beside the digest (Eg: %s:<tag>@%s).", ref, r.Name, r.Digest)
		return
	}
	c.refuse("infra-image-unsupported-version", key,
		"outside the versions the toolkit is written against",
		"is %q, which can leave %s, the versions the toolkit's code is written against. Fix the first part of the tag.", ref, strings.Join(prefixes, " or "))
}

// imageForAbsentPostgres warns about an image for a database that is not
// rendered.
//
// A clustered app has no Postgres service: it connects to the local HAProxy,
// and the cluster's version is set once for every database rather than per
// app. The declaration is harmless, and placement can change, so this warns
// rather than refusing.
func (c *checker) imageForAbsentPostgres() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Placement.Mode != config.PlacementCluster {
			continue
		}
		if _, ok := app.Images[blueprints.PostgresService]; !ok {
			continue
		}
		c.warn("image-for-absent-postgres", fmt.Sprintf("apps.%s.images.%s", name, blueprints.PostgresService),
			"database image is declared but nothing uses it",
			"declares a database image, but the app has cluster placement and runs no database of its own: it connects to the local HAProxy, and the cluster's version comes from cluster.postgres_version. Nothing renders this. Remove it, or pin the app if it was meant to have its own database.")
	}
}

// sortedKeys returns a map's keys in sorted order, so that one file reports its
// problems in the same order every run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pinnedOntoWitness warns about disk contention with etcd.
//
// etcd's stability depends on fsync latency, and an application with its own
// database is not a quiet neighbour. Contention means missed heartbeats,
// spurious elections, and the database failing over because something else was
// compacting. This is risky rather than incoherent, so it warns.
func (c *checker) pinnedOntoWitness() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Placement.Mode != config.PlacementPinned {
			continue
		}
		site, ok := c.cfg.Sites[app.Placement.Site]
		if !ok || !site.Has(config.RoleWitness) {
			continue
		}
		// The gate keeps nothing on disk, so it is the quiet neighbour etcd
		// needs, and it is placed on the gateway by the toolkit, often the
		// witness, where a warning would name a choice nobody made.
		if app.Blueprint == config.BlueprintOAuth2Proxy {
			continue
		}
		c.warn("pinned-app-on-witness", fmt.Sprintf("apps.%s.placement", name),
			"pinned app shares a site with the witness",
			"pins the app to %s, which holds the witness role. etcd depends on fsync latency and an application with its own database is not a quiet neighbour: the failure mode is the database failing over for no reason. Prefer a separate site for pinned apps, or give etcd its own device there.",
			app.Placement.Site)
	}
}

// gatewayOnDataSite warns that failover will not reach the public path.
//
// With one site this is correct and total failure of that site is the expected
// behaviour of having one site. Once a second data site exists, the database
// fails over in about a minute while public DNS still points at the dead
// machine, and automatic failover behind a manual DNS change is not automatic.
func (c *checker) gatewayOnDataSite() {
	if len(c.cfg.DataSites()) < 2 {
		return
	}
	for _, name := range c.cfg.SiteNames() {
		site := c.cfg.Sites[name]
		if site.Has(config.RoleGateway) && site.Has(config.RoleData) {
			c.warn("gateway-on-data-site", fmt.Sprintf("sites.%s.roles", name),
				"gateway shares a site with a data role",
				"holds both gateway and data while %d data sites are declared. The database would fail over in about a minute, but public DNS would still point here, so nothing is reachable until a record changes and propagates. Move the gateway off both data sites.",
				len(c.cfg.DataSites()))
		}
	}
}

// portCollision refuses two things on one site binding the same port.
//
// Infrastructure runs with host networking and every app publishes on its
// site's mesh address, so two of them on one port cannot both start, and the
// second fails only when its container does: on a real data and apps site,
// Spilo's bg_mon took 8080 before Mbin could publish there. The listeners are
// read from render, which takes them from the same conditions and constants
// the rendered files use, so the check cannot drift from what lands on a host.
// A clustered app is counted on every apps site and a pinned one on its own.
func (c *checker) portCollision() {
	for _, name := range c.cfg.SiteNames() {
		listeners := render.SiteListeners(c.cfg, name)
		reported := map[string]bool{}
		for i, a := range listeners {
			for _, b := range listeners[i+1:] {
				if !collides(a, b) {
					continue
				}
				// One finding per pair and port: a service on the mesh
				// address and loopback is one collision, not two.
				pair := fmt.Sprintf("%s|%s|%d/%s", a.Owner, b.Owner, a.Port, a.Proto)
				if reported[pair] {
					continue
				}
				reported[pair] = true
				c.refuse("port-collision", fmt.Sprintf("sites.%s", name),
					"two roles on one site bind the same port",
					"%s binds %s and %s binds %s, so whichever starts second fails at container start. Pin one of them to another site, or move the role that brings the other.",
					a.Owner, a, b.Owner, b)
			}
		}
	}
}

// collides reports whether two listeners on one site cannot both bind. The
// same owner under the same key is one listener named twice, such as a
// service on its mesh address and on loopback, which never overlaps anyway;
// the same owner under two keys, such as an app published on its mesh
// address and again on a web_server listen, is two binds like any others.
func collides(a, b render.Listener) bool {
	if a.Owner == b.Owner && a.Key == b.Key {
		return false
	}
	return a.Overlaps(b)
}

// pocketIDFileBackend warns about a backend set to anything other than
// database. Unset is not warned about: the pocket-id template renders
// `database` when the setting is absent, so unset already is the backend this
// check asks for.
//
// Its uploads are avatars and admin branding, about a megabyte in total. In
// Postgres they ride streaming replication, so a promoted site has them
// already with no sync job. It also keeps object storage off the dependency
// list of the one service that gates every other one.
func (c *checker) pocketIDFileBackend() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Blueprint != config.BlueprintPocketID {
			continue
		}
		backend, _ := app.Settings["file_backend"].(string)
		if backend == "" || backend == "database" {
			continue
		}
		c.warn("pocket-id-file-backend", fmt.Sprintf("apps.%s.settings.file_backend", name),
			"pocket id file backend is not database",
			"is %s. Prefer `database`: the uploads are about a megabyte of avatars and branding, in Postgres they ride streaming replication with no sync job, and sign in then does not depend on object storage being up. On filesystem, a promoted site serves broken avatars and stock branding mid incident.",
			backend)
	}
}

// pocketIDStandbyMarkerUnknown warns when a Pocket ID on more than one apps
// site runs an image whose refusal text the toolkit has not recorded. Every
// site but one stands by on that text (blueprints.PocketIDStandbyMarker); an
// unknown image gets the default image's, which may not match. A warning,
// not a refusal: a marker that does not match fails safe, the standby site's
// container restarting under Docker's policy rather than running twice, and
// apply's gate says so.
func (c *checker) pocketIDStandbyMarkerUnknown() {
	sites := len(c.cfg.AppsSites())
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.Blueprint != config.BlueprintPocketID || app.Placement.Mode == config.PlacementPinned || sites < 2 {
			continue
		}
		image := app.Images["app"]
		if image == "" {
			continue
		}
		if _, known := blueprints.StandbyMarker(image); known {
			continue
		}
		c.warn("pocket-id-standby-marker-unknown", fmt.Sprintf("apps.%s.images.app", name),
			"pocket id image has an unrecorded standby marker",
			"is %s, whose refusal text is not recorded, on %d apps sites. Every site but one stands by when Pocket ID logs that another instance holds the database, and this image is assumed to log what the default does. If it does not, the standby sites restart in a loop instead of standing by, and apply's health gate fails there. Check its backend/internal/bootstrap/bootstrap.go.",
			image, sites)
	}
}

// latestACMEImage refuses a gateway image on `latest`, or on no tag, which
// means `latest`.
//
// A tag that moves is otherwise welcome here, unlike under an app: apply
// resolves acme.image to the digest its tag names while it plans, renders
// that digest, and records it, so a moving tag (Eg: `2`, or `2.11`) brings
// rebuilds in as an ordinary planned change that every gateway gate checks.
// What resolving cannot do is keep a tag inside one major version. `latest`
// moves to Caddy 3 the day it ships, and a major version can change the
// Caddyfile syntax the gateway is rendered with. The gateway's validation gate
// would refuse that build, and so every apply from that day on, with nothing
// in the configuration having changed. Naming the major version is the whole
// of the fix.
func (c *checker) latestACMEImage() {
	if c.cfg.ACME.Image == "" {
		return
	}
	why := blueprints.ParseReference(c.cfg.ACME.Image).Floating()
	if why == "" {
		return
	}
	c.refuse("acme-image-is-latest", "acme.image",
		"acme image follows latest, which crosses Caddy major versions",
		"is %q and %s. apply resolves a moving tag to the build it names, so a rebuild is fine, but `latest` moves to the next Caddy major version as soon as one ships, and a major version can change the Caddyfile syntax the gateway is rendered with: from that day every apply would stop at the gateway's validation, with nothing in this configuration changed. Name the major version as the tag (Eg: `2`), a narrower version, or a digest.",
		c.cfg.ACME.Image, why)
}

// gatedOnUngateableKind refuses gated: true on a blueprint that cannot sit
// behind one.
//
// README: "A Matrix hostname must never be gated." The gate answers a request
// with no session with a redirect to a passkey prompt. A Matrix client is not
// a browser and does not follow it, and neither does a federating server
// fetching the delegation documents, so a gate on a homeserver does not
// restrict access: it ends client login and federation outright. Pocket ID is
// the sign-in flow, so gating it gates the login. The
// failure in every case reads as something mysteriously unable
// to sign in, with nothing in the configuration saying why.
//
// Access control for these lives elsewhere: for a homeserver at the identity
// provider, where the group restriction decides who may be provisioned an
// account at all. That is why this is a refusal and not a warning: there is no
// reading of a gated one that works.
func (c *checker) gatedOnUngateableBlueprint() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.Gated || blueprints.GateFor(app.Blueprint).Gateable {
			continue
		}
		c.refuse("gated-on-ungateable-blueprint", fmt.Sprintf("apps.%s.gated", name),
			"gated is set on a blueprint that cannot sit behind the gate",
			"is true on a %s app, which cannot sit behind the gate. The gate redirects a request with no session to a passkey prompt, and its blueprint says it cannot take one: its clients or federating servers would not follow the redirect, it is the sign-in flow itself, or it must stay readable while the gate is down. Remove gated.",
			app.Blueprint)
	}
}

// gatedWithoutSignedFetch refuses gated: true on a federating app that
// does not refuse an unsigned ActivityPub read: one whose blueprint records no
// way to, or whose settings.authorized_fetch is false.
//
// The gate sends every ActivityPub request to the app without asking for a
// session, because a federating peer has none and proves who it is by
// signature instead. Anyone can send an ActivityPub Accept header, so that is
// only safe when the app refuses a read that is not signed by an instance on
// its allow list. Without that it would serve its whole read surface to
// anyone who asked for JSON. A WriteFreely reads only from its allowlist in
// private mode, so for one that also means private is not false.
func (c *checker) gatedWithoutSignedFetch() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.Gated || !blueprints.Federates(app) || blueprints.SignedFetchFor(app) != nil {
			continue
		}
		c.refuse("gated-without-signed-fetch", fmt.Sprintf("apps.%s.gated", name),
			"gated federating app still answers unsigned reads",
			"is true, but this %s app federates and would not refuse an unsigned ActivityPub read. The gate leaves ActivityPub requests to the app, and anyone can send an ActivityPub Accept header, so every page would be readable as JSON by anyone. Remove settings.authorized_fetch: false (and any setting its blueprint says turns its signed reads off), turn federation off for this app, or remove gated.",
			app.Blueprint)
	}
}

// autoLoginWithoutARule refuses auto_login: true on a blueprint with no
// auto-login rule. The key would do nothing, and an operator reading it would
// believe a member is signed straight in. A blueprint whose sign-in screen
// redirects itself needs none, and the blueprints that cannot sit behind the
// gate have none to take.
func (c *checker) autoLoginWithoutARule() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.AutoLogin || len(blueprints.GateFor(app.Blueprint).AutoLogin) > 0 {
			continue
		}
		c.refuse("auto-login-without-a-rule", fmt.Sprintf("apps.%s.auto_login", name),
			"auto_login is set on a blueprint with no auto-login rule",
			"is true on a %s app, which has no auto-login rule, so it would do nothing: its blueprint declares none, because its sign-in screen already redirects itself or because it cannot sit behind the gate. Remove auto_login.",
			app.Blueprint)
	}
}

// usesGateOutsideTheDomain refuses gated or auto_login on an app with a
// hostname outside the community's domain.
//
// The gate's session cookie is scoped to the community's domain, so a browser
// never sends it to a hostname elsewhere. A gated app there would send every
// member to sign in, back to the app with no cookie, and to sign in again,
// forever; auto-login would never fire. A media or join hostname is never
// gated, so it is not checked.
func (c *checker) usesGateOutsideTheDomain() {
	domain := c.cfg.Community.Domain
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.UsesGate() {
			continue
		}
		hostnames := map[string]string{"hostname": app.Hostname}
		for _, rh := range blueprints.RoleHostnames(c.cfg, app) {
			if rh.Role != blueprints.MediaRole && rh.Role != blueprints.JoinRole {
				hostnames["hostnames."+rh.Role] = rh.Hostname
			}
		}
		for _, key := range sortedKeys(hostnames) {
			h := hostnames[key]
			if h == domain || strings.HasSuffix(h, "."+domain) {
				continue
			}
			c.refuse("uses-gate-outside-the-domain", fmt.Sprintf("apps.%s.%s", name, key),
				"gated or auto-login app on a hostname outside the domain",
				"is %s, outside %s, and this app sets gated or auto_login. The gate's session cookie is scoped to %s, so a browser never sends it there: a gated app would send every member back to sign in forever, and auto-login would never fire. Move the hostname under %s, or remove gated and auto_login.",
				h, domain, domain, domain)
		}
	}
}

// mediaHostnameShape refuses a media hostname that cannot be one.
//
// Every app that stores objects serves them on a hostname of its own, derived
// as <label>-media.<domain> from the app's own hostname unless the app
// declares `hostnames.media`. Either way the name becomes a site address on
// the gateway and a URL an application writes into every page, feed and
// federated post, so a name that is not a hostname is a gateway that will not
// load or a URL nothing can fetch.
//
// A declared name must also sit under community.domain. The certificate is
// issued over DNS-01 against the deployment's own zone, and a name outside it
// is a name the configured DNS provider cannot answer a challenge for. A
// derived name is under the domain by construction, and can still fail the
// hostname check: a 60 character label is legal and is not once -media is
// added to it.
func (c *checker) mediaHostnameShape() {
	domain := strings.ToLower(c.cfg.Community.Domain)
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		media := blueprints.MediaHostname(c.cfg, app)
		if media == "" {
			continue
		}
		key := fmt.Sprintf("apps.%s.hostnames.%s", name, blueprints.MediaRole)
		derived := blueprints.MediaHostnameIsDerived(c.cfg, app)
		if problem := config.HostnameProblem(media); problem != "" {
			if derived {
				c.refuse("media-hostname-is-not-a-hostname", key,
					"media hostname is not a valid hostname",
					"is not declared, and the name derived from apps.%s.hostname is %q, which is not a hostname: %s. The media hostname is <label>-media.<domain>, a sibling of the app's own hostname. Declare hostnames.media to choose another name under %s.",
					name, media, problem, c.cfg.Community.Domain)
			} else {
				c.refuse("media-hostname-is-not-a-hostname", key,
					"media hostname is not a valid hostname",
					"is %q, which is not a hostname: %s. It becomes a site address on the gateway and a URL the app publishes to readers, so it has to be a name a browser can fetch, for example %s.",
					media, problem, name+blueprints.MediaSuffix+"."+c.cfg.Community.Domain)
			}
			continue
		}
		if derived || domain == "" {
			continue
		}
		if !strings.HasSuffix(media, "."+domain) {
			c.refuse("media-hostname-outside-the-domain", key,
				"media hostname is outside community.domain",
				"is %q, which is not under community.domain (%s). Its certificate is issued over DNS-01 against the deployment's own zone, so the configured DNS provider cannot answer for a name outside it. Use a name under %s, for example %s.",
				media, c.cfg.Community.Domain, c.cfg.Community.Domain, name+blueprints.MediaSuffix+"."+c.cfg.Community.Domain)
		}
	}
}

// mailDomains refuses a community.mail_domains entry that is not a hostname
// under community.domain.
//
// dns init leaves a mail domain's mail records alone and locks down every
// other name it publishes. An entry it never publishes at, outside the domain,
// would say nothing at all, and an operator who wrote one expecting it to
// protect that name's mail would never be told. An entry that is not a
// hostname is a typo, and a typo here locks the name it meant out of its own
// mail.
func (c *checker) mailDomains() {
	domain := strings.TrimSuffix(strings.ToLower(c.cfg.Community.Domain), ".")
	for i, entry := range c.cfg.Community.MailDomains {
		key := fmt.Sprintf("community.mail_domains[%d]", i)
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry)), ".")
		if problem := config.HostnameProblem(name); problem != "" {
			c.refuse("mail-domain-is-not-a-hostname", key,
				"mail domain is not a hostname",
				"is %q, which is not a hostname: %s. List the name mail is addressed to, the part after the @, for example %s.",
				entry, problem, c.cfg.Community.Domain)
			continue
		}
		if domain != "" && name != domain && !strings.HasSuffix(name, "."+domain) {
			c.refuse("mail-domain-outside-the-domain", key,
				"mail domain is outside community.domain",
				"is %q, which is neither community.domain (%s) nor a name under it. dns init publishes records only for this deployment's names, so listing another name changes nothing. List %s, or a name under it.",
				entry, c.cfg.Community.Domain, c.cfg.Community.Domain)
		}
	}
}

// mediaHostnameUnderAnAppHostname refuses a media hostname that is a child of
// an app's own hostname.
//
// media.talk.example.org would sit under talk.example.org, so any cookie an
// app sets with a Domain attribute on its own host is sent to the media
// hostname too, and a file served from there could set cookies the app would
// then read back. The media hostname exists partly to be a different origin
// from every app, and a child shares exactly the cookie scope that origin was
// meant to leave behind. A sibling, <label>-media.<domain>, shares nothing
// with the app but the community's own domain.
//
// That domain is the one exception, because it cannot be otherwise: every
// hostname in a deployment sits under it, and a synapse app commonly serves
// its delegation documents on it. A cookie scoped to the community's domain is
// a deliberate choice, made by the gate, and it already reaches every app.
func (c *checker) mediaHostnameUnderAnAppHostname() {
	domain := strings.ToLower(c.cfg.Community.Domain)
	type claim struct{ key, hostname string }
	var appHosts []claim
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		appHosts = append(appHosts, claim{fmt.Sprintf("apps.%s.hostname", name), app.Hostname})
		for _, rh := range blueprints.RoleHostnames(c.cfg, app) {
			if rh.Role == blueprints.MediaRole {
				continue
			}
			key := fmt.Sprintf("apps.%s.hostnames.%s", name, rh.Role)
			if rh.Derived {
				key += " (derived)"
			}
			appHosts = append(appHosts, claim{key, rh.Hostname})
		}
	}
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		media := strings.ToLower(blueprints.MediaHostname(c.cfg, app))
		if media == "" {
			continue
		}
		for _, host := range appHosts {
			parent := strings.ToLower(strings.TrimSpace(host.hostname))
			if parent == "" || parent == domain {
				continue
			}
			if !strings.HasSuffix(media, "."+parent) {
				continue
			}
			c.refuse("media-hostname-under-an-app-hostname", fmt.Sprintf("apps.%s.hostnames.%s", name, blueprints.MediaRole),
				"media hostname sits under an app hostname",
				"is %q, a child of %q (%s). Cookies an app scopes to its own host reach every name under it, so a media hostname there shares the cookie scope it exists to be outside of. Use a sibling instead, for example %s.",
				media, host.hostname, host.key, name+blueprints.MediaSuffix+"."+c.cfg.Community.Domain)
			break
		}
	}
}

// duplicateHostname refuses the same public name claimed twice.
//
// Every hostname an app declares, primary or role, becomes a site address in
// the gateway's Caddyfile. Caddy refuses a configuration in which two site
// blocks claim the same address, because it cannot decide which one a request
// belongs to, and it refuses the whole file rather than the one block. So a
// name typed twice takes every hostname in the deployment down, not the one
// that was duplicated.
//
// It became reachable when an app gained the ability to answer on several
// hostnames: before that an app had exactly one name and a clash needed two
// apps to be given the same one, which is hard to do by accident. Setting one
// app's `hostname` to the value another app already uses for a role is not.
// This is the same failure unknown-hostname-role and gate-without-a-gate-app
// exist to prevent, arrived at from a different direction.
func (c *checker) duplicateHostname() {
	type claim struct {
		key, hostname string
		// derived is set for a hostname nobody wrote (a media or join
		// hostname, or the gate's), so the message can say where the name came from
		// rather than quoting a line the file does not have; from says what
		// it was derived from.
		derived bool
		from    string
	}
	describe := func(cl claim) string {
		if cl.derived {
			return cl.key + ", derived from " + cl.from + ","
		}
		return cl.key
	}
	first := map[string]claim{} // hostname -> the claim that took it first
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		claims := []claim{{key: fmt.Sprintf("apps.%s.hostname", name), hostname: app.Hostname}}
		if app.Derived {
			// The gate, which the toolkit adds when an app sets gated or
			// auto_login, at a name under the community's domain.
			claims[0].derived, claims[0].from = true, "the community's domain for the gate"
		}
		// Every hostname beyond the primary that exists, derived ones claimed
		// exactly as if they had been written, because each becomes a site
		// address exactly as if it had been. A derived one is the claim most
		// likely to collide unseen: an app whose hostname is
		// talk-media.example.org clashes with talk's media hostname, and one
		// on join.example.org with an account app's join hostname, and
		// neither line in the file says so.
		for _, rh := range blueprints.RoleHostnames(c.cfg, app) {
			cl := claim{key: fmt.Sprintf("apps.%s.hostnames.%s", name, rh.Role), hostname: rh.Hostname, derived: rh.Derived}
			if rh.Derived {
				cl.from = "the app's own hostname"
				if rh.Role == blueprints.JoinRole {
					cl.from = "the community's domain"
				}
			}
			claims = append(claims, cl)
		}
		for _, cl := range claims {
			if cl.hostname == "" {
				continue
			}
			if earlier, taken := first[cl.hostname]; taken {
				said := fmt.Sprintf("is %q", cl.hostname)
				if cl.derived {
					said = fmt.Sprintf("is not declared, so it is derived as %q", cl.hostname)
				}
				c.refuse("duplicate-hostname", cl.key,
					"two apps claim the same hostname",
					"%s, which %s already claims. Every hostname becomes a site address in the gateway's Caddyfile, and Caddy refuses a configuration where two site blocks claim one address rather than choosing between them, so this takes every hostname in the deployment down rather than these two. Give each name to one app and one role; a media or join hostname can be chosen with hostnames.media or hostnames.join.",
					said, describe(earlier))
				continue
			}
			first[cl.hostname] = cl
		}
	}
}

// hostnameRoles refuses a hostname role the blueprint ships no snippet for.
//
// A role selects a Caddy snippet. A role nobody ships renders a host block
// importing a file that does not exist, so the gateway fails to load its whole
// configuration, taking every other hostname down with it. That is incoherent
// rather than risky.
func (c *checker) hostnameRoles() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		for _, role := range sortedKeys(app.Hostnames) {
			if blueprints.HasHostnameRole(app.Blueprint, role) {
				continue
			}
			c.refuse("unknown-hostname-role", fmt.Sprintf("apps.%s.hostnames.%s", name, role),
				"hostname role is not known to this blueprint",
				"names a hostname role %q, which the %s blueprint does not understand. A role selects the Caddy snippet that hostname gets, so an unknown one would import a file that does not exist and the gateway would fail to load at all. Roles for this blueprint: %s.",
				role, app.Blueprint, strings.Join(blueprints.HostnameRoles(app.Blueprint), ", "))
		}
	}
}

// configKeyIsNestedInAnEnvFile refuses a dotted key on a blueprint configured
// by environment.
//
// Env has no nesting. In ini and yaml a dot is a path and means something, so
// the same habit carried over here produces a variable called `a.b` that the
// application never reads and the shell may not even accept. It is refused
// rather than passed through because the likeliest reading is a typo for an
// underscore, and a typo that renders is the worst outcome available.
func (c *checker) configKeyIsNestedInAnEnvFile() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if blueprints.ConfigFormat(app.Blueprint) != blueprints.ConfigEnv {
			continue
		}
		for _, key := range sortedKeys(app.Config) {
			if !strings.Contains(key, ".") {
				continue
			}
			c.refuse("config-key-is-nested-in-an-env-file", fmt.Sprintf("apps.%s.config.%s", name, key),
				"config key has a dot in an env file",
				"is a dotted key, and %s is an env file. Env has no nesting, so a dot is a typo here rather than a path. Write the variable name the application reads, for example with underscores.",
				blueprints.ConfigFile(app.Blueprint))
		}
	}
}

// secretWords are the fragments of a key name that suggest a credential,
// matched against the lowercased name with `_`, `-` and `.` removed, so that
// SENDGRID_API_KEY, privateKey and private-key all read as the same word.
var secretWords = []string{"password", "secret", "token", "apikey", "privatekey"}

// configKeyLooksLikeASecret refuses a key named like a credential.
//
// paisans-stack.yaml is plaintext and meant to be read, committed and diffed. A
// passthrough map is exactly where somebody pastes an API token at the end of
// a long day. The check is by name and is deliberately narrow: it refuses the
// one thing it can see, and says so, rather than claiming to be a policy.
func (c *checker) configKeyLooksLikeASecret() {
	squash := strings.NewReplacer("_", "", "-", "", ".", "")
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		for _, key := range sortedKeys(app.Config) {
			squashed := squash.Replace(strings.ToLower(key))
			for _, word := range secretWords {
				if !strings.Contains(squashed, word) {
					continue
				}
				c.refuse("config-key-looks-like-a-secret", fmt.Sprintf("apps.%s.config.%s", name, key),
					"config key is named like a credential",
					"is named like a credential (it contains %q, ignoring case and the separators _ - and .). "+config.FileName+" is plaintext and meant to be committed, so a secret does not belong in it: put the value in secrets.enc.yaml. This check is by name and will not catch a credential named something else, so it is no substitute for looking.",
					word)
				break
			}
		}
	}
}

// composeSteeringPrefixes are the variable name prefixes refused on an env
// blueprint by configKeySteersCompose.
var composeSteeringPrefixes = []string{"COMPOSE_", "DOCKER_"}

// configKeySteersCompose refuses an env key that would configure compose
// rather than the application.
//
// apply runs `docker compose -f /srv/paisans/<token>/<app>/compose.yaml up -d`, and compose
// reads the .env beside that file for its own settings as well as handing it
// to the containers. Run with `docker compose config` on Docker Compose
// v5.4.0 (the operator's Mac, not a host): COMPOSE_PROJECT_NAME in that .env
// renamed the project and COMPOSE_PROFILES changed which services were
// active. That apply would then address containers under another project
// name than the ones it started before is reasoned, not run. DOCKER_HOST in
// the same .env did NOT redirect `docker compose ps` in that run; DOCKER_ is
// refused anyway as the docker CLI's own namespace, which no application
// variable should need, so the rule does not depend on knowing which of
// those compose honours from a file.
func (c *checker) configKeySteersCompose() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if blueprints.ConfigFormat(app.Blueprint) != blueprints.ConfigEnv {
			continue
		}
		for _, key := range sortedKeys(app.Config) {
			for _, prefix := range composeSteeringPrefixes {
				if !strings.HasPrefix(key, prefix) {
					continue
				}
				c.refuse("config-key-steers-compose", fmt.Sprintf("apps.%s.config.%s", name, key),
					"config key would steer compose itself",
					"starts with %s. When apply runs compose for this stack, compose reads the stack's %s for its own settings as well as handing it to the application, and a variable there can rename the project or change which services are active. That is compose's configuration, not the application's: it belongs in the toolkit, not in config.",
					prefix, blueprints.ConfigFile(app.Blueprint))
				break
			}
		}
	}
}

// configKeyAlreadyRendered refuses a passthrough key the stack already owns:
// one a template writes, one compose sets or reads, or one a template leaves
// out on purpose. Two sources of truth for one value is how a deployment ends
// up with a setting nobody can locate, and how the toolkit's own decisions get
// quietly undone.
//
// Render refuses the same keys, and keeps doing so, but a whole apply renders
// only in its pull stage, after init and host prepare have run. Whether a
// template writes a key can depend on other keys (a gated Mbin's template
// writes MBIN_USE_FEDERATION_ALLOW_LIST, an ungated one does not), so this
// renders the deployment rather than keeping a second list: with secrets
// generated in memory and thrown away, since which keys a template writes
// depends on the configuration and never on a secret's value.
func (c *checker) configKeyAlreadyRendered() {
	declared := false
	for _, name := range c.cfg.AppNames() {
		if len(c.cfg.Apps[name].Config) > 0 {
			declared = true
		}
	}
	if !declared {
		return
	}
	collisions, err := throwawayCollisions(c.cfg)
	if err != nil {
		// Generating a secret failed, which is the machine's trouble and not
		// this file's; render meets the same keys at apply.
		return
	}
	redundant := map[blueprints.RedundantKey]bool{}
	for _, k := range blueprints.RedundantGateConfig(c.cfg) {
		redundant[k] = true
	}
	for _, col := range collisions {
		detail := col.Detail
		if redundant[blueprints.RedundantKey{App: col.App, Key: col.Key}] {
			detail += " It repeats the value gating requires, so `paisans doctor --execute` removes it, and changes nothing else."
		}
		c.refuse("config-key-already-rendered", col.ConfigKey(),
			"config key is already rendered by the toolkit", "%s", detail)
	}
}

// throwaway is the secrets configKeyAlreadyRendered renders with, kept for
// the life of the process and only topped up: generating them is almost all
// of the rule's cost (an Mbin's OAuth key pair alone takes a few hundred
// milliseconds), and their values never matter. They never leave memory.
var throwaway struct {
	sync.Mutex
	secrets config.Secrets
}

// throwawayCollisions renders cfg's passthrough keys with the throwaway set,
// filled for cfg first. The lock covers the render too, which reads the set
// another check may be filling; a render takes milliseconds.
func throwawayCollisions(cfg *config.Config) ([]*render.ConfigCollision, error) {
	throwaway.Lock()
	defer throwaway.Unlock()
	if _, err := secretsgen.Fill(cfg, &throwaway.secrets); err != nil {
		return nil, err
	}
	return render.ConfigCollisions(cfg, &throwaway.secrets), nil
}

// adminGroupNotAdmins warns about an app that reads its administrators from
// a group other than admins or operators in a deployment that runs Pocket ID.
// The community's admins are the members of admins, and the operators of
// operators, which apply fills from the operators section and the operator
// reconciler checks; an app reading another group has administrators that
// neither the community nor the operators section decides.
func (c *checker) adminGroupNotAdmins() {
	if len(c.pocketIDApps()) == 0 {
		return
	}
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		spec, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name)
		if !ok {
			continue
		}
		admin, _ := spec.Groups(app)
		if admin == "" || admin == blueprints.AdminsGroup || admin == blueprints.OperatorsGroup {
			continue
		}
		c.warn("admin-group-not-admins", spec.AdminGroupSource(name),
			"admin group is neither admins nor operators",
			"is %s. The community's admins are the members of %s, and its operators of %s, so %s's administrators are a group neither one decides. Name %s or %s here, or accept that this app's administrators are chosen elsewhere.",
			admin, blueprints.AdminsGroup, blueprints.OperatorsGroup, name, blueprints.AdminsGroup, blueprints.OperatorsGroup)
	}
}

// oidcMemberGroupNotAName refuses a member_group or admin_group setting that
// is not a group name.
//
// A blank or non-string value is read as absent, and the blueprint's
// default holds, so the client is still restricted. But the operator wrote
// something, and what they wrote is not what the client admits; left alone,
// that reads as a restriction to a group that does not exist.
func (c *checker) oidcMemberGroupNotAName() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		spec, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name)
		if !ok {
			continue
		}
		for _, key := range []string{spec.MemberGroupSetting, spec.AdminGroupSetting} {
			if key == "" {
				continue
			}
			v, present := app.Settings[key]
			if !present {
				continue
			}
			if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
				continue
			}
			c.refuse("oidc-member-group-not-a-name", fmt.Sprintf("apps.%s.settings.%s", name, key),
				"oidc member group is not a group name",
				"is %v, which is not a group name. Name the identity provider group, or remove the key to take the blueprint's default. The client at Pocket ID admits exactly the groups named here, so a value that is not a name is a restriction to nothing.", v)
		}
		// Extra groups are created at Pocket ID by apply, so a name it
		// refuses would otherwise fail there, after validate passed.
		for _, group := range spec.ExtraGroups(app) {
			if pocketid.ValidGroupName(group) {
				continue
			}
			c.refuse("oidc-group-not-a-name", fmt.Sprintf("apps.%s.settings.%s", name, spec.ExtraGroupsSetting),
				"a group to create is not a group name",
				"%q is not a name Pocket ID takes for a group. A group name is 2 to 50 characters.", group)
		}
	}
}

// oidcMemberGroupDisagreesWithGate refuses an app using the gate whose client
// admits a different member group than the gate does.
//
// Two member groups decide who reads a gated app: the gate admits members
// (and admins, as every client does by default), and the app's client at
// Pocket ID admits the app's member group. When they differ,
// someone in one and not the other is let through the gate and refused by the
// app, or admitted by the app on a path the gate does not cover. Auto-login has
// the same split: it signs into the app whoever the gate admits, and the app's
// client refuses them. The gate's group is fixed, so this fires only when the
// app's was changed.
func (c *checker) oidcMemberGroupDisagreesWithGate() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.UsesGate() {
			continue
		}
		spec, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name)
		if !ok {
			continue
		}
		_, member := spec.Groups(app)
		if member == blueprints.MembersGroup {
			continue
		}
		c.refuse("oidc-member-group-disagrees-with-gate", fmt.Sprintf("apps.%s.settings.%s", name, spec.MemberGroupSetting),
			"member group differs between gate and client",
			"is %s, but the gate's member group is %s, and this app sets gated or auto_login. The gate admits one group and the app's client at Pocket ID admits the other, so the two disagree about who a member is. Remove the setting, so the app admits %s too.",
			member, blueprints.MembersGroup, blueprints.MembersGroup)
	}
}

// oidcClientUnrestricted refuses a gateable app whose client would admit
// every Pocket ID account.
//
// A blueprint that can sit behind the gate is member facing, and its client
// is the control that stops a person outside the community before the app
// sees them. Every such blueprint has a default member group today, so this is
// the floor under that: a blueprint added without one, or a change that empties
// one, is refused here rather than shipping an open client.
func (c *checker) oidcClientUnrestricted() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		spec, ok := blueprints.OIDCClient(app.Blueprint, app.Hostname, name)
		if !ok || !blueprints.GateFor(app.Blueprint).Gateable {
			continue
		}
		if _, member := spec.Groups(app); member != "" {
			continue
		}
		c.refuse("oidc-client-unrestricted", fmt.Sprintf("apps.%s", name),
			"oidc client admits anyone with an account",
			"names no member group, so its client at Pocket ID would admit anyone with an account there. A member facing app's client is restricted to the member group and admins; name one in apps.%s.settings.%s.", name, spec.MemberGroupSetting)
	}
}

// smtpOnAKindWithoutMail refuses an app level smtp block that nothing reads.
func (c *checker) smtpOnABlueprintWithoutMail() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if app.SMTP == nil || blueprints.SendsMail(app.Blueprint) {
			continue
		}
		c.refuse("smtp-on-a-blueprint-without-mail", fmt.Sprintf("apps.%s.smtp", name),
			"smtp is declared for a blueprint that sends no mail",
			"is declared, but %s does not read the toolkit's smtp settings, so this override would be ignored without a word. Remove it, or configure that application's mail where it keeps it.",
			app.Blueprint)
	}
}

// uptimeWithoutSMTP warns that a monitor with no mail server cannot email.
func (c *checker) uptimeWithoutSMTP() {
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Blueprint != config.BlueprintUptime || c.cfg.SMTPFor(name).Host != "" {
			continue
		}
		c.warn("uptime-without-smtp", fmt.Sprintf("apps.%s.smtp", name),
			"uptime monitor has no smtp host",
			"resolves no SMTP host, so no email channel in the monitor can send. It still checks and records incidents. Declare a top level smtp block, or one on this app, or set SMTP in the monitor's own settings page, which the toolkit then leaves alone.")
	}
}
