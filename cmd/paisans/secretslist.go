package main

import (
	"flag"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/secretsrotate"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// listedSecret is one row of `secrets list`: a path, whether the file holds
// a value there, where the value comes from, and what `secrets rotate` does
// with it. It has no field for the value, so no row can print one.
type listedSecret struct {
	Path   string
	Set    bool
	Source secretsgen.Source
	// Rotate is the path `secrets rotate` takes for this secret, which is a
	// unit's for a secret rotated with its unit; "kept" for one rotate
	// keeps, or "mesh rekey" for a WireGuard key, which that command
	// replaces; empty when it has nothing to rotate.
	Rotate string
}

// runSecretsList prints every secret the configuration needs, and every other
// one the secrets file holds for something it still declares, by path: set
// or unset, generated, pasted or captured, and the path secrets rotate takes
// for it. It is how an operator finds the path to give secrets set or secrets
// rotate without opening the decrypted file, which is why it prints names
// alone: not a value, not part of one, not a length.
//
// The table is on stdout and the report on stderr, so the table can be piped.
// It reads the local files only, and changes nothing.
func runSecretsList(args []string, stdout io.Writer) error {
	fs := newFlagSet("secrets list", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	secretsPath := fs.String("secrets", "", "path to the secrets file (default: secrets.enc.yaml beside the config)")
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "secrets list takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	r := errReporter(reporter())
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
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

	rows, orphans := listSecrets(cfg, secrets)
	rotate, kept, err := rotateAll(cfg, secrets, rotateWiring{cfg: cfg})
	if err != nil {
		// The rotate column is a convenience; the paths and their state are
		// the list, and they do not depend on it.
		r.Warn("secrets list: which secrets rotate can take could not be read", err.Error())
	} else {
		markRotation(cfg, rows, rotate, kept)
	}

	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tSTATE\tSOURCE\tROTATE")
	unset := 0
	for _, row := range rows {
		state := "set"
		if !row.Set {
			state = "unset"
			unset++
		}
		how := row.Rotate
		if how == "" {
			how = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.Path, state, row.Source, how)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if orphans > 0 {
		r.Note(fmt.Sprintf("secrets: %d more path(s) in the file name something %s does not declare, and are not listed", orphans, config.FileName), "`paisans secrets prune` lists them")
	}
	r.Result("%d secret(s), %d unset. A generated one is filled by `paisans init`, a pasted one by `paisans secrets set`, and a captured one by `paisans deployment apply --execute`.", len(rows), unset)
	return nil
}

// listSecrets is every secret the configuration needs, and every other path
// the file holds a value at that names nothing gone from the configuration,
// sorted by path; and how many paths the file holds for something it no
// longer declares, which secrets prune reports.
func listSecrets(cfg *config.Config, secrets *config.Secrets) ([]listedSecret, int) {
	held := configstore.Flatten(secrets)
	var rows []listedSecret
	listed := map[string]bool{}
	for _, d := range secretsgen.Declare(cfg, secrets) {
		_, set := held[d.Path]
		rows = append(rows, listedSecret{Path: d.Path, Set: set, Source: d.Source})
		listed[d.Path] = true
	}
	orphans := secretsgen.Orphans(cfg, secrets, nil)
	gone := func(p string) bool {
		for _, o := range orphans {
			if p == o.Key || strings.HasPrefix(p, o.Key+".") {
				return true
			}
		}
		return false
	}
	stale := 0
	for p := range held {
		switch {
		case listed[p]:
		case gone(p):
			stale++
		default:
			rows = append(rows, listedSecret{Path: p, Set: true, Source: heldSource(p)})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows, stale
}

// heldSource is where a value the configuration does not call for came
// from, by its section: one recorded from a running service is captured, a
// next WireGuard key is generated by mesh rekey, and anything else was set by
// hand. Every secret init generates for a declared site or app is in
// secretsgen.Declare already.
func heldSource(p string) secretsgen.Source {
	switch {
	case strings.HasPrefix(p, "oidc_clients."), strings.HasPrefix(p, "pocket_id_groups."):
		return secretsgen.Captured
	case strings.HasSuffix(p, ".wireguard_next_private_key"),
		strings.HasPrefix(p, "cluster."), strings.HasPrefix(p, "storage."),
		p == "backup.restic_password", p == "backup.walg_libsodium_key":
		return secretsgen.Generated
	}
	return secretsgen.Pasted
}

// markRotation fills each row's Rotate from what `secrets rotate --all`
// would rotate on this deployment and what it keeps. A unit of secrets an
// app's blueprint groups is rotated by the unit's path, and an app's S3 pair
// by its secret key's, so each member names that path.
func markRotation(cfg *config.Config, rows []listedSecret, rotate []secretsrotate.Rotator, kept []keptSecret) {
	by := map[string]string{}
	for _, rp := range rotate {
		p := rp.Path()
		by[p] = p
		parts := strings.Split(p, ".")
		if len(parts) != 3 || parts[0] != "apps" {
			continue
		}
		app, last := parts[1], parts[2]
		if last == "s3_secret_access_key" {
			by["apps."+app+".s3_access_key_id"] = p
		}
		for _, m := range blueprints.SecretGroup(cfg.Apps[app].Blueprint, last) {
			by["apps."+app+"."+m.Name] = p
		}
	}
	for i := range rows {
		if !rows[i].Set {
			continue
		}
		if p, ok := by[rows[i].Path]; ok {
			rows[i].Rotate = p
			continue
		}
		for _, k := range kept {
			if ok, _ := path.Match(k.Path, rows[i].Path); ok {
				rows[i].Rotate = "kept"
				if k.Reason == wireguardKept {
					rows[i].Rotate = "mesh rekey"
				}
				break
			}
		}
	}
}
