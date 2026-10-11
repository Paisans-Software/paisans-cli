package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// webServerFilesHost reaches a monitor site to write the toolkit's files for
// its web server: as the ssh user, and through sudo with the answer the run
// settled where the ssh user may not write. Tests replace it.
var webServerFilesHost = webServerHost

// convergeWebServerFiles writes each external app's site into the include
// directory of the web server in front of it, before the web server check.
// Tests not about it replace it.
var convergeWebServerFiles = writeWebServerFiles

// webServerFiles is what writing the site files came to, for the web server
// check after it and for how the run ends.
type webServerFiles struct {
	// headed is whether the web server section is open.
	headed bool
	// apps is each app's site file, by app.
	apps map[string]*siteFile
	// warned are the steps that ended on a warning, an app's title or a
	// site's, and failed those that failed outright.
	warned, failed []string
}

// siteFile is what the site file step came to for one app.
type siteFile struct {
	// until is set when the step ended on a warning or a failure with
	// instructions of its own: what the app's check waits on, which says
	// no more than that.
	until string
	// ws is the web server found in front of the app, and h how to reach
	// its host. path is the file, set once it is in place, and reloaded
	// whether the server took it in during this run.
	ws       webserver.WebServer
	h        webserver.Host
	path     string
	reloaded bool
}

// writeWebServerFiles writes, on each monitor site with managed_by: external,
// the file that serves its app into the include directory of the operator's
// web server, validates the server's configuration, reloads a server in a
// container and leaves one on the host itself for the operator to reload,
// and removes every file it recorded there that is no longer wanted. An
// include_dir not yet in paisans.yaml is derived beside the server's
// configuration and written there. Nothing is written where the server
// does not include the directory by a wildcard. Each problem with the
// operator's web server is a warning on its step's line, with the one set
// of instructions that fixes it; what cannot be read or written on the
// host is a failure.
func writeWebServerFiles(r ui.Reporter, cfg *config.Config, configPath string, sudo bool) webServerFiles {
	out := webServerFiles{apps: map[string]*siteFile{}}
	d := cfg.Deployment()
	head := func() {
		if !out.headed {
			r.Section("web server")
			out.headed = true
		}
	}
	for _, site := range cfg.MonitorSites() {
		s := cfg.Sites[site]
		if s.WebServerManagedBy() != config.ManagedByExternal || s.WebServer == nil {
			continue
		}
		var apps []string
		for _, app := range externalApps(cfg) {
			if t, err := webserver.For(cfg, app); err == nil && t.Site == site {
				apps = append(apps, app)
			}
		}
		t := convergeHost(site, s, sudo)
		recorded, err := apply.ReadWebServerRecord(t, d)
		if err != nil {
			head()
			title := "site files on " + site
			r.Step(title).Fail(err)
			r.Explain(errLine(err))
			out.failed = append(out.failed, title)
			for _, app := range apps {
				out.apps[app] = &siteFile{until: "its site file is in place"}
			}
			continue
		}
		if len(recorded) == 0 && (len(apps) == 0 || s.WebServer.IncludeDir.Off) {
			continue
		}
		head()
		h := webServerFilesHost(cfg, site, sudo)
		if len(apps) == 0 || s.WebServer.IncludeDir.Off {
			// Nothing is wanted here any more: what is recorded goes.
			title := "remove site files on " + site
			step := r.Step(title)
			keep, outcomes := webserver.Sync(h, webserver.WebServer{}, webserver.Include{}, nil, recorded)
			out.add(title, reportWebServerFiles(r, step, &siteFile{}, outcomes, recorded, keep, d, t, "", ""))
			continue
		}
		for _, app := range apps {
			sf, mark := writeWebServerFile(r, cfg, configPath, site, app, sudo, h, t, recorded)
			out.apps[app] = sf
			out.add("site file for "+app, mark)
		}
	}
	return out
}

// add counts a step that ended on m.
func (w *webServerFiles) add(title string, m ui.Mark) {
	switch m {
	case ui.Warned:
		w.warned = append(w.warned, title)
	case ui.Failed:
		w.failed = append(w.failed, title)
	}
}

// writeWebServerFile writes one app's file, and returns what came of it
// and the mark its step ended on.
func writeWebServerFile(r ui.Reporter, cfg *config.Config, configPath, site, app string, sudo bool, h webserver.Host, t apply.Transport, recorded []render.ManifestWebServer) (*siteFile, ui.Mark) {
	sf := &siteFile{h: h, until: "its site file is in place"}
	step := r.Step("site file for " + app)
	failed := func(err error) (*siteFile, ui.Mark) {
		step.Fail(err)
		r.Explain(errLine(err))
		return sf, ui.Failed
	}
	warned := func(short, detail string) (*siteFile, ui.Mark) {
		step.End(ui.Warned, short)
		r.Explain(detail)
		return sf, ui.Warned
	}
	target, err := webserver.For(cfg, app)
	if err != nil {
		return failed(err)
	}
	optOut := fmt.Sprintf("Or set sites.%s.web_server.include_dir: false, and add what paisans web-server show --app %s prints to your web server yourself.", site, app)
	step.Progress("read the web server on " + site)
	ws := findWebServer(cfg, target, sudo)
	sf.ws = ws
	if !ws.Manages() {
		sf.until = "your web server serves it"
		return warned("no site file written",
			fmt.Sprintf("%s\nApply writes a site file for Caddy, nginx and Apache whose configuration it can find. Add what paisans web-server show --app %s prints to your web server yourself, and set sites.%s.web_server.include_dir: false so apply does not try again.", ws.Summary(), app, site))
	}
	dir := cfg.Sites[site].WebServer.IncludeDir
	var inc webserver.Include
	var why string
	if dir.Path != "" {
		inc, why = webserver.IncludeAt(ws, dir.Path)
	} else {
		inc, why = webserver.DeriveInclude(ws)
	}
	if why != "" {
		return warned(fmt.Sprintf("no directory %s reads to write in", ws.Server), why+"\n"+optOut)
	}
	conf := ws.Config
	if conf == "" {
		conf = ws.Main
	}
	name := webserver.FileName(cfg.Deployment().Prefix(), app, ws.Server)
	state, found, err := webserver.CheckInclude(h, ws, inc.Seen, name)
	line, where := webserver.IncludeLine(ws.Server, inc.Seen)
	switch {
	case err != nil:
		return failed(err)
	case state == webserver.IncludeMissing:
		return warned(fmt.Sprintf("%s does not include %s", ws.Server, inc.Seen),
			fmt.Sprintf("Nothing was written. Add this line to %s, %s, once:\n\n    %s\n\nthen run paisans apply --execute again. It is a wildcard so that the server still starts, without the paisans sites, if the directory is ever gone.\n%s", conf, where, line, optOut))
	case state == webserver.IncludeRefused:
		return warned(fmt.Sprintf("%s includes %s by a path, not a wildcard", ws.Server, inc.Seen),
			fmt.Sprintf("Nothing was written. %s has:\n\n    %s\n\nand %s. Replace it with:\n\n    %s\n\nthen run paisans apply --execute again.", conf, found, webserver.RefusedWhy(ws.Server, found), line))
	}
	if dir.Unset() {
		// Recorded only once the server is known to read it, so a
		// directory nothing includes is never written into paisans.yaml.
		if err := config.SetIncludeDir(configPath, site, inc.Dir); err != nil {
			return failed(err)
		}
		cfg.Sites[site].WebServer.IncludeDir = config.IncludeDir{Path: inc.Dir}
		// Said once the step has ended, so it does not break into it.
		defer r.Note(fmt.Sprintf("%s: include_dir set to %s in %s", site, inc.Dir, filepath.Base(configPath)),
			fmt.Sprintf("Apply derived it beside %s's configuration, %s, which includes it. Change it there to use another directory, or set it to false to have apply write nothing for your web server.", ws.Server, conf))
	}
	step.Progress("write " + name)
	files := []webserver.File{{App: app, Name: name, Content: webserver.FileContent(target, ws)}}
	keep, outcomes := webserver.Sync(h, ws, inc, files, recorded)
	advice := ""
	if ws.Certbot != "" {
		advice = "If the certificate is not there yet, obtain it as the others were, then run paisans apply --execute again:\n\n    " + ws.Certbot
	}
	sf.until = ""
	return sf, reportWebServerFiles(r, step, sf, outcomes, recorded, keep, cfg.Deployment(), t, app, advice)
}

// problem is one thing a step ended on: short on its line, detail under it.
type problem struct{ short, detail string }

// reportWebServerFiles ends step with what happened to the files, on a
// warning or a failure with what to do about each problem, and records
// what is kept in the site's manifest. It notes in sf what the app's check
// needs to know, and returns the step's mark.
func reportWebServerFiles(r ui.Reporter, step ui.Step, sf *siteFile, outcomes []webserver.Outcome, recorded, keep []render.ManifestWebServer, d deployment.Deployment, t apply.Transport, app, advice string) ui.Mark {
	// Which server each file is for: a removed file's as recorded, a
	// written one's as found.
	entries := map[string]render.ManifestWebServer{}
	for _, e := range recorded {
		entries[e.Path] = e
	}
	for _, o := range outcomes {
		if o.App == app && sf.ws.Server != "" {
			entries[o.Path] = render.ManifestWebServer{Server: string(sf.ws.Server), Container: sf.ws.Container}
		}
	}
	var warned, failed []problem
	var done []string
	for _, o := range outcomes {
		srv := entries[o.Path].Server
		switch o.Kind {
		case webserver.Written, webserver.Removed:
			what := " written"
			if o.Kind == webserver.Removed {
				what = " removed"
				step.Detail("removed %s", o.Path)
			} else {
				step.Detail("wrote %s", o.Path)
				sf.path = o.Path
			}
			switch o.Reload.Kind {
			case webserver.Reloaded:
				done = append(done, o.Path+what+", "+srv+" reloaded")
				sf.reloaded = true
			case webserver.Restarted:
				step.Detail("%s's reload failed: %s", srv, o.Reload.Why)
				done = append(done, fmt.Sprintf("%s%s, container %s restarted: its reload failed", o.Path, what, entries[o.Path].Container))
				sf.reloaded = true
			case webserver.OperatorReloads:
				warned = append(warned, problem{o.Path + what + ", " + srv + " not reloaded", reloadAdvice(srv, o.Reload.Command, o.Kind == webserver.Removed, app)})
				if o.Kind == webserver.Written {
					sf.until = srv + " is reloaded"
				}
			default:
				done = append(done, o.Path+what)
			}
		case webserver.Unchanged:
			done = append(done, o.Path)
			sf.path = o.Path
		case webserver.Gone:
			step.Detail("%s already gone", o.Path)
		case webserver.Edited:
			warned = append(warned, problem{"left " + o.Path + " as it is", fmt.Sprintf("Apply did not change it because %s. Delete it to have apply write it again.", o.Why)})
			if o.App == app {
				sf.until = "its site file is as apply writes it"
			}
		case webserver.Refused:
			detail := o.Why + "\nThe file is as it was before, and the web server runs its previous configuration."
			if advice != "" {
				detail += "\n" + advice
			}
			warned = append(warned, problem{srv + " refused " + o.Path, detail})
			if o.App == app {
				sf.until = "its site file is in place"
			}
		case webserver.Failed:
			failed = append(failed, problem{"could not change " + o.Path, o.Why})
			if o.App == app {
				sf.until = "its site file is in place"
			}
		}
	}
	if err := apply.WriteWebServerRecord(t, d, keep); err != nil {
		failed = append(failed, problem{"the record of its site files was not written", errLine(err)})
	}
	mark, problems := ui.OK, append(failed, warned...)
	switch {
	case len(failed) > 0:
		mark = ui.Failed
	case len(warned) > 0:
		mark = ui.Warned
	default:
		step.Done(strings.Join(done, "; "))
		return mark
	}
	var shorts, details []string
	for _, p := range problems {
		shorts, details = append(shorts, p.short), append(details, p.detail)
	}
	step.End(mark, strings.Join(shorts, "; "))
	r.Explain(strings.Join(details, "\n\n"))
	return mark
}

// reloadAdvice tells the operator to reload a web server on the host
// itself, which apply validated and left alone: with command, then, for a
// written file, check the app.
func reloadAdvice(server, command string, removed bool, app string) string {
	if removed {
		return fmt.Sprintf("%s's configuration is valid without it, and %s runs on the host itself, so apply leaves its reload to you; until then it still serves what the file held:\n\n    %s", server, server, command)
	}
	return fmt.Sprintf("%s's configuration is valid with it, and %s runs on the host itself, so apply leaves its reload to you:\n\n    %s\n\nthen run paisans web-server check --app %s.", server, server, command, app)
}
