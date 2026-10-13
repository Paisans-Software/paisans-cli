package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostarch"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// readArchitectures asks each of sites for its architecture, one after
// another, and refuses a host that contradicts the arch its site records
// before anything on it changes. See hostarch.Mismatch.
func readArchitectures(r ui.Reporter, cfg *config.Config, sites []string, host func(name string) hostarch.Runner) (map[string]string, error) {
	line := r.Step("read each site's architecture")
	read := map[string]string{}
	var shown []string
	for _, name := range sites {
		arch, err := hostarch.Read(name, host(name))
		if err == nil {
			err = hostarch.Mismatch(name, cfg.Sites[name].Arch, arch)
		}
		if err != nil {
			line.Fail(err)
			return nil, err
		}
		read[name] = arch
		shown = append(shown, name+" "+arch)
	}
	line.Done(strings.Join(shown, ", "))
	return read, nil
}

// recordArchitectures puts every architecture in read that its site does not
// record yet into the configuration. With execute it writes them into the
// file at path in one edit, says so for each, and returns the configuration
// read again, as init's write is. A whole apply does this before it publishes
// the file, because each of its steps refuses bytes changed after the
// publish. A dry run writes nothing and sets them in memory, so its plan is
// checked against what the hosts reported.
//
// Either way, validate's data site rule is applied again afterwards: a site
// whose key was missing when validate ran could otherwise carry a mixed
// cluster past it.
func recordArchitectures(r ui.Reporter, path string, cfg *config.Config, read map[string]string, execute bool) (*config.Config, error) {
	missing := map[string]string{}
	for name, arch := range read {
		if cfg.Sites[name].Arch == "" {
			missing[name] = arch
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		if execute {
			if err := config.SetArch(path, missing); err != nil {
				return nil, err
			}
			for _, name := range names {
				r.Note(fmt.Sprintf("%s: arch %s recorded in %s", name, missing[name], config.FileName), "")
			}
			reread, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			cfg = reread
		} else {
			for _, name := range names {
				site := cfg.Sites[name]
				site.Arch = missing[name]
				cfg.Sites[name] = site
				r.Note(fmt.Sprintf("%s: arch %s, recorded in %s by --execute", name, missing[name], config.FileName), "")
			}
		}
	}
	for _, f := range validate.Check(cfg).Findings {
		if f.Rule == "data-sites-differ-in-arch" {
			return nil, errors.New(f.Key + ": " + f.Message)
		}
	}
	return cfg, nil
}
