package blueprints

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// Every blueprint the configuration accepts has a record, and every record is a
// blueprint the configuration accepts, so a blueprint cannot be added to one
// list and answer zero to every question the other asks.
func TestEveryBlueprintHasARecord(t *testing.T) {
	accepted := map[config.Blueprint]bool{}
	for _, blueprint := range Builtin() {
		accepted[blueprint] = true
		if _, ok := For(blueprint); !ok {
			t.Errorf("%s is accepted by the configuration but has no record", blueprint)
		}
	}
	for _, blueprint := range known() {
		if _, fromCatalog := Source(blueprint); fromCatalog {
			continue
		}
		if !accepted[blueprint] {
			t.Errorf("%s has a record but the configuration does not accept it", blueprint)
		}
	}
}

// Every record names its services, its port and its health route: a record
// without one renders a stack with nothing to run, nothing to route to, or
// nothing to watch.
func TestEveryRecordIsComplete(t *testing.T) {
	for _, blueprint := range known() {
		d := defs[blueprint]
		if len(d.Services) == 0 || d.Services[0].Name != "app" {
			t.Errorf("%s: the first service must be app, got %+v", blueprint, d.Services)
		}
		if d.Port == 0 {
			t.Errorf("%s has no port", blueprint)
		}
		if d.Health.Path == "" {
			t.Errorf("%s has no health route", blueprint)
		}
		if d.ConfigFile == "" || d.ConfigFormat == ConfigNone {
			t.Errorf("%s has no config file", blueprint)
		}
	}
}

// No two blueprints rotate a secret of the same name by re-rendering their own
// stack, so the blueprint a key belongs to has one answer.
func TestSelfRotatedSecretsAreUnique(t *testing.T) {
	owner := map[string]config.Blueprint{}
	for _, blueprint := range known() {
		for _, key := range defs[blueprint].SelfRotated {
			if other, ok := owner[key]; ok {
				t.Errorf("%s is self rotated by both %s and %s", key, other, blueprint)
			}
			owner[key] = blueprint
			found := false
			for _, k := range defs[blueprint].AppSecrets {
				found = found || k == key
			}
			if !found {
				t.Errorf("%s rotates %s, which is not one of its app secrets", blueprint, key)
			}
		}
	}
}

// A blueprint with gate settings can read them back from a running app, and one
// with a live reader has settings for it to read.
func TestGateSettingsAndTheirReaderComeTogether(t *testing.T) {
	for _, blueprint := range known() {
		d := defs[blueprint]
		if (len(d.GateSettings) > 0) != (d.LiveSettings != nil) {
			t.Errorf("%s: %d gate settings but live reader %v", blueprint, len(d.GateSettings), d.LiveSettings)
		}
	}
}
