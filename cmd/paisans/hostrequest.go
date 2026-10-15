package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runHostRequest is `paisans host request`: the request a host's owner
// admits one site from, on stdout, and its digest on stderr, so the request
// can be redirected to a file and the digest still read. It reaches no host.
func runHostRequest(args []string) error {
	fs := newFlagSet("host request", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	site := fs.String("site", "", "the site to request admission for, by the name it has in the configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("host request takes no argument: paisans host request --site <name> > <name>.admit. Got extra argument(s): %s", strings.Join(fs.Args(), " "))
	}
	if *site == "" {
		return fmt.Errorf("host request: name the site with --site, Eg: paisans host request --site <name> > <name>.admit")
	}
	// The request goes to stdout and nothing else does.
	r := errReporter(reporter())
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	result := validate.Check(cfg)
	if result.Refused() {
		reportFindings(r, *configPath, result)
		return refused(*configPath, len(result.Refusals()), "No request is written for a configuration validate refuses.")
	}
	req, err := admission.Build(cfg, *site)
	if err != nil {
		return fmt.Errorf("host request: %w", err)
	}
	data := admission.Encode(req)
	if _, err := os.Stdout.Write(data); err != nil {
		return fmt.Errorf("host request: writing the request: %w", err)
	}
	r.Result("digest %s: tell it to the host's owner over a channel you both trust, such as a call; host admit shows the same", admission.Digest(data))
	return nil
}
