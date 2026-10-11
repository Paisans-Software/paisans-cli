package apply

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/render"
)

// The sweep is the last step of a whole apply. Pruning after each healthy
// stack removes only what that stack's action superseded, so it never
// reaches a stack this apply left alone (an image kept by an earlier
// --keep-images, or one whose removal failed), nor an apply that moved
// nothing, nor the repository of an app taken out of the configuration. The
// sweep covers all three: it removes every image of a repository this site
// renders or has rendered, as the manifest records it, that nothing the
// deployment renders names and no container uses.
//
// Only a whole apply sweeps. A partial one (--only, a pass holding apps back
// for their sign-in clients, a scoped write) is a step of something larger,
// and the whole apply that finishes it sweeps; a step that swept could remove
// what a later step of the same change is about to start from.

// sweepEnd is the sweep read's last line, so an answer cut short is told from
// a host that has nothing.
const sweepEnd = "paisans-sweep-end"

// sweepRead reads, in one round trip, everything the sweep decides from:
// every other deployment's root on the host, every image, the IDs of the
// images keep names, and what every container, running or stopped, was
// created from, by name and by ID (see containerImages).
func sweepRead(d deployment.Deployment, keep []string) string {
	probe := ""
	if len(keep) > 0 {
		probe = imageProbe(keep) + "; "
	}
	return fmt.Sprintf(`set -e; for d in %[1]s/*; do if [ -d "$d" ] && [ "$d" != %[2]s ]; then echo "deployment ${d##*/}"; fi; done; %[3]s; %[4]sdocker ps -a --format 'container {{.Image}}'; ids=$(docker ps -aq --no-trunc); if [ -n "$ids" ]; then docker container inspect --format 'container {{.Image}}' $ids; fi; echo %[5]s`,
		deployment.Base, shellQuote(d.Root()), imageList, probe, sweepEnd)
}

// sweepView is what the sweep read found.
type sweepView struct {
	// others are the other deployments' roots on the host, by name.
	others []string
	listed []listedImage
	ids    map[string]string
	inUse  []string
}

func parseSweepRead(out string, keep []string) (sweepView, error) {
	v := sweepView{ids: map[string]string{}}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != sweepEnd {
		return v, fmt.Errorf("the answer was cut short: %s", firstLine(strings.TrimSpace(out)))
	}
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "{"):
			var image listedImage
			if err := json.Unmarshal([]byte(line), &image); err != nil {
				return v, fmt.Errorf("unreadable `docker image ls` line %q: %w", line, err)
			}
			v.listed = append(v.listed, image)
		case len(fields) == 2 && fields[0] == "deployment":
			v.others = append(v.others, fields[1])
		case len(fields) == 2 && fields[0] == "container":
			v.inUse = append(v.inUse, fields[1])
		case len(fields) == 3 && fields[0] == "present":
			v.ids[fields[2]] = fields[1]
		case len(fields) == 2 && fields[0] == "absent":
			v.ids[fields[1]] = ""
		}
	}
	// An image keep names that went unanswered might be one the sweep is
	// about to take for unrendered, so the read is refused instead.
	for _, ref := range keep {
		if _, ok := v.ids[ref]; !ok {
			return v, fmt.Errorf("no answer for %s", ref)
		}
	}
	return v, nil
}

// sweeps reports whether this plan's apply ends with the sweep.
func (p *Plan) sweeps() bool { return p.Whole() && !p.KeepImages }

// sweepRepositories is every repository the sweep may remove images of: the
// manifest's record of what this site rendered before, and what it renders
// now.
func (p *Plan) sweepRepositories() map[string]bool {
	repos := map[string]bool{}
	for _, image := range p.imageHistory {
		repos[repository(image.Repository)] = true
	}
	for _, ref := range allRefs(p.images) {
		repos[repository(ref)] = true
	}
	return repos
}

// swept is what the sweep removes, given what the host has: nothing at all
// when another deployment has a root on the host, since what that one renders
// is in its own files, which this apply does not read.
func (p *Plan) swept(v sweepView) []Prune {
	if len(v.others) > 0 {
		return nil
	}
	return superseded([]pruneScope{{repos: p.sweepRepositories()}}, p.keepRefs(), v.ids, v.listed, v.inUse)
}

// othersNote is why a host with another deployment's root on it is not swept.
func (p *Plan) othersNote(others []string) string {
	var roots []string
	for _, name := range others {
		roots = append(roots, deployment.Base+"/"+name)
	}
	return fmt.Sprintf("%s holds another deployment (%s), whose images this apply does not know, so it removes no image beyond what each stack it moves superseded", p.Site, strings.Join(roots, ", "))
}

// probeSweep lists, for the plan, what the sweep at the end of the apply
// would remove from the host as it is now. An image a stack about to be
// recreated still runs is not in it: it is that stack's prune, listed under
// the stack. A failed read is a note and not a refusal, as a failed sweep is
// a warning: it costs disk, not service.
func (p *Plan) probeSweep(t Transport) {
	done := p.step("find old images")
	keep := p.keepRefs()
	out, err := t.Run(sweepRead(p.Deployment, keep))
	var v sweepView
	if err == nil {
		v, err = parseSweepRead(out, keep)
	}
	if err != nil {
		p.Notes = append(p.Notes, Note{Hint: p.Site + ": old images not listed", Text: fmt.Sprintf("reading the host's images for the sweep at the end of the apply: %v", err)})
		done.Done("not read")
		return
	}
	if len(v.others) > 0 {
		p.Notes = append(p.Notes, Note{Text: p.othersNote(v.others)})
		done.Done("another deployment here")
		return
	}
	pruned := map[string]bool{}
	for _, prune := range p.Prunes {
		pruned[prune.ID] = true
	}
	for _, prune := range p.swept(v) {
		if !pruned[prune.ID] {
			p.Sweep = append(p.Sweep, prune)
		}
	}
	done.Done(imageCount(len(p.Sweep)) + " to sweep")
}

// sweepImages is the whole apply's last step, once every stack is healthy and
// the manifest is written: it reads the host again, since each stack's action
// moved containers off old images, and removes what swept picks. Any failure
// is a warning, as pruneStack's is.
func sweepImages(plan *Plan, t Transport) {
	done := plan.step("sweep old images")
	done.Detail("images of the repositories %s's stacks use or used that nothing the deployment renders names and no container uses", plan.Site)
	warn := func(format string, args ...any) {
		plan.warn(plan.Site+": old images left in place", fmt.Sprintf(format, args...))
	}
	keep := plan.keepRefs()
	out, err := t.Run(sweepRead(plan.Deployment, keep))
	var v sweepView
	if err == nil {
		v, err = parseSweepRead(out, keep)
	}
	if err != nil {
		warn("reading the host's images: %v", err)
		done.Done("")
		return
	}
	if len(v.others) > 0 {
		plan.say("%s", plan.othersNote(v.others))
		done.Done("another deployment here")
		return
	}
	removed := 0
	for _, p := range plan.swept(v) {
		if out, err := t.Run("docker image rm " + shellQuote(p.ID)); err != nil {
			warn("could not remove %s (%s): %s", p.Ref, shortID(p.ID), firstLine(strings.TrimSpace(out)))
			continue
		}
		removed++
		plan.say("swept %s", p.Ref)
	}
	done.Done(imageCount(removed) + " removed")
}

// imageCount counts images for a step's summary (Eg: 1 image, 3 images),
// nothing for none.
func imageCount(n int) string {
	switch n {
	case 0:
		return "nothing"
	case 1:
		return "1 image"
	}
	return fmt.Sprintf("%d images", n)
}

// imageRecord is the manifest's record of the images this site has rendered:
// what Build read, with every reference this plan renders added.
func imageRecord(plan *Plan) []render.ManifestImage {
	refs := map[string]map[string]bool{}
	add := func(repo, ref string) {
		if refs[repo] == nil {
			refs[repo] = map[string]bool{}
		}
		if ref != "" {
			refs[repo][ref] = true
		}
	}
	for _, image := range plan.imageHistory {
		add(image.Repository, "")
		for _, ref := range image.References {
			add(image.Repository, ref)
		}
	}
	for _, ref := range allRefs(plan.images) {
		add(repository(ref), ref)
	}
	out := make([]render.ManifestImage, 0, len(refs))
	for repo, list := range refs {
		out = append(out, render.ManifestImage{Repository: repo, References: sortedKeys(list)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repository < out[j].Repository })
	return out
}

// otherSitesImages is every image the other sites of the deployment render,
// read from their compose files in the render. The Caddy reference there is
// the configuration's tag: only a site's own apply pins it to a build.
func otherSitesImages(site string, plan *render.Plan) []string {
	seen := map[string]bool{}
	for _, file := range plan.Files {
		name, rel, ok := strings.Cut(file.Path, "/")
		if !ok || name == site {
			continue
		}
		if _, _, rest, ok := deployment.SplitRel(rel); !ok || rest != "compose.yaml" {
			continue
		}
		images, err := composeImages(file.Content)
		if err != nil {
			continue
		}
		for _, ref := range images {
			seen[ref] = true
		}
	}
	return sortedKeys(seen)
}
