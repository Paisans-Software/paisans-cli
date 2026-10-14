package blueprints

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Every kind the configuration accepts has a record, and every record is a
// kind the configuration accepts, so a kind cannot be added to one list and
// answer zero to every question the other asks.
func TestEveryKindHasARecord(t *testing.T) {
	accepted := map[config.Kind]bool{}
	for _, kind := range Builtin() {
		accepted[kind] = true
		if _, ok := For(kind); !ok {
			t.Errorf("%s is accepted by the configuration but has no record", kind)
		}
	}
	for _, kind := range known() {
		if _, fromCatalog := Source(kind); fromCatalog {
			continue
		}
		if !accepted[kind] {
			t.Errorf("%s has a record but the configuration does not accept it", kind)
		}
	}
}

// Every record names its services, its port and its health route: a record
// without one renders a stack with nothing to run, nothing to route to, or
// nothing to watch.
func TestEveryRecordIsComplete(t *testing.T) {
	for _, kind := range known() {
		d := defs[kind]
		if len(d.Services) == 0 || d.Services[0].Name != "app" {
			t.Errorf("%s: the first service must be app, got %+v", kind, d.Services)
		}
		if d.Port == 0 {
			t.Errorf("%s has no port", kind)
		}
		if d.Health.Path == "" {
			t.Errorf("%s has no health route", kind)
		}
		if d.ConfigFile == "" || d.ConfigFormat == ConfigNone {
			t.Errorf("%s has no config file", kind)
		}
	}
}

// No two kinds rotate a secret of the same name by re-rendering their own
// stack, so the kind a key belongs to has one answer.
func TestSelfRotatedSecretsAreUnique(t *testing.T) {
	owner := map[string]config.Kind{}
	for _, kind := range known() {
		for _, key := range defs[kind].SelfRotated {
			if other, ok := owner[key]; ok {
				t.Errorf("%s is self rotated by both %s and %s", key, other, kind)
			}
			owner[key] = kind
			found := false
			for _, k := range defs[kind].AppSecrets {
				found = found || k == key
			}
			if !found {
				t.Errorf("%s rotates %s, which is not one of its app secrets", kind, key)
			}
		}
	}
}

// A kind with gate settings can read them back from a running app, and one
// with a live reader has settings for it to read.
func TestGateSettingsAndTheirReaderComeTogether(t *testing.T) {
	for _, kind := range known() {
		d := defs[kind]
		if (len(d.GateSettings) > 0) != (d.LiveSettings != nil) {
			t.Errorf("%s: %d gate settings but live reader %v", kind, len(d.GateSettings), d.LiveSettings)
		}
	}
}
