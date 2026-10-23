package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/localstore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows is the workstation's saved deployments (internal/localstore):
// deployment apply --execute and deployment recover --execute save the
// version they publish or recover, deployment fetch saves what a host
// holds, and deployment list shows them.

// openStore is the saved deployments. Tests point it at a directory of their
// own through localstore.DirVariable.
var openStore = localstore.Open

// saveVersion saves version sha of the deployment whose files are in l, as
// the trust rules there judged it, with what it is and where it came from.
// key is the key line the version was signed with, "" when this workstation
// did not sign it. A failure is a warning: the version is on the sites
// already, and the saved copy is the operator's convenience, not the store.
func saveVersion(r ui.Reporter, l configstore.Local, sha string, revision int, files configstore.Files, key, source string) {
	s := r.Step("save " + sha[:12] + " to this workstation's deployments")
	err := func() error {
		cfg, err := config.LoadBytes(files.Paisans)
		if err != nil {
			return err
		}
		st, err := openStore()
		if err != nil {
			return err
		}
		return st.Save(l, sha, revision, key, localstore.Saved{ID: cfg.ID, Domain: cfg.Community.Domain, Source: source, SavedAt: configNow().UTC()})
	}()
	if err != nil {
		s.End(ui.Warned, "not saved")
		r.Explain(errLine(err) + ". The version is on the sites; deployment fetch saves it later.")
		return
	}
	s.Done("")
}

// runDeploymentList is `paisans deployment list`: the saved deployments as a
// table on stdout, so it can be piped. It reads the saved deployments only.
func runDeploymentList(args []string, stdout io.Writer) error {
	fs := newFlagSet("deployment list", flag.ContinueOnError)
	verboseFlags(fs, &verboseRun)
	if pos, err := parseAll(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return &ui.Problem{Hint: "deployment list takes no argument", Explain: "Got " + strings.Join(pos, " ") + "."}
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	listed, err := st.List()
	if err != nil {
		return err
	}
	if len(listed) == 0 {
		fmt.Fprintf(stdout, "No deployment is saved in %s. deployment apply --execute saves the one it applies, and deployment fetch --ssh <user@host> every one a host holds.\n", st.Base)
		return nil
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDOMAIN\tREVISION\tVERSION\tSTATUS\tSAVED\tSOURCE")
	for _, l := range listed {
		if l.Err != nil {
			fmt.Fprintf(w, "%s\t-\t-\t-\tunreadable: %s\t-\t-\n", l.ID, errLine(l.Err))
			continue
		}
		version := l.SHA
		if len(version) > 12 {
			version = version[:12]
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", l.ID, orDash(l.Domain), l.Revision, orDash(version), l.Status, l.SavedAt.Local().Format("2006-01-02 15:04"), orDash(l.Source))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nIn %s; each directory holds %s, secrets.enc.yaml and .sops.yaml.\n", st.Base+"/deployments", config.FileName)
	return nil
}

// fetchGather gathers deployment id's versions from one host into l, judged
// from l's anchor. Tests replace it.
var fetchGather = func(l configstore.Local, host string, t apply.Transport, id string) (configstore.Gathered, error) {
	a, err := l.Anchor()
	if err != nil {
		return configstore.Gathered{}, err
	}
	return configstore.GatherAnchored(l, map[string]apply.Transport{host: t}, 1, id, a, configstore.GatherOptions{})
}

// runDeploymentFetch is `paisans deployment fetch`: every deployment in the
// registry of the host --ssh names, saved to this workstation. The registry
// and the configuration stores are root's, so the host is reached as its
// owner, through sudo, as site deployments reaches it. Nothing on the host
// changes: no site is asked to sync, which only --execute may ask.
//
// What a host serves is judged as deployment pull judges it, from the anchor
// this workstation saved for the deployment: the key that signed what it
// applied, or the root it pinned. A deployment it holds no anchor for gets
// nothing written as its own files, since a history found only on a host
// could have been written by anyone with root on it; its newest versions are
// set aside in .paisans/incoming/ and listed as unverified.
func runDeploymentFetch(args []string) error {
	fs := newFlagSet("deployment fetch", flag.ContinueOnError)
	reporter := commonFlags(fs)
	sshFlag := fs.String("ssh", "", "the host to fetch from, user@host[:port] or an alias of your ssh config, [user@]alias[:port]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return &ui.Problem{Hint: "deployment fetch takes no argument", Explain: "Got " + strings.Join(fs.Args(), " ") + "."}
	}
	if *sshFlag == "" {
		return &ui.Problem{Hint: "deployment fetch needs --ssh", Explain: "Name the host with --ssh user@host[:port], or an alias of your ssh config, Eg: --ssh admin@203.0.113.9 or --ssh home-a."}
	}
	dest, err := parseDestination(*sshFlag)
	if err != nil {
		return sshFlagProblem(*sshFlag, err)
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	r := reporter()
	t := reachOwner(dest, true)
	r.Section(fmt.Sprintf("deployments on %s", dest))
	s := r.Step("read " + registry.Path)
	reg, err := registry.Read(t)
	// An entry that does not say which build claimed the host still names
	// a deployment whose store can be read.
	var noBuild *registry.NoBuildError
	if errors.As(err, &noBuild) {
		err = nil
	}
	if err != nil {
		s.Fail(err)
		return &ui.Problem{Hint: "the host's registry cannot be read", Explain: errLine(err) + ". Nothing was fetched.", Cause: err}
	}
	s.Done(fmt.Sprintf("%d deployment(s)", len(reg.Deployments)))
	if len(reg.Deployments) == 0 {
		r.Result("%s holds no deployment; nothing was fetched.", dest)
		return nil
	}
	// The source saved with each copy is dest as messages show it: for an
	// alias, both the alias and the address it resolved to, since the alias
	// means something only in this workstation's ssh config and may later
	// name another host.
	source := "fetched from " + dest.String()
	var saved, unverified, failed int
	for _, id := range slices.Sorted(maps.Keys(reg.Deployments)) {
		e := reg.Deployments[id]
		r.Section(fmt.Sprintf("%s (%s)", id, orDash(e.Domain)))
		switch fetchOne(r, st, dest.Arg(), t, id, e.Domain, source) {
		case localstore.Verified:
			saved++
		case localstore.Unverified, localstore.Fork:
			unverified++
		default:
			failed++
		}
	}
	r.Result("%d saved, %d set aside unverified, %d not fetched, in %s. %s deployment list shows them.", saved, unverified, failed, st.Base, progname.Cmd())
	if failed > 0 {
		return &ui.Problem{Hint: fmt.Sprintf("%d deployment(s) were not fetched", failed), Explain: "Each is listed above with why. The others were saved."}
	}
	return nil
}

// fetchOne fetches deployment id into the store and says how it was saved,
// "" when it was not.
func fetchOne(r ui.Reporter, st localstore.Store, host string, t apply.Transport, id, domain, source string) localstore.Status {
	l, err := st.Local(id)
	if err != nil {
		r.Warn("not fetched", errLine(err))
		return ""
	}
	s := r.Step("gather the versions " + host + " holds")
	g, err := fetchGather(l, host, t, id)
	var rb *configstore.RollbackError
	switch {
	case errors.As(err, &rb):
		s.End(ui.Warned, "older than this workstation has seen")
		r.Explain(rb.Error() + ". The saved copy is kept as it was: a host that deleted the newer versions can serve an old one. deployment log shows what each site holds.")
		return ""
	case errors.Is(err, configstore.ErrNoAnchor):
		s.End(ui.Warned, "nothing here to trust it from")
		return setAsideUnverified(r, st, l, g, id, domain, source, host)
	case err == nil && len(g.Reached) > 0 && g.Reached[0].Err != nil:
		err = g.Reached[0].Err
	}
	if err != nil {
		s.Fail(err)
		r.Explain(errLine(err) + ".")
		return ""
	}
	if len(g.Versions) == 0 {
		s.End(ui.Warned, "no version")
		r.Explain(host + " holds no version for this deployment. Nothing was saved.")
		return ""
	}
	if len(g.Verdict.Heads) == 0 {
		s.End(ui.Warned, "no trusted version")
		r.Explain(fmt.Sprintf("%s holds %d version(s) and none descends from what this workstation trusts. Nothing was saved.", host, len(g.Versions)))
		return ""
	}
	s.Done(fmt.Sprintf("%d version(s)", len(g.Versions)))
	if g.Verdict.Fork {
		for _, h := range g.Verdict.Heads {
			if err := l.WriteIncomingFor(h); err != nil {
				r.Warn("not saved", errLine(err))
				return ""
			}
		}
		r.Warn("a fork", fmt.Sprintf("Each head's files are in %s/<sha>/. Merge them in the deployment's own directory and publish with %s deployment publish --merge.", l.Incoming(), progname.Cmd()))
		head := g.Verdict.Heads[0]
		if err := st.Record(localstore.Saved{ID: id, Domain: domain, SHA: head, Revision: g.Versions[head].Revision, Status: localstore.Fork, Source: source, SavedAt: configNow().UTC()}); err != nil {
			r.Warn("not recorded", errLine(err))
		}
		return localstore.Fork
	}
	head := g.Verdict.Heads[0]
	s = r.Step("save " + describe(g, head))
	err = func() error {
		if err := l.Checkout(head); err != nil {
			return err
		}
		if err := l.RecordSeen(g); err != nil {
			return err
		}
		return st.Record(localstore.Saved{ID: id, Domain: domain, SHA: head, Revision: g.Versions[head].Revision, Status: localstore.Verified, Source: source, SavedAt: configNow().UTC()})
	}()
	if err != nil {
		s.Fail(err)
		r.Explain(errLine(err) + ".")
		return ""
	}
	s.Done(l.Dir)
	return localstore.Verified
}

// setAsideUnverified writes the files of the highest-revision versions g
// holds to .paisans/incoming/<sha>/, never as the copy's own, and names the
// recovery that pins one.
func setAsideUnverified(r ui.Reporter, st localstore.Store, l configstore.Local, g configstore.Gathered, id, domain, source, host string) localstore.Status {
	top := 0
	for _, v := range g.Versions {
		if v.TreeOK {
			top = max(top, v.Revision)
		}
	}
	var newest []string
	for sha, v := range g.Versions {
		if v.TreeOK && v.Revision == top {
			newest = append(newest, sha)
		}
	}
	slices.Sort(newest)
	if len(newest) == 0 {
		r.Explain(host + " holds no version for this deployment. Nothing was saved.")
		return ""
	}
	for _, sha := range newest {
		if err := l.WriteIncomingFor(sha); err != nil {
			r.Warn("not saved", errLine(err))
			return ""
		}
	}
	r.Warn(fmt.Sprintf("unverified: revision %d, %s", top, strings.Join(newest, ", ")),
		fmt.Sprintf("This workstation never applied or recovered this deployment, so it holds no key or root to judge what %s serves, and anyone with root there could have written it. Its files are in %s/<sha>/ only. Check the hash with another operator, then pin it: %s deployment recover --from %s --id %s --root <sha> --config <empty directory>/%s --execute.", host, l.Incoming(), progname.Cmd(), host, id, config.FileName))
	if err := st.Record(localstore.Saved{ID: id, Domain: domain, SHA: newest[0], Revision: top, Status: localstore.Unverified, Source: source, SavedAt: configNow().UTC()}); err != nil {
		r.Warn("not recorded", errLine(err))
	}
	return localstore.Unverified
}
