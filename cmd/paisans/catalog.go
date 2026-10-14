package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// The catalog and blueprint commands (docs/specs/2026-10-12-blueprints-and-
// catalogs.md, "Commands"). They edit paisans-stack.yaml and read catalogs;
// none of them reaches a host.

func runCatalog(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return &ui.Problem{Hint: "catalog takes one subcommand, add, update, remove or list"}
	}
	switch args[0] {
	case "add":
		return runCatalogAdd(args[1:])
	case "update":
		return runCatalogUpdate(args[1:])
	case "remove":
		return runCatalogRemove(args[1:])
	case "list":
		return runCatalogList(args[1:], stdout)
	}
	return &ui.Problem{Hint: fmt.Sprintf("catalog has no subcommand %q", args[0]), Explain: "It takes add, update, remove or list."}
}

func runBlueprint(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return &ui.Problem{Hint: "blueprint takes one subcommand, list, show or lint"}
	}
	switch args[0] {
	case "list":
		return runBlueprintList(args[1:], stdout)
	case "show":
		return runBlueprintShow(args[1:], stdout)
	case "lint":
		return runBlueprintLint(args[1:])
	}
	return &ui.Problem{Hint: fmt.Sprintf("blueprint has no subcommand %q", args[0]), Explain: "It takes list, show or lint."}
}

// catalogRelease is the release of the catalog at url to pin: the tag asked
// for, or the newest, the commit it names, and a checkout of it fetched
// before any of its keys is trusted, checked to be a catalog.
func catalogRelease(url, tag string) (config.Catalog, string, []string, error) {
	var commit string
	var err error
	if tag == "" {
		tag, commit, err = blueprints.Newest(url)
	} else {
		commit, err = blueprints.TagCommit(url, tag)
	}
	if err != nil {
		return config.Catalog{}, "", nil, &ui.Problem{Hint: "the catalog's release cannot be read", Explain: err.Error() + "."}
	}
	cat := config.Catalog{URL: url, Tag: tag, Commit: commit}
	dir, err := blueprints.Fetch(cat)
	if err == nil {
		_, err = blueprints.ReadCatalog(dir)
	}
	var names []string
	if err == nil {
		names, err = blueprints.Blueprints(dir)
	}
	if err != nil {
		return config.Catalog{}, "", nil, &ui.Problem{Hint: url + " at " + tag + " is not a catalog this toolkit can read", Explain: err.Error() + "."}
	}
	return cat, dir, names, nil
}

func runCatalogAdd(args []string) error {
	fs := newFlagSet("catalog add", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	tag := fs.String("tag", "", "the release to pin (default: the newest tag)")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	name, url, err := catalogAddArgs(pos)
	if err != nil {
		return err
	}
	if name == config.CoreCatalog {
		return &ui.Problem{Hint: "core is the toolkit's own catalog", Explain: "It is always there. Point it at a mirror by writing catalogs.core.url by hand."}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if _, ok := cfg.Catalogs[name]; ok {
		return &ui.Problem{Hint: "catalogs." + name + " is already declared", Explain: "Move its pin with `paisans catalog update " + name + "`."}
	}
	r := reporter()
	r.Section("catalog " + name)
	s := r.Step("read " + url)
	cat, _, names, err := catalogRelease(url, *tag)
	if err != nil {
		s.Fail(err)
		return err
	}
	s.Done(fmt.Sprintf("%s, %s", cat.Tag, cat.Commit[:12]))
	r.Detail("blueprints: %s", strings.Join(names, ", "))
	s = r.Step("pin catalogs." + name + " in " + *configPath)
	if err := config.SetCatalog(*configPath, name, cat); err != nil {
		s.Fail(err)
		return err
	}
	s.Done("")
	r.Result("%s pinned at %s; its blueprints are %s", name, cat.Tag, qualify(name, names))
	return nil
}

// catalogAddArgs is the name and URL catalog add was given. With a URL alone
// the name is <owner>/<name>, its repository's name without the paisans-
// prefix; <owner>/<name> alone is the GitHub repository
// github.com/<owner>/paisans-<name>, as `brew tap user/repo` is
// github.com/user/homebrew-repo.
func catalogAddArgs(pos []string) (string, string, error) {
	usage := &ui.Problem{Hint: "catalog add takes a URL, or <owner>/<name>, with a name before it if you want another", Explain: "paisans catalog add https://github.com/example/paisans-blueprints\npaisans catalog add example/blueprints\npaisans catalog add notes https://github.com/example/paisans-blueprints"}
	switch len(pos) {
	case 2:
		if !config.ValidCatalogName(pos[0]) {
			return "", "", &ui.Problem{Hint: pos[0] + " is not a catalog name", Explain: "A catalog name is lowercase letters, digits and hyphens, or two such words as <owner>/<name>."}
		}
		if _, repo, ok := config.CatalogRepo(pos[1]); !ok || !strings.HasPrefix(strings.ToLower(repo), config.CatalogRepoPrefix) {
			return "", "", &ui.Problem{Hint: pos[1] + " is not a catalog's repository", Explain: "A catalog's repository is named " + config.CatalogRepoPrefix + "<name>."}
		}
		return pos[0], pos[1], nil
	case 1:
	default:
		return "", "", usage
	}
	arg := pos[0]
	if shorthand.MatchString(arg) {
		owner, name, _ := strings.Cut(arg, "/")
		return strings.ToLower(arg), "https://github.com/" + owner + "/" + config.CatalogRepoPrefix + name, nil
	}
	name, ok := config.DefaultCatalogName(arg)
	if !ok {
		_, repo, _ := config.CatalogRepo(arg)
		return "", "", &ui.Problem{Hint: arg + " is not a catalog's repository", Explain: "A catalog's repository is named " + config.CatalogRepoPrefix + "<name>, and " + repo + " is not. A catalog is added by its URL, or by <owner>/<name> for github.com/<owner>/" + config.CatalogRepoPrefix + "<name>."}
	}
	return name, arg, nil
}

// shorthand is <owner>/<name>, which catalog add reads as a GitHub
// repository rather than as a URL or a path.
var shorthand = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func qualify(catalog string, names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = catalog + "/" + n
	}
	return strings.Join(out, ", ")
}

func runCatalogUpdate(args []string) error {
	fs := newFlagSet("catalog update", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	tag := fs.String("tag", "", "the release to move to (default: the newest tag); needs one catalog named")
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (*tag != "" && len(pos) != 1) {
		return &ui.Problem{Hint: "catalog update takes at most one catalog, and --tag only with one", Explain: "paisans catalog update [<name>] [--tag <tag>]"}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var names []string
	if len(pos) == 1 {
		if _, ok := cfg.Catalogs[pos[0]]; !ok || pos[0] == config.CoreCatalog {
			return &ui.Problem{Hint: "catalogs." + pos[0] + " is not a declared catalog this command updates", Explain: "The core catalog moves with the toolkit."}
		}
		names = []string{pos[0]}
	} else {
		for name := range cfg.Catalogs {
			if name != config.CoreCatalog {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	r := reporter()
	moved := 0
	for _, name := range names {
		old := cfg.Catalogs[name]
		r.Section("catalog " + name)
		s := r.Step("read " + old.URL)
		next, dir, _, err := catalogRelease(old.URL, *tag)
		if err != nil {
			s.Fail(err)
			return err
		}
		if next.Commit == old.Commit {
			s.Done("up to date at " + old.Tag)
			continue
		}
		s.Done(fmt.Sprintf("%s, %s, was %s", next.Tag, next.Commit[:12], old.Tag))
		for _, app := range cfg.AppNames() {
			catalog, bp := config.SplitBlueprint(cfg.Apps[app].Blueprint)
			if catalog != name {
				continue
			}
			before, _ := blueprints.For(cfg.Apps[app].Blueprint)
			after, err := blueprints.ReadFrom(dir, bp)
			if err != nil {
				return &ui.Problem{Hint: "apps." + app + " would lose its blueprint", Explain: fmt.Sprintf("%s at %s: %v. Nothing was changed.", name, next.Tag, err)}
			}
			changes := blueprints.Changes(before, after)
			if len(changes) == 0 {
				r.Detail("%s: %s/%s changes nothing it grants", app, name, bp)
				continue
			}
			for _, c := range changes {
				r.Note(app+": "+name+"/"+bp+" "+c, "")
			}
		}
		s = r.Step("pin catalogs." + name + " in " + *configPath)
		if err := config.SetCatalog(*configPath, name, next); err != nil {
			s.Fail(err)
			return err
		}
		s.Done(next.Tag)
		moved++
	}
	r.Result("%d of %d catalogs moved", moved, len(names))
	return nil
}

func runCatalogRemove(args []string) error {
	fs := newFlagSet("catalog remove", flag.ContinueOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, configHelp)
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &ui.Problem{Hint: "catalog remove takes one catalog", Explain: "paisans catalog remove <name>"}
	}
	name := pos[0]
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if _, ok := cfg.Catalogs[name]; !ok {
		return &ui.Problem{Hint: "catalogs." + name + " is not declared"}
	}
	var users []string
	for _, app := range cfg.AppNames() {
		if c, _ := config.SplitBlueprint(cfg.Apps[app].Blueprint); c == name {
			users = append(users, app)
		}
	}
	if len(users) > 0 {
		return &ui.Problem{Hint: "catalogs." + name + " is in use", Explain: "Its blueprints run " + strings.Join(users, ", ") + ". Remove those apps first (`paisans app remove`), then the catalog."}
	}
	r := reporter()
	s := r.Step("remove catalogs." + name + " from " + *configPath)
	if err := config.RemoveCatalog(*configPath, name); err != nil {
		s.Fail(err)
		return err
	}
	s.Done("")
	return nil
}

func runCatalogList(args []string, stdout io.Writer) error {
	fs := newFlagSet("catalog list", flag.ContinueOnError)
	commonFlags(fs) // it prints a table, not a report, but takes the flags every command takes
	configPath := fs.String("config", config.FileName, configHelp)
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "catalog list takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	core := cfg.Catalogs[config.CoreCatalog]
	coreHow := "pinned here"
	if core.Commit == "" {
		coreHow = "its newest release"
	}
	cat := blueprints.CorePin(cfg, config.CoreCatalog)
	if cat.Commit == "" {
		if err := blueprints.LoadCore(core.URL); err != nil {
			return err
		}
	}
	coreURL, coreCommit := cat.URL, cat.Commit
	if cat.Tag != "" {
		coreHow = cat.Tag + ", " + coreHow
	}
	fmt.Fprintf(stdout, "core\t%s\t%s\t%s\n", coreURL, coreCommit[:12], coreHow)
	var names []string
	for name := range cfg.Catalogs {
		if name != config.CoreCatalog {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		c := cfg.Catalogs[name]
		tag := c.Tag
		if tag == "" {
			tag = "(no tag)"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s %s\n", name, c.URL, tag, c.Commit[:12])
	}
	return nil
}

func runBlueprintList(args []string, stdout io.Writer) error {
	fs := newFlagSet("blueprint list", flag.ContinueOnError)
	commonFlags(fs) // it prints a table, not a report, but takes the flags every command takes
	configPath := fs.String("config", config.FileName, configHelp)
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "blueprint list takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	reserved, core := blueprints.BuiltinNames()
	for _, n := range reserved {
		fmt.Fprintf(stdout, "%s\treserved\n", n)
	}
	for _, n := range core {
		fmt.Fprintf(stdout, "%s\tcore\n", n)
	}
	var names []string
	for name := range cfg.Catalogs {
		if name != config.CoreCatalog {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		dir, err := blueprints.Fetch(cfg.Catalogs[name])
		if err != nil {
			return &ui.Problem{Hint: "catalogs." + name + " cannot be read", Explain: err.Error() + "."}
		}
		bps, err := blueprints.Blueprints(dir)
		if err != nil {
			return &ui.Problem{Hint: "catalogs." + name + " cannot be read", Explain: err.Error() + "."}
		}
		for _, bp := range bps {
			fmt.Fprintf(stdout, "%s/%s\t%s\n", name, bp, cfg.Catalogs[name].Tag)
		}
	}
	return nil
}

func runBlueprintShow(args []string, stdout io.Writer) error {
	fs := newFlagSet("blueprint show", flag.ContinueOnError)
	commonFlags(fs) // it prints a table, not a report, but takes the flags every command takes
	configPath := fs.String("config", config.FileName, configHelp)
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &ui.Problem{Hint: "blueprint show takes one blueprint", Explain: "paisans blueprint show <name>, or <catalog>/<name>"}
	}
	catalog, name := config.SplitBlueprint(config.Blueprint(pos[0]))
	if catalog == "" {
		spec, ok := blueprints.BuiltinSpec(name)
		if !ok {
			return &ui.Problem{Hint: name + " is not a built in blueprint"}
		}
		_, err := stdout.Write(spec)
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	cat, ok := cfg.Catalogs[catalog]
	if !ok {
		return &ui.Problem{Hint: "catalogs." + catalog + " is not declared"}
	}
	dir, err := blueprints.Fetch(cat)
	if err != nil {
		return &ui.Problem{Hint: "catalogs." + catalog + " cannot be read", Explain: err.Error() + "."}
	}
	if _, err := blueprints.ReadFrom(dir, name); err != nil {
		return &ui.Problem{Hint: pos[0] + " cannot be read", Explain: err.Error() + "."}
	}
	spec, err := readFile(dir, "blueprints", name, blueprints.File)
	if err != nil {
		return err
	}
	_, err = stdout.Write(spec)
	return err
}

func runBlueprintLint(args []string) error {
	fs := newFlagSet("blueprint lint", flag.ContinueOnError)
	reporter := commonFlags(fs)
	pos, err := parseAll(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return &ui.Problem{Hint: "blueprint lint takes one or more blueprint directories"}
	}
	r := reporter()
	failed := 0
	for _, dir := range pos {
		s := r.Step("lint " + dir)
		_, problems, err := blueprints.Lint(dir)
		switch {
		case err != nil:
			s.Fail(err)
			r.Explain(err.Error())
			failed++
		case len(problems) > 0:
			s.Fail(fmt.Errorf("%d problems", len(problems)))
			for _, p := range problems {
				r.Explain(p)
			}
			failed++
		default:
			s.Done("")
		}
	}
	if failed > 0 {
		return &ui.Problem{Hint: fmt.Sprintf("%d of %d blueprints did not lint", failed, len(pos))}
	}
	return nil
}

func readFile(parts ...string) ([]byte, error) { return os.ReadFile(filepath.Join(parts...)) }

// parseAll parses fs over args with positional arguments anywhere among the
// flags, as host allow does, and returns the positional ones in order.
func parseAll(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
