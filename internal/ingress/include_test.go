package ingress_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/ingress"
	"github.com/paisans-software/paisans-stack/internal/ingress/ingresstest"
	"github.com/paisans-software/paisans-stack/internal/render"
)

const fileName = "paisans-f2a9-status.caddy"

// dockerCaddy is a Caddy in a container whose configuration directory is
// mounted whole.
var dockerCaddy = ingress.WebServer{
	Server: ingress.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Mounts: []ingress.Mount{{Source: "/opt/caddy", Destination: "/etc/caddy"}, {Source: "/var/lib/docker/volumes/caddy_data/_data", Destination: "/data"}},
	Certs:  ingress.CertsAutomatic,
}

var caddyInclude = ingress.Include{Dir: "/opt/caddy/paisans.d", Seen: "/etc/caddy/paisans.d"}

func host(s *ingresstest.Server) ingress.Host {
	return ingress.Host{Plain: s.Plain(), Sudo: s.Sudo()}
}

// Beside the main configuration: on the host, and through the mount of its
// directory for a container.
func TestDeriveInclude(t *testing.T) {
	inc, why := ingress.DeriveInclude(dockerCaddy)
	if why != "" || inc != caddyInclude {
		t.Errorf("docker: %+v %q", inc, why)
	}
	bare := ingress.WebServer{Server: ingress.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}
	if inc, why := ingress.DeriveInclude(bare); why != "" || inc != (ingress.Include{Dir: "/etc/nginx/paisans.d", Seen: "/etc/nginx/paisans.d"}) {
		t.Errorf("bare metal: %+v %q", inc, why)
	}
	// A mount of the directory itself, as the advice below says to add.
	own := dockerCaddy
	own.Mounts = []ingress.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}, {Source: "/srv/web/paisans.d", Destination: "/etc/caddy/paisans.d"}}
	if inc, why := ingress.DeriveInclude(own); why != "" || inc != (ingress.Include{Dir: "/srv/web/paisans.d", Seen: "/etc/caddy/paisans.d"}) {
		t.Errorf("own mount: %+v %q", inc, why)
	}
}

// A container that mounts its configuration file alone has no directory
// beside it to write in: nothing is derived, and why is the mount to add.
func TestDeriveIncludeWithASingleFileMount(t *testing.T) {
	ws := dockerCaddy
	ws.Mounts = []ingress.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}}
	inc, why := ingress.DeriveInclude(ws)
	if inc != (ingress.Include{}) {
		t.Errorf("derived %+v", inc)
	}
	for _, want := range []string{"mounts /opt/caddy/Caddyfile alone", "- /opt/caddy/paisans.d:/etc/caddy/paisans.d", "compose file"} {
		if !strings.Contains(why, want) {
			t.Errorf("why lacks %q:\n%s", want, why)
		}
	}
}

// A directory paisans.yaml names is seen through the mount it is under, or
// refused with the mount to add.
func TestIncludeAt(t *testing.T) {
	if inc, why := ingress.IncludeAt(dockerCaddy, "/opt/caddy/sites/paisans"); why != "" || inc.Seen != "/etc/caddy/sites/paisans" {
		t.Errorf("%+v %q", inc, why)
	}
	_, why := ingress.IncludeAt(dockerCaddy, "/srv/elsewhere")
	if !strings.Contains(why, "- /srv/elsewhere:/etc/caddy/paisans.d") {
		t.Errorf("why %q", why)
	}
}

// The configuration must include the directory by a wildcard taking in the
// toolkit's files. A literal path, or Apache's plain Include, is refused
// with the line found; no line at all is missing.
func TestCheckInclude(t *testing.T) {
	apache := ingress.WebServer{Server: ingress.ServerApache, Process: "apache2", Config: "/etc/apache2/apache2.conf", Main: "/etc/apache2/apache2.conf"}
	nginx := ingress.WebServer{Server: ingress.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}
	for _, tc := range []struct {
		name   string
		ws     ingress.WebServer
		seen   string
		file   string
		config string
		want   ingress.IncludeState
		line   string
	}{
		{"caddy glob", dockerCaddy, "/etc/caddy/paisans.d", fileName, "{\n\temail ops@example.org\n}\nimport /etc/caddy/paisans.d/*\nblog.example.org {\n}\n", ingress.IncludeWildcard, ""},
		{"caddy relative glob", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import paisans.d/*.caddy\n", ingress.IncludeWildcard, ""},
		{"caddy literal", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import /etc/caddy/paisans.d/" + fileName + "\n", ingress.IncludeRefused, "import /etc/caddy/paisans.d/" + fileName},
		{"caddy commented", dockerCaddy, "/etc/caddy/paisans.d", fileName, "# import /etc/caddy/paisans.d/*\n", ingress.IncludeMissing, ""},
		{"caddy missing", dockerCaddy, "/etc/caddy/paisans.d", fileName, "blog.example.org {\n}\n", ingress.IncludeMissing, ""},
		{"nginx glob", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-status.conf", "# configuration file /etc/nginx/nginx.conf:\nhttp {\n    include /etc/nginx/conf.d/*.conf;\n    include paisans.d/*.conf;\n}\n", ingress.IncludeWildcard, ""},
		{"nginx glob that misses", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-status.conf", "include /etc/nginx/paisans.d/*.vhost;\n", ingress.IncludeMissing, ""},
		{"nginx literal", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-status.conf", "include /etc/nginx/paisans.d/paisans-f2a9-status.conf;\n", ingress.IncludeRefused, "include /etc/nginx/paisans.d/paisans-f2a9-status.conf;"},
		{"apache optional", apache, "/etc/apache2/paisans.d", "paisans-f2a9-status.conf", "IncludeOptional mods-enabled/*.load\nIncludeOptional /etc/apache2/paisans.d/*.conf\n", ingress.IncludeWildcard, ""},
		{"apache include", apache, "/etc/apache2/paisans.d", "paisans-f2a9-status.conf", "Include /etc/apache2/paisans.d/*.conf\n", ingress.IncludeRefused, "Include /etc/apache2/paisans.d/*.conf"},
	} {
		s := ingresstest.New(tc.config)
		got, line, err := ingress.CheckInclude(host(s), tc.ws, tc.seen, tc.file)
		if err != nil || got != tc.want || line != tc.line {
			t.Errorf("%s: got %v %q %v, want %v %q", tc.name, got, line, err, tc.want, tc.line)
		}
	}
	if !strings.Contains(ingress.RefusedWhy(ingress.ServerApache, "Include /etc/apache2/paisans.d/*.conf"), "Include fails when nothing matches") {
		t.Error("Apache's Include is not explained")
	}
}

func TestIncludeLine(t *testing.T) {
	for s, want := range map[ingress.Server]string{
		ingress.ServerCaddy:  "import /etc/caddy/paisans.d/*",
		ingress.ServerNginx:  "include /etc/caddy/paisans.d/*.conf;",
		ingress.ServerApache: "IncludeOptional /etc/caddy/paisans.d/*.conf",
	} {
		if got, where := ingress.IncludeLine(s, "/etc/caddy/paisans.d"); got != want || where == "" {
			t.Errorf("%s: %q %q", s, got, where)
		}
	}
}

func statusFile(t *testing.T, ws ingress.WebServer) ingress.File {
	t.Helper()
	target := external(t, "127.0.0.1:8480")
	return ingress.File{App: "status", Name: ingress.FileName("paisans-f2a9", "status", ws.Server), Content: ingress.FileContent(target, ws)}
}

const statusPath = "/opt/caddy/paisans.d/" + fileName

// With the directory included, the file is written, the whole
// configuration validated and the server reloaded, inside its container,
// and the file is recorded with what removing it needs.
func TestSyncWritesValidatesAndReloads(t *testing.T) {
	s := ingresstest.New("")
	f := statusFile(t, dockerCaddy)
	record, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{f}, nil)
	if len(out) != 1 || out[0].Kind != ingress.Written || out[0].Path != statusPath {
		t.Fatalf("outcomes %+v", out)
	}
	content := s.Files[statusPath]
	for _, want := range []string{"status.example.org {", "reverse_proxy 127.0.0.1:8480", "written by paisans apply"} {
		if !strings.Contains(content, want) {
			t.Errorf("file lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "tls ") {
		t.Errorf("a Caddy that obtains its own certificates got a tls line:\n%s", content)
	}
	if !s.Ran("mkdir -p '/opt/caddy/paisans.d'") {
		t.Errorf("the directory was not created: %v", s.Asked)
	}
	if !s.Ran("docker exec 'caddy-1' sh -c 'caddy validate --config '\\''/etc/caddy/Caddyfile'\\'' --adapter caddyfile") ||
		!s.Ran("docker exec 'caddy-1' sh -c 'caddy reload --config '\\''/etc/caddy/Caddyfile'\\'' --adapter caddyfile") {
		t.Errorf("not validated and reloaded in the container: %v", s.Asked)
	}
	want := []render.ManifestIngress{{App: "status", Path: statusPath, SHA256: record[0].SHA256, Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}}
	if len(record) != 1 || record[0] != want[0] || record[0].SHA256 == "" {
		t.Errorf("record %+v", record)
	}
	for _, c := range s.Asked {
		if strings.HasPrefix(c, "sudo ") {
			t.Errorf("sudo was used where the ssh user could: %s", c)
		}
	}

	// Again, unchanged: nothing written, nothing reloaded.
	s.Asked = nil
	again, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{f}, record)
	if out[0].Kind != ingress.Unchanged || s.Ran("printf") || s.Ran("reload") || s.Ran("validate") {
		t.Errorf("unchanged: %+v, asked %v", out, s.Asked)
	}
	if len(again) != 1 || again[0] != record[0] {
		t.Errorf("record %+v", again)
	}
}

// A configuration the server refuses leaves no file behind and no reload:
// the server keeps the configuration it ran, and its error is reported.
func TestSyncRemovesAFileTheServerRefuses(t *testing.T) {
	s := ingresstest.New("")
	s.Validate = "Error: adapting config using caddyfile: ambiguous site definition: status.example.org"
	record, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{statusFile(t, dockerCaddy)}, nil)
	if len(out) != 1 || out[0].Kind != ingress.Failed || !strings.Contains(out[0].Why, "ambiguous site definition") {
		t.Fatalf("outcomes %+v", out)
	}
	if _, ok := s.Files[statusPath]; ok || len(record) != 0 {
		t.Errorf("the refused file stayed: %v, record %+v", s.Files, record)
	}
	if s.Ran("reload") {
		t.Errorf("reloaded after a failed validate: %v", s.Asked)
	}
}

// A refused rewrite puts back the file it replaced, and keeps its record.
func TestSyncRestoresTheFileARefusedRewriteReplaced(t *testing.T) {
	s := ingresstest.New("")
	f := statusFile(t, dockerCaddy)
	record, _ := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{f}, nil)
	before := s.Files[statusPath]
	s.Reload = "Error: loading new config: http.handlers.reverse_proxy: upstream unreachable"
	f.Content = strings.Replace(f.Content, "status.example.org", "uptime.example.org", -1)
	kept, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{f}, record)
	if out[0].Kind != ingress.Failed || !strings.Contains(out[0].Why, "did not reload") {
		t.Fatalf("outcomes %+v", out)
	}
	if s.Files[statusPath] != before || len(kept) != 1 || kept[0] != record[0] {
		t.Errorf("not restored: %q, record %+v", s.Files[statusPath], kept)
	}
}

// A file changed on the host since apply wrote it, or one there that was
// never recorded, is left as it is.
func TestSyncLeavesAnEditedFile(t *testing.T) {
	s := ingresstest.New("")
	f := statusFile(t, dockerCaddy)
	s.Files[statusPath] = "status.example.org {\n\trespond \"mine\"\n}\n"
	record, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{f}, nil)
	if out[0].Kind != ingress.Edited || len(record) != 0 || s.Ran("printf") || s.Ran("reload") {
		t.Errorf("%+v %+v %v", out, record, s.Asked)
	}
}

// A recorded file no longer wanted, the app's hostname gone from this site
// or its directory moved, is removed, and the server validated and
// reloaded. Nothing else in the directory is touched, nor the directory.
func TestSyncRemovesWhatIsNoLongerWanted(t *testing.T) {
	s := ingresstest.New("")
	record, _ := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{statusFile(t, dockerCaddy)}, nil)
	s.Files["/opt/caddy/paisans.d/blog.caddy"] = "blog.example.org {\n}\n"
	s.Asked = nil
	kept, out := ingress.Sync(host(s), dockerCaddy, caddyInclude, nil, record)
	if len(out) != 1 || out[0].Kind != ingress.Removed || len(kept) != 0 {
		t.Fatalf("%+v %+v", out, kept)
	}
	if _, ok := s.Files[statusPath]; ok {
		t.Error("the file stayed")
	}
	if _, ok := s.Files["/opt/caddy/paisans.d/blog.caddy"]; !ok {
		t.Error("somebody else's file went")
	}
	for _, c := range s.Asked {
		if strings.Contains(c, "rm ") && !strings.Contains(c, fileName) || strings.Contains(c, "rmdir") || strings.Contains(c, "rm -r") {
			t.Errorf("removed more than its file: %s", c)
		}
	}
	if !s.Ran("caddy validate") || !s.Ran("caddy reload") {
		t.Errorf("not validated and reloaded: %v", s.Asked)
	}
}

// Removal: a file already gone is fine, an edited one is kept with its
// record, and one whose removal the server refuses is put back.
func TestRemove(t *testing.T) {
	s := ingresstest.New("")
	record, _ := ingress.Sync(host(s), dockerCaddy, caddyInclude, []ingress.File{statusFile(t, dockerCaddy)}, nil)
	e := record[0]

	s.Validate = "Error: a site of the operator's imports a snippet from it"
	if o := ingress.Remove(host(s), e); o.Kind != ingress.Failed || s.Files[statusPath] == "" {
		t.Errorf("refused removal: %+v, file %q", o, s.Files[statusPath])
	}
	s.Validate = ""

	s.Files[statusPath] += "# mine\n"
	if o := ingress.Remove(host(s), e); o.Kind != ingress.Edited || s.Files[statusPath] == "" {
		t.Errorf("edited: %+v", o)
	}

	delete(s.Files, statusPath)
	s.Asked = nil
	if o := ingress.Remove(host(s), e); o.Kind != ingress.Gone || s.Ran("reload") {
		t.Errorf("gone: %+v %v", o, s.Asked)
	}
}

// Where the ssh user may not write, as under /etc on bare metal, the write
// goes through sudo, and the server's own commands with it only when they
// are refused too.
func TestSyncUsesSudoOnlyWhereRefused(t *testing.T) {
	s := ingresstest.New("")
	s.Denied = true
	nginx := ingress.WebServer{Server: ingress.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf",
		Certs: ingress.CertsFiles, CertFile: "/etc/letsencrypt/live/status.example.org/fullchain.pem", KeyFile: "/etc/letsencrypt/live/status.example.org/privkey.pem"}
	inc := ingress.Include{Dir: "/etc/nginx/paisans.d", Seen: "/etc/nginx/paisans.d"}
	_, out := ingress.Sync(host(s), nginx, inc, []ingress.File{statusFile(t, nginx)}, nil)
	if out[0].Kind != ingress.Written {
		t.Fatalf("%+v %v", out, s.Asked)
	}
	content := s.Files["/etc/nginx/paisans.d/paisans-f2a9-status.conf"]
	if !strings.Contains(content, "ssl_certificate     /etc/letsencrypt/live/status.example.org/fullchain.pem;") {
		t.Errorf("file:\n%s", content)
	}
	if !s.Ran("sudo set -e; mkdir -p") || s.Ran("sudo nginx -t") || !s.Ran("nginx -s reload") {
		t.Errorf("asked %v", s.Asked)
	}
}
