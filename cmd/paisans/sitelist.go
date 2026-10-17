package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// runSiteList prints every site the configuration declares, by name, with
// its roles and mesh address. It is how an operator finds the name to give
// --site or site remove without opening the declaration.
//
// The table is on stdout, so it can be piped. It reads the local file only,
// reaches no host, and changes nothing.
func runSiteList(args []string, stdout io.Writer) error {
	cfg, err := loadForList("site list", args)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tROLES\tADDRESS")
	for _, name := range cfg.SiteNames() {
		s := cfg.Sites[name]
		roles := make([]string, len(s.Roles))
		for i, r := range s.Roles {
			roles[i] = string(r)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, orDash(strings.Join(roles, ",")), orDash(s.Address))
	}
	return w.Flush()
}

// runHostList prints every site the configuration declares, by name, with
// the SSH destination the toolkit reaches its host at and its public
// addresses. The destination is the one every command that logs in uses,
// with the defaults filled in, so an operator sees where `host prepare
// --site` would connect without working the defaults out by hand.
//
// The table is on stdout, so it can be piped. It reads the local file only,
// reaches no host, and changes nothing.
func runHostList(args []string, stdout io.Writer) error {
	cfg, err := loadForList("host list", args)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tSSH\tPUBLIC\tPUBLIC6")
	for _, name := range cfg.SiteNames() {
		s := cfg.Sites[name]
		ssh := "-"
		if d := s.Destination(); d.Host != "" {
			ssh = d.String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, ssh, orDash(s.PublicAddress), orDash(s.PublicAddress6))
	}
	return w.Flush()
}

// loadForList parses the flags host list and site list share, --config and
// --verbose, and loads the declaration. It does not validate it: a
// list is for finding names, and a declaration validate would refuse still
// names its sites.
func loadForList(name string, args []string) (*config.Config, error) {
	fs := newFlagSet(name, flag.ContinueOnError)
	verboseFlags(fs, &verboseRun)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	if pos, err := parseAll(fs, args); err != nil {
		return nil, err
	} else if len(pos) > 0 {
		return nil, &ui.Problem{Hint: name + " takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	return config.Load(*configPath)
}

// orDash is s, or "-" for a column with nothing in it, so every row has the
// same number of fields.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
