package config

// Backup is what paisans backup does: how often, and where to. See
// docs/specs/2026-10-11-backups.md. It is a pointer on Config so that a
// declaration without the section can be told from one that has it: no
// section means no backups, and a section without `enabled` means backups on.
type Backup struct {
	// Enabled is nil when not written, which is on. Only an explicit false
	// turns backups off.
	Enabled  *bool    `yaml:"enabled"`
	Schedule Schedule `yaml:"schedule"`
	// At is when a run starts, HH:MM in UTC.
	At       string         `yaml:"at"`
	Postgres BackupPostgres `yaml:"postgres"`
	// Destinations are written in order, and the first is the primary: the
	// one Postgres streams its write ahead log to. The others receive copies.
	Destinations []BackupDestination `yaml:"destinations"`
}

// Schedule is how often a run happens. It is not retention: Keep says how
// many of each period's newest backup survive.
type Schedule string

const (
	ScheduleDaily   Schedule = "daily"
	ScheduleWeekly  Schedule = "weekly"
	ScheduleMonthly Schedule = "monthly"
	ScheduleYearly  Schedule = "yearly"
)

// Schedules are the schedules, finest first.
var Schedules = []Schedule{ScheduleDaily, ScheduleWeekly, ScheduleMonthly, ScheduleYearly}

type BackupPostgres struct {
	// DeltaMaxSteps is how many WAL-G delta backups run between full base
	// backups. nil takes the default phase 2 sets.
	DeltaMaxSteps *int `yaml:"delta_max_steps"`
}

// BackupDestination is one S3 compatible bucket. Its credentials are
// secrets, under backup.destinations.<name>.
type BackupDestination struct {
	Name     string `yaml:"name"`
	Endpoint string `yaml:"endpoint"`
	Region   string `yaml:"region"`
	Bucket   string `yaml:"bucket"`
	Prefix   string `yaml:"prefix"`
	// Prune is nil when not written, which is true: retention deletes what
	// Keep no longer covers. Declaring Keep is the approval for those
	// deletions.
	Prune *bool `yaml:"prune"`
	Keep  Keep  `yaml:"keep"`
	// VerifyFraction is n in restic's --read-data-subset=k/n, 0 for the
	// default phase 5 sets.
	VerifyFraction int `yaml:"verify_fraction"`
	// UploadLimitKiB caps upload bandwidth; 0 is unlimited.
	UploadLimitKiB int `yaml:"upload_limit_kib"`
}

// Keep is how many of each period's newest backup survive, restic's own model
// (restic forget --keep-daily and the rest).
type Keep struct {
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
	Yearly  int `yaml:"yearly"`
}

// On reports whether backups run. A nil section is off, as is an explicit
// `enabled: false`; validate warns about either on every run.
func (b *Backup) On() bool { return b != nil && (b.Enabled == nil || *b.Enabled) }

// Prunes reports whether retention deletes from this destination.
func (d BackupDestination) Prunes() bool { return d.Prune == nil || *d.Prune }

// Empty reports whether no tier keeps anything.
func (k Keep) Empty() bool { return len(k.Tiers()) == 0 }

// Tiers is each tier that keeps something, keyed by its period.
func (k Keep) Tiers() map[Schedule]int {
	out := map[Schedule]int{}
	for s, n := range map[Schedule]int{ScheduleDaily: k.Daily, ScheduleWeekly: k.Weekly, ScheduleMonthly: k.Monthly, ScheduleYearly: k.Yearly} {
		if n != 0 {
			out[s] = n
		}
	}
	return out
}

// BacksUp reports whether what the app owns is backed up. Only an explicit
// `backup: false` opts it out.
func (a App) BacksUp() bool { return a.Backup == nil || *a.Backup }
