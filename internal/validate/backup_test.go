package validate_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// withBackup is the valid fixture with its backup section changed by edit.
func withBackup(t *testing.T, edit func(b *config.Backup)) validate.Result {
	t.Helper()
	cfg := load(t, "valid")
	edit(cfg.Backup)
	return validate.Check(cfg)
}

func refused(r validate.Result, rule string) bool {
	for _, f := range r.Refusals() {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func TestBackupRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		edit       func(b *config.Backup)
	}{
		{"no destinations", "backup-without-destinations", func(b *config.Backup) { b.Destinations = nil }},
		{"unknown schedule", "backup-schedule-unknown", func(b *config.Backup) { b.Schedule = "hourly" }},
		{"empty schedule", "backup-schedule-unknown", func(b *config.Backup) { b.Schedule = "" }},
		{"at is not a time", "backup-at-not-a-time", func(b *config.Backup) { b.At = "3am" }},
		{"at out of range", "backup-at-not-a-time", func(b *config.Backup) { b.At = "24:00" }},
		{"at without a leading zero", "backup-at-not-a-time", func(b *config.Backup) { b.At = "3:00" }},
		{"no endpoint", "backup-destination-incomplete", func(b *config.Backup) { b.Destinations[0].Endpoint = "" }},
		{"no bucket", "backup-destination-incomplete", func(b *config.Backup) { b.Destinations[0].Bucket = "" }},
		{"no region", "backup-destination-incomplete", func(b *config.Backup) { b.Destinations[0].Region = "" }},
		{"no name", "backup-destination-name", func(b *config.Backup) { b.Destinations[0].Name = "" }},
		{"a name that is no secrets key", "backup-destination-name", func(b *config.Backup) { b.Destinations[0].Name = "B2 main" }},
		{"same name twice", "backup-destination-duplicate", func(b *config.Backup) { b.Destinations[1].Name = b.Destinations[0].Name }},
		{"endpoint without a scheme", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "s3.example.net" }},
		{"endpoint with a path", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://s3.example.net/bucket" }},
		{"prune with nothing kept", "backup-keep-empty", func(b *config.Backup) { b.Destinations[0].Keep = config.Keep{} }},
		{"a negative tier", "backup-keep-negative", func(b *config.Backup) { b.Destinations[0].Keep.Weekly = -1 }},
		{"daily kept on a weekly schedule", "backup-keep-never-filled", func(b *config.Backup) { b.Schedule = config.ScheduleWeekly }},
		{"weekly kept on a monthly schedule", "backup-keep-never-filled", func(b *config.Backup) {
			b.Schedule = config.ScheduleMonthly
			for i := range b.Destinations {
				b.Destinations[i].Keep = config.Keep{Weekly: 4, Monthly: 6}
			}
		}},
		{"a negative upload limit", "backup-number-negative", func(b *config.Backup) { b.Destinations[0].UploadLimitKiB = -1 }},
		{"a negative verify fraction", "backup-number-negative", func(b *config.Backup) { b.Destinations[0].VerifyFraction = -1 }},
		{"a negative delta count", "backup-number-negative", func(b *config.Backup) { n := -1; b.Postgres.DeltaMaxSteps = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := withBackup(t, tc.edit); !refused(r, tc.rule) {
				t.Fatalf("%s did not fire: %v", tc.rule, r.Findings)
			}
		})
	}
}

func TestBackupAccepts(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name string
		edit func(b *config.Backup)
	}{
		{"every tier on a daily schedule", func(b *config.Backup) {
			b.Destinations[0].Keep = config.Keep{Daily: 7, Weekly: 4, Monthly: 12, Yearly: 3}
		}},
		{"only yearly on a yearly schedule", func(b *config.Backup) {
			b.Schedule = config.ScheduleYearly
			for i := range b.Destinations {
				b.Destinations[i].Keep = config.Keep{Yearly: 5}
			}
		}},
		{"an empty keep with prune off", func(b *config.Backup) {
			b.Destinations[1].Keep = config.Keep{}
			b.Destinations[1].Prune = &no
		}},
		{"a plain http endpoint with a port", func(b *config.Backup) { b.Destinations[1].Endpoint = "http://10.0.0.5:3900" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := withBackup(t, tc.edit); r.Refused() {
				t.Fatalf("refused: %v", r.Refusals())
			}
		})
	}
}

func TestADisabledSectionIsNotCheckedFurther(t *testing.T) {
	no := false
	r := withBackup(t, func(b *config.Backup) { b.Enabled = &no; b.Destinations = nil; b.Schedule = "hourly" })
	if r.Refused() {
		t.Fatalf("a disabled section was refused: %v", r.Refusals())
	}
}

func TestAnEmptySectionIsEnabledAndRefusedForNoDestinations(t *testing.T) {
	cfg := load(t, "valid")
	cfg.Backup = &config.Backup{}
	if r := validate.Check(cfg); !refused(r, "backup-without-destinations") {
		t.Fatalf("findings: %v", r.Findings)
	}
}

func TestNoSectionIsNotRefused(t *testing.T) {
	cfg := load(t, "valid")
	cfg.Backup = nil
	if r := validate.Check(cfg); r.Refused() {
		t.Fatalf("a deployment without backups was refused: %v", r.Refusals())
	}
}
func TestPinnedPostgresWithoutWALG(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name      string
		image     string
		appOff    bool
		backupOff bool
		refuse    bool
	}{
		{"another image", "postgres:17-trixie", false, false, true},
		{"the walg image by tag", kinds.WALGImage + ":17", false, false, false},
		{"the walg image by digest", kinds.WALGImage + "@sha256:" + strings.Repeat("a", 64), false, false, false},
		{"a lookalike repository", kinds.WALGImage + "-fork:17", false, false, true},
		{"the app opted out", "postgres:17-trixie", true, false, false},
		{"backups off", "postgres:17-trixie", false, true, false},
		{"no backup section", "postgres:17-trixie", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := withPinnedForum(t)
			app := cfg.Apps["forum"]
			app.Images = map[string]string{kinds.PostgresService: tc.image}
			if tc.appOff {
				app.Backup = &no
			}
			cfg.Apps["forum"] = app
			if tc.backupOff {
				cfg.Backup.Enabled = &no
			}
			if tc.name == "no backup section" {
				cfg.Backup = nil
			}
			if got := refused(validate.Check(cfg), "pinned-postgres-without-walg"); got != tc.refuse {
				t.Fatalf("refused = %v, want %v", got, tc.refuse)
			}
		})
	}
}

// withPinnedForum is the valid fixture plus an Mbin pinned to watch, which
// runs a Postgres of its own.
func withPinnedForum(t *testing.T) *config.Config {
	t.Helper()
	cfg := load(t, "valid")
	cfg.Apps["forum"] = config.App{
		Kind:      config.KindMbin,
		Hostname:  "forum.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"},
	}
	if !kinds.Has(config.KindMbin, kinds.PostgresService) {
		t.Fatal("mbin no longer declares a postgres service; pick a kind that does")
	}
	return cfg
}

func TestAnExcludedAppIsWarnedAbout(t *testing.T) {
	no := false
	cfg := withPinnedForum(t)
	app := cfg.Apps["forum"]
	app.Backup = &no
	cfg.Apps["forum"] = app
	if !warned(validate.Check(cfg), "backup-app-excluded") {
		t.Fatal("no warning for an app opted out of backups")
	}
}

func warned(r validate.Result, rule string) bool {
	for _, f := range r.Findings {
		if f.Level == validate.Warn && f.Rule == rule {
			return true
		}
	}
	return false
}
