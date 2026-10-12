package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/hostprep"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// host allow and host disallow change a host's key allowlist: the
// fingerprints host prepare may add to the login user's authorized_keys
// there (internal/hostprep/allow.go). The list is the host owner's, shared
// by every deployment on the host, so these commands claim nothing in the
// registry and touch nothing of a deployment's. They read paisans.yaml only
// to name the site's host, its login user, and an admin's keys.
//
// Writing the list is an owner granting or withdrawing access, so it is
// never scripted: --execute asks for the site's name at a terminal, as
// site remove --delete-data does.

// allowHost is how host allow and host disallow reach a site. Tests replace
// it.
var allowHost = func(site string, declared config.Site, destination string, sudo bool) hostprep.Reader {
	return siteTransport(site, declared, destination, sudo)
}

// allowOptions are the flags host allow and host disallow share, and what
// they were given.
type allowOptions struct {
	config, ssh   string
	execute, sudo bool
	site          string
	names         []string
}

// parseAllow reads `<site> <name>...` and the flags, which may come before,
// between or after them.
func parseAllow(command string, args []string) (ui.Reporter, allowOptions, error) {
	fs := newFlagSet(command, flag.ContinueOnError)
	reporter := commonFlags(fs)
	var o allowOptions
	fs.StringVar(&o.config, "config", config.FileName, "path to the deployment declaration")
	fs.StringVar(&o.ssh, "ssh", "", "ssh destination, used verbatim in place of the site's ssh section")
	fs.BoolVar(&o.execute, "execute", false, "actually write the list; asks for the site's name at a terminal")
	fs.BoolVar(&o.sudo, "sudo", true, "run remote commands through sudo, since /etc/paisans is root's")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, o, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	what := "admin"
	if command == "host disallow" {
		what = "admin|fingerprint"
	}
	if len(positional) < 2 {
		return nil, o, &ui.Problem{Hint: command + " takes a site and at least one " + strings.ReplaceAll(what, "|", " or "), Explain: "Usage: paisans " + command + " <site> <" + what + ">... [--config " + config.FileName + "] [--execute]"}
	}
	o.site, o.names = positional[0], positional[1:]
	return reporter(), o, nil
}

// allowTarget is the site whose list a command changes, loaded and checked.
type allowTarget struct {
	cfg  *config.Config
	user string
	host hostprep.Reader
	path string
}

func loadAllowTarget(r ui.Reporter, o allowOptions) (*allowTarget, error) {
	cfg, err := config.Load(o.config)
	if err != nil {
		return nil, err
	}
	result := validate.Check(cfg)
	reportFindings(r, o.config, result)
	if result.Refused() {
		return nil, refused(o.config, len(result.Refusals()), "")
	}
	declared, ok := cfg.Sites[o.site]
	if !ok {
		return nil, fmt.Errorf("%s declares no site %q. Declared sites are %s", o.config, o.site, strings.Join(cfg.SiteNames(), ", "))
	}
	host := allowHost(o.site, declared, o.ssh, o.sudo)
	describe := o.site
	if d, ok := host.(interface{ Describe() string }); ok {
		describe = d.Describe()
	}
	r.Section(fmt.Sprintf("%s (%s)", o.site, describe))
	return &allowTarget{cfg: cfg, user: declared.SSH.User, host: host, path: hostprep.AllowPath(declared.SSH.User)}, nil
}

// adminKeys is each key of the named admins, as the list would hold it.
func adminKeys(cfg *config.Config, configPath string, names []string) ([]hostprep.AllowLine, error) {
	var out []hostprep.AllowLine
	for _, name := range names {
		admin, ok := cfg.Admins[name]
		if !ok {
			return nil, fmt.Errorf("%s declares no admin %q. Declared admins are %s", configPath, name, strings.Join(adminNames(cfg), ", "))
		}
		for _, line := range admin.KeyLines() {
			k, _, err := config.ParseKeyLine(line)
			if err != nil {
				continue // validate has refused it already
			}
			comment := name
			if k.Comment != "" {
				comment += " " + k.Comment
			}
			out = append(out, hostprep.AllowLine{Fingerprint: k.Fingerprint, Comment: comment})
		}
	}
	return out, nil
}

func adminNames(cfg *config.Config) []string { return slices.Sorted(maps.Keys(cfg.Admins)) }

// showList is the file as it would be, at every verbosity: what the owner
// is about to put on the host. Its comments are the host's text and the
// configuration's, so each line is shown with any control character
// escaped.
func showList(r ui.Reporter, path, content string) {
	r.Step("write "+path).End(ui.Pending, "")
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		r.Explain(ui.Visible(line))
	}
}

// allowLabel is one line of a list as a step names it, with any control
// character in it escaped: a comment is the host's text or an admin's.
func allowLabel(l hostprep.AllowLine) string {
	if l.Comment == "" {
		return ui.Visible(l.Fingerprint)
	}
	return ui.Visible(l.Fingerprint + " (" + l.Comment + ")")
}

// readList reads a list to rewrite it, refusing one with a line it cannot
// read: the rewrite keeps every line it does not change as the owner wrote
// it, and cannot keep or drop a line without knowing what it means.
func readList(t *allowTarget, o allowOptions, content string) (hostprep.AllowFile, error) {
	f, err := hostprep.ParseAllowFile(content)
	if err != nil {
		return f, &ui.Problem{
			Hint:    t.path + " on " + o.site + " has a line this command cannot read",
			Explain: sentence(err.Error()) + " It allows no key as it is. host allow and host disallow keep every line they do not change, so they change nothing until it is fixed or deleted by hand, Eg: with sudoedit " + t.path + ". Nothing was changed.",
		}
	}
	return f, nil
}

// writeList asks for the site's name and writes the list, unless it changed
// since it was read.
func writeList(t *allowTarget, o allowOptions, stdin io.Reader, stdout io.Writer, found bool, was, content, what string) error {
	if !stdinIsTerminal(stdin) {
		return noTerminal("Writing a host's key allowlist is its owner granting or withdrawing access, never a script's")
	}
	if err := confirmWord(stdin, stdout, o.site, what); err != nil {
		return err
	}
	out, err := t.host.Run(hostprep.WriteAllowCommand(t.user, found, was, content))
	if err != nil {
		if strings.Contains(out, hostprep.AllowChangedMarker) {
			return &ui.Problem{Hint: t.path + " changed while it was read", Explain: "Someone else wrote it meanwhile. Nothing was changed; run the command again to see the list as it is now."}
		}
		return fmt.Errorf("writing %s: %w: %s", t.path, err, strings.TrimSpace(out))
	}
	return nil
}

// runHostAllow adds admins' keys to a site's host's allowlist.
func runHostAllow(args []string, stdin io.Reader, stdout io.Writer) error {
	r, o, err := parseAllow("host allow", args)
	if err != nil {
		return err
	}
	t, err := loadAllowTarget(r, o)
	if err != nil {
		return err
	}
	keys, err := adminKeys(t.cfg, o.config, o.names)
	if err != nil {
		return err
	}
	type read struct {
		content string
		found   bool
		claimed []hostprep.AllowLine
	}
	got, err := ui.Get(r, "read "+t.path, func() (read, error) {
		content, found, err := t.host.ReadFile(t.path)
		if err != nil || found {
			return read{content, found, nil}, err
		}
		claimed, err := hostprep.ClaimedKeys(t.host, t.user)
		return read{content, found, claimed}, err
	})
	if err != nil {
		return err
	}
	file, err := readList(t, o, got.content)
	if err != nil {
		return err
	}
	lines := file.Lines()
	// A new list starts with every key a deployment already claims, so
	// that making one refuses no access the host already has.
	var seeded []hostprep.AllowLine
	for _, c := range got.claimed {
		if !hostprep.Allows(keys, c.Fingerprint) {
			seeded = append(seeded, c)
		}
	}
	var added []hostprep.AllowLine
	for _, k := range keys {
		s := r.Step("allow " + allowLabel(k))
		if hostprep.Allows(lines, k.Fingerprint) {
			s.Done("already listed")
			continue
		}
		s.End(ui.Pending, "")
		added = append(added, k)
	}
	for _, c := range seeded {
		s := r.Step("keep " + allowLabel(c))
		s.Detail("already authorized for %s by a deployment on this host; listed so that making the list refuses no access the host has", t.user)
		s.End(ui.Pending, "a deployment claims it")
	}
	if len(added) == 0 && got.found {
		r.Result("%s already allows every key of %s. Nothing to do.", t.path, strings.Join(o.names, ", "))
		return nil
	}
	if !got.found {
		r.Explain(fmt.Sprintf("%s has no list yet, so host prepare adds any key now. Once written, it adds only the keys the list holds.", o.site))
	}
	// An existing list keeps every line as its owner wrote it, and gains the
	// new keys at its end.
	content := file.Add(added)
	if !got.found {
		content = hostprep.EncodeAllow(t.user, append(seeded, added...))
	}
	next := hostprep.ParseAllow(content)
	showList(r, t.path, content)
	if !o.execute {
		r.Result("Nothing changed. Check each fingerprint with its admin, then re-run with --execute; it asks for the site's name at a terminal.")
		return nil
	}
	what := fmt.Sprintf("This lets host prepare add the keys above to %s's authorized_keys on %s, whoever runs it. Check each fingerprint with its admin before you proceed.", t.user, o.site)
	if err := writeList(t, o, stdin, stdout, got.found, got.content, content, what); err != nil {
		return err
	}
	r.Result("%s now allows %s.", t.path, plural(len(next), "key"))
	return nil
}

// runHostDisallow takes keys off a site's host's allowlist, by admin or by
// fingerprint. It removes no key from authorized_keys.
func runHostDisallow(args []string, stdin io.Reader, stdout io.Writer) error {
	r, o, err := parseAllow("host disallow", args)
	if err != nil {
		return err
	}
	t, err := loadAllowTarget(r, o)
	if err != nil {
		return err
	}
	var drop []hostprep.AllowLine
	var admins []string
	for _, name := range o.names {
		if strings.HasPrefix(name, "SHA256:") {
			drop = append(drop, hostprep.AllowLine{Fingerprint: name})
			continue
		}
		admins = append(admins, name)
	}
	keys, err := adminKeys(t.cfg, o.config, admins)
	if err != nil {
		return err
	}
	drop = append(drop, keys...)
	type read struct {
		content string
		found   bool
	}
	got, err := ui.Get(r, "read "+t.path, func() (read, error) {
		content, found, err := t.host.ReadFile(t.path)
		return read{content, found}, err
	})
	if err != nil {
		return err
	}
	if !got.found {
		r.Result("%s has no list for %s, so host prepare adds any key and there is nothing to take off. paisans host allow makes one.", o.site, t.user)
		return nil
	}
	file, err := readList(t, o, got.content)
	if err != nil {
		return err
	}
	lines := file.Lines()
	content, removed := file.Remove(drop)
	// A list left with no line at all says what it is, and that it allows
	// nothing, rather than being a bare empty file.
	if strings.TrimSpace(content) == "" {
		content = hostprep.EncodeAllow(t.user, nil)
	}
	kept := hostprep.ParseAllow(content)
	taken := len(removed)
	for _, l := range removed {
		r.Step("disallow "+allowLabel(l)).End(ui.Pending, "")
	}
	for _, d := range drop {
		if !hostprep.Allows(lines, d.Fingerprint) {
			r.Step("disallow " + ui.Visible(d.Fingerprint)).Done("not on the list")
		}
	}
	if taken == 0 {
		r.Result("%s lists none of them. Nothing to do.", t.path)
		return nil
	}
	r.Note("a key already in "+t.user+"'s authorized_keys keeps working", fmt.Sprintf("The list decides what host prepare may add, not who may log in now. To take a key away, remove its admin from sites.%s.ssh.keys and run paisans host prepare --site %s --execute, or delete its line by hand.", o.site, o.site))
	if len(kept) == 0 {
		r.Note("the list will be empty, which allows no key", "host prepare then adds no key on "+o.site+". To allow every key again, delete "+t.path+" by hand.")
	}
	showList(r, t.path, content)
	if !o.execute {
		r.Result("Nothing changed. Re-run with --execute; it asks for the site's name at a terminal.")
		return nil
	}
	what := fmt.Sprintf("This stops host prepare adding the keys above to %s's authorized_keys on %s. It removes none of them from authorized_keys.", t.user, o.site)
	if err := writeList(t, o, stdin, stdout, got.found, got.content, content, what); err != nil {
		return err
	}
	if len(kept) == 0 {
		r.Result("%s now allows no key.", t.path)
		return nil
	}
	r.Result("%s now allows %s.", t.path, plural(len(kept), "key"))
	return nil
}
