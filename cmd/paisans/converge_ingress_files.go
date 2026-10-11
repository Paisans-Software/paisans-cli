package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/ingress"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// ingressFilesHost reaches a monitor site to write the toolkit's files for
// its web server: as the ssh user, and through sudo with the answer the run
// settled where the ssh user may not write. Tests replace it.
var ingressFilesHost = ingressHost

// convergeIngressFiles writes each external app's site into the include
// directory of the web server in front of it, before the ingress check.
// Tests not about it replace it.
var convergeIngressFiles = writeIngressFiles

// writeIngressFiles writes, on each monitor site in ingress mode external,
// the file that serves its app into the include directory of the operator's
// web server, validates the server's configuration and reloads it, and
// removes every file it recorded there that is no longer wanted. An
// include_dir not yet in paisans.yaml is derived beside the server's
// configuration and written there. Nothing is written where the server
// does not include the directory by a wildcard; that app is returned, with
// a warning carrying the one line to add, as is every app whose file could
// not be written. headed is whether it opened the ingress section.
func writeIngressFiles(r ui.Reporter, cfg *config.Config, configPath string, sudo bool) (failed []string, headed bool) {
	d := cfg.Deployment()
	for _, site := range cfg.MonitorSites() {
		s := cfg.Sites[site]
		if s.IngressMode() != config.IngressExternal || s.Ingress == nil {
			continue
		}
		var apps []string
		for _, app := range externalApps(cfg) {
			if t, err := ingress.For(cfg, app); err == nil && t.Site == site {
				apps = append(apps, app)
			}
		}
		t := convergeHost(site, s, sudo)
		recorded, err := apply.ReadIngressRecord(t, d)
		if err != nil {
			if !headed {
				r.Section("ingress")
				headed = true
			}
			r.Step("site files on " + site).Fail(err)
			failed = append(failed, apps...)
			continue
		}
		if len(recorded) == 0 && (len(apps) == 0 || s.Ingress.IncludeDir.Off) {
			continue
		}
		if !headed {
			r.Section("ingress")
			headed = true
		}
		h := ingressFilesHost(cfg, site, sudo)
		if len(apps) == 0 || s.Ingress.IncludeDir.Off {
			// Nothing is wanted here any more: what is recorded goes.
			step := r.Step("remove site files on " + site)
			keep, outcomes := ingress.Sync(h, ingress.WebServer{}, ingress.Include{}, nil, recorded)
			reportIngressFiles(r, step, site, outcomes, keep, d, t, "", "")
			continue
		}
		for _, app := range apps {
			if !writeIngressFile(r, cfg, configPath, site, app, sudo, h, t, recorded) {
				failed = append(failed, app)
			}
		}
	}
	return failed, headed
}

// writeIngressFile writes one app's file, and reports whether it is in
// place, written or already as it should be.
func writeIngressFile(r ui.Reporter, cfg *config.Config, configPath, site, app string, sudo bool, h ingress.Host, t apply.Transport, recorded []render.ManifestIngress) bool {
	step := r.Step("site file for " + app)
	target, err := ingress.For(cfg, app)
	if err != nil {
		step.Fail(err)
		return false
	}
	optOut := fmt.Sprintf("Or set sites.%s.ingress.include_dir: false, and add what paisans ingress show --app %s prints to your web server yourself.", site, app)
	step.Progress("read the web server on " + site)
	ws := ingressWebServer(cfg, target, sudo)
	if !ws.Manages() {
		step.End(ui.Failed, "no file written")
		r.Note(app+": no site file written ("+ws.Summary()+")",
			fmt.Sprintf("Apply writes a site file for Caddy, nginx and Apache whose configuration it can find. Add what paisans ingress show --app %s prints to your web server yourself, and set sites.%s.ingress.include_dir: false so apply does not try again.", app, site))
		return false
	}
	dir := cfg.Sites[site].Ingress.IncludeDir
	var inc ingress.Include
	var why string
	if dir.Path != "" {
		inc, why = ingress.IncludeAt(ws, dir.Path)
	} else {
		inc, why = ingress.DeriveInclude(ws)
	}
	if why != "" {
		step.End(ui.Failed, "no directory to write in")
		r.Note(app+": no directory "+string(ws.Server)+" reads to write its site file in", why+"\n"+optOut)
		return false
	}
	conf := ws.Config
	if conf == "" {
		conf = ws.Main
	}
	name := ingress.FileName(cfg.Deployment().Prefix(), app, ws.Server)
	state, found, err := ingress.CheckInclude(h, ws, inc.Seen, name)
	line, where := ingress.IncludeLine(ws.Server, inc.Seen)
	switch {
	case err != nil:
		step.Fail(err)
		return false
	case state == ingress.IncludeMissing:
		step.End(ui.Failed, fmt.Sprintf("%s does not include %s", ws.Server, inc.Seen))
		r.Note(fmt.Sprintf("%s: %s does not include %s, so nothing was written", app, ws.Server, inc.Seen),
			fmt.Sprintf("Add this line to %s, %s, once:\n\n    %s\n\nthen run paisans apply --execute again. It is a wildcard so that the server still starts, without the paisans sites, if the directory is ever gone.\n%s", conf, where, line, optOut))
		return false
	case state == ingress.IncludeRefused:
		step.End(ui.Failed, fmt.Sprintf("%s includes %s by a path, not a wildcard", ws.Server, inc.Seen))
		r.Note(fmt.Sprintf("%s: %s includes %s in a way apply does not write into", app, ws.Server, inc.Seen),
			fmt.Sprintf("%s has:\n\n    %s\n\nand %s. Replace it with:\n\n    %s\n\nthen run paisans apply --execute again.", conf, found, ingress.RefusedWhy(ws.Server, found), line))
		return false
	}
	if dir.Unset() {
		// Recorded only once the server is known to read it, so a
		// directory nothing includes is never written into paisans.yaml.
		if err := config.SetIncludeDir(configPath, site, inc.Dir); err != nil {
			step.Fail(err)
			return false
		}
		cfg.Sites[site].Ingress.IncludeDir = config.IncludeDir{Path: inc.Dir}
		// Said once the step has ended, so it does not break into it.
		defer r.Note(fmt.Sprintf("%s: include_dir set to %s in %s", site, inc.Dir, filepath.Base(configPath)),
			fmt.Sprintf("Apply derived it beside %s's configuration, %s, which includes it. Change it there to use another directory, or set it to false to have apply write nothing for your web server.", ws.Server, conf))
	}
	step.Progress("write " + name)
	files := []ingress.File{{App: app, Name: name, Content: ingress.FileContent(target, ws)}}
	keep, outcomes := ingress.Sync(h, ws, inc, files, recorded)
	advice := ""
	if ws.Certbot != "" {
		advice = "If the certificate is not there yet, obtain it as the others were, then run paisans apply --execute again:\n\n    " + ws.Certbot
	}
	return reportIngressFiles(r, step, site, outcomes, keep, cfg.Deployment(), t, string(ws.Server), advice)
}

// reportIngressFiles ends step with what happened to the files, warns of
// each left or refused, and records what is kept in the site's manifest.
// It reports whether every file came out as wanted.
func reportIngressFiles(r ui.Reporter, step ui.Step, site string, outcomes []ingress.Outcome, keep []render.ManifestIngress, d deployment.Deployment, t apply.Transport, server, advice string) bool {
	ok := true
	var notes [][2]string
	var done []string
	for _, o := range outcomes {
		switch o.Kind {
		case ingress.Written:
			step.Detail("wrote %s", o.Path)
			done = append(done, o.Path+" written, "+server+" reloaded")
		case ingress.Unchanged:
			done = append(done, o.Path)
		case ingress.Removed:
			step.Detail("removed %s", o.Path)
			done = append(done, o.Path+" removed")
		case ingress.Gone:
			step.Detail("%s already gone", o.Path)
		case ingress.Edited:
			ok = false
			notes = append(notes, [2]string{fmt.Sprintf("%s: left %s as it is", site, o.Path), fmt.Sprintf("Apply did not change it because %s. Delete it to have apply write it again.", o.Why)})
		case ingress.Failed:
			ok = false
			detail := o.Why + "\nThe file is as it was before, and the web server runs its previous configuration."
			if advice != "" {
				detail += "\n" + advice
			}
			notes = append(notes, [2]string{fmt.Sprintf("%s: the web server refused the change to %s", site, o.Path), detail})
		}
	}
	if err := apply.WriteIngressRecord(t, d, keep); err != nil {
		ok = false
		notes = append(notes, [2]string{site + ": the record of its site files was not written", errLine(err)})
	}
	if ok {
		step.Done(strings.Join(done, "; "))
	} else {
		step.End(ui.Failed, "see below")
	}
	for _, n := range notes {
		r.Note(n[0], n[1])
	}
	return ok
}
