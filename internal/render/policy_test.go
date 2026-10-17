package render

import (
	"strings"
	"testing"
)

// What a catalog blueprint's compose file may not ask of the host is
// refused, each by the key that asked for it; a stack keeping to its own
// directory and its own namespaces passes.
func TestTheComposePolicy(t *testing.T) {
	root := "/srv/paisans/f2a9/notes"
	ok := `services:
  app:
    image: x
    volumes:
      - /srv/paisans/f2a9/notes/data:/data
      - ./config.ini:/etc/app.ini:ro
      - cache:/cache
      - {type: bind, source: /srv/paisans/f2a9/notes/x, target: /x}
`
	if err := checkComposePolicy(ok, root); err != nil {
		t.Errorf("a stack within its own directory was refused: %v", err)
	}
	for _, c := range []struct{ yaml, want string }{
		{"services:\n  app: {privileged: true}\n", "services.app.privileged"},
		{"services:\n  app: {cap_add: [NET_ADMIN]}\n", "services.app.cap_add"},
		{"services:\n  app: {devices: [/dev/kvm]}\n", "services.app.devices"},
		{"services:\n  app: {security_opt: [seccomp=unconfined]}\n", "services.app.security_opt"},
		{"services:\n  app: {network_mode: host}\n", "services.app.network_mode"},
		{"services:\n  app: {pid: host}\n", "services.app.pid"},
		{"services:\n  app: {network_mode: \"container:other\"}\n", "services.app.network_mode"},
		{"services:\n  app: {volumes: [/var/run/docker.sock:/var/run/docker.sock]}\n", "Docker socket"},
		{"services:\n  app: {volumes: [/etc:/host-etc]}\n", "outside the stack's own directory"},
		{"services:\n  app: {volumes: [/srv/paisans/f2a9/talk:/other]}\n", "outside the stack's own directory"},
		{"services:\n  app: {volumes: [../talk:/other]}\n", "leaves the stack's own directory"},
		{"services:\n  app: {volumes: [{type: bind, source: /root, target: /r}]}\n", "outside the stack's own directory"},
	} {
		err := checkComposePolicy(c.yaml, root)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.yaml, err, c.want)
		}
	}
}

func TestCatalogSecurityOptions(t *testing.T) {
	for _, options := range []string{
		`["no-new-privileges:true"]`,
		`["no-new-privileges=true"]`,
		`["no-new-privileges:true", "no-new-privileges=true"]`,
	} {
		if err := checkComposePolicy("services:\n  app:\n    security_opt: "+options+"\n", "/srv/paisans/test/app"); err != nil {
			t.Errorf("hardening option %s was refused: %v", options, err)
		}
	}
	for _, options := range []string{
		`["no-new-privileges:false"]`,
		`["no-new-privileges=false"]`,
		`["no-new-privileges:true", "seccomp=unconfined"]`,
		`["no-new-privileges:true", "no-new-privileges:false"]`,
		`["apparmor=unconfined"]`,
		`["label=disable"]`,
		`["unknown=true"]`,
		`[true]`,
		`[{no-new-privileges: true}]`,
		`"no-new-privileges:true"`,
		`null`,
	} {
		if err := checkComposePolicy("services:\n  app:\n    security_opt: "+options+"\n", "/srv/paisans/test/app"); err == nil || !strings.Contains(err.Error(), "services.app.security_opt") {
			t.Errorf("unsafe or malformed options %s: got %v, want security_opt refusal", options, err)
		}
	}
}
