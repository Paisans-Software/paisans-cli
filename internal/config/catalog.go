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
// signed tag it is pinned to (docs/specs/2026-10-12-blueprints-and-
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
	// Signers are the SSH public keys a tag must be signed by. Unsigned
	// accepts a catalog with no signed tags, which the grant review says
	// every time the pin moves.
	Signers  []string `yaml:"signers"`
	Unsigned bool     `yaml:"unsigned"`
}

// ResolveBlueprints, when set, resolves every app's blueprint once the
// configuration is structurally sound: it fetches and reads the catalogs the
// apps name, and returns a problem for each app whose blueprint it cannot
// find. internal/kinds sets it, because only it can read a blueprint; this
// package checks only that a qualified name's catalog is declared.
var ResolveBlueprints func(*Config) []string

var (
	catalogName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	blueprintName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	fullCommit    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// SplitBlueprint is a blueprint name's catalog and its name in that catalog:
// `example/journal` is ("example", "journal"), and an unqualified `mbin` is
// ("", "mbin"), which always means the built in blueprints.
func SplitBlueprint(kind Kind) (catalog, name string) {
	if c, n, ok := strings.Cut(string(kind), "/"); ok {
		return c, n
	}
	return "", string(kind)
}

// catalogProblems checks `catalogs` and every app's blueprint name against it.
func (c *Config) catalogProblems() []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	for _, name := range sortedKeys(c.Catalogs) {
		cat := c.Catalogs[name]
		key := "catalogs." + name
		if !catalogName.MatchString(name) {
			add("%s: %q is not a catalog name: lowercase letters, digits and hyphens, starting with a letter or digit.", key, name)
			continue
		}
		if strings.TrimSpace(cat.URL) == "" && name != CoreCatalog {
			add("%s.url: required, the git repository the catalog is fetched from.", key)
		}
		if cat.Commit != "" && !fullCommit.MatchString(cat.Commit) {
			add("%s.commit: %q is not a full 40 character lowercase commit id. A pin names exactly what renders; `paisans catalog add` writes it.", key, cat.Commit)
		}
		if cat.Commit == "" && name != CoreCatalog {
			add("%s.commit: required, the commit the catalog is pinned to. `paisans catalog add` writes it.", key)
		}
		if name != CoreCatalog && len(cat.Signers) == 0 && !cat.Unsigned {
			add("%s: names no signers and is not unsigned. A pin trusts the keys its tags are signed by: `paisans catalog add` records the key you accept, or write unsigned: true to check nothing.", key)
		}
		if name != CoreCatalog && len(cat.Signers) > 0 && cat.Unsigned {
			add("%s: names signers and is unsigned; it is one or the other.", key)
		}
		if name != CoreCatalog && len(cat.Signers) > 0 && cat.Tag == "" {
			add("%s.tag: required with signers, since the signature is on the tag.", key)
		}
		for i, s := range cat.Signers {
			if _, _, err := ParseKeyLine(s); err != nil {
				add("%s.signers[%d]: is not an SSH public key: %v.", key, i, err)
			}
		}
		if name == CoreCatalog && len(cat.Signers) > 0 {
			add("%s.signers: the core catalog's keys are built into the toolkit and cannot be replaced; a mirror must carry tags they signed.", key)
		}
		if name == CoreCatalog && cat.Unsigned {
			add("%s.unsigned: the core catalog cannot be unsigned.", key)
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
		case strings.Count(string(kind), "/") > 1 || !blueprintName.MatchString(bp):
			add("%s: %q is not a blueprint name. Write a built in one (mbin) or one from a declared catalog (example/journal).", key, kind)
		case catalog == "":
			if !knownKinds[kind] {
				add("%s: unknown blueprint %q. The ones built into this toolkit are element, mbin, oauth2-proxy, outline, pocket-id, synapse, uptime and writefreely; one from a catalog is written <catalog>/<name>.", key, kind)
			}
		case catalog == CoreCatalog:
			add("%s: %q names the core catalog, whose blueprints are written unqualified: %s.", key, kind, bp)
		default:
			if _, ok := c.Catalogs[catalog]; !ok {
				add("%s: %q names the catalog %q, which catalogs does not declare. Add it with `paisans catalog add %s <url>`.", key, kind, catalog, catalog)
			}
		}
	}
	return out
}
