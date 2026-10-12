package config

import (
	"strings"
	"testing"
)

const withConfigKeys = `apps:
  talk:
    kind: mbin
    gated: true
    config:
      # Federation on.
      KBIN_FEDERATION_ENABLED: true
      # The allow list, before the gate wrote it.
      MBIN_USE_FEDERATION_ALLOW_LIST: true
      KBIN_REGISTRATIONS_ENABLED: false
    settings:
      admin_group: admins
  blog:
    kind: writefreely
    # Only one.
    config:
      # Private.
      app.private: true
    settings:
      site_name: Blog
`

// One key goes with the comment above it, and every other line stays as it
// was; the last key takes config: with it.
func TestRemoveConfigKeyKeepsEveryOtherLine(t *testing.T) {
	out, err := removeConfigKey([]byte(withConfigKeys), "talk", "MBIN_USE_FEDERATION_ALLOW_LIST")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(withConfigKeys, "      # The allow list, before the gate wrote it.\n      MBIN_USE_FEDERATION_ALLOW_LIST: true\n", "", 1)
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}

	out, err = removeConfigKey([]byte(withConfigKeys), "blog", "app.private")
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Replace(withConfigKeys, "    # Only one.\n    config:\n      # Private.\n      app.private: true\n", "", 1)
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestRemoveConfigKeyRefusesWhatItCannotEditInPlace(t *testing.T) {
	for _, c := range []struct{ app, key, body string }{
		{"talk", "NOPE", withConfigKeys},
		{"nobody", "X", withConfigKeys},
		{"talk", "A", "apps:\n  talk:\n    config: {A: true}\n"},
		{"talk", "A", "apps:\n  talk:\n    config:\n      A:\n        - 1\n"},
	} {
		if _, err := removeConfigKey([]byte(c.body), c.app, c.key); err == nil {
			t.Errorf("apps.%s.config.%s in\n%s\nwas removed", c.app, c.key, c.body)
		}
	}
}
