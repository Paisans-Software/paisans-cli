package validate

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// destinationName is a destination name usable as a secrets key segment,
// backup.destinations.<name>.access_key_id, and as an object prefix.
var destinationName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// clock is HH:MM, 00:00 to 23:59.
var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// backup checks the backup section (README, "Backup and restore";
// docs/specs/2026-10-11-backups.md, "Validation").
func (c *checker) backup() {
	b := c.cfg.Backup
	if b == nil {
		c.warn("backups-off", "backup",
			"backups are off",
			"is missing. Nothing in this deployment is backed up: not its databases, its files, its buckets or its configuration. Add a backup section, with a schedule and at least one destination, to turn them on; the next apply does the rest.")
		return
	}
	if !b.On() {
		c.warn("backups-off", "backup.enabled",
			"backups are off",
			"is false. Nothing in this deployment is backed up: not its databases, its files, its buckets or its configuration.")
		return
	}
	c.pinnedPostgresWithoutWALG()
	c.excludedApps()
	c.backupMonitor()
	if !scheduleKnown(b.Schedule) {
		c.refuse("backup-schedule-unknown", "backup.schedule",
			"schedule is not daily, weekly, monthly or yearly",
			"is %q. It is how often a run happens: daily, weekly, monthly or yearly.", b.Schedule)
	}
	if !clock.MatchString(b.At) {
		c.refuse("backup-at-not-a-time", "backup.at",
			"at is not a time of day",
			"is %q. It is when a run starts, as HH:MM in UTC, Eg: \"03:00\". Quote it: unquoted, YAML may read it as a number.", b.At)
	}
	if b.Postgres.DeltaMaxSteps != nil && *b.Postgres.DeltaMaxSteps < 0 {
		c.refuse("backup-number-negative", "backup.postgres.delta_max_steps",
			"delta_max_steps is negative",
			"is %d. It counts delta backups between full ones, 0 or more.", *b.Postgres.DeltaMaxSteps)
	}
	if len(b.Destinations) == 0 {
		c.refuse("backup-without-destinations", "backup.destinations",
			"backups are on and go nowhere",
			"is empty. Declare at least one S3 compatible destination, with a name, endpoint, region and bucket, or turn backups off with `enabled: false`.")
		return
	}
	seen := map[string]bool{}
	for i, d := range b.Destinations {
		key := fmt.Sprintf("backup.destinations[%d]", i)
		c.destination(key, d, b.Schedule, seen)
	}
	if len(b.Destinations) == 1 {
		c.warn("backup-single-destination", "backup.destinations",
			"backups go to one place",
			"has one destination, so one provider is one failure domain: losing its account, its region or its bucket loses every backup. A second destination with another provider, or on hardware at home, removes that.")
	}
}

func (c *checker) destination(key string, d config.BackupDestination, schedule config.Schedule, seen map[string]bool) {
	if !destinationName.MatchString(d.Name) {
		c.refuse("backup-destination-name", key+".name",
			"destination name is not a lowercase name",
			"is %q. A destination's name is part of its secrets' keys, backup.destinations.<name>.access_key_id, so it is lowercase letters, digits and hyphens, starting with a letter.", d.Name)
	} else if seen[d.Name] {
		c.refuse("backup-destination-duplicate", key+".name",
			"two destinations share a name",
			"is %q, which an earlier destination already uses. Each destination's credentials and retention are kept under its name, so names are unique.", d.Name)
	}
	seen[d.Name] = true
	var missing []string
	for _, f := range []struct{ name, value string }{{"endpoint", d.Endpoint}, {"region", d.Region}, {"bucket", d.Bucket}} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		c.refuse("backup-destination-incomplete", key,
			"destination lacks an endpoint, region or bucket",
			"has no %s. An S3 destination needs all three: the endpoint URL, the region the provider names (Garage's is its own s3_region), and the bucket.", strings.Join(missing, ", "))
	}
	if d.Endpoint != "" {
		u, err := url.Parse(d.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			c.refuse("backup-endpoint-not-a-url", key+".endpoint",
				"endpoint is not an S3 endpoint URL",
				"is %q. It is the provider's S3 endpoint, scheme and host only, Eg: https://s3.us-west-004.backblazeb2.com. The bucket goes in bucket, not in the URL.", d.Endpoint)
		}
	}
	tiers := d.Keep.Tiers()
	// config.Schedules, not the map, sets the order, so findings come out
	// the same way every run (TestFindingsAreDeterministic).
	for _, tier := range config.Schedules {
		if n := tiers[tier]; n < 0 {
			c.refuse("backup-keep-negative", fmt.Sprintf("%s.keep.%s", key, tier),
				"a keep tier is negative",
				"is %d. It is how many of that period's newest backup survive, 0 or more.", n)
		}
	}
	if d.Prunes() && d.Keep.Empty() {
		c.refuse("backup-keep-empty", key+".keep",
			"prune is on and nothing is kept",
			"is empty while prune is on, which would delete every backup. Declare how many of each period to keep, Eg: keep: { daily: 7, weekly: 4 }, or set prune: false.")
	}
	if !d.Prunes() {
		c.warn("backup-prune-off", key+".prune",
			"a destination is never pruned",
			"is false, so nothing in %s is ever deleted and its bucket only grows.", d.Name)
	}
	if scheduleKnown(schedule) {
		for _, tier := range config.Schedules {
			if n := tiers[tier]; n > 0 && finer(tier, schedule) {
				c.refuse("backup-keep-never-filled", fmt.Sprintf("%s.keep.%s", key, tier),
					"keeps a period shorter than the schedule",
					"keeps %d %s backups, but the schedule is %s, so there is never more than one backup per %s. Remove keep.%s, or run more often.", n, tier, schedule, schedulePeriod(schedule), tier)
			}
		}
	}
	for _, f := range []struct {
		name string
		n    int
	}{{"verify_fraction", d.VerifyFraction}, {"upload_limit_kib", d.UploadLimitKiB}} {
		if f.n < 0 {
			c.refuse("backup-number-negative", key+"."+f.name,
				"a destination number is negative",
				"is %d. It is 0 for the default, or a positive number.", f.n)
		}
	}
}

func scheduleKnown(s config.Schedule) bool {
	for _, known := range config.Schedules {
		if s == known {
			return true
		}
	}
	return false
}

// finer reports whether tier is a shorter period than schedule.
func finer(tier, schedule config.Schedule) bool {
	index := func(s config.Schedule) int {
		for i, known := range config.Schedules {
			if s == known {
				return i
			}
		}
		return -1
	}
	return index(tier) < index(schedule)
}

func schedulePeriod(s config.Schedule) string {
	return map[config.Schedule]string{config.ScheduleDaily: "day", config.ScheduleWeekly: "week", config.ScheduleMonthly: "month", config.ScheduleYearly: "year"}[s]
}

// pinnedPostgresWithoutWALG refuses a pinned app whose Postgres image is not
// the postgres-walg image while backups are on: that database would have no
// backup, and nothing else would say so. The app either runs the image or
// opts out with `backup: false`, which is warned about.
func (c *checker) pinnedPostgresWithoutWALG() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		if !app.BacksUp() || app.Placement.Mode != config.PlacementPinned || !kinds.Has(app.Kind, kinds.PostgresService) {
			continue
		}
		image := app.Images[kinds.PostgresService]
		if image == "" || imageRepository(image) == kinds.WALGImage {
			continue
		}
		c.refuse("pinned-postgres-without-walg", fmt.Sprintf("apps.%s.images.postgres", name),
			"this Postgres would have no backup",
			"is %s, which carries no WAL-G, so while backups are on this database would not be backed up. Use %s:<major>, or leave images.postgres unset, or opt the app out with apps.%s.backup: false.", image, kinds.WALGImage, name)
	}
}

// excludedApps warns about every app opted out of backups, on every run.
func (c *checker) excludedApps() {
	for _, name := range c.cfg.AppNames() {
		if !c.cfg.Apps[name].BacksUp() {
			c.warn("backup-app-excluded", fmt.Sprintf("apps.%s.backup", name),
				"this app is not backed up",
				"is false: nothing %s owns, its database, files or buckets, is backed up.", name)
		}
	}
}

// imageRepository is an image reference without its tag or digest.
func imageRepository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// backupMonitor warns when nothing would page anyone about a failed run: the
// runner reports to the deployment's uptime monitor, and without one a
// failure is silent.
func (c *checker) backupMonitor() {
	for _, name := range c.cfg.AppNames() {
		if c.cfg.Apps[name].Kind == config.KindUptime {
			return
		}
	}
	c.warn("backup-without-a-monitor", "backup",
		"no monitor to report failed backups to",
		"is on, but the deployment runs no uptime app, so a failed backup run is reported to nobody. Declare one (README, \"Monitoring\").")
}
