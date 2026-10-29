package blueprints

import (
	"strings"
	"testing"
)

// minimalV2 is minimal in the format that reads heartbeat, minimalV3 to
// minimalV5 in each format after it, minimalV5 the newest.
var (
	minimalV2 = strings.Replace(minimal, APIVersionV1, APIVersionV2, 1)
	minimalV3 = strings.Replace(minimal, APIVersionV1, APIVersionV3, 1)
	minimalV4 = strings.Replace(minimal, APIVersionV1, APIVersionV4, 1)
	minimalV5 = strings.Replace(minimal, APIVersionV1, APIVersion, 1)
)

// A blueprint that declares a heartbeat gets the monitor's name and a
// generated token in the site heartbeat token's shape, beside its own
// secrets, so init and secrets rotate need nothing of their own for it.
func TestABlueprintHeartbeatParses(t *testing.T) {
	d, err := Parse("example", []byte(minimalV2+"heartbeat: {name: settings}\nsecrets:\n  generate: [api_key]\n"), always)
	if err != nil {
		t.Fatal(err)
	}
	if d.Heartbeat == nil || d.Heartbeat.Name != "settings" {
		t.Fatalf("heartbeat %+v", d.Heartbeat)
	}
	if strings.Join(d.AppSecrets, ",") != "api_key,"+HeartbeatTokenKey {
		t.Errorf("app secrets %v", d.AppSecrets)
	}
	spec := d.Secrets[len(d.Secrets)-1]
	if spec.Name != HeartbeatTokenKey || spec.Format != FormatHex16 || spec.NeverRotate != "" || spec.Group != "" || spec.Rotate != nil {
		t.Errorf("token spec %+v", spec)
	}
}

// A blueprint with no heartbeat has no token, in either format.
func TestABlueprintWithoutHeartbeatHasNoToken(t *testing.T) {
	for _, doc := range []string{minimal, minimalV2} {
		d, err := Parse("example", []byte(doc), always)
		if err != nil {
			t.Fatal(err)
		}
		if d.Heartbeat != nil || len(d.AppSecrets) != 0 {
			t.Errorf("%+v %v", d.Heartbeat, d.AppSecrets)
		}
	}
}

// Each mistake in a heartbeat is refused by name when the blueprint is read.
func TestABlueprintHeartbeatMistakeIsRefused(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"in the first format", minimal + "heartbeat: {name: settings}\n", "heartbeat: needs apiVersion " + APIVersionV2},
		{"no name", minimalV2 + "heartbeat: {}\n", "heartbeat.name"},
		{"a name with capitals", minimalV2 + "heartbeat: {name: Settings}\n", "heartbeat.name"},
		{"a name with a dash", minimalV2 + "heartbeat: {name: \"a — b\"}\n", "heartbeat.name"},
		{"a name another check has", minimalV2 + "heartbeat: {name: public}\n", "another monitor"},
		{"the token declared again", minimalV2 + "heartbeat: {name: settings}\nsecrets:\n  generate: [heartbeat_token]\n", "secrets.generate"},
		{"an unknown key", minimalV2 + "heartbeat: {name: settings, every: 30}\n", "every"},
	}
	for _, c := range cases {
		_, err := Parse("example", []byte(c.doc), always)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

// Every format is read; any other is refused naming each.
func TestEveryBlueprintFormatIsRead(t *testing.T) {
	for _, doc := range []string{minimal, minimalV2, minimalV3, minimalV4, minimalV3 + "heartbeat: {name: settings}\nsecrets:\n  generate: [api_key]\n"} {
		if _, err := Parse("example", []byte(doc), always); err != nil {
			t.Error(err)
		}
	}
	_, err := Parse("example", []byte(strings.Replace(minimal, APIVersionV1, "paisans.blueprint/v9", 1)), always)
	if err == nil || !strings.Contains(err.Error(), APIVersionV1) || !strings.Contains(err.Error(), APIVersionV2) || !strings.Contains(err.Error(), APIVersionV3) || !strings.Contains(err.Error(), APIVersion) {
		t.Errorf("%v", err)
	}
}
