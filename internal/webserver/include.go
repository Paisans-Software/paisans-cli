package webserver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/render"
)

// The toolkit's own files for the operator's web server live in a directory
// of their own, which the server's configuration includes with one wildcard
// line the operator adds once. The toolkit writes, rewrites and deletes only
// the files it records in the deployment's manifest, each named for the
// deployment, and never edits the server's configuration itself.
//
// The line must be a wildcard because the directory may go: a deployment
// removed, or a directory deleted by hand. Caddy's import of a glob that
// matches nothing, nginx's include of one, and Apache's IncludeOptional all
// start with no file there, without the paisans sites. A literal path, or
// Apache's plain Include, fails on the next start or reload once the file or
// directory is gone, and that takes down every site the server carries.

// Include is the directory the toolkit writes in: Dir on the host, Seen as
// the web server reads it, which differs for a server in a container.
type Include struct {
	Dir, Seen string
}

// IncludeName is the directory apply derives beside the server's main
// configuration.
const IncludeName = "paisans.d"

// Manages reports whether ws is a web server the toolkit can write files
// for: one it has a snippet for, whose main configuration is known.
func (ws WebServer) Manages() bool {
	switch ws.Server {
	case ServerCaddy, ServerNginx, ServerApache:
		return ws.Main != ""
	}
	return false
}

// DeriveInclude is the directory beside the server's main configuration:
// <its directory>/paisans.d. For a server in a container, that directory
// must be visible inside it through a mount, and the host path is the
// mount's; when only the configuration file itself is mounted, there is no
// such directory, and why says which mount to add.
func DeriveInclude(ws WebServer) (inc Include, why string) {
	seen := path.Join(path.Dir(ws.Main), IncludeName)
	if ws.Container == "" {
		return Include{Dir: seen, Seen: seen}, ""
	}
	if dir, ok := hostPath(ws.Mounts, seen); ok {
		return Include{Dir: dir, Seen: seen}, ""
	}
	return Include{}, mountAdvice(ws, seen)
}

// IncludeAt is a directory the configuration names, as the server sees it:
// for a server in a container, through the mount it is under.
func IncludeAt(ws WebServer, dir string) (inc Include, why string) {
	if ws.Container == "" {
		return Include{Dir: dir, Seen: dir}, ""
	}
	for _, m := range ws.Mounts {
		if rest, ok := under(dir, m.Source); ok {
			return Include{Dir: dir, Seen: m.Destination + rest}, ""
		}
	}
	seen := path.Join(path.Dir(ws.Main), IncludeName)
	return Include{}, fmt.Sprintf("%s Add it to the volumes of that container's service in its compose file:\n\n    - %s:%s\n\nand recreate the container (a restart does not add a mount), then run paisans apply --execute again.", unmounted(ws, dir), dir, seen)
}

func mountAdvice(ws WebServer, seen string) string {
	host := "/path/on/the/host/" + IncludeName
	if ws.Config != "" {
		host = path.Join(path.Dir(ws.Config), IncludeName)
	}
	return fmt.Sprintf("%s Add one to the volumes of that container's service in its compose file:\n\n    - %s:%s\n\nand recreate the container (a restart does not add a mount), then run paisans apply --execute again: it finds the directory and records it in paisans.yaml.", unmounted(ws, ""), host, seen)
}

// unmounted says that the server in a container cannot see dir, the
// include_dir paisans.yaml names, or with dir empty the directory beside
// its configuration.
func unmounted(ws WebServer, dir string) string {
	if dir != "" {
		return fmt.Sprintf("%s is not mounted into the container %s, so %s cannot read what is written there.", dir, ws.Container, ws.Server)
	}
	what := "its configuration"
	if ws.Config != "" {
		what = ws.Config
	}
	return fmt.Sprintf("The container %s mounts %s alone, so no directory beside it is visible inside the container.", ws.Container, what)
}

// hostPath is the host path of p inside a container, through the mount of
// a directory it is under.
func hostPath(mounts []Mount, p string) (string, bool) {
	for _, m := range mounts {
		if rest, ok := under(p, m.Destination); ok {
			return m.Source + rest, true
		}
	}
	return "", false
}

// under reports whether p is dir or inside it, and the rest of p after dir.
func under(p, dir string) (string, bool) {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return "", false
	}
	if p == dir {
		return "", true
	}
	if strings.HasPrefix(p, dir+"/") {
		return p[len(dir):], true
	}
	return "", false
}

// IncludeLine is the one line the server's configuration needs to include
// every file in seen, and where it goes.
func IncludeLine(s Server, seen string) (line, where string) {
	switch s {
	case ServerCaddy:
		return "import " + seen + "/*", "at the top level of the Caddyfile, outside every site block"
	case ServerNginx:
		return "include " + seen + "/*.conf;", "inside the http block"
	case ServerApache:
		return "IncludeOptional " + seen + "/*.conf", "at the top level of the server's configuration"
	}
	return "", ""
}

// FileName is the toolkit's file for app, named for the deployment, so two
// deployments writing in one directory never collide.
func FileName(prefix, app string, s Server) string {
	if s == ServerCaddy {
		return prefix + "-" + app + ".caddy"
	}
	return prefix + "-" + app + ".conf"
}

// FileContent is what the toolkit writes for t: a header saying whose it
// is, then the snippet `web-server show` prints for ws.
func FileContent(t Target, ws WebServer) string {
	header := fmt.Sprintf("# %s, the paisans app %s, written by paisans apply.\n# Every apply rewrites it, and app remove and site remove delete it:\n# change paisans.yaml rather than this file.\n", t.Hostname, t.App)
	switch ws.Server {
	case ServerCaddy:
		return header + caddySnippet(t, ws)
	case ServerNginx:
		return header + nginxSnippet(t, ws)
	case ServerApache:
		return header + apacheSnippet(t, ws)
	}
	return ""
}

// IncludeState is whether the server's configuration includes the toolkit's
// directory.
type IncludeState int

const (
	// IncludeMissing is no line naming the directory.
	IncludeMissing IncludeState = iota
	// IncludeWildcard is a wildcard line that takes in the toolkit's files.
	IncludeWildcard
	// IncludeRefused is a line naming the directory that fails the server's
	// next start once the directory or a file is gone: a literal path, or
	// Apache's plain Include.
	IncludeRefused
)

var (
	caddyImport   = regexp.MustCompile(`^import\s+("[^"]*"|\S+)`)
	nginxInclude  = regexp.MustCompile(`^include\s+("[^"]*"|'[^']*'|[^;\s]+)\s*;`)
	apacheInclude = regexp.MustCompile(`(?i)^(include|includeoptional)\s+("[^"]*"|\S+)`)
	apacheRoot    = regexp.MustCompile(`(?i)^serverroot\s+("[^"]*"|\S+)`)
)

// CheckInclude reads the server's configuration and reports whether it
// includes seen by a wildcard that takes in a file named name, and the line
// it found when that line is refused. It reads only.
func CheckInclude(h Host, ws WebServer, seen, name string) (IncludeState, string, error) {
	lines, err := includeLines(h, ws)
	if err != nil {
		return IncludeMissing, "", err
	}
	state, refused := IncludeMissing, ""
	for _, l := range lines {
		dir, file := path.Dir(l.path), path.Base(l.path)
		switch {
		case l.path == seen || (dir == seen && !strings.ContainsAny(file, "*?[")):
			state, refused = IncludeRefused, l.text
		case dir != seen:
		case !l.optional:
			state, refused = IncludeRefused, l.text
		default:
			if ok, _ := path.Match(file, name); ok {
				return IncludeWildcard, "", nil
			}
		}
	}
	return state, refused, nil
}

// includeLine is one line of a server's configuration that includes
// others: as written, the absolute path it names, and whether it starts
// with nothing matching it.
type includeLine struct {
	text, path string
	optional   bool
}

// includeLines reads the server's configuration for the lines that
// include others. It reads only.
func includeLines(h Host, ws WebServer) ([]includeLine, error) {
	var conf string
	var err error
	switch ws.Server {
	case ServerCaddy:
		conf, err = serverRun(h, ws.Container, "cat "+quote(ws.Main))
	case ServerNginx:
		conf, err = serverRun(h, ws.Container, "nginx -T 2>&1")
	case ServerApache:
		dirs := "/etc/apache2 /etc/httpd"
		if ws.Container != "" {
			dirs = quote(path.Dir(ws.Main))
		}
		conf, err = serverRun(h, ws.Container, "grep -rhiE '^[[:space:]]*(Include|IncludeOptional|ServerRoot)[[:space:]]' "+dirs+" 2>/dev/null; true")
	default:
		return nil, fmt.Errorf("there is no include line for %s", ws.Server)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s's configuration: %s", ws.Server, firstLine(conf, err))
	}
	base := path.Dir(ws.Main)
	if ws.Server == ServerApache {
		for _, line := range strings.Split(conf, "\n") {
			if m := apacheRoot.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				base = strings.Trim(m[1], `"`)
			}
		}
	}
	var out []includeLine
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		var arg string
		optional := true
		switch ws.Server {
		case ServerCaddy:
			if m := caddyImport.FindStringSubmatch(line); m != nil {
				arg = m[1]
			}
		case ServerNginx:
			if m := nginxInclude.FindStringSubmatch(line); m != nil {
				arg = m[1]
			}
		case ServerApache:
			if m := apacheInclude.FindStringSubmatch(line); m != nil {
				arg, optional = m[2], strings.EqualFold(m[1], "includeoptional")
			}
		}
		if arg == "" {
			continue
		}
		p := strings.Trim(arg, `"'`)
		if !path.IsAbs(p) {
			p = path.Join(base, p)
		}
		out = append(out, includeLine{text: line, path: path.Clean(p), optional: optional})
	}
	return out, nil
}

// wildcardLine is the line of the server's configuration that includes
// seen by a wildcard taking in a file named name, or empty when there is
// none or the configuration cannot be read.
func wildcardLine(h Host, ws WebServer, seen, name string) string {
	lines, err := includeLines(h, ws)
	if err != nil {
		return ""
	}
	for _, l := range lines {
		if !l.optional || path.Dir(l.path) != seen {
			continue
		}
		if ok, _ := path.Match(path.Base(l.path), name); ok && strings.ContainsAny(path.Base(l.path), "*?[") {
			return l.text
		}
	}
	return ""
}

// RefusedWhy is why a line naming the toolkit's directory is refused.
func RefusedWhy(s Server, line string) string {
	if s == ServerApache && !strings.HasPrefix(strings.ToLower(line), "includeoptional") {
		return "Apache's Include fails when nothing matches it, so once the directory is gone the next restart fails and takes every site on the host down with it"
	}
	return "it names a path rather than a wildcard, so once that file or the directory is gone the next restart fails and takes every site on the host down with it"
}

// Outcome is what writing or removing one file came to.
type Outcome struct {
	App  string
	Path string
	Kind OutcomeKind
	// Why is the server's error for Refused, what could not be done for
	// Failed, and why the file is left for Edited.
	Why string
	// Reload is what became of the server, for Written and Removed.
	Reload Reload
}

// ReloadKind is what became of a web server once its configuration
// validated with a change.
type ReloadKind int

const (
	// NotReloaded is a server nothing was asked of.
	NotReloaded ReloadKind = iota
	// Reloaded is a server in a container reloaded gracefully inside it.
	Reloaded
	// Restarted is a server in a container whose graceful reload failed,
	// restarted with docker restart.
	Restarted
	// OperatorReloads is a server on the host itself: validated, and left
	// for the operator to reload, since it is theirs to restart.
	OperatorReloads
)

// Reload is what became of a web server once its configuration validated.
type Reload struct {
	Kind ReloadKind
	// Command reloads a server on the host itself, for the operator to
	// run, with OperatorReloads.
	Command string
	// Why is the graceful reload's error, with Restarted.
	Why string
}

// OutcomeKind is what happened to one file.
type OutcomeKind int

const (
	// Unchanged is a file already as it should be: nothing written, the
	// server not reloaded.
	Unchanged OutcomeKind = iota
	// Written is a file written and the configuration validated, with
	// Reload what became of the server.
	Written
	// Removed is a file deleted and the configuration validated, with
	// Reload what became of the server.
	Removed
	// Gone is a recorded file already deleted.
	Gone
	// Edited is a file left as it is: changed on the host since apply wrote
	// it, or there and never recorded as this deployment's.
	Edited
	// Refused is a file whose write or removal the server refused, its
	// configuration invalid with it, or a server in a container neither
	// reloaded nor restarted: its previous content is back, and the server
	// runs its previous configuration.
	Refused
	// Failed is a file that could not be read, written or removed on the
	// host: nothing was asked of the server.
	Failed
)

// File is one file the toolkit wants in the include directory.
type File struct {
	App     string
	Name    string
	Content string
}

// Sync makes the include directory hold files and nothing else of the
// toolkit's: first each recorded file not among them is removed, so a
// hostname moving between two files is never in both at once; then each of
// files is written. The directory is created when it is missing. It returns
// the record to keep and an outcome per file.
func Sync(h Host, ws WebServer, inc Include, files []File, recorded []render.ManifestWebServer) ([]render.ManifestWebServer, []Outcome) {
	want := map[string]bool{}
	for _, f := range files {
		want[path.Join(inc.Dir, f.Name)] = true
	}
	var keep []render.ManifestWebServer
	var outcomes []Outcome
	byPath := map[string]render.ManifestWebServer{}
	for _, e := range recorded {
		if want[e.Path] {
			byPath[e.Path] = e
			continue
		}
		o := Remove(h, e)
		outcomes = append(outcomes, o)
		if o.Kind != Removed && o.Kind != Gone {
			keep = append(keep, e)
		}
	}
	for _, f := range files {
		p := path.Join(inc.Dir, f.Name)
		rec, ok := byPath[p]
		var prev *render.ManifestWebServer
		if ok {
			prev = &rec
		}
		e, o := write(h, ws, p, f, prev)
		outcomes = append(outcomes, o)
		if e != nil {
			keep = append(keep, *e)
		}
	}
	return keep, outcomes
}

// write writes one file, validates the server's whole configuration and
// reloads it as control.apply does, and puts back what was there when that
// fails.
func write(h Host, ws WebServer, p string, f File, rec *render.ManifestWebServer) (*render.ManifestWebServer, Outcome) {
	o := Outcome{App: f.App, Path: p}
	entry := &render.ManifestWebServer{App: f.App, Path: p, SHA256: sum(f.Content), Server: string(ws.Server), Container: ws.Container, Config: ws.Main}
	have, found, err := readFile(h, p)
	if err != nil {
		o.Kind, o.Why = Failed, err.Error()
		return rec, o
	}
	switch {
	case found && have == f.Content:
		o.Kind = Unchanged
		return entry, o
	case found && rec == nil:
		o.Kind, o.Why = Edited, "it is there and not recorded as this deployment's"
		return nil, o
	case found && sum(have) != rec.SHA256:
		o.Kind, o.Why = Edited, "it was changed on the host since apply wrote it"
		return rec, o
	}
	if err := writeFile(h, p, f.Content); err != nil {
		o.Kind, o.Why = Failed, err.Error()
		return rec, o
	}
	ctl := control{server: ws.Server, container: ws.Container, main: ws.Main}
	reload, why := ctl.apply(h)
	if why != "" {
		if found {
			err = writeFile(h, p, have)
		} else {
			err = removeFile(h, p)
		}
		if err != nil {
			why += "; and putting back what was there failed: " + err.Error()
		}
		o.Kind, o.Why = Refused, why
		if found {
			return rec, o
		}
		return nil, o
	}
	o.Kind, o.Reload = Written, reload
	return entry, o
}

// Remove deletes one recorded file, validates the server's whole
// configuration and reloads it as control.apply does, and puts the file
// back when that fails.
// A file changed since it was written is left, and one already gone is
// fine.
func Remove(h Host, e render.ManifestWebServer) Outcome {
	o := Outcome{App: e.App, Path: e.Path}
	have, found, err := readFile(h, e.Path)
	switch {
	case err != nil:
		o.Kind, o.Why = Failed, err.Error()
		return o
	case !found:
		o.Kind = Gone
		return o
	case sum(have) != e.SHA256:
		o.Kind, o.Why = Edited, "it was changed on the host since apply wrote it"
		return o
	}
	if err := removeFile(h, e.Path); err != nil {
		o.Kind, o.Why = Failed, err.Error()
		return o
	}
	ctl := control{server: Server(e.Server), container: e.Container, main: e.Config}
	reload, why := ctl.apply(h)
	if why != "" {
		if err := writeFile(h, e.Path, have); err != nil {
			why += "; and putting the file back failed: " + err.Error()
		}
		o.Kind, o.Why = Refused, why
		return o
	}
	o.Kind, o.Reload = Removed, reload
	return o
}

// ValidateAndReload validates ws's whole configuration and, for a server in
// a container, reloads it, as writing a file does; a server on the host
// itself is left for the operator to reload. The error is the server's own
// when it refuses, and nothing is reloaded then.
func ValidateAndReload(h Host, ws WebServer) (Reload, error) {
	reload, why := control{server: ws.Server, container: ws.Container, main: ws.Main}.apply(h)
	if why != "" {
		return Reload{}, errors.New(why)
	}
	return reload, nil
}

// control validates one web server, and reloads one in a container.
type control struct {
	server    Server
	container string
	main      string
}

// apply validates the server's whole configuration, and is the server's own
// error when it fails, the server left on the configuration it ran. A
// server in a container is then reloaded gracefully inside it, and
// restarted when that fails. One on the host itself is the operator's to
// restart, so it is not reloaded: the command that reloads it is returned
// for them to run.
func (c control) apply(h Host) (Reload, string) {
	var validate, reload string
	switch c.server {
	case ServerCaddy:
		validate = "caddy validate --config " + quote(c.main) + " --adapter caddyfile"
		reload = "caddy reload --config " + quote(c.main) + " --adapter caddyfile"
	case ServerNginx:
		validate, reload = "nginx -t", "nginx -s reload"
	case ServerApache:
		validate, reload = "apachectl configtest", "apachectl graceful"
	default:
		return Reload{}, "there is no way to validate and reload " + string(c.server)
	}
	if out, err := serverRun(h, c.container, validate+" 2>&1"); err != nil {
		return Reload{}, string(c.server) + " refused the configuration: " + serverError(out, err)
	}
	if c.container == "" {
		return Reload{Kind: OperatorReloads, Command: c.reloadCommand(h)}, ""
	}
	out, err := serverRun(h, c.container, reload+" 2>&1")
	if err == nil {
		return Reload{Kind: Reloaded}, ""
	}
	why := serverError(out, err)
	if back, err := run(h, "docker restart "+quote(c.container)+" 2>&1"); err != nil {
		return Reload{}, string(c.server) + " did not reload: " + why + "; and docker restart " + c.container + " failed: " + serverError(back, err)
	}
	return Reload{Kind: Restarted, Why: why}, ""
}

// units are the systemd units each server runs as on the host itself.
var units = map[Server][]string{
	ServerCaddy:  {"caddy"},
	ServerNginx:  {"nginx"},
	ServerApache: {"apache2", "httpd"},
}

// reloadCommand is what reloads a server on the host itself: its systemd
// unit's reload when one is active, otherwise the server's own.
func (c control) reloadCommand(h Host) string {
	for _, unit := range units[c.server] {
		if _, err := h.Plain.Run("systemctl is-active --quiet " + unit); err == nil {
			return "sudo systemctl reload " + unit
		}
	}
	switch c.server {
	case ServerCaddy:
		return "caddy reload --config " + c.main + " --adapter caddyfile"
	case ServerNginx:
		return "sudo nginx -s reload"
	}
	return "sudo apachectl graceful"
}

// serverError is the lines of a server's answer that say what is wrong:
// its last few, where validate and reload put the error.
func serverError(out string, err error) string {
	text := strings.TrimSpace(out)
	if text == "" {
		return firstLine(out, err)
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, "\n")
}

// serverRun runs a command of the server's own: inside its container, or on
// the host.
func serverRun(h Host, container, command string) (string, error) {
	if container != "" {
		command = "docker exec " + quote(container) + " sh -c " + quote(command)
	}
	return run(h, command)
}

// run runs a command as the ssh user, and through sudo when that is
// refused for want of permission.
func run(h Host, command string) (string, error) {
	out, err := h.Plain.Run(command)
	if err != nil && h.Sudo != nil && denied(out, err) {
		return h.Sudo.Run(command)
	}
	return out, err
}

func denied(out string, err error) bool {
	s := strings.ToLower(out + " " + err.Error())
	return strings.Contains(s, "permission denied") || strings.Contains(s, "operation not permitted")
}

const absent = "__PAISANS_ABSENT__"

func readFile(h Host, p string) (string, bool, error) {
	out, err := run(h, fmt.Sprintf("if [ -e %[1]s ]; then cat %[1]s; else echo %[2]s; fi", quote(p), absent))
	if err != nil {
		return "", false, fmt.Errorf("reading %s: %s", p, firstLine(out, err))
	}
	if out == absent+"\n" {
		return "", false, nil
	}
	return out, true, nil
}

// writeFile writes through a temporary file beside p, so the server never
// reads half a file, creating the directory when it is missing.
func writeFile(h Host, p, content string) error {
	tmp := path.Join(path.Dir(p), "."+path.Base(p)+".paisans-tmp")
	out, err := run(h, fmt.Sprintf("set -e; mkdir -p %s; printf '%%s' %s > %s; chmod 0644 %s; mv -f %s %s", quote(path.Dir(p)), quote(content), quote(tmp), quote(tmp), quote(tmp), quote(p)))
	if err != nil {
		return fmt.Errorf("writing %s: %s", p, firstLine(out, err))
	}
	return nil
}

func removeFile(h Host, p string) error {
	out, err := run(h, "rm -f -- "+quote(p))
	if err != nil {
		return fmt.Errorf("removing %s: %s", p, firstLine(out, err))
	}
	return nil
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
