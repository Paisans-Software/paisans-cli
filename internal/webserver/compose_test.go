package webserver_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/webserver"
	"github.com/paisans-software/paisans-stack/internal/webserver/webservertest"
)

// composeCaddy is a Caddy in a container compose started, mounting its
// Caddyfile alone.
var composeCaddy = webserver.WebServer{
	Server: webserver.ServerCaddy, Container: "caddy-1", Config: "/opt/x/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Mounts: []webserver.Mount{{Source: "/opt/x/Caddyfile", Destination: "/etc/caddy/Caddyfile"}},
	Certs:  webserver.CertsAutomatic,
}

const composeLabels = `{"com.docker.compose.project":"x","com.docker.compose.service":"web","com.docker.compose.project.working_dir":"/opt/x","com.docker.compose.project.config_files":"/opt/x/compose.yaml"}`

// composeHost is the host of composeCaddy, its compose file holding
// compose, and its Caddyfile importing the directory apply derives.
func composeHost(compose string) *webservertest.Server {
	s := webservertest.New("{\n\temail ops@example.org\n}\nimport /etc/caddy/paisans.d/*\n")
	s.Labels = composeLabels
	s.Files["/opt/x/compose.yaml"] = compose
	return s
}

func diagnose(t *testing.T, s *webservertest.Server, ws webserver.WebServer, dir string) webserver.MountDiagnosis {
	t.Helper()
	d := webserver.DiagnoseMount(host(s), ws, dir, fileName)
	t.Log("\n" + d.Text)
	if d.Text == "" {
		t.Fatal("no text")
	}
	return d
}

func contains(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("lacks %q:\n%s", w, text)
		}
	}
}

// The compose file declares the mount, by a path relative to the project,
// on the server's own service, and the running container lacks it: it
// predates the line, and recreating it is the fix.
func TestDiagnoseMountDeclaredButNotRecreated(t *testing.T) {
	s := composeHost("services:\n  web:\n    image: caddy:2\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile:ro\n      - ./paisans.d:/etc/caddy/paisans.d\n  monitor:\n    image: monitor:1\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountNotRecreated {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text,
		"/opt/x/compose.yaml declares /opt/x/paisans.d:/etc/caddy/paisans.d for web",
		"caddy-1 was created before it",
		"a restart does not add a mount",
		"    cd /opt/x && docker compose up -d web\n",
		"Until the mount exists, caddy's import /etc/caddy/paisans.d/* matches nothing, which is harmless.",
	)
	if strings.Contains(d.Text, "Add this line") {
		t.Errorf("advises adding what is there:\n%s", d.Text)
	}
	for _, c := range s.Asked {
		if strings.Contains(c, "compose up") || strings.Contains(c, "restart") || strings.Contains(c, "printf") {
			t.Errorf("changed the host: %s", c)
		}
	}
}

// The mount is declared, in compose's long syntax, under another service:
// the diagnosis names both.
func TestDiagnoseMountOnAnotherService(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n  monitor:\n    volumes:\n      - type: bind\n        source: /opt/x/paisans.d\n        target: /etc/caddy/paisans.d\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountOnOtherService {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text,
		"declares /opt/x/paisans.d:/etc/caddy/paisans.d for monitor, but caddy runs in web's container caddy-1",
		"move the line to web's volumes",
		"cd /opt/x && docker compose up -d web",
	)
	if strings.Contains(d.Text, "is not the directory") {
		t.Errorf("a path that is right is called wrong:\n%s", d.Text)
	}
}

// Under another service and at another path, as an operator may write it:
// each mistake is said once, then the one command.
func TestDiagnoseMountOnAnotherServiceAtAnotherPath(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n  monitor:\n    volumes:\n      - /opt/x/paisans.d:/etc/caddy/paisans\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountOnOtherService {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text,
		"for monitor, but caddy runs in web's container caddy-1",
		"Its container path, /etc/caddy/paisans, is not /etc/caddy/paisans.d, the directory caddy's configuration imports (import /etc/caddy/paisans.d/*): change it to /etc/caddy/paisans.d.",
	)
	if strings.Count(d.Text, "docker compose up") != 1 {
		t.Errorf("not one command:\n%s", d.Text)
	}
}

// On the server's service, in long syntax relative to the project, but at
// a container path the configuration does not import.
func TestDiagnoseMountAtAnotherPath(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n      - type: bind\n        source: ./paisans.d\n        target: /etc/caddy/paisans\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountPathDiffers {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text,
		"/opt/x/compose.yaml mounts /opt/x/paisans.d at /etc/caddy/paisans for web",
		"caddy's configuration imports /etc/caddy/paisans.d (import /etc/caddy/paisans.d/*): change /etc/caddy/paisans to /etc/caddy/paisans.d",
		"cd /opt/x && docker compose up -d web",
	)
	if strings.Contains(d.Text, "matches nothing") {
		t.Errorf("says the import twice:\n%s", d.Text)
	}
}

// Nothing declared: the line to add, to the service and the file now
// known, and the command that recreates it.
func TestDiagnoseMountNotDeclared(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n      - caddy_data:/data\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountNotDeclared {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text,
		"The container caddy-1 mounts /opt/x/Caddyfile alone",
		"Add this line to the volumes of web in /opt/x/compose.yaml:\n\n    - /opt/x/paisans.d:/etc/caddy/paisans.d\n",
		"cd /opt/x && docker compose up -d web",
		"a restart does not add a mount",
		"matches nothing, which is harmless",
	)
}

// A volume written with a variable is compared as written, and the
// diagnosis says so.
func TestDiagnoseMountComparesVariablesAsWritten(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - ${CONF}/Caddyfile:/etc/caddy/Caddyfile\n")
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountNotDeclared {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text, "${CONF}/Caddyfile", "compared as written")
}

// Compose files with names compose does not look for, or several, are
// named in the command.
func TestDiagnoseMountNamesTheComposeFiles(t *testing.T) {
	s := composeHost("")
	s.Labels = strings.Replace(composeLabels, `"/opt/x/compose.yaml"`, `"/opt/x/web.yaml,/opt/x/web.override.yaml"`, 1)
	s.Files["/opt/x/web.yaml"] = "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n"
	s.Files["/opt/x/web.override.yaml"] = "services:\n  web:\n    volumes:\n      - ./paisans.d:/etc/caddy/paisans.d\n"
	d := diagnose(t, s, composeCaddy, "")
	if d.Case != webserver.MountNotRecreated {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text, "/opt/x/web.override.yaml declares", "docker compose -p x -f /opt/x/web.yaml -f /opt/x/web.override.yaml up -d web")
}

// A container compose did not start, or a compose file that cannot be
// read, gets the advice there is without one.
func TestDiagnoseMountWithoutCompose(t *testing.T) {
	s := composeHost("")
	s.Labels = ""
	d := diagnose(t, s, composeCaddy, "")
	_, why := webserver.DeriveInclude(composeCaddy)
	if d.Case != webserver.MountNotDeclared || !strings.HasPrefix(d.Text, why) {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
}

// An include_dir paisans.yaml names is the mount's host path: declared on
// the server's service, the container predates it.
func TestDiagnoseMountForAnIncludeDir(t *testing.T) {
	s := composeHost("services:\n  web:\n    volumes:\n      - /srv/sites:/etc/caddy/sites\n")
	d := diagnose(t, s, composeCaddy, "/srv/sites")
	if d.Case != webserver.MountNotRecreated {
		t.Errorf("case %v:\n%s", d.Case, d.Text)
	}
	contains(t, d.Text, "declares /srv/sites:/etc/caddy/sites for web")
	if strings.Contains(d.Text, "matches nothing") {
		t.Errorf("says what an import it cannot know matches:\n%s", d.Text)
	}
}
