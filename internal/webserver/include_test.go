package webserver_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/webserver"
	"github.com/paisans-software/paisans-cli/internal/webserver/webservertest"
)

const fileName = "paisans-f2a9-health.caddy"

// dockerCaddy is a Caddy in a container whose configuration directory is
// mounted whole.
var dockerCaddy = webserver.WebServer{
	Server: webserver.ServerCaddy, Container: "caddy-1", Config: "/opt/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile",
	Mounts: []webserver.Mount{{Source: "/opt/caddy", Destination: "/etc/caddy"}, {Source: "/var/lib/docker/volumes/caddy_data/_data", Destination: "/data"}},
	Certs:  webserver.CertsAutomatic,
}

var caddyInclude = webserver.Include{Dir: "/opt/caddy/paisans.d", Seen: "/etc/caddy/paisans.d"}

func host(s *webservertest.Server) webserver.Host {
	return webserver.Host{Plain: s.Plain(), Sudo: s.Sudo()}
}

// Beside the main configuration: on the host, and through the mount of its
// directory for a container.
func TestDeriveInclude(t *testing.T) {
	inc, why := webserver.DeriveInclude(dockerCaddy)
	if why != "" || inc != caddyInclude {
		t.Errorf("docker: %+v %q", inc, why)
	}
	bare := webserver.WebServer{Server: webserver.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}
	if inc, why := webserver.DeriveInclude(bare); why != "" || inc != (webserver.Include{Dir: "/etc/nginx/paisans.d", Seen: "/etc/nginx/paisans.d"}) {
		t.Errorf("bare metal: %+v %q", inc, why)
	}
	// A mount of the directory itself, as the advice below says to add.
	own := dockerCaddy
	own.Mounts = []webserver.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}, {Source: "/srv/web/paisans.d", Destination: "/etc/caddy/paisans.d"}}
	if inc, why := webserver.DeriveInclude(own); why != "" || inc != (webserver.Include{Dir: "/srv/web/paisans.d", Seen: "/etc/caddy/paisans.d"}) {
		t.Errorf("own mount: %+v %q", inc, why)
	}
}

// A container that mounts its configuration file alone has no directory
// beside it to write in: nothing is derived, and why is the mount to add.
func TestDeriveIncludeWithASingleFileMount(t *testing.T) {
	ws := dockerCaddy
	ws.Mounts = []webserver.Mount{{Source: "/opt/caddy/Caddyfile", Destination: "/etc/caddy/Caddyfile"}}
	inc, why := webserver.DeriveInclude(ws)
	if inc != (webserver.Include{}) {
		t.Errorf("derived %+v", inc)
	}
	for _, want := range []string{"mounts /opt/caddy/Caddyfile alone", "- /opt/caddy/paisans.d:/etc/caddy/paisans.d", "compose file"} {
		if !strings.Contains(why, want) {
			t.Errorf("why lacks %q:\n%s", want, why)
		}
	}
}

// A directory paisans-stack.yaml names is seen through the mount it is under, or
// refused with the mount to add.
func TestIncludeAt(t *testing.T) {
	if inc, why := webserver.IncludeAt(dockerCaddy, "/opt/caddy/sites/paisans"); why != "" || inc.Seen != "/etc/caddy/sites/paisans" {
		t.Errorf("%+v %q", inc, why)
	}
	_, why := webserver.IncludeAt(dockerCaddy, "/srv/elsewhere")
	if !strings.Contains(why, "- /srv/elsewhere:/etc/caddy/paisans.d") {
		t.Errorf("why %q", why)
	}
}

// The configuration must include the directory by a wildcard taking in the
// toolkit's files. A literal path, or Apache's plain Include, is refused
// with the line found; no line at all is missing.
func TestCheckInclude(t *testing.T) {
	apache := webserver.WebServer{Server: webserver.ServerApache, Process: "apache2", Config: "/etc/apache2/apache2.conf", Main: "/etc/apache2/apache2.conf"}
	nginx := webserver.WebServer{Server: webserver.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}
	for _, tc := range []struct {
		name   string
		ws     webserver.WebServer
		seen   string
		file   string
		config string
		want   webserver.IncludeState
		line   string
	}{
		{"caddy glob", dockerCaddy, "/etc/caddy/paisans.d", fileName, "{\n\temail ops@example.org\n}\nimport /etc/caddy/paisans.d/*\nblog.example.org {\n}\n", webserver.IncludeWildcard, ""},
		{"caddy relative glob", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import paisans.d/*.caddy\n", webserver.IncludeWildcard, ""},
		{"caddy literal", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import /etc/caddy/paisans.d/" + fileName + "\n", webserver.IncludeRefused, "import /etc/caddy/paisans.d/" + fileName},
		{"caddy commented", dockerCaddy, "/etc/caddy/paisans.d", fileName, "# import /etc/caddy/paisans.d/*\n", webserver.IncludeMissing, ""},
		{"caddy missing", dockerCaddy, "/etc/caddy/paisans.d", fileName, "blog.example.org {\n}\n", webserver.IncludeMissing, ""},
		{"caddy glob after options, comments between", dockerCaddy, "/etc/caddy/paisans.d", fileName, "# options\n{\n\temail ops@example.org # who\n}\n\n# paisans\nimport /etc/caddy/paisans.d/*\n", webserver.IncludeWildcard, ""},
		{"caddy glob without options", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import /etc/caddy/paisans.d/*\nblog.example.org {\n\trespond \"{\" 200\n}\n", webserver.IncludeWildcard, ""},
		{"caddy glob after a site", dockerCaddy, "/etc/caddy/paisans.d", fileName, "blog.example.org,\nwww.example.org {\n\theader X-Host {host}\n}\nimport /etc/caddy/paisans.d/*\n", webserver.IncludeWildcard, ""},
		{"caddy glob after a snippet import", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import common.caddy\n{$SITE} {\n}\nimport /etc/caddy/paisans.d/*\n", webserver.IncludeWildcard, ""},
		{"caddy glob above options", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import /etc/caddy/paisans.d/*\n{\n\temail ops@example.org\n}\nblog.example.org {\n}\n", webserver.IncludeAboveOptions, "import /etc/caddy/paisans.d/*"},
		{"caddy glob above options, comments between", dockerCaddy, "/etc/caddy/paisans.d", fileName, "# paisans\n\nimport /etc/caddy/paisans.d/* # the toolkit's\n\n# options\n{\n\temail ops@example.org\n}\n", webserver.IncludeAboveOptions, "import /etc/caddy/paisans.d/*"},
		{"caddy glob in a site", dockerCaddy, "/etc/caddy/paisans.d", fileName, "blog.example.org {\n\timport /etc/caddy/paisans.d/*\n}\n", webserver.IncludeInBlock, "import /etc/caddy/paisans.d/*"},
		{"caddy glob in a snippet", dockerCaddy, "/etc/caddy/paisans.d", fileName, "(common) {\n\timport /etc/caddy/paisans.d/*\n}\n", webserver.IncludeInBlock, "import /etc/caddy/paisans.d/*"},
		{"caddy glob in the options", dockerCaddy, "/etc/caddy/paisans.d", fileName, "{\n\timport /etc/caddy/paisans.d/*\n}\n", webserver.IncludeInBlock, "import /etc/caddy/paisans.d/*"},
		{"caddy glob above options and after", dockerCaddy, "/etc/caddy/paisans.d", fileName, "import /etc/caddy/paisans.d/*\n{\n}\nimport /etc/caddy/paisans.d/*\n", webserver.IncludeAboveOptions, "import /etc/caddy/paisans.d/*"},
		{"nginx glob", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-health.conf", "# configuration file /etc/nginx/nginx.conf:\nhttp {\n    include /etc/nginx/conf.d/*.conf;\n    include paisans.d/*.conf;\n}\n", webserver.IncludeWildcard, ""},
		{"nginx glob that misses", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-health.conf", "include /etc/nginx/paisans.d/*.vhost;\n", webserver.IncludeMissing, ""},
		{"nginx literal", nginx, "/etc/nginx/paisans.d", "paisans-f2a9-health.conf", "include /etc/nginx/paisans.d/paisans-f2a9-health.conf;\n", webserver.IncludeRefused, "include /etc/nginx/paisans.d/paisans-f2a9-health.conf;"},
		{"apache optional", apache, "/etc/apache2/paisans.d", "paisans-f2a9-health.conf", "IncludeOptional mods-enabled/*.load\nIncludeOptional /etc/apache2/paisans.d/*.conf\n", webserver.IncludeWildcard, ""},
		{"apache include", apache, "/etc/apache2/paisans.d", "paisans-f2a9-health.conf", "Include /etc/apache2/paisans.d/*.conf\n", webserver.IncludeRefused, "Include /etc/apache2/paisans.d/*.conf"},
	} {
		s := webservertest.New(tc.config)
		got, line, err := webserver.CheckInclude(host(s), tc.ws, tc.seen, tc.file)
		if err != nil || got != tc.want || line != tc.line {
			t.Errorf("%s: got %v %q %v, want %v %q", tc.name, got, line, err, tc.want, tc.line)
		}
	}
	if !strings.Contains(webserver.RefusedWhy(webserver.ServerApache, "Include /etc/apache2/paisans.d/*.conf"), "Include fails when nothing matches") {
		t.Error("Apache's Include is not explained")
	}
}

func TestIncludeLine(t *testing.T) {
	for s, want := range map[webserver.Server]string{
		webserver.ServerCaddy:  "import /etc/caddy/paisans.d/*",
		webserver.ServerNginx:  "include /etc/caddy/paisans.d/*.conf;",
		webserver.ServerApache: "IncludeOptional /etc/caddy/paisans.d/*.conf",
	} {
		if got, where := webserver.IncludeLine(s, "/etc/caddy/paisans.d"); got != want || where == "" {
			t.Errorf("%s: %q %q", s, got, where)
		}
	}
}

// Caddy's import goes after the global options block, and the example
// shows where.
func TestIncludeLineForCaddyComesAfterTheGlobalOptions(t *testing.T) {
	if _, where := webserver.IncludeLine(webserver.ServerCaddy, "/etc/caddy/paisans.d"); !strings.Contains(where, "after the global options block if it has one") {
		t.Errorf("where %q", where)
	}
	want := "    {\n        email admin@example.org\n    }\n\n    import /etc/caddy/paisans.d/*\n\n    example.org {\n        ...\n    }\n"
	if got := webserver.IncludeExample(webserver.ServerCaddy, "/etc/caddy/paisans.d"); got != want {
		t.Errorf("example:\n%s", got)
	}
	if got := webserver.IncludeExample(webserver.ServerNginx, "/etc/nginx/paisans.d"); got != "" {
		t.Errorf("nginx example %q", got)
	}
}

func healthFile(t *testing.T, ws webserver.WebServer) webserver.File {
	t.Helper()
	target := external(t, "127.0.0.1:8480")
	return webserver.File{App: "health", Name: webserver.FileName("paisans-f2a9", "health", ws.Server), Content: webserver.FileContent(target, ws)}
}

const healthPath = "/opt/caddy/paisans.d/" + fileName

// With the directory included, the file is written, the whole
// configuration validated and the server reloaded, inside its container,
// and the file is recorded with what removing it needs.
func TestSyncWritesValidatesAndReloads(t *testing.T) {
	s := webservertest.New("")
	f := healthFile(t, dockerCaddy)
	record, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{f}, nil)
	if len(out) != 1 || out[0].Kind != webserver.Written || out[0].Path != healthPath || out[0].Reload.Kind != webserver.Reloaded {
		t.Fatalf("outcomes %+v", out)
	}
	content := s.Files[healthPath]
	for _, want := range []string{"health.example.org {", "reverse_proxy 127.0.0.1:8480", "written by paisans apply"} {
		if !strings.Contains(content, want) {
			t.Errorf("file lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "tls /") || !strings.Contains(content, siteIssuer) {
		t.Errorf("a Caddy that obtains its own certificates got certificate files, or no issuer of its own:\n%s", content)
	}
	if !s.Ran("mkdir -p '/opt/caddy/paisans.d'") {
		t.Errorf("the directory was not created: %v", s.Asked)
	}
	if !s.Ran("docker exec 'caddy-1' sh -c 'caddy validate --config '\\''/etc/caddy/Caddyfile'\\'' --adapter caddyfile") ||
		!s.Ran("docker exec 'caddy-1' sh -c 'caddy reload --config '\\''/etc/caddy/Caddyfile'\\'' --adapter caddyfile") {
		t.Errorf("not validated and reloaded in the container: %v", s.Asked)
	}
	want := []render.ManifestWebServer{{App: "health", Path: healthPath, SHA256: record[0].SHA256, Server: "caddy", Container: "caddy-1", Config: "/etc/caddy/Caddyfile"}}
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
	again, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{f}, record)
	if out[0].Kind != webserver.Unchanged || s.Ran("printf") || s.Ran("reload") || s.Ran("validate") {
		t.Errorf("unchanged: %+v, asked %v", out, s.Asked)
	}
	if len(again) != 1 || again[0] != record[0] {
		t.Errorf("record %+v", again)
	}
}

// A configuration the server refuses leaves no file behind and no reload:
// the server keeps the configuration it ran, and its error is reported.
func TestSyncRemovesAFileTheServerRefuses(t *testing.T) {
	s := webservertest.New("")
	s.Validate = "Error: adapting config using caddyfile: ambiguous site definition: health.example.org"
	record, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{healthFile(t, dockerCaddy)}, nil)
	if len(out) != 1 || out[0].Kind != webserver.Refused || !strings.Contains(out[0].Why, "ambiguous site definition") {
		t.Fatalf("outcomes %+v", out)
	}
	if _, ok := s.Files[healthPath]; ok || len(record) != 0 {
		t.Errorf("the refused file stayed: %v, record %+v", s.Files, record)
	}
	if s.Ran("reload") || s.Ran("restart") {
		t.Errorf("reloaded after a failed validate: %v", s.Asked)
	}
}

// A host that cannot be reached is a failure of its own, apart from a
// server refusing the change: nothing was written.
func TestSyncFailsOnAHostItCannotReach(t *testing.T) {
	s := webservertest.New("")
	s.Unreachable = true
	_, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{healthFile(t, dockerCaddy)}, nil)
	if len(out) != 1 || out[0].Kind != webserver.Failed || !strings.Contains(out[0].Why, "cannot be reached") {
		t.Fatalf("outcomes %+v", out)
	}
}

// A server in a container whose graceful reload fails is restarted, and
// the outcome says so with the reload's error.
func TestSyncRestartsAContainerWhoseReloadFails(t *testing.T) {
	s := webservertest.New("")
	s.Reload = "Error: loading new config: admin endpoint disabled"
	record, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{healthFile(t, dockerCaddy)}, nil)
	if len(out) != 1 || out[0].Kind != webserver.Written || out[0].Reload.Kind != webserver.Restarted || !strings.Contains(out[0].Reload.Why, "admin endpoint disabled") {
		t.Fatalf("outcomes %+v", out)
	}
	if !s.Ran("docker restart 'caddy-1'") || len(record) != 1 || s.Files[healthPath] == "" {
		t.Errorf("record %+v, asked %v", record, s.Asked)
	}
}

// A server on the host itself is validated and never reloaded: the
// outcome carries the command that reloads it, its systemd unit's when one
// is active, for the operator to run.
func TestSyncLeavesTheReloadOfABareMetalServer(t *testing.T) {
	for _, c := range []struct {
		ws    webserver.WebServer
		units []string
		want  string
	}{
		{webserver.WebServer{Server: webserver.ServerCaddy, Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: webserver.CertsAutomatic}, []string{"caddy"}, "sudo systemctl reload caddy"},
		{webserver.WebServer{Server: webserver.ServerCaddy, Process: "caddy", Config: "/etc/caddy/Caddyfile", Main: "/etc/caddy/Caddyfile", Certs: webserver.CertsAutomatic}, nil, "caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile"},
		{webserver.WebServer{Server: webserver.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}, []string{"nginx"}, "sudo systemctl reload nginx"},
		{webserver.WebServer{Server: webserver.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf"}, nil, "sudo nginx -s reload"},
		{webserver.WebServer{Server: webserver.ServerApache, Process: "apache2", Config: "/etc/apache2/apache2.conf", Main: "/etc/apache2/apache2.conf"}, []string{"apache2"}, "sudo systemctl reload apache2"},
		{webserver.WebServer{Server: webserver.ServerApache, Process: "httpd", Config: "/etc/httpd/conf/httpd.conf", Main: "/etc/httpd/conf/httpd.conf"}, []string{"httpd"}, "sudo systemctl reload httpd"},
		{webserver.WebServer{Server: webserver.ServerApache, Process: "httpd", Config: "/etc/httpd/conf/httpd.conf", Main: "/etc/httpd/conf/httpd.conf"}, nil, "sudo apachectl graceful"},
	} {
		s := webservertest.New("")
		s.Units = c.units
		inc := webserver.Include{Dir: "/etc/x/paisans.d", Seen: "/etc/x/paisans.d"}
		record, out := webserver.Sync(host(s), c.ws, inc, []webserver.File{healthFile(t, c.ws)}, nil)
		if len(out) != 1 || out[0].Kind != webserver.Written || out[0].Reload.Kind != webserver.OperatorReloads || out[0].Reload.Command != c.want {
			t.Errorf("%s %v: outcomes %+v", c.ws.Server, c.units, out)
			continue
		}
		if len(record) != 1 || !s.Ran("validate") && !s.Ran("nginx -t") && !s.Ran("configtest") {
			t.Errorf("%s: record %+v, asked %v", c.ws.Server, record, s.Asked)
		}
		if s.Ran("reload --config") || s.Ran("-s reload") || s.Ran("graceful") || s.Ran("systemctl reload") || s.Ran("restart") {
			t.Errorf("%s: a server on the host itself was reloaded: %v", c.ws.Server, s.Asked)
		}
	}
}

// A rewrite whose reload and restart both fail puts back the file it
// replaced, and keeps its record.
func TestSyncRestoresTheFileARefusedRewriteReplaced(t *testing.T) {
	s := webservertest.New("")
	f := healthFile(t, dockerCaddy)
	record, _ := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{f}, nil)
	before := s.Files[healthPath]
	s.Reload = "Error: loading new config: http.handlers.reverse_proxy: upstream unreachable"
	s.Restart = "Error response from daemon: container caddy-1 is restarting"
	f.Content = strings.Replace(f.Content, "health.example.org", "uptime.example.org", -1)
	kept, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{f}, record)
	if out[0].Kind != webserver.Refused || !strings.Contains(out[0].Why, "did not reload") || !strings.Contains(out[0].Why, "is restarting") {
		t.Fatalf("outcomes %+v", out)
	}
	if s.Files[healthPath] != before || len(kept) != 1 || kept[0] != record[0] {
		t.Errorf("not restored: %q, record %+v", s.Files[healthPath], kept)
	}
}

// A file changed on the host since apply wrote it, or one there that was
// never recorded, is left as it is.
func TestSyncLeavesAnEditedFile(t *testing.T) {
	s := webservertest.New("")
	f := healthFile(t, dockerCaddy)
	s.Files[healthPath] = "health.example.org {\n\trespond \"mine\"\n}\n"
	record, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{f}, nil)
	if out[0].Kind != webserver.Edited || len(record) != 0 || s.Ran("printf") || s.Ran("reload") {
		t.Errorf("%+v %+v %v", out, record, s.Asked)
	}
}

// A recorded file no longer wanted, the app's hostname gone from this site
// or its directory moved, is removed, and the server validated and
// reloaded. Nothing else in the directory is touched, nor the directory.
func TestSyncRemovesWhatIsNoLongerWanted(t *testing.T) {
	s := webservertest.New("")
	record, _ := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{healthFile(t, dockerCaddy)}, nil)
	s.Files["/opt/caddy/paisans.d/blog.caddy"] = "blog.example.org {\n}\n"
	s.Asked = nil
	kept, out := webserver.Sync(host(s), dockerCaddy, caddyInclude, nil, record)
	if len(out) != 1 || out[0].Kind != webserver.Removed || len(kept) != 0 {
		t.Fatalf("%+v %+v", out, kept)
	}
	if _, ok := s.Files[healthPath]; ok {
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
	s := webservertest.New("")
	record, _ := webserver.Sync(host(s), dockerCaddy, caddyInclude, []webserver.File{healthFile(t, dockerCaddy)}, nil)
	e := record[0]

	s.Validate = "Error: a site of the operator's imports a snippet from it"
	if o := webserver.Remove(host(s), e); o.Kind != webserver.Refused || s.Files[healthPath] == "" {
		t.Errorf("refused removal: %+v, file %q", o, s.Files[healthPath])
	}
	s.Validate = ""

	s.Files[healthPath] += "# mine\n"
	if o := webserver.Remove(host(s), e); o.Kind != webserver.Edited || s.Files[healthPath] == "" {
		t.Errorf("edited: %+v", o)
	}

	delete(s.Files, healthPath)
	s.Asked = nil
	if o := webserver.Remove(host(s), e); o.Kind != webserver.Gone || s.Ran("reload") {
		t.Errorf("gone: %+v %v", o, s.Asked)
	}
}

// Where the ssh user may not write, as under /etc on bare metal, the write
// goes through sudo, and the server's validation with it only when it is
// refused too.
func TestSyncUsesSudoOnlyWhereRefused(t *testing.T) {
	s := webservertest.New("")
	s.Denied = true
	nginx := webserver.WebServer{Server: webserver.ServerNginx, Process: "nginx", Config: "/etc/nginx/nginx.conf", Main: "/etc/nginx/nginx.conf",
		Certs: webserver.CertsFiles, CertFile: "/etc/letsencrypt/live/health.example.org/fullchain.pem", KeyFile: "/etc/letsencrypt/live/health.example.org/privkey.pem"}
	inc := webserver.Include{Dir: "/etc/nginx/paisans.d", Seen: "/etc/nginx/paisans.d"}
	_, out := webserver.Sync(host(s), nginx, inc, []webserver.File{healthFile(t, nginx)}, nil)
	if out[0].Kind != webserver.Written {
		t.Fatalf("%+v %v", out, s.Asked)
	}
	content := s.Files["/etc/nginx/paisans.d/paisans-f2a9-health.conf"]
	if !strings.Contains(content, "ssl_certificate     /etc/letsencrypt/live/health.example.org/fullchain.pem;") {
		t.Errorf("file:\n%s", content)
	}
	if !s.Ran("sudo set -e; mkdir -p") || s.Ran("sudo nginx -t") || !s.Ran("nginx -t") || s.Ran("nginx -s reload") {
		t.Errorf("asked %v", s.Asked)
	}
}
