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
// its roles, its mesh address, the SSH destination the toolkit reaches its
// host at and its public addresses. It is how an operator finds the name to
// give --site or site remove, and where site prepare --site would connect,
// without opening the declaration and working the ssh defaults out by hand.
//
// It loads the declaration without validating it: a list is for finding
// names, and a declaration validate would refuse still names its sites. The
// table is on stdout, so it can be piped. It reads the local file only,
// reaches no host, and changes nothing.
func runSiteList(args []string, stdout io.Writer) error {
	fs := newFlagSet("site list", flag.ContinueOnError)
	verboseFlags(fs, &verboseRun)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "site list takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tROLES\tADDRESS\tSSH\tPUBLIC\tPUBLIC6")
	for _, name := range cfg.SiteNames() {
		s := cfg.Sites[name]
		roles := make([]string, len(s.Roles))
		for i, r := range s.Roles {
			roles[i] = string(r)
		}
		ssh := ""
		if d := s.Destination(); d.Host != "" {
			ssh = d.String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, orDash(strings.Join(roles, ",")), orDash(s.Address), orDash(ssh), orDash(s.PublicAddress), orDash(s.PublicAddress6))
	}
	return w.Flush()
}

// orDash is s, or "-" for a column with nothing in it, so every row has the
// same number of fields.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
