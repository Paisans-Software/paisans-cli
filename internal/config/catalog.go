package config

import (
	"fmt"
	"regexp"
	"strings"
)

// CoreCatalog is the name the core catalog is declared under, to point it at
// a mirror or pin it to another commit. It is always there, declared or not.
const CoreCatalog = "core"

// Catalog is one entry of `catalogs`: a git repository of blueprints and the
// release it is pinned to (docs/specs/2026-10-12-blueprints-and-
// catalogs.md, "Catalogs").
type Catalog struct {
	// URL is anything `git clone` accepts. The toolkit fetches with the git
	// binary, so a private catalog works with the operator's own git
	// credentials.
	URL string `yaml:"url"`
	// Tag is the release the pin follows, what `catalog update` moves and an
	// operator reads. Commit is what renders: a tag that now names another
	// commit is refused.
	Tag    string `yaml:"tag"`
	Commit string `yaml:"commit"`
}

// ResolveBlueprints, when set, resolves every app's blueprint once the
// configuration is structurally sound: it fetches and reads the catalogs the
// apps name, and returns a problem for each app whose blueprint it cannot
// find. internal/kinds sets it, because only it can read a blueprint; this
// package checks only that a qualified name's catalog is declared.
var ResolveBlueprints func(*Config) []string

var (
	// catalogName is a catalog's name: one word, or <owner>/<name> as
	// `catalog add` names a catalog by default.
	catalogName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}(/[a-z0-9][a-z0-9-]{0,62})?$`)
	blueprintName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	fullCommit    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// CatalogRepoPrefix starts the name of every catalog's repository, as
// homebrew- starts every tap's: paisans-blueprints, paisans-media. It is what
// lets a catalog's name leave it out (example/blueprints for
// example/paisans-blueprints) and still say, read on its own, where it comes
// from.
const CatalogRepoPrefix = "paisans-"

// ValidCatalogName reports whether name is one a catalog may be declared
// under.
func ValidCatalogName(name string) bool { return catalogName.MatchString(name) }

// CatalogRepo is the owner and repository a catalog URL names: the last two
// parts of its path, with any .git dropped. It reads an https or ssh URL, a
// scp-like git@host:owner/repo, and a local path alike. ok is false for a URL
// with fewer than two parts.
func CatalogRepo(url string) (owner, repo string, ok bool) {
	u := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(url), "/"), ".git")
	parts := strings.FieldsFunc(u, func(r rune) bool { return r == '/' || r == ':' })
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

// DefaultCatalogName is the name `catalog add` gives a catalog at url when
// none is given: <owner>/<repository without its paisans- prefix>, lowercased.
// ok is false for a URL whose repository is not named with the prefix, or
// whose name would not be a catalog name.
func DefaultCatalogName(url string) (string, bool) {
	owner, repo, ok := CatalogRepo(url)
	if !ok {
		return "", false
	}
	rest, ok := strings.CutPrefix(strings.ToLower(repo), CatalogRepoPrefix)
	name := strings.ToLower(owner) + "/" + rest
	return name, ok && rest != "" && catalogName.MatchString(name)
}

// BlueprintName reports whether name can be a blueprint's, unqualified.
func BlueprintName(name string) bool { return blueprintName.MatchString(name) }

// SplitBlueprint is a blueprint name's catalog and its name in that catalog,
// split at the last slash: `example/blueprints/journal` is
// ("example/blueprints", "journal"), `notes/journal` is ("notes", "journal"),
// and an unqualified `mbin` is ("", "mbin"), which always means the built in
// blueprints.
func SplitBlueprint(kind Kind) (catalog, name string) {
	s := string(kind)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// catalogProblems checks `catalogs` and every app's blueprint name against it.
func (c *Config) catalogProblems() []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	for _, name := range sortedKeys(c.Catalogs) {
		cat := c.Catalogs[name]
		key := "catalogs." + name
		if !catalogName.MatchString(name) {
			add("%s: %q is not a catalog name: lowercase letters, digits and hyphens, starting with a letter or digit, or two such words as <owner>/<name>.", key, name)
			continue
		}
		if strings.TrimSpace(cat.URL) == "" && name != CoreCatalog {
			add("%s.url: required, the git repository the catalog is fetched from.", key)
		}
		if _, repo, ok := CatalogRepo(cat.URL); name != CoreCatalog && cat.URL != "" && (!ok || !strings.HasPrefix(strings.ToLower(repo), CatalogRepoPrefix)) {
			add("%s.url: %q is not a catalog's repository. A catalog's repository is named %s<name>, as in https://github.com/example/%sblueprints.", key, cat.URL, CatalogRepoPrefix, CatalogRepoPrefix)
		}
		if cat.Commit != "" && !fullCommit.MatchString(cat.Commit) {
			add("%s.commit: %q is not a full 40 character lowercase commit id. A pin names exactly what renders; `paisans catalog add` writes it.", key, cat.Commit)
		}
		if cat.Commit == "" && name != CoreCatalog {
			add("%s.commit: required, the commit the catalog is pinned to. `paisans catalog add` writes it.", key)
		}
	}
	for _, name := range c.AppNames() {
		kind := c.Apps[name].Kind
		if kind == "" {
			continue
		}
		catalog, bp := SplitBlueprint(kind)
		key := "apps." + name + ".blueprint"
		switch {
		case strings.Count(string(kind), "/") > 2 || !blueprintName.MatchString(bp):
			add("%s: %q is not a blueprint name. Write a built in one (mbin) or one from a declared catalog (example/blueprints/journal).", key, kind)
		case catalog == "":
			// A reserved or core blueprint. Which the core catalog holds is
			// known only once its pin is read, so ResolveBlueprints says
			// whether this one is among them.
		case catalog == CoreCatalog:
			add("%s: %q names the core catalog, whose blueprints are written unqualified: %s.", key, kind, bp)
		default:
			if _, ok := c.Catalogs[catalog]; !ok {
				how := "paisans catalog add " + catalog + " <url>"
				if strings.Contains(catalog, "/") {
					how = "paisans catalog add " + catalog + "`, which reads https://github.com/" + strings.Replace(catalog, "/", "/"+CatalogRepoPrefix, 1) + ", or `paisans catalog add " + catalog + " <url>"
				}
				add("%s: %q names the catalog %q, which catalogs does not declare. Add it with `%s`.", key, kind, catalog, how)
			}
		}
	}
	return out
}
