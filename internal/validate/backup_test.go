package validate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
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
		{"two names for one bucket", "backup-destination-same-place", func(b *config.Backup) {
			b.Destinations[1].Endpoint, b.Destinations[1].Bucket = b.Destinations[0].Endpoint, b.Destinations[0].Bucket
		}},
		{"one bucket, the endpoint spelled with a slash and capitals", "backup-destination-same-place", func(b *config.Backup) {
			b.Destinations[1].Endpoint, b.Destinations[1].Bucket = "HTTPS://S3.Example.net/", b.Destinations[0].Bucket
		}},
		{"endpoint without a scheme", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "s3.example.net" }},
		{"endpoint with a path", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://s3.example.net/bucket" }},
		{"endpoint with a fragment", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://s3.example.net#frag" }},
		{"endpoint with an empty query", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://s3.example.net?" }},
		{"endpoint with a port and no host", "backup-endpoint-not-a-url", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://:9000" }},
		{"endpoint with a user and password", "backup-endpoint-has-credentials", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://AKIAEXAMPLE:hunter2@s3.example.net" }},
		{"endpoint with a user alone", "backup-endpoint-has-credentials", func(b *config.Backup) { b.Destinations[0].Endpoint = "https://AKIAEXAMPLE@s3.example.net/" }},
		{"bucket too short", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "ab" }},
		{"bucket too long", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = strings.Repeat("a", 64) }},
		{"bucket with capitals", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "Backups" }},
		{"bucket with a space", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "bad bucket" }},
		{"bucket with a slash", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "backups/x" }},
		{"bucket starting with a hyphen", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "-backups" }},
		{"bucket ending with a dot", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "backups." }},
		{"bucket with two dots in a row", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "back..ups" }},
		{"bucket shaped like an address", "backup-bucket-name", func(b *config.Backup) { b.Destinations[0].Bucket = "192.168.5.4" }},
		{"prefix with a leading slash", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "/paisans" }},
		{"prefix with a trailing slash", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "paisans/" }},
		{"prefix with an empty segment", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "a//b" }},
		{"prefix climbing out", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "a/../b" }},
		{"prefix of a dot", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "./a" }},
		{"prefix with a space", "backup-prefix-not-a-path", func(b *config.Backup) { b.Destinations[0].Prefix = "my backups" }},
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

func TestOneBucketUnderTwoPrefixesIsTwoPlaces(t *testing.T) {
	r := withBackup(t, func(b *config.Backup) {
		b.Destinations[1].Endpoint, b.Destinations[1].Bucket = b.Destinations[0].Endpoint, b.Destinations[0].Bucket
		b.Destinations[1].Prefix = "second"
	})
	if refused(r, "backup-destination-same-place") {
		t.Fatalf("two prefixes in one bucket were refused: %v", r.Refusals())
	}
}

// Well formed values from real providers: none of these may be refused.
func TestBackupDestinationsThatAreFine(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(d *config.BackupDestination)
	}{
		{"an endpoint with a port", func(d *config.BackupDestination) { d.Endpoint = "http://10.44.0.5:3900" }},
		{"an endpoint with a trailing slash", func(d *config.BackupDestination) { d.Endpoint = "https://s3.example.net/" }},
		{"a bucket of three characters", func(d *config.BackupDestination) { d.Bucket = "abc" }},
		{"a bucket of sixty three", func(d *config.BackupDestination) { d.Bucket = strings.Repeat("a", 63) }},
		{"a bucket with dots and hyphens", func(d *config.BackupDestination) { d.Bucket = "paisans.backups-1" }},
		{"a nested prefix", func(d *config.BackupDestination) { d.Prefix = "communities/paisans_2026" }},
		{"a prefix with capitals and dots", func(d *config.BackupDestination) { d.Prefix = "Backups/v1.2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := withBackup(t, func(b *config.Backup) { tc.edit(&b.Destinations[0]) })
			if r.Refused() {
				t.Fatalf("refused: %v", r.Refusals())
			}
		})
	}
}

// A credential pasted into an endpoint is refused without being printed back:
// validate's output lands in terminals, CI logs and transcripts.
func TestAnEndpointCredentialIsNeverEchoed(t *testing.T) {
	for _, endpoint := range []string{
		"https://AKIAEXAMPLE:hunter2@s3.example.net",
		"https://AKIAEXAMPLE:hunter2@s3.example.net/bucket#x",
		"ftp://AKIAEXAMPLE:hunter2@",
	} {
		r := withBackup(t, func(b *config.Backup) { b.Destinations[0].Endpoint = endpoint })
		if !refused(r, "backup-endpoint-has-credentials") {
			t.Errorf("%s: not refused for its credentials: %v", endpoint, r.Findings)
		}
		for _, f := range r.Findings {
			if text := f.String(); strings.Contains(text, "hunter2") || strings.Contains(text, "AKIAEXAMPLE") {
				t.Errorf("%s: a finding repeats the credential: %s", endpoint, text)
			}
		}
	}
}

// A section written with no value, as a file holds it rather than as a test
// builds it: yaml.v3 decodes `backup:` to nil, the same as no section, and
// Load has to tell them apart. Written empty, backups are on and go nowhere;
// left out, they are off.
func TestABareSectionIsOnAndAnAbsentOneIsOff(t *testing.T) {
	valid, err := os.ReadFile(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(valid), "\nbackup:")
	if i < 0 {
		t.Fatal("valid.yaml has no backup section to replace")
	}
	base := string(valid[:i+1])
	for _, tc := range []struct {
		name, section string
		on            bool
	}{
		{"bare", "backup:\n", true},
		{"null", "backup: ~\n", true},
		{"empty", "backup: {}\n", true},
		{"absent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paisans-stack.yaml")
			if err := os.WriteFile(path, []byte(base+tc.section), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			r := validate.Check(cfg)
			if got := refused(r, "backup-without-destinations"); got != tc.on {
				t.Errorf("refused for no destinations = %v, want %v: %v", got, tc.on, r.Findings)
			}
			if got := warned(r, "backups-off"); got == tc.on {
				t.Errorf("warned backups-off = %v, want %v: %v", got, !tc.on, r.Findings)
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
		{"the walg image by tag", blueprints.WALGImage + ":17", false, false, false},
		{"the walg image by digest", blueprints.WALGImage + "@sha256:" + strings.Repeat("a", 64), false, false, false},
		{"a lookalike repository", blueprints.WALGImage + "-fork:17", false, false, true},
		{"the app opted out", "postgres:17-trixie", true, false, false},
		{"backups off", "postgres:17-trixie", false, true, false},
		{"no backup section", "postgres:17-trixie", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := withPinnedForum(t)
			app := cfg.Apps["forum"]
			app.Images = map[string]string{blueprints.PostgresService: tc.image}
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
		Blueprint: config.Blueprint("mbin"),
		Hostname:  "forum.example.org",
		Placement: config.Placement{Mode: config.PlacementPinned, Site: "watch"},
	}
	if !blueprints.Has(config.Blueprint("mbin"), blueprints.PostgresService) {
		t.Fatal("mbin no longer declares a postgres service; pick a blueprint that does")
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

func TestBackupWarnings(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name, rule string
		edit       func(cfg *config.Config)
	}{
		{"disabled", "backups-off", func(cfg *config.Config) { cfg.Backup.Enabled = &no }},
		{"no section", "backups-off", func(cfg *config.Config) { cfg.Backup = nil }},
		{"prune off", "backup-prune-off", func(cfg *config.Config) { cfg.Backup.Destinations[0].Prune = &no }},
		{"one destination", "backup-single-destination", func(cfg *config.Config) { cfg.Backup.Destinations = cfg.Backup.Destinations[:1] }},
		{"no uptime app", "backup-without-a-monitor", func(cfg *config.Config) {
			for name, a := range cfg.Apps {
				if a.Blueprint == config.BlueprintUptime {
					delete(cfg.Apps, name)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t, "valid")
			tc.edit(cfg)
			if r := validate.Check(cfg); !warned(r, tc.rule) {
				t.Fatalf("%s did not fire: %v", tc.rule, r.Findings)
			}
		})
	}
}

func TestDisabledBackupsWarnOnlyThatTheyAreDisabled(t *testing.T) {
	no := false
	cfg := load(t, "valid")
	cfg.Backup.Enabled = &no
	cfg.Backup.Destinations = cfg.Backup.Destinations[:1]
	for _, f := range validate.Check(cfg).Findings {
		if strings.HasPrefix(f.Rule, "backup") && f.Rule != "backups-off" {
			t.Errorf("a disabled section also warned %s", f.Rule)
		}
	}
}
