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

// runSiteRequest is `paisans site request`: the request a host's owner
// admits one site from, on stdout, and its digest on stderr, so the request
// can be redirected to a file and the digest still read. It reaches no host.
func runSiteRequest(args []string) error {
	fs := newFlagSet("site request", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	site := fs.String("site", "", "the site to request admission for, by the name it has in the configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("site request takes no argument: paisans site request --site <name> > <name>.admit. Got extra argument(s): %s", quoteAll(fs.Args()))
	}
	if *site == "" {
		return fmt.Errorf("site request: name the site with --site, Eg: paisans site request --site <name> > <name>.admit")
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
		return fmt.Errorf("site request: %w", err)
	}
	data := admission.Encode(req)
	if _, err := os.Stdout.Write(data); err != nil {
		return fmt.Errorf("site request: writing the request: %w", err)
	}
	r.Result("digest %s: tell it to the host's owner over a channel you both trust, such as a call; site admit shows the same", admission.Digest(data))
	return nil
}

// quoteAll is args each quoted and joined by spaces, so an argument holding
// spaces or a newline cannot pass for text of the message.
func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = fmt.Sprintf("%q", a)
	}
	return strings.Join(q, " ")
}
