package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// imagesTransport reaches a site for `paisans images`, which only reads.
// Tests replace it.
var imagesTransport = func(name string, site config.Site) apply.Transport {
	return siteTransport(name, site, "")
}

// errImagesMove is --check's answer when a build would move or an override
// lags. It is not a failure, and exits 3 so a schedule can tell it from one.
var errImagesMove = errors.New("a build would move, or an override lags")

// imageRow is one image one site runs, as `paisans images` prints it.
type imageRow struct {
	site, stack, service, origin, running, newest, note string
	// moves is whether the next apply changes the build the site runs.
	moves bool
}

func runImages(args []string, stdout io.Writer) error {
	fs := newFlagSet("images", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets (default: secrets.enc.yaml beside the config)")
	check := fs.Bool("check", false, "exit 3 when any build would move or any override lags")
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "images takes no arguments", Explain: fmt.Sprintf("It lists every site's images; %q is not a flag.", pos[0])}
	}
	r := errReporter(reporter())
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	reportFindings(r, *configPath, result)
	if result.Refused() {
		return refused(*configPath, len(result.Refusals()), "")
	}
	if *secretsPath == "" {
		*secretsPath = filepath.Join(filepath.Dir(*configPath), "secrets.enc.yaml")
	}
	secrets, err := config.LoadDeploymentSecrets(*secretsPath, cfg.ID)
	if err != nil {
		return err
	}
	if !secrets.Encrypted {
		warnUnencrypted(r, *secretsPath)
	}
	return writeImages(r, cfg, secrets, stdout, *check)
}

// writeImages prints the table imageRows lists. It returns an error naming
// each site that could not be read, after the whole table is out, and
// errImagesMove when check is set and a build would move or an override lags.
func writeImages(r ui.Reporter, cfg *config.Config, secrets *config.Secrets, stdout io.Writer, check bool) error {
	rows, lags, unread, err := imageRows(r, cfg, secrets)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tSTACK\tSERVICE\tSOURCE\tRUNNING\tNEWEST\tNOTE")
	moves := lags
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.site, row.stack, row.service, row.origin, row.running, row.newest, row.note)
		if row.moves {
			moves = true
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(unread) > 0 {
		return unreadProblem(unread)
	}
	if check && moves {
		return errImagesMove
	}
	return nil
}

// imageRows plans every site as a dry run of apply does and lists each image
// it would run, and reports whether any override lags what it replaced. An
// image the registry cannot resolve is a row with newest unknown
// (apply.TolerateUnresolved). A site that cannot be planned is warned about,
// listed from the render alone and named in the third result, so one site
// down hides nothing about the others. It writes nothing to any host and
// asks for no sudo: a plan only reads (apply.Build).
func imageRows(r ui.Reporter, cfg *config.Config, secrets *config.Secrets) ([]imageRow, bool, []string, error) {
	defer cacheDigests()()
	defer planEverySite()()
	var rendered *render.Plan
	var rows []imageRow
	lags := false
	var unread []string
	seen := map[string]bool{}
	for _, site := range cfg.SiteNames() {
		plan, err := planSiteApply(cfg, secrets, site, imagesTransport(site, cfg.Sites[site]), apply.SkipSweepRead(), apply.TolerateUnresolved())
		if err != nil {
			r.Warn(fmt.Sprintf("could not plan %s; its running builds are unknown", site), err.Error())
			if rendered == nil {
				if rendered, err = render.Build(cfg, secrets); err != nil {
					return nil, false, nil, err
				}
			}
			for _, image := range rendered.Images[site] {
				origin := image.Origin
				if origin == "" {
					origin = "-"
				}
				rows = append(rows, imageRow{site: site, stack: image.Stack, service: strings.Join(image.Services, ","), origin: origin, running: "unknown", newest: "unknown"})
			}
			unread = append(unread, site)
			continue
		}
		reportNotes(r, plan, seen)
		for _, pin := range plan.Pins {
			rows = append(rows, pinRow(site, pin))
			lags = lags || pin.Behind
		}
	}
	return rows, lags, unread, nil
}

// pinRow is one pin as a row: the build the site runs, the build the next
// apply runs, and what moves or lags.
func pinRow(site string, pin apply.ImagePin) imageRow {
	row := imageRow{site: site, stack: pin.Stack, service: strings.Join(pin.Services, ","), origin: pin.Origin, running: "-", newest: apply.ReferenceLabel(pin.Reference)}
	if pin.Deployed != "" {
		row.running = apply.ReferenceLabel(pin.Deployed)
	}
	var notes []string
	switch pin.Source {
	case "deployed":
		// The registry did not answer, and the plan keeps what runs.
		row.newest = "unknown"
	case "unresolved":
		// The registry did not answer, and nothing runs to keep.
		row.newest = "unknown"
		notes = append(notes, "unresolved: "+shortReason(pin.Unresolved))
	}
	if pin.Source != "unresolved" && (pin.Moved() || pin.FirstPin() || (pin.Deployed == "" && pin.Reference != "")) {
		row.moves = true
		notes = append(notes, "moves on next apply")
	}
	switch {
	case pin.Behind:
		notes = append(notes, "behind "+pin.Replaced)
	case pin.Replaced != "":
		notes = append(notes, "replaces "+pin.Replaced)
	}
	row.note = strings.Join(notes, "; ")
	if row.origin == "" {
		row.origin = "-"
	}
	return row
}

// shortReason is the last clause of a registry's error, which names what
// went wrong (Eg: no such host; not found; rate limited).
func shortReason(reason string) string {
	if i := strings.LastIndex(reason, ": "); i >= 0 {
		return reason[i+2:]
	}
	return reason
}

// unreadProblem is the error for the sites imageRows could not read. Past
// three, the hint counts them and the explanation names them.
func unreadProblem(unread []string) *ui.Problem {
	explain := "Their running builds are unknown; the warnings above say why, and the table lists what the configuration would run there."
	hint := "could not read " + strings.Join(unread, ", ")
	if len(unread) > 3 {
		hint = fmt.Sprintf("could not read %d sites", len(unread))
		explain = "The sites are " + strings.Join(unread, ", ") + ". " + explain
	}
	return &ui.Problem{Hint: hint, Explain: explain}
}
