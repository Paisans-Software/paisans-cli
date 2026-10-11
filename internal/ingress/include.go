package ingress

import (
	"crypto/sha256"
	"encoding/hex"
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
	return Include{}, fmt.Sprintf("%s is not mounted into the container %s, so %s cannot read what is written there. Add it to the volumes of that container's service in its compose file:\n\n    - %s:%s\n\nand recreate the container, then run paisans apply --execute again.", dir, ws.Container, ws.Server, dir, seen)
}

func mountAdvice(ws WebServer, seen string) string {
	host := "/path/on/the/host/" + IncludeName
	if ws.Config != "" {
		host = path.Join(path.Dir(ws.Config), IncludeName)
	}
	what := "its configuration"
	if ws.Config != "" {
		what = ws.Config
	}
	return fmt.Sprintf("The container %s mounts %s alone, so no directory beside it is visible inside the container. Add one to the volumes of that container's service in its compose file:\n\n    - %s:%s\n\nand recreate the container, then run paisans apply --execute again: it finds the directory and records it in paisans.yaml.", ws.Container, what, host, seen)
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
// is, then the snippet `ingress show` prints for ws.
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
		return IncludeMissing, "", fmt.Errorf("there is no include line for %s", ws.Server)
	}
	if err != nil {
		return IncludeMissing, "", fmt.Errorf("reading %s's configuration: %s", ws.Server, firstLine(conf, err))
	}
	base := path.Dir(ws.Main)
	if ws.Server == ServerApache {
		for _, line := range strings.Split(conf, "\n") {
			if m := apacheRoot.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				base = strings.Trim(m[1], `"`)
			}
		}
	}
	state, refused := IncludeMissing, ""
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
		p = path.Clean(p)
		dir, file := path.Dir(p), path.Base(p)
		switch {
		case p == seen || (dir == seen && !strings.ContainsAny(file, "*?[")):
			state, refused = IncludeRefused, line
		case dir != seen:
		case !optional:
			state, refused = IncludeRefused, line
		default:
			if ok, _ := path.Match(file, name); ok {
				return IncludeWildcard, "", nil
			}
		}
	}
	return state, refused, nil
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
	// Why is the server's error for Failed, and why the file is left for
	// Edited.
	Why string
}

// OutcomeKind is what happened to one file.
type OutcomeKind int

const (
	// Unchanged is a file already as it should be: nothing written, the
	// server not reloaded.
	Unchanged OutcomeKind = iota
	// Written is a file written, the configuration validated and the server
	// reloaded.
	Written
	// Removed is a file deleted, the configuration validated and the server
	// reloaded.
	Removed
	// Gone is a recorded file already deleted.
	Gone
	// Edited is a file left as it is: changed on the host since apply wrote
	// it, or there and never recorded as this deployment's.
	Edited
	// Failed is a file whose write or removal the server refused, or that
	// could not be reached: its previous content is back, and the server
	// runs its previous configuration.
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
func Sync(h Host, ws WebServer, inc Include, files []File, recorded []render.ManifestIngress) ([]render.ManifestIngress, []Outcome) {
	want := map[string]bool{}
	for _, f := range files {
		want[path.Join(inc.Dir, f.Name)] = true
	}
	var keep []render.ManifestIngress
	var outcomes []Outcome
	byPath := map[string]render.ManifestIngress{}
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
		var prev *render.ManifestIngress
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
// reloads it, and puts back what was there when either fails.
func write(h Host, ws WebServer, p string, f File, rec *render.ManifestIngress) (*render.ManifestIngress, Outcome) {
	o := Outcome{App: f.App, Path: p}
	entry := &render.ManifestIngress{App: f.App, Path: p, SHA256: sum(f.Content), Server: string(ws.Server), Container: ws.Container, Config: ws.Main}
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
	if why := ctl.apply(h); why != "" {
		if found {
			err = writeFile(h, p, have)
		} else {
			err = removeFile(h, p)
		}
		if err != nil {
			why += "; and putting back what was there failed: " + err.Error()
		}
		o.Kind, o.Why = Failed, why
		if found {
			return rec, o
		}
		return nil, o
	}
	o.Kind = Written
	return entry, o
}

// Remove deletes one recorded file, validates the server's whole
// configuration and reloads it, and puts the file back when either fails.
// A file changed since it was written is left, and one already gone is
// fine.
func Remove(h Host, e render.ManifestIngress) Outcome {
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
	if why := ctl.apply(h); why != "" {
		if err := writeFile(h, e.Path, have); err != nil {
			why += "; and putting the file back failed: " + err.Error()
		}
		o.Kind, o.Why = Failed, why
		return o
	}
	o.Kind = Removed
	return o
}

// control validates and reloads one web server.
type control struct {
	server    Server
	container string
	main      string
}

// apply validates the server's whole configuration and reloads it, and is
// the server's own error when either fails, the server left on the
// configuration it ran.
func (c control) apply(h Host) string {
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
		return "there is no way to validate and reload " + string(c.server)
	}
	if out, err := serverRun(h, c.container, validate+" 2>&1"); err != nil {
		return string(c.server) + " refused the configuration: " + serverError(out, err)
	}
	if out, err := serverRun(h, c.container, reload+" 2>&1"); err != nil {
		return string(c.server) + " did not reload: " + serverError(out, err)
	}
	return ""
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
