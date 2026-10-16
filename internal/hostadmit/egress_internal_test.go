package hostadmit

import (
	"strings"
	"testing"
)

// --mesh-lan opens the private network of the default route's interface,
// a /16 or narrower, and nothing else.
func TestMeshLAN(t *testing.T) {
	for _, c := range []struct{ dev, addr, want, refused string }{
		{"eth0", "192.168.1.20/24", "192.168.1.0/24", ""},
		{"eth0", "10.1.2.3/16", "10.1.0.0/16", ""},
		{"eth0", "172.16.5.9/20", "172.16.0.0/20", ""},
		{"eth0", "10.20.30.40/8", "", "not a private network of a /16 or narrower"},
		{"eth0", "172.31.0.1/12", "", "not a private network"},
		{"eth0", "203.0.113.10/24", "", "203.0.113.0/24, which is not a private network"},
		{"eth0", "100.64.1.2/24", "", "not a private network"},
		{"eth0", "192.168.1.20", "", "is not a prefix"},
		{"absent", "", "", "no default route"},
		{"", "", "", "no default route"},
		{"eth0", "", "", "no default route"},
		{"et\x1bh0", "8.8.8.8/24", "", `et\x1bh0`},
	} {
		got, err := meshLAN(c.dev, c.addr)
		switch {
		case c.refused == "" && (err != nil || got != c.want):
			t.Errorf("meshLAN(%q, %q) = %q, %v; want %q", c.dev, c.addr, got, err, c.want)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("meshLAN(%q, %q) = %q, %v; want a refusal saying %q", c.dev, c.addr, got, err, c.refused)
		case c.refused != "" && strings.Contains(err.Error(), "\x1b"):
			t.Errorf("meshLAN(%q, %q) echoes a control character: %q", c.dev, c.addr, err)
		}
	}
}

// The prefix --mesh-lan opens is the one the record carries, and without
// the flag the record carries none.
func TestPlanEgressRecordsTheMeshLAN(t *testing.T) {
	for flag, want := range map[bool]string{true: "192.168.1.0/24", false: ""} {
		h := newTestHost()
		h.set("passwd", "paisans-f2a9:x:1001:1001::/home/paisans-f2a9:/bin/bash")
		h.set("group", "paisans-f2a9")
		h.facts = append(h.facts, [2]string{"subuid", "paisans-f2a9:165536:65536"}, [2]string{"default-dev", "eth0"}, [2]string{"lan", "192.168.1.20/24"})
		var got string
		withParts(t, []part{{"egress", planEgress}, {"record", func(p *planner) error { got = p.record.MeshLAN; return nil }}})
		in := testInput(t)
		in.Flags.MeshLAN = flag
		if _, err := Build(h, in); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("--mesh-lan %v: the record's mesh_lan is %q, want %q", flag, got, want)
		}
	}
}
