// Package secretsrotate is the engine of `paisans secrets rotate`: one secret
// at a time replaced where it is checked and where it is used, in staged runs
// that resume where they stopped.
//
// A generated secret is held in two places, the side that checks it and the
// stacks that present it, and replacing it in the secrets file alone breaks
// every service still on the old value. So each rotation goes through four
// stages, each ending at a gate. Stage 1 writes a next value to the secrets
// file under rotations, before any host is handed it. Stage 2 makes the
// checking side accept it, and the gate is that it does. Stage 3 hands it to
// each user in turn, by a scoped apply or the use's own step, each healthy
// before the next. Stage 4 promotes it: the next value becomes the secret's
// own, and the entry under rotations is removed.
//
// The next value lives in the file from stage 1, so a run that stops after
// Postgres holds the new password and before the apps do finishes with that
// value rather than generating another. A rotation is pending while its path
// is under rotations; a resumed run does the pending ones first, asks the
// checking side whether it already accepts the value, and hands the users the
// same value again, which changes nothing on a user that already presents it.
// docs/specs/2026-10-12-secrets-rotate.md is the approved design.
package secretsrotate

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// Store is the secrets file: read decrypted and written encrypted to its
// recipients, on the workstation. Every write is read back and compared, so
// a value no host has yet is never handed out before the file holds it.
type Store interface {
	Load() (*config.Secrets, error)
	Save(*config.Secrets) error
}

// Env is everything a rotation reads and the ways it changes the deployment.
type Env struct {
	Cfg   *config.Config
	Store Store
	// Sites reach every site the configuration declares.
	Sites map[string]apply.Transport
	// Apply is a scoped apply of stacks on site, rendered with s.
	Apply func(site string, s *config.Secrets, stacks []string) error
	// Healthy is the gate after each Use: the stacks on site are up and
	// healthy.
	Healthy func(site string, stacks []string) error
	Now     func() time.Time
}

// CannotFinish is what every refusal over a pending rotation that cannot
// finish ends with: the section that says how to end it by hand, and what to
// check on the hosts first.
const CannotFinish = `A rotation that cannot finish, Eg: of a secret whose app was removed, is ended by hand, after a look at which value the hosts accept: README.md, "A rotation that cannot finish".`

// Stage is where one secret's rotation stands.
type Stage int

const (
	// StageNext is a rotation with no next value: the whole of it is ahead.
	StageNext Stage = iota + 1
	// StageChecked is a next value the checking side does not accept yet.
	StageChecked
	// StageUsers is a next value the checking side accepts, which the users
	// are handed.
	StageUsers
	// StagePromote is the next value becoming the secret's own.
	StagePromote
)

// Item is one secret's rotation as planned.
type Item struct {
	Rotator Rotator
	Cost    Cost
	// From is the stage the rotation starts at.
	From Stage
	// Uses are the rotator's users, in order, as the plan shows them. Stage 3
	// reads them again when it starts, since a rotation before it in the run
	// can change them.
	Uses []Use
	// Pending is a rotation an earlier run left with a next value.
	Pending bool
	// Whole is a rotation that keeps its own staging.
	Whole bool
	// Notes are what the plan says about this rotation beyond its cost,
	// shown under it at every verbosity, Eg: that the old client secrets are
	// deleted at Pocket ID once every site runs the new one. The command
	// that plans the run fills them in, since they come from the deployment.
	Notes []string
}

// Run is a whole run: the rotations in order.
type Run struct {
	Items []Item
	// Resumed is an --all run picking up the paths an earlier --all run had
	// left, which the secrets file records. The secrets that run finished are
	// not rotated again.
	Resumed bool
	// Restarted is an --all --restart run: what an earlier --all run left is
	// discarded, and the full list starts again.
	Restarted bool
	// Kept is what the run leaves alone because its precheck found it kept,
	// Eg: an admin role that cannot log in, each with why.
	Kept []KeptError
	// Report receives each rotation as a section and each stage as a step.
	// Nil discards it.
	Report ui.Reporter

	all              bool
	signOutConfirmed bool
}

func (r *Run) reporter() ui.Reporter {
	if r.Report == nil {
		return ui.Discard
	}
	return r.Report
}

// SignsOut reports whether any rotation in the run signs members out.
func (r *Run) SignsOut() bool {
	for _, it := range r.Items {
		if it.Cost.SignsOut {
			return true
		}
	}
	return false
}

// Plan plans the rotation of rs, in the order given, after any rotation an
// earlier run left pending. rs must hold a rotator for every pending path.
// It reads the file and the checking sides, and changes nothing.
func Plan(e Env, rs []Rotator) (*Run, error) { return plan(e, rs, false, false) }

// PlanAll plans `--all`: rs is every secret in scope, in the order --all
// rotates them. A run that an earlier --all left part way does only the
// paths it recorded as left, and rs must hold a rotator for each of them.
func PlanAll(e Env, rs []Rotator) (*Run, error) { return plan(e, rs, true, false) }

// PlanAllRestart plans `--all --restart`: the full list of rs, whatever an
// earlier --all run left, which executing it replaces. It is refused while
// any rotation is pending, since that one has to finish first.
func PlanAllRestart(e Env, rs []Rotator) (*Run, error) { return plan(e, rs, true, true) }

func plan(e Env, rs []Rotator, all, restart bool) (*Run, error) {
	if e.Store == nil {
		return nil, fmt.Errorf("secrets rotate: no secrets file to write the next values to")
	}
	if e.Healthy == nil {
		return nil, fmt.Errorf("secrets rotate: no health gate for stage 3")
	}
	byPath := map[string]Rotator{}
	for _, r := range rs {
		if _, dup := byPath[r.Path()]; dup {
			return nil, fmt.Errorf("secrets rotate: %s is named twice", r.Path())
		}
		byPath[r.Path()] = r
	}
	s, err := e.Store.Load()
	if err != nil {
		return nil, err
	}
	// A pending rotation goes first, named or not: until it is promoted the
	// hosts may disagree about its value, and no other rotation starts on a
	// deployment in that state.
	pending := s.RotationPending()
	for _, path := range pending {
		if _, ok := byPath[path]; !ok {
			return nil, fmt.Errorf("secrets rotate: %s has a rotation under way, and this run cannot finish it. It finishes first, before anything else changes. %s Nothing was changed", path, CannotFinish)
		}
	}
	if restart && len(pending) > 0 {
		return nil, fmt.Errorf("secrets rotate --all --restart: %s has a rotation under way, and starting the list again would leave it unfinished. Finish it first with paisans secrets rotate %s --execute. Nothing was changed", strings.Join(pending, ", "), strings.Join(pending, " "))
	}
	resume := all && !restart && len(s.RotateRemaining) > 0
	if resume {
		// A recorded path this run has no rotation of would never be taken
		// off the list, and every later --all would stop short of it.
		for _, path := range s.RotateRemaining {
			if _, ok := byPath[path]; !ok {
				return nil, fmt.Errorf("secrets rotate --all: the earlier --all run left %s to rotate, and this deployment has no rotation of it any more, Eg: the app that held it is gone. Run paisans secrets rotate --all --restart to discard what that run left and start the full list again. Nothing was changed", path)
			}
		}
	}
	run := &Run{all: all, Resumed: resume, Restarted: restart && len(s.RotateRemaining) > 0}
	var order []Rotator
	for _, r := range rs {
		if slices.Contains(pending, r.Path()) {
			order = append(order, r)
		}
	}
	for _, r := range rs {
		if slices.Contains(pending, r.Path()) {
			continue
		}
		// An --all run that stopped resumes with the paths it had left, so a
		// secret it already promoted is not rotated a second time.
		if resume && !slices.Contains(s.RotateRemaining, r.Path()) {
			continue
		}
		order = append(order, r)
	}
	for _, r := range order {
		it := Item{Rotator: r, Cost: r.Cost(), From: StageNext}
		path := r.Path()
		if _, ok := r.(Whole); ok {
			it.Whole = true
		}
		next, isPending := s.Rotations[path]
		if !isPending {
			// A pending rotation passed its precheck when it started, and its
			// hosts may already hold the next value the precheck compares.
			if p, ok := r.(Prechecker); ok {
				if err := p.Precheck(e); err != nil {
					// A secret nothing checks on this deployment is listed
					// as kept, with why, rather than refusing the run.
					var k *KeptError
					if errors.As(err, &k) {
						run.Kept = append(run.Kept, *k)
						continue
					}
					return nil, fmt.Errorf("secrets rotate %s: %w. Nothing was changed", path, err)
				}
			}
		} else if !it.Whole {
			ui.Secret(next)
			it.Pending = true
			// A pending rotation's own refusals still say whether it can
			// finish, Eg: its app taken out of the configuration, or an admin
			// role found NOLOGIN after it began. A pasted secret's compares
			// the hosts with the file, which a rotation under way changes,
			// so it is not asked.
			if p, ok := r.(Prechecker); ok && !Pasted(r) {
				if err := p.Precheck(e); err != nil {
					return nil, fmt.Errorf("secrets rotate %s: a rotation is under way and cannot finish: %w. %s Nothing was changed", path, err, CannotFinish)
				}
			}
			ok, err := r.Accepts(e, next)
			if err != nil {
				return nil, fmt.Errorf("secrets rotate %s: a rotation is under way and its stage cannot be read from the hosts: %w. A person has to look before anything else changes. %s Nothing was changed", path, err, CannotFinish)
			}
			it.From = StageChecked
			if ok {
				it.From = StageUsers
			}
		}
		if !it.Whole {
			it.Uses = r.Users(e)
			for _, u := range it.Uses {
				if u.Do == nil && e.Apply == nil {
					return nil, fmt.Errorf("secrets rotate %s: no apply to hand %s the next value with", path, u.Site)
				}
			}
		}
		run.Items = append(run.Items, it)
	}
	return run, nil
}

// costText is a rotation's cost as the plan says it.
func costText(c Cost) string {
	var parts []string
	if c.SignsOut {
		parts = append(parts, "signs members out")
	}
	if c.Downtime != "" {
		parts = append(parts, "briefly unavailable: "+c.Downtime)
	}
	if c.Reconnects {
		parts = append(parts, "users reconnect")
	}
	if len(parts) == 0 {
		return "none to members"
	}
	return strings.Join(parts, "; ")
}

func useTitle(u Use) string {
	return fmt.Sprintf("hand %s the next value on %s", strings.Join(u.Stacks, ", "), u.Site)
}

// Show prints the run as planned: each secret's path, what it costs, and the
// stages ahead of it with the users of stage 3 in order.
func (r *Run) Show(w ui.Reporter) {
	r.ShowResume(w)
	if r.SignsOut() {
		w.Note("this run signs members out", "--execute asks for `sign out` to be typed at a terminal before anything changes, and without a terminal it is refused")
	}
	for _, it := range r.Items {
		path := it.Rotator.Path()
		w.Section("rotate " + path)
		w.Step("cost: "+costText(it.Cost)).End(ui.Pending, "")
		for _, note := range it.Notes {
			w.Explain(note)
		}
		if it.Whole {
			w.Step("rotated by its own staged command").End(ui.Pending, "")
			continue
		}
		if it.Pending {
			w.Step("stage 1, next value").End(ui.OK, "already in the secrets file")
		} else {
			w.Step("stage 1, next value").End(ui.Pending, "")
			w.Detail("generated on the workstation and written to the secrets file under rotations, before any host is handed it")
		}
		if it.From >= StageUsers {
			w.Step("stage 2, where it is checked").End(ui.OK, "accepts the next value")
		} else {
			w.Step("stage 2, where it is checked").End(ui.Pending, "")
		}
		w.Step("stage 3, where it is used").End(ui.Pending, "")
		if len(it.Uses) == 0 {
			w.Detail("no stack presents it")
		}
		for _, u := range it.Uses {
			w.Step(useTitle(u)).End(ui.Pending, "")
			w.Detail("then the health gate on %s before anything after it", u.Site)
		}
		if _, ok := it.Rotator.(Retirer); ok {
			w.Step("retire the old value at its issuer").End(ui.Pending, "")
			w.Detail("only once every user above presents the next value")
		}
		w.Step("stage 4, promote").End(ui.Pending, "")
		w.Detail("the next value becomes %s, and its entry under rotations is removed", path)
	}
}

// ShowResume says, at every verbosity, that the run picks up an earlier
// --all run or discards it: an operator who ran --all expects every secret,
// and one resumed does only what was left. The command calls it on --execute,
// where the plan itself is shown only when verbose.
func (r *Run) ShowResume(w ui.Reporter) {
	switch {
	case r.Resumed:
		w.Note(fmt.Sprintf("resuming the --all run the secrets file records as left: %d rotations", len(r.Items)),
			"the secrets that run finished are not rotated again. To rotate every secret once more, run paisans secrets rotate --all --restart")
	case r.Restarted:
		w.Note("starting --all again", "what an earlier --all run left is discarded, and every secret in scope is rotated, including those it finished")
	}
}

// Execute runs the plan. A run that holds a rotation that signs members out
// asks confirm to have "sign out" typed first, and changes nothing when it
// fails. A rotation that stops leaves its next value in the file, and the
// next run resumes it.
func (r *Run) Execute(e Env, confirm func(word, what string) error) error {
	if err := r.ConfirmSignOut(confirm); err != nil {
		return err
	}
	if r.all {
		var paths []string
		for _, it := range r.Items {
			paths = append(paths, it.Rotator.Path())
		}
		if err := r.record(e, paths); err != nil {
			return fmt.Errorf("secrets rotate: recording the secrets --all rotates: %w. Nothing was changed", err)
		}
	}
	for _, it := range r.Items {
		if err := r.rotate(e, it); err != nil {
			return fmt.Errorf("secrets rotate %s stopped: %w. Nothing after it ran. Fix the cause and run secrets rotate again: it resumes with the next value the secrets file holds", it.Rotator.Path(), err)
		}
	}
	return nil
}

// ConfirmSignOut asks confirm to have "sign out" typed when the run holds a
// rotation that signs members out, and nothing otherwise. A caller that has
// more to do before Execute that a refusal should spare, Eg: claiming the
// hosts, asks it first; once answered, Execute does not ask again.
func (r *Run) ConfirmSignOut(confirm func(word, what string) error) error {
	if !r.SignsOut() || r.signOutConfirmed {
		return nil
	}
	var paths []string
	for _, it := range r.Items {
		if it.Cost.SignsOut {
			paths = append(paths, it.Rotator.Path())
		}
	}
	what := fmt.Sprintf("rotating %s signs members out of the sessions it signs", strings.Join(paths, ", "))
	if confirm == nil {
		return fmt.Errorf("secrets rotate: %s, and there is no way to ask. Nothing was changed", what)
	}
	if err := confirm("sign out", what); err != nil {
		// confirmWord says so already; another confirm may not.
		if strings.HasSuffix(err.Error(), "Nothing was changed") {
			return fmt.Errorf("secrets rotate: %w", err)
		}
		return fmt.Errorf("secrets rotate: %w. Nothing was changed", err)
	}
	r.signOutConfirmed = true
	return nil
}

// record writes the paths an --all run has left to the secrets file, and
// reads it back.
func (r *Run) record(e Env, paths []string) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	if slices.Equal(s.RotateRemaining, paths) {
		return nil
	}
	s.RotateRemaining = paths
	if err := e.Store.Save(s); err != nil {
		return err
	}
	if s, err = e.Store.Load(); err != nil || !slices.Equal(s.RotateRemaining, paths) {
		return errors.Join(err, fmt.Errorf("the secrets file does not hold the paths it was written with"))
	}
	return nil
}

func (r *Run) rotate(e Env, it Item) error {
	w := r.reporter()
	rot, path := it.Rotator, it.Rotator.Path()
	w.Section("rotate " + path)

	if it.Whole {
		step := w.Step("rotate " + path + " by its own stages")
		if err := rot.(Whole).RunWhole(e); err != nil {
			step.Fail(err)
			return err
		}
		if err := r.finish(e, path, nil); err != nil {
			step.Fail(err)
			return err
		}
		step.Done("rotated")
		return nil
	}

	// Stage 1: the next value, in the file before any host has it. One a
	// stopped run left is kept: a host may already hold it.
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	next, pending := s.Rotations[path]
	if !pending {
		step := w.Step("stage 1, next value")
		if next, err = rot.Next(e, s); err != nil {
			step.Fail(err)
			return err
		}
		if next == "" {
			err = fmt.Errorf("the next value is empty")
			step.Fail(err)
			return err
		}
		ui.Secret(next)
		if s.Rotations == nil {
			s.Rotations = map[string]string{}
		}
		s.Rotations[path] = next
		if err := e.Store.Save(s); err != nil {
			step.Fail(err)
			return fmt.Errorf("writing the next value: %w. No host was touched", err)
		}
		if s, err = e.Store.Load(); err != nil || s.Rotations[path] != next {
			err = errors.Join(err, fmt.Errorf("the secrets file does not hold the next value it was written with. No host was touched"))
			step.Fail(err)
			return err
		}
		step.Done("written")
	}
	ui.Secret(next)

	// Stage 2: the side that checks the secret accepts the next value.
	step := w.Step("stage 2, where it is checked")
	ok, err := rot.Accepts(e, next)
	if err != nil {
		step.Fail(err)
		return err
	}
	if !ok {
		if err := rot.Checked(e, next); err != nil {
			step.Fail(err)
			return err
		}
		if ok, err = rot.Accepts(e, next); err != nil || !ok {
			err = errors.Join(err, fmt.Errorf("gate: the checking side does not accept the next value"))
			step.Fail(err)
			return err
		}
	}
	step.Done("accepts the next value")

	// Stage 3: each user in turn, rendered with the next value while the
	// file still names the old one, each healthy before the next. The users
	// are read again here rather than taken from the plan: a rotation earlier
	// in the run can change their order, Eg: a cluster role's switchover moves
	// the leader, which the next cluster role must restart last.
	shown := cloneSecrets(s)
	if err := rot.Promote(shown, next); err != nil {
		return err
	}
	uses := rot.Users(e)
	for _, u := range uses {
		if u.Do == nil && e.Apply == nil {
			return fmt.Errorf("no apply to hand %s the next value with", u.Site)
		}
	}
	for _, u := range uses {
		step := w.Step(useTitle(u))
		if u.Do != nil {
			err = u.Do(e, shown)
		} else {
			err = e.Apply(u.Site, shown, u.Stacks)
		}
		if err != nil {
			step.Fail(err)
			return err
		}
		if err := e.Healthy(u.Site, u.Stacks); err != nil {
			step.Fail(err)
			return fmt.Errorf("gate: %s on %s: %w", strings.Join(u.Stacks, ", "), u.Site, err)
		}
		step.Done("healthy")
	}

	// The old value retired at its issuer, once every user presents the next.
	if rr, ok := rot.(Retirer); ok {
		step := w.Step("retire the old value at its issuer")
		if err := rr.Retire(e); err != nil {
			step.Fail(err)
			return err
		}
		step.Done("retired")
	}

	// Stage 4: promote.
	step = w.Step("stage 4, promote")
	if err := r.finish(e, path, func(s *config.Secrets) error { return rot.Promote(s, next) }); err != nil {
		step.Fail(err)
		return err
	}
	step.Done("promoted")
	return nil
}

// finish promotes path with promote, when there is one, removes its entry
// under rotations and its place in rotate_remaining, writes the file, and
// reads it back: it must hold the promoted value and no trace of the
// rotation.
func (r *Run) finish(e Env, path string, promote func(*config.Secrets) error) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	if promote != nil {
		if err := promote(s); err != nil {
			return err
		}
	}
	delete(s.Rotations, path)
	if len(s.Rotations) == 0 {
		s.Rotations = nil
	}
	s.RotateRemaining = slices.DeleteFunc(slices.Clone(s.RotateRemaining), func(p string) bool { return p == path })
	if len(s.RotateRemaining) == 0 {
		s.RotateRemaining = nil
	}
	if err := e.Store.Save(s); err != nil {
		return err
	}
	back, err := e.Store.Load()
	if err != nil {
		return err
	}
	_, still := back.Rotations[path]
	held := !still && !slices.Contains(back.RotateRemaining, path)
	if held && promote != nil {
		// Promotion is safe to run twice, so the file holds the promoted
		// value exactly when promoting it again changes nothing.
		again := cloneSecrets(back)
		if err := promote(again); err != nil {
			return err
		}
		a, errA := yaml.Marshal(back)
		b, errB := yaml.Marshal(again)
		if err := errors.Join(errA, errB); err != nil {
			return err
		}
		held = bytes.Equal(a, b)
	}
	if !held {
		return fmt.Errorf("the secrets file does not hold the promoted value alone")
	}
	return nil
}

// cloneSecrets copies the secrets deep enough that promoting a value in the
// copy leaves the original alone.
func cloneSecrets(s *config.Secrets) *config.Secrets {
	c := *s
	c.Sites = cloneMap(s.Sites)
	c.External = cloneMap(s.External)
	c.OIDCClients = cloneMap(s.OIDCClients)
	c.PocketIDGroups = cloneMap(s.PocketIDGroups)
	c.Rotations = cloneMap(s.Rotations)
	c.MeshRekeyRemaining = slices.Clone(s.MeshRekeyRemaining)
	c.RotateRemaining = slices.Clone(s.RotateRemaining)
	if s.Apps != nil {
		c.Apps = make(map[string]map[string]any, len(s.Apps))
		for name, entries := range s.Apps {
			c.Apps[name] = cloneMap(entries)
		}
	}
	return &c
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
