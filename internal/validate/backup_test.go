package validate_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
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
