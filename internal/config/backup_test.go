package config_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

const backupSection = `backup:
  schedule: daily
  at: "03:00"
  destinations:
    - name: b2
      endpoint: https://s3.us-west-004.backblazeb2.com
      region: us-west-004
      bucket: example-backups
      keep: { daily: 7, weekly: 3 }
    - name: home
      endpoint: https://s3.home.example.org
      region: garage
      bucket: paisans
      prune: false
`

func TestLoadReadsTheBackupSection(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+backupSection))
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Backup
	if b == nil || !b.On() || b.Schedule != config.ScheduleDaily || b.At != "03:00" {
		t.Fatalf("backup = %+v", b)
	}
	if len(b.Destinations) != 2 || b.Destinations[0].Name != "b2" {
		t.Fatalf("destinations out of order: %+v", b.Destinations)
	}
	if !b.Destinations[0].Prunes() || b.Destinations[1].Prunes() {
		t.Error("prune defaults to true and an explicit false is kept")
	}
	if got := b.Destinations[0].Keep.Tiers(); got[config.ScheduleDaily] != 7 || got[config.ScheduleWeekly] != 3 || len(got) != 2 {
		t.Errorf("tiers = %v", got)
	}
	if !b.Destinations[1].Keep.Empty() {
		t.Error("an absent keep is empty")
	}
}

func TestBackupIsOnUnlessExplicitlyOff(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		present, on   bool
	}{
		{"absent", "", false, false},
		{"empty", "backup: {}\n", true, true},
		{"no value", "backup:\n", true, true},
		{"explicit true", "backup:\n  enabled: true\n", true, true},
		{"explicit false", "backup:\n  enabled: false\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(write(t, minimal+tc.section))
			if err != nil {
				t.Fatal(err)
			}
			if (cfg.Backup != nil) != tc.present || cfg.Backup.On() != tc.on {
				t.Fatalf("present %v on %v, want %v %v", cfg.Backup != nil, cfg.Backup.On(), tc.present, tc.on)
			}
		})
	}
}

func TestAnAppBacksUpUnlessItSaysNot(t *testing.T) {
	body := strings.Replace(minimal, "    blueprint: ", "    backup: false\n    blueprint: ", 1)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	off := 0
	for _, a := range cfg.Apps {
		if !a.BacksUp() {
			off++
		}
	}
	if off != 1 {
		t.Fatalf("%d apps opted out, want 1", off)
	}
}

func TestAnUnknownBackupKeyIsRefused(t *testing.T) {
	_, err := config.Load(write(t, minimal+"backup:\n  schedul: daily\n"))
	if err == nil || !strings.Contains(err.Error(), "schedul") {
		t.Fatalf("Load: %v", err)
	}
}
