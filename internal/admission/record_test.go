package admission_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/admission"
)

// host is a host's files, read as the deployment's user reads them.
type host struct {
	files map[string]string
	err   error
	read  []string
}

func (h *host) ReadFile(path string) (string, bool, error) {
	h.read = append(h.read, path)
	if h.err != nil {
		return "", false, h.err
	}
	c, ok := h.files[path]
	return c, ok, nil
}

func encode(t *testing.T, r admission.Record) []byte {
	t.Helper()
	b, err := admission.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var at = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// The record admit writes for the home-a request, with both grants given:
// its golden file pins every field and its order.
func TestRecordGolden(t *testing.T) {
	req := build(t, "home-a")
	rec := admission.NewRecord(req, admission.Grants{Watchdog: true, PublicPorts: true}, admission.Digest(admission.Encode(req)), at)
	rec.DataDir = "/srv/data/paisans-f2a9"
	rec.MeshLAN = "192.168.1.0/24"
	rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic", AdminTCP: "localhost:2019"}
	rec.SystemDockerDisabled = true
	got := encode(t, rec)
	path := filepath.Join("testdata", "admitted.f2a9.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	back, err := admission.ParseRecord(got, "f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if string(encode(t, back)) != string(got) {
		t.Errorf("read back differently:\n%s", encode(t, back))
	}
}

// What NewRecord derives: ports from the request and the grants,
// forwarding from the endpoint, the time in UTC, nothing given that the
// owner did not give.
func TestNewRecord(t *testing.T) {
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at)
	if rec.Grants != (admission.Grants{Watchdog: true}) || !rec.Forwarding || rec.User != "paisans-f2a9" {
		t.Errorf("got %+v", rec)
	}
	if rec.Ports.SSH != 22 || rec.Ports.WireGuard != 51820 || len(rec.Ports.Public) != 0 {
		t.Errorf("ports %+v: 80 and 443 opened without public_ports", rec.Ports)
	}
	if rec.AdmittedAt != "2026-10-15T10:00:00Z" {
		t.Errorf("admitted_at %q, want UTC", rec.AdmittedAt)
	}
	open := admission.NewRecord(build(t, "watch"), admission.Grants{PublicPorts: true}, "", at)
	if len(open.Ports.Public) != 2 || open.Ports.Public[0] != 80 || open.Ports.Public[1] != 443 {
		t.Errorf("public ports %v", open.Ports.Public)
	}
	if store := admission.NewRecord(build(t, "store"), admission.Grants{}, "", at); store.Forwarding || store.Ports.WireGuard != 0 {
		t.Errorf("store: %+v", store)
	}
}

// Read reads /etc/paisans/admitted.<token>.json through the transport it is
// given, with no sudo, and says when there is none.
func TestRead(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "admitted.f2a9.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := &host{files: map[string]string{"/etc/paisans/admitted.f2a9.json": string(data)}}
	rec, err := admission.Read(h, "f2a9")
	if err != nil {
		t.Fatal(err)
	}
	if rec.User != "paisans-f2a9" || !rec.Grants.Watchdog || rec.WebServer == nil || rec.WebServer.AdminTCP != "localhost:2019" {
		t.Errorf("got %+v", rec)
	}
	if len(h.read) != 1 || h.read[0] != "/etc/paisans/admitted.f2a9.json" {
		t.Errorf("read %q", h.read)
	}
	if _, err := admission.Read(&host{files: map[string]string{}}, "f2a9"); !errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("no record: %v", err)
	}
	down := errors.New("ssh: connect to host home-a.example.org port 22: Operation timed out")
	if _, err := admission.Read(&host{err: down}, "f2a9"); !errors.Is(err, down) || errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("a failed read: %v", err)
	}
}

// A record is read as strictly as a request: another version, a field
// missing or unknown, another deployment's, or a user paisans refuses.
// What a refusal echoes from the file is quoted.
func TestParseRecordRefuses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "admitted.f2a9.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := string(data)
	digest := admission.Digest(admission.Encode(build(t, "home-a")))
	for _, c := range []struct {
		name, data, token, want string
	}{
		{"not JSON", "{", "f2a9", "it is not JSON"},
		{"another version", strings.Replace(g, `"version": 1`, `"version": 2`, 1), "f2a9", `version: "2", and this paisans reads version 1`},
		{"no version", strings.Replace(g, `"version": 1,`, ``, 1), "f2a9", "version: missing"},
		{"a missing field", strings.Replace(g, `"forwarding": true,`, ``, 1), "f2a9", "forwarding: missing"},
		{"an unknown field", strings.Replace(g, `"forwarding": true,`, `"forwarding": true, "sudo": true,`, 1), "f2a9", "unknown field"},
		{"another token", g, "0a0b", `is not a deployment whose token is "0a0b"`},
		{"a user paisans refuses", strings.Replace(g, `"user": "paisans-f2a9"`, `"user": "a;b"`, 1), "f2a9", `user: "a;b"`},
		{"a digest of the old form", strings.Replace(g, `"digest": "`+digest+`"`, `"digest": "b1fb-1e05-4815"`, 1), "f2a9", `digest: "b1fb-1e05-4815"`},
		{"a digest not as written", strings.Replace(g, `"digest": "`+digest+`"`, `"digest": "`+strings.ToUpper(digest)+`"`, 1), "f2a9", `digest: "` + strings.ToUpper(digest) + `"`},
		{"a time that is not RFC 3339 in UTC", strings.Replace(g, `2026-10-15T10:00:00Z`, "yesterday noon", 1), "f2a9", `admitted_at:`},
		{"a second document", g + g, "f2a9", "it is not JSON"},
		{"root", strings.Replace(g, `"user": "paisans-f2a9"`, `"user": "root"`, 1), "f2a9", "user: root is never a deployment's user"},
		{"another deployment's user", strings.Replace(g, `"user": "paisans-f2a9"`, `"user": "paisans-0a0b"`, 1), "f2a9", "user: paisans-0a0b is another deployment's name"},
		{"public ports without the grant", strings.Replace(g, `"public_ports": true`, `"public_ports": false`, 1), "f2a9", "ports.public: [80 443], and public_ports was not granted, so it is []"},
		{"80 alone", strings.Replace(g, "\"public\": [\n      80,\n      443\n    ]", "\"public\": [\n      80\n    ]", 1), "f2a9", "ports.public: [80], and public_ports was granted, so it is [80 443]"},
		{"ssh among the public ports", strings.Replace(g, "\"public\": [\n      80,\n      443\n    ]", "\"public\": [\n      22,\n      80,\n      443\n    ]", 1), "f2a9", "ports.public: [22 80 443]"},
		{"no public ports with the grant", strings.Replace(g, "\"public\": [\n      80,\n      443\n    ]", "\"public\": []", 1), "f2a9", "ports.public: [], and public_ports was granted"},
		{"a relative data_dir", strings.Replace(g, `"data_dir": "/srv/data/paisans-f2a9"`, `"data_dir": "srv/data/paisans-f2a9"`, 1), "f2a9", `data_dir: "srv/data/paisans-f2a9" is not`},
		{"a data_dir not clean", strings.Replace(g, `"data_dir": "/srv/data/paisans-f2a9"`, `"data_dir": "/srv/../data/paisans-f2a9"`, 1), "f2a9", `data_dir: "/srv/../data/paisans-f2a9" is not`},
		{"a data_dir of another token", strings.Replace(g, `"data_dir": "/srv/data/paisans-f2a9"`, `"data_dir": "/srv/data/paisans-0a0b"`, 1), "f2a9", `data_dir: "/srv/data/paisans-0a0b" is not`},
		{"a data_dir with a control character", strings.Replace(g, `"data_dir": "/srv/data/paisans-f2a9"`, `"data_dir": "/srv/da\nta/paisans-f2a9"`, 1), "f2a9", `data_dir: "/srv/da\nta/paisans-f2a9" is not`},
		{"a public mesh_lan", strings.Replace(g, `"mesh_lan": "192.168.1.0/24"`, `"mesh_lan": "8.8.8.0/24"`, 1), "f2a9", `mesh_lan: "8.8.8.0/24" is not`},
		{"a mesh_lan wider than a /16", strings.Replace(g, `"mesh_lan": "192.168.1.0/24"`, `"mesh_lan": "10.0.0.0/8"`, 1), "f2a9", `mesh_lan: "10.0.0.0/8" is not`},
		{"a mesh_lan not masked", strings.Replace(g, `"mesh_lan": "192.168.1.0/24"`, `"mesh_lan": "192.168.1.20/24"`, 1), "f2a9", `mesh_lan: "192.168.1.20/24" is not`},
		{"a mesh_lan of one address", strings.Replace(g, `"mesh_lan": "192.168.1.0/24"`, `"mesh_lan": "192.168.1.1"`, 1), "f2a9", `mesh_lan: "192.168.1.1" is not`},
		{"no mesh_subnet", strings.Replace(g, "  \"mesh_subnet\": \"10.44.0.0/24\",\n", "", 1), "f2a9", "mesh_subnet: missing"},
		{"an empty mesh_subnet", strings.Replace(g, `"mesh_subnet": "10.44.0.0/24"`, `"mesh_subnet": ""`, 1), "f2a9", `mesh_subnet: "" is not a network`},
		// Differences compares mesh_subnet with mesh.subnet as strings, which
		// holds because both are only ever the canonical spelling.
		{"a mesh_subnet that is a host address", strings.Replace(g, `"mesh_subnet": "10.44.0.0/24"`, `"mesh_subnet": "10.44.0.1/24"`, 1), "f2a9", `mesh_subnet: "10.44.0.1/24" is a host address`},
		{"a mesh_subnet with a leading zero", strings.Replace(g, `"mesh_subnet": "10.44.0.0/24"`, `"mesh_subnet": "10.044.0.0/24"`, 1), "f2a9", `mesh_subnet: "10.044.0.0/24" is not`},
		{"a public mesh_subnet", strings.Replace(g, `"mesh_subnet": "10.44.0.0/24"`, `"mesh_subnet": "8.8.8.0/24"`, 1), "f2a9", `mesh_subnet: "8.8.8.0/24" is outside the private ranges`},
		{"an IPv6 mesh_lan", strings.Replace(g, `"mesh_lan": "192.168.1.0/24"`, `"mesh_lan": "fd00::/64"`, 1), "f2a9", `mesh_lan: "fd00::/64" is not`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := admission.ParseRecord([]byte(c.data), c.token); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v\nwant it to say %q", err, c.want)
			}
		})
	}
}

// A record with no digest, as admit writes from the configuration, is read.
func TestParseRecordWithoutDigest(t *testing.T) {
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{}, "", at)
	back, err := admission.ParseRecord(encode(t, rec), "f2a9")
	if err != nil || back.Digest != "" {
		t.Errorf("got %+v, %v", back, err)
	}
}

// The path, the mode and the directory host admit writes to.
func TestRecordPlace(t *testing.T) {
	if got, err := admission.Path("f2a9"); err != nil || got != "/etc/paisans/admitted.f2a9.json" {
		t.Errorf("Path = %s, %v", got, err)
	}
	if admission.Mode != 0o644 || admission.DirMode != 0o755 {
		t.Errorf("Mode = %o, DirMode = %o", admission.Mode, admission.DirMode)
	}
	for _, token := range []string{"../../tmp/x", "", "F2A9", "f2a", "f2a9a", "f2a/", "f2a9\n", "g2a9"} {
		if got, err := admission.Path(token); err == nil || got != "" || !strings.Contains(err.Error(), "token") {
			t.Errorf("Path(%q) = %q, %v", token, got, err)
		}
		if _, err := admission.Read(&host{files: map[string]string{}}, token); err == nil || errors.Is(err, admission.ErrNotAdmitted) {
			t.Errorf("Read(%q): %v", token, err)
		}
	}
}

// ParseRecord reads exactly the bytes EncodeRecord writes, so a duplicate
// key, a key in another case, a null, a reordering or a change of spacing
// cannot make paisans read a record differently from jq or a person.
func TestParseRecordByteExact(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "admitted.f2a9.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := string(data)
	for _, c := range []struct{ name, data string }{
		{"a repeated key", strings.Replace(g, `"user": "paisans-f2a9",`, `"user": "paisans-f2a9", "user": "paisans-zzzz",`, 1)},
		{"a key in another case", strings.Replace(g, `"forwarding": true,`, `"forwarding": true, "User": "paisans-zzzz",`, 1)},
		{"a repeated key in grants", strings.Replace(g, `"watchdog": true,`, `"watchdog": true, "watchdog": false,`, 1)},
		{"a key in another case in grants", strings.Replace(g, `"watchdog": true,`, `"watchdog": true, "Watchdog": false,`, 1)},
		{"null grants", strings.Replace(g, "\"grants\": {\n    \"watchdog\": true,\n    \"public_ports\": true\n  }", `"grants": null`, 1)},
		{"a null field", strings.Replace(g, `"forwarding": true`, `"forwarding": null`, 1)},
		{"a ports object without ssh", strings.Replace(g, "    \"ssh\": 22,\n", "", 1)},
		{"a change of spacing", strings.Replace(g, `"version": 1`, `"version":  1`, 1)},
		{"no final newline", strings.TrimSuffix(g, "\n")},
		{"windows line endings", strings.ReplaceAll(g, "\n", "\r\n")},
		{"keys reordered", strings.Replace(strings.Replace(g, `"version": 1,`, ``, 1), `"deployment"`, `"version": 1, "deployment"`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := admission.ParseRecord([]byte(c.data), "f2a9")
			if err == nil {
				t.Fatal("read")
			}
		})
	}
	_, err = admission.ParseRecord([]byte(strings.Replace(g, `"version": 1`, `"version":  1`, 1)), "f2a9")
	if err == nil || !strings.Contains(err.Error(), "not exactly as host admit wrote it") {
		t.Errorf("got %v", err)
	}
}

// Ports are ports: the SSH port 1-65535, the WireGuard port 0 (none) to
// 65535, and the public ports exactly [80, 443] with public_ports.
func TestParseRecordPorts(t *testing.T) {
	good := admission.NewRecord(build(t, "home-a"), admission.Grants{PublicPorts: true}, "", at)
	for _, c := range []struct {
		name string
		edit func(*admission.Record)
		want string
	}{
		{"ssh 0", func(r *admission.Record) { r.Ports.SSH = 0 }, "ports.ssh: 0"},
		{"ssh negative", func(r *admission.Record) { r.Ports.SSH = -5 }, "ports.ssh: -5"},
		{"ssh too high", func(r *admission.Record) { r.Ports.SSH = 65536 }, "ports.ssh: 65536"},
		{"wireguard negative", func(r *admission.Record) { r.Ports.WireGuard = -1 }, "ports.wireguard: -1"},
		{"wireguard too high", func(r *admission.Record) { r.Ports.WireGuard = 70000 }, "ports.wireguard: 70000"},
		{"public 0", func(r *admission.Record) { r.Ports.Public = []int{0} }, "ports.public: [0]"},
		{"public too high", func(r *admission.Record) { r.Ports.Public = []int{80, 99999} }, "ports.public: [80 99999]"},
		{"public twice", func(r *admission.Record) { r.Ports.Public = []int{443, 443} }, "ports.public: [443 443]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.edit(&r)
			if _, err := admission.ParseRecord(encode(t, r), "f2a9"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v\nwant it to say %q", err, c.want)
			}
		})
	}
	r := good
	r.Ports.WireGuard = 0
	if _, err := admission.ParseRecord(encode(t, r), "f2a9"); err != nil {
		t.Errorf("no WireGuard port: %v", err)
	}
}

// A record over 64 KiB is refused before it is decoded.
func TestParseRecordSize(t *testing.T) {
	big := strings.Repeat(" ", 64*1024+1)
	if _, err := admission.ParseRecord([]byte(big), "f2a9"); err == nil || !strings.Contains(err.Error(), "65537 bytes") || !strings.Contains(err.Error(), "65536") {
		t.Errorf("got %v", err)
	}
}

// The record carries the request's site and roles, so a command that has
// no configuration (site remove --force --id, host deployments) learns
// what the host was admitted as.
func TestNewRecordCarriesTheSiteAndRoles(t *testing.T) {
	req := build(t, "home-a")
	rec := admission.NewRecord(req, admission.Grants{Watchdog: true}, "", at)
	if rec.Site != "home-a" || strings.Join(rec.Roles, ",") != strings.Join(req.Roles, ",") {
		t.Errorf("site %q roles %v, want home-a %v", rec.Site, rec.Roles, req.Roles)
	}
}

// A record without them, or with roles out of order or unknown, is not one
// admit writes.
func TestParseRecordRefusesABadSiteOrRoles(t *testing.T) {
	good := encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at))
	for name, edit := range map[string]func(string) string{
		"no site":      func(s string) string { return strings.Replace(s, "  \"site\": \"home-a\",\n", "", 1) },
		"bad site":     func(s string) string { return strings.Replace(s, `"site": "home-a"`, `"site": "Home A"`, 1) },
		"unknown role": func(s string) string { return strings.Replace(s, `"data"`, `"database"`, 1) },
	} {
		if _, err := admission.ParseRecord([]byte(edit(string(good))), "f2a9"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Mine is the login's own record: the one under /etc/paisans whose user is
// the name `id -un` prints, whatever other deployments are admitted beside it.
func TestMineIsTheLoginsOwnRecord(t *testing.T) {
	mine := encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true}, "", at))
	other := strings.ReplaceAll(strings.ReplaceAll(string(mine), "f2a9c4e1", "0b0bc4e1"), "paisans-f2a9", "paisans-0b0b")
	h := &listing{host: host{files: map[string]string{
		"/etc/paisans/admitted.f2a9.json": string(mine),
		"/etc/paisans/admitted.0b0b.json": other,
	}}, user: "paisans-f2a9"}
	rec, err := admission.Mine(h)
	if err != nil || rec.User != "paisans-f2a9" || rec.Site != "home-a" {
		t.Fatalf("Mine = %+v, %v", rec, err)
	}
	h.user = "ubuntu"
	if _, err := admission.Mine(h); !errors.Is(err, admission.ErrNotMine) {
		t.Errorf("a login with no record of its own: %v", err)
	}
}

// listing is a host that answers Mine's listing and the login's name.
type listing struct {
	host
	user string
}

func (l *listing) Run(command string) (string, error) {
	var names []string
	for p := range l.files {
		names = append(names, p)
	}
	slices.Sort(names)
	return "user " + l.user + "\n" + strings.Join(names, "\n") + "\n", nil
}

// Every difference between what the owner admitted and what the
// declaration says is named; none when they agree.
func TestDifferences(t *testing.T) {
	cfg := fixture(t)
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true, PublicPorts: true}, "", at)
	if d := rec.Differences(cfg, "home-a"); len(d) != 0 {
		t.Fatalf("an exact admission differs: %v", d)
	}
	rec.Grants.Watchdog = false
	rec.Ports.SSH = 2222
	d := rec.Differences(cfg, "home-a")
	if len(d) != 2 || !strings.Contains(d[0], "ssh port") || !strings.Contains(d[1], "--watchdog") {
		t.Errorf("differences %q", d)
	}
	if d := rec.Differences(cfg, "home-b"); len(d) == 0 || !strings.Contains(d[0], "admitted as site home-a") {
		t.Errorf("another site's record: %q", d)
	}
}

// A mesh subnet changed after admission is a difference, since admit baked
// it into root's state; the site's mesh address is not, since nothing of
// root's restricts it.
func TestDifferencesMeshSubnet(t *testing.T) {
	cfg := fixture(t)
	rec := admission.NewRecord(build(t, "home-a"), admission.Grants{Watchdog: true, PublicPorts: true}, "", at)
	if rec.MeshSubnet != "10.44.0.0/24" {
		t.Fatalf("NewRecord's mesh subnet is %q, want the request's 10.44.0.0/24", rec.MeshSubnet)
	}
	cfg.Mesh.Subnet = "10.44.1.0/24"
	want := "the host admitted mesh subnet 10.44.0.0/24, and mesh.subnet is 10.44.1.0/24"
	if d := rec.Differences(cfg, "home-a"); len(d) != 1 || d[0] != want {
		t.Errorf("differences %q, want [%q]", d, want)
	}
	cfg = fixture(t)
	s := cfg.Sites["home-a"]
	s.Address = "10.44.0.99"
	cfg.Sites["home-a"] = s
	if d := rec.Differences(cfg, "home-a"); len(d) != 0 {
		t.Errorf("a changed mesh address differs: %q", d)
	}
}

// ForSite reads the record as the deployment's user and judges it.
func TestForSite(t *testing.T) {
	cfg := fixture(t)
	h := &host{files: map[string]string{}}
	if _, err := admission.ForSite(h, cfg, "home-a"); !errors.Is(err, admission.ErrNotAdmitted) {
		t.Errorf("no record: %v", err)
	}
	h.files["/etc/paisans/admitted.f2a9.json"] = string(encode(t, admission.NewRecord(build(t, "home-a"), admission.Grants{}, "", at)))
	_, err := admission.ForSite(h, cfg, "home-a")
	var se *admission.SiteError
	if !errors.As(err, &se) || se.Site != "home-a" || len(se.Problems) != 2 {
		t.Errorf("grants missing: %v", err)
	}
}
