package admission_test

import (
	"errors"
	"os"
	"path/filepath"
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

var at = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// The record admit writes for the home-a request, with both grants given:
// its golden file pins every field and its order.
func TestRecordGolden(t *testing.T) {
	req := build(t, "home-a")
	rec := admission.NewRecord(req, admission.Grants{Watchdog: true, PublicPorts: true}, admission.Digest(admission.Encode(req)), at)
	rec.DataDir = "/srv/data/paisans-f2a9"
	rec.WebServer = &admission.WebServer{Server: "caddy", Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: "automatic", AdminTCP: "localhost:2019"}
	got := admission.EncodeRecord(rec)
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
	if string(admission.EncodeRecord(back)) != string(got) {
		t.Errorf("read back differently:\n%s", admission.EncodeRecord(back))
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
	back, err := admission.ParseRecord(admission.EncodeRecord(rec), "f2a9")
	if err != nil || back.Digest != "" {
		t.Errorf("got %+v, %v", back, err)
	}
}

// The path, the mode and the directory phase 5 writes to.
func TestRecordPlace(t *testing.T) {
	if got := admission.Path("f2a9"); got != "/etc/paisans/admitted.f2a9.json" {
		t.Errorf("Path = %s", got)
	}
	if admission.Mode != 0o644 {
		t.Errorf("Mode = %o", admission.Mode)
	}
}
