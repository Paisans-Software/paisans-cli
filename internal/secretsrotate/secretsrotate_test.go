package secretsrotate_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/secretsrotate"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// world is the fake deployment: the secrets file, the value each checking
// side accepts, the value each site's stacks present, and a log of every
// call the engine made, in order.
type world struct {
	t       *testing.T
	secrets *config.Secrets
	saves   int
	// drop makes the store lose the rotations on save, as a file that did
	// not take the write would.
	drop bool
	// accepted is the value each path's checking side accepts.
	accepted map[string]string
	// presented is the value a site presents for a path, keyed site/path.
	presented map[string]string
	// failApply fails the next apply on that site, once.
	failApply map[string]error
	log       []string
}

func newWorld(t *testing.T, values map[string]string) *world {
	t.Helper()
	w := &world{t: t, secrets: &config.Secrets{}, accepted: map[string]string{}, presented: map[string]string{}, failApply: map[string]error{}}
	for path, v := range values {
		if err := w.secrets.Set(path, v); err != nil {
			t.Fatal(err)
		}
		w.accepted[path] = v
	}
	return w
}

func clone(s *config.Secrets) *config.Secrets {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var out config.Secrets
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return &out
}

func (w *world) Load() (*config.Secrets, error) { return clone(w.secrets), nil }

func (w *world) Save(s *config.Secrets) error {
	w.saves++
	w.secrets = clone(s)
	if w.drop {
		w.secrets.Rotations = nil
	}
	return nil
}

func (w *world) get(path string) string {
	v, _ := w.secrets.Get(path)
	return v
}

// env is an Env whose Apply records which value of each rotated path the
// site would present, and logs a change only when that value differs from
// what the site already presents: an apply with nothing to change is a no-op.
func (w *world) env(paths ...string) secretsrotate.Env {
	return secretsrotate.Env{
		Store: w,
		Apply: func(site string, s *config.Secrets, stacks []string) error {
			if err := w.failApply[site]; err != nil {
				delete(w.failApply, site)
				w.log = append(w.log, "apply-failed "+site)
				return err
			}
			for _, path := range paths {
				v, _ := s.Get(path)
				if w.presented[site+"/"+path] != v {
					w.presented[site+"/"+path] = v
					w.log = append(w.log, fmt.Sprintf("apply %s %s %s=%s", site, strings.Join(stacks, ","), path, v))
				}
			}
			return nil
		},
		Healthy: func(site string, stacks []string) error {
			w.log = append(w.log, "healthy "+site)
			return nil
		},
	}
}

// rot is a fake rotator: a generated secret whose next values are next-1,
// next-2 and so on, checked by a side whose accepted value the world holds.
type rot struct {
	secretsrotate.Generated
	w     *world
	cost  secretsrotate.Cost
	uses  []secretsrotate.Use
	gen   int
	check error
	pre   error
}

func newRot(w *world, path string, sites ...string) *rot {
	r := &rot{Generated: secretsrotate.Generated{SecretPath: path}, w: w}
	for _, s := range sites {
		r.uses = append(r.uses, secretsrotate.Use{Site: s, Stacks: []string{"app"}})
	}
	return r
}

func (r *rot) Cost() secretsrotate.Cost { return r.cost }

func (r *rot) Next(e secretsrotate.Env, s *config.Secrets) (string, error) {
	r.gen++
	r.w.log = append(r.w.log, "next "+r.Path())
	return fmt.Sprintf("%s-next-%d", r.Path(), r.gen), nil
}

func (r *rot) Accepts(e secretsrotate.Env, next string) (bool, error) {
	return r.w.accepted[r.Path()] == next, nil
}

func (r *rot) Checked(e secretsrotate.Env, next string) error {
	if r.check != nil {
		return r.check
	}
	r.w.log = append(r.w.log, "checked "+r.Path()+"="+next)
	r.w.accepted[r.Path()] = next
	return nil
}

func (r *rot) Users(e secretsrotate.Env) []secretsrotate.Use { return r.uses }

// prechecked is a rot that refuses at plan time.
type prechecked struct{ *rot }

func (p prechecked) Precheck(e secretsrotate.Env) error { return p.pre }

// whole is a rotation that keeps its own staging.
type whole struct{ *rot }

func (h whole) RunWhole(e secretsrotate.Env) error {
	h.w.log = append(h.w.log, "whole "+h.Path())
	return nil
}

func noConfirm(word, what string) error { return errors.New("not asked") }

func execute(t *testing.T, e secretsrotate.Env, rs ...secretsrotate.Rotator) error {
	t.Helper()
	run, err := secretsrotate.Plan(e, rs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return run.Execute(e, noConfirm)
}

func wantLog(t *testing.T, w *world, want ...string) {
	t.Helper()
	if strings.Join(w.log, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log:\n%s\nwant:\n%s", strings.Join(w.log, "\n"), strings.Join(want, "\n"))
	}
}

const pw = "cluster.standby_password"

// A full run goes through the four stages in order: the next value, the
// checking side, each user in turn with its health gate, then promotion.
func TestFullRunExecutesTheStagesInOrder(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a", "b")
	if err := execute(t, w.env(pw), r); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w,
		"next "+pw,
		"checked "+pw+"="+pw+"-next-1",
		"apply a app "+pw+"="+pw+"-next-1",
		"healthy a",
		"apply b app "+pw+"="+pw+"-next-1",
		"healthy b",
	)
	if got := w.get(pw); got != pw+"-next-1" {
		t.Fatalf("%s = %q after promotion", pw, got)
	}
	if len(w.secrets.Rotations) != 0 {
		t.Fatalf("rotations left after promotion: %v", w.secrets.Rotations)
	}
}

// Stage 3 is handed the next value while the file still names the old one
// as current: the users present it before promotion.
func TestUsersPresentTheNextValueBeforePromotion(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	e := w.env(pw)
	inner := e.Apply
	e.Apply = func(site string, s *config.Secrets, stacks []string) error {
		if got := w.get(pw); got != "old" {
			t.Fatalf("the file names %q as current during stage 3", got)
		}
		return inner(site, s, stacks)
	}
	if err := execute(t, e, newRot(w, pw, "a")); err != nil {
		t.Fatal(err)
	}
}

// Review Focus 1: a run stopped after stage 2, with the checking side already
// on the new value and the users on the old, resumes at stage 3 with the
// same next value rather than generating another.
func TestStoppedAfterStage2ResumesAtStage3WithTheSameValue(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a")
	w.failApply["a"] = errors.New("ssh dropped")
	err := execute(t, w.env(pw), r)
	if err == nil || !strings.Contains(err.Error(), "ssh dropped") {
		t.Fatalf("err = %v", err)
	}
	if got := w.secrets.Rotations[pw]; got != pw+"-next-1" {
		t.Fatalf("rotations[%s] = %q after the stop", pw, got)
	}
	if w.get(pw) != "old" {
		t.Fatal("promoted after a failed stage 3")
	}

	w.log = nil
	run, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Items[0].From; got != secretsrotate.StageUsers {
		t.Fatalf("resumes from stage %d, want %d", got, secretsrotate.StageUsers)
	}
	if err := run.Execute(w.env(pw), noConfirm); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "apply a app "+pw+"="+pw+"-next-1", "healthy a")
	if r.gen != 1 {
		t.Fatalf("generated %d next values", r.gen)
	}
	if got := w.get(pw); got != pw+"-next-1" {
		t.Fatalf("%s = %q", pw, got)
	}
}

// A run stopped after stage 1 resumes at stage 2 with the value in the file.
func TestStoppedAfterStage1ResumesAtStage2(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a")
	r.check = errors.New("postgres refused")
	if err := execute(t, w.env(pw), r); err == nil {
		t.Fatal("no error from a failed stage 2")
	}
	r.check = nil
	w.log = nil
	run, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Items[0].From; got != secretsrotate.StageChecked {
		t.Fatalf("resumes from stage %d, want %d", got, secretsrotate.StageChecked)
	}
	if err := run.Execute(w.env(pw), noConfirm); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "checked "+pw+"="+pw+"-next-1", "apply a app "+pw+"="+pw+"-next-1", "healthy a")
}

// A stage 3 that stopped at its second user resumes there: the first user
// already presents the next value, so nothing changes on it again.
func TestStoppedStage3ResumesAtTheFailedUse(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a", "b", "c")
	w.failApply["b"] = errors.New("b unhealthy")
	if err := execute(t, w.env(pw), r); err == nil {
		t.Fatal("no error")
	}
	for _, l := range w.log {
		if strings.HasPrefix(l, "apply c") {
			t.Fatalf("a use after the failed one ran: %v", w.log)
		}
	}
	w.log = nil
	if err := execute(t, w.env(pw), r); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w,
		"healthy a",
		"apply b app "+pw+"="+pw+"-next-1",
		"healthy b",
		"apply c app "+pw+"="+pw+"-next-1",
		"healthy c",
	)
}

// Promotion clears the rotation and the --all bookkeeping; --all records
// every path before it starts, and a resumed --all does only what was left.
func TestAllRecordsRemainingAndPromotionClearsIt(t *testing.T) {
	const tok = "storage.garage.admin_token"
	w := newWorld(t, map[string]string{pw: "old", tok: "oldtok"})
	a, b := newRot(w, pw, "a"), newRot(w, tok, "a")
	w.failApply["a"] = errors.New("stop")
	run, err := secretsrotate.PlanAll(w.env(pw, tok), []secretsrotate.Rotator{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw, tok), noConfirm); err == nil {
		t.Fatal("no error")
	}
	if got := strings.Join(w.secrets.RotateRemaining, " "); got != pw+" "+tok {
		t.Fatalf("rotate_remaining = %q", got)
	}

	// The first finishes; the run then stops before the second's users.
	w.log = nil
	w.failApply["a"] = nil
	a2, b2 := newRot(w, pw, "a"), newRot(w, tok, "b")
	a2.gen = a.gen
	w.failApply["b"] = errors.New("stop again")
	run, err = secretsrotate.PlanAll(w.env(pw, tok), []secretsrotate.Rotator{a2, b2})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw, tok), noConfirm); err == nil {
		t.Fatal("no error")
	}
	if got := strings.Join(w.secrets.RotateRemaining, " "); got != tok {
		t.Fatalf("rotate_remaining = %q after the first promoted", got)
	}
	if _, ok := w.secrets.Rotations[pw]; ok {
		t.Fatal("rotations still holds the promoted path")
	}

	// A resumed --all does only the path it had left.
	w.log = nil
	a3 := newRot(w, pw, "a")
	run, err = secretsrotate.PlanAll(w.env(pw, tok), []secretsrotate.Rotator{a3, b2})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Resumed || len(run.Items) != 1 || run.Items[0].Rotator.Path() != tok {
		t.Fatalf("resumed run plans %+v", run.Items)
	}
	if err := run.Execute(w.env(pw, tok), noConfirm); err != nil {
		t.Fatal(err)
	}
	if len(w.secrets.RotateRemaining) != 0 || len(w.secrets.Rotations) != 0 {
		t.Fatalf("left rotate_remaining %v, rotations %v", w.secrets.RotateRemaining, w.secrets.Rotations)
	}
	if a3.gen != 0 {
		t.Fatal("a resumed --all rotated a promoted secret again")
	}
}

// A pending rotation goes first, whatever order the run names.
func TestPendingRotationGoesFirst(t *testing.T) {
	const tok = "storage.garage.admin_token"
	w := newWorld(t, map[string]string{pw: "old", tok: "oldtok"})
	w.secrets.Rotations = map[string]string{tok: "tok-next"}
	run, err := secretsrotate.Plan(w.env(pw, tok), []secretsrotate.Rotator{newRot(w, pw, "a"), newRot(w, tok, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if run.Items[0].Rotator.Path() != tok || run.Items[0].From != secretsrotate.StageChecked {
		t.Fatalf("first item %s from %d", run.Items[0].Rotator.Path(), run.Items[0].From)
	}
}

// A pending rotation the run has no rotator for is refused: it has to finish
// before anything else changes.
func TestPendingWithoutRotatorIsRefused(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.secrets.Rotations = map[string]string{"storage.garage.admin_token": "x"}
	_, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a")})
	if err == nil || !strings.Contains(err.Error(), "storage.garage.admin_token") {
		t.Fatalf("err = %v", err)
	}
}

// A run with a rotation that signs members out does nothing until the
// operator types "sign out".
func TestSignsOutWithoutConfirmationRunsNothing(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a")
	r.cost.SignsOut = true
	run, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	err = run.Execute(w.env(pw), func(word, what string) error {
		asked = append(asked, word)
		return errors.New("no terminal")
	})
	if err == nil {
		t.Fatal("ran without confirmation")
	}
	if strings.Join(asked, "|") != "sign out" {
		t.Fatalf("asked %q", asked)
	}
	if len(w.log) != 0 || w.saves != 0 {
		t.Fatalf("ran %v, saved %d times", w.log, w.saves)
	}

	asked = nil
	if err := run.Execute(w.env(pw), func(word, what string) error {
		asked = append(asked, word)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || w.get(pw) != pw+"-next-1" {
		t.Fatalf("asked %q, value %q", asked, w.get(pw))
	}
}

// Asked before the run, as the command does before it claims the hosts,
// sign out is not asked again when the run executes; refused, nothing runs.
func TestSignOutAskedOnceBeforeExecute(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a")
	r.cost.SignsOut = true
	run, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{r})
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	confirm := func(word, what string) error { asked++; return nil }
	if err := run.ConfirmSignOut(confirm); err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw), confirm); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("asked %d times", asked)
	}

	w = newWorld(t, map[string]string{pw: "old"})
	r = newRot(w, pw, "a")
	r.cost.SignsOut = true
	run, _ = secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{r})
	if err := run.ConfirmSignOut(func(string, string) error { return errors.New("no") }); err == nil {
		t.Fatal("a refusal passed")
	}
	if err := run.Execute(w.env(pw), nil); err == nil || w.saves != 0 {
		t.Fatalf("ran after a refused confirmation: %v, %d saves", err, w.saves)
	}
}

// A run without one asks nothing.
func TestNoSignOutAsksNothing(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	run, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw), func(word, what string) error {
		t.Fatalf("asked %q", word)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The plan names each rotator's path, its cost and its users in order.
func TestShowNamesEachCostAndUse(t *testing.T) {
	const tok = "storage.garage.rpc_secret"
	const sec = "apps.mbin.app_secret"
	w := newWorld(t, map[string]string{pw: "old", tok: "aa", sec: "s"})
	a := newRot(w, pw, "home-a", "home-b")
	a.cost.Reconnects = true
	b := newRot(w, tok, "vm")
	b.cost.Downtime = "storage, for one restart"
	c := newRot(w, sec, "home-a")
	c.cost.SignsOut = true
	run, err := secretsrotate.Plan(w.env(), []secretsrotate.Rotator{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{Verbose_: true}
	run.Show(rec)
	for _, want := range []string{pw, tok, sec, "reconnect", "storage, for one restart", "signs members out", "home-a", "home-b", "vm", "sign out"} {
		found := false
		for _, e := range rec.Events {
			if strings.Contains(e.Text, want) || strings.Contains(e.Extra, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("plan does not name %q:\n%s", want, rec.Lines())
		}
	}
	ia, ib := rec.Index("step", "home-a"), rec.Index("step", "home-b")
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("users out of order:\n%s", rec.Lines())
	}
	if rec.Index("section", pw) > rec.Index("section", tok) || rec.Index("section", tok) > rec.Index("section", sec) {
		t.Errorf("rotators out of order:\n%s", rec.Lines())
	}
	if w.saves != 0 || len(w.log) != 0 {
		t.Fatal("plan or show changed something")
	}
}

// A rotation's notes are shown under it at every verbosity, not as details.
func TestShowExplainsEachNote(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	run, err := secretsrotate.Plan(w.env(), []secretsrotate.Rotator{newRot(w, pw, "home-a")})
	if err != nil {
		t.Fatal(err)
	}
	run.Items[0].Notes = []string{"a note the operator must read"}
	rec := &ui.Recorder{}
	run.Show(rec)
	if i := rec.Index("explain", "a note the operator must read"); i < rec.Index("section", pw) {
		t.Errorf("the note is not explained under its rotation:\n%s", rec.Lines())
	}
}

// A precheck refuses at plan time, before anything changes.
func TestPrecheckRefusesAtPlan(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw, "a")
	r.pre = errors.New("unchanged since the last apply")
	_, err := secretsrotate.Plan(w.env(pw), []secretsrotate.Rotator{prechecked{r}})
	if err == nil || !strings.Contains(err.Error(), "unchanged since the last apply") {
		t.Fatalf("err = %v", err)
	}
	if w.saves != 0 {
		t.Fatal("saved")
	}
}

// A rotation with its own staging is handed the whole of it, and leaves the
// --all bookkeeping when it succeeds.
func TestWholeRunsItsOwnStaging(t *testing.T) {
	const cs = "oidc_clients.outline.client_secret"
	w := newWorld(t, map[string]string{pw: "old", cs: "c"})
	h := whole{newRot(w, cs, "a")}
	run, err := secretsrotate.PlanAll(w.env(pw), []secretsrotate.Rotator{h, newRot(w, pw, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw), noConfirm); err != nil {
		t.Fatal(err)
	}
	if w.log[0] != "whole "+cs || h.gen != 0 {
		t.Fatalf("log %v, generated %d", w.log, h.gen)
	}
	if len(w.secrets.RotateRemaining) != 0 {
		t.Fatalf("rotate_remaining %v", w.secrets.RotateRemaining)
	}
}

// A use with its own step runs it in place of the scoped apply, then the
// health gate.
func TestUseDoReplacesApply(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	r := newRot(w, pw)
	r.uses = []secretsrotate.Use{{Site: "a", Stacks: []string{"patroni"}, Do: func(e secretsrotate.Env, s *config.Secrets) error {
		v, _ := s.Get(pw)
		w.log = append(w.log, "switchover with "+v)
		return nil
	}}}
	if err := execute(t, w.env(pw), r); err != nil {
		t.Fatal(err)
	}
	wantLog(t, w, "next "+pw, "checked "+pw+"="+pw+"-next-1", "switchover with "+pw+"-next-1", "healthy a")
}

// A secrets file that does not hold what was written stops the run before
// any host is touched.
func TestStoreThatLosesTheWriteStops(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.drop = true
	err := execute(t, w.env(pw), newRot(w, pw, "a"))
	if err == nil {
		t.Fatal("no error")
	}
	for _, l := range w.log {
		if strings.HasPrefix(l, "checked") || strings.HasPrefix(l, "apply") {
			t.Fatalf("touched a host: %v", w.log)
		}
	}
}

// The embeddable default generates a replacement for the path and promotes
// it there.
func TestGeneratedDefaults(t *testing.T) {
	s := &config.Secrets{}
	if err := s.Set(pw, "old"); err != nil {
		t.Fatal(err)
	}
	g := secretsrotate.Generated{SecretPath: pw}
	next, err := g.Next(secretsrotate.Env{}, s)
	if err != nil || next == "" || next == "old" {
		t.Fatalf("next %q, %v", next, err)
	}
	if err := g.Promote(s, next); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(pw); v != next || g.Path() != pw {
		t.Fatalf("promoted %q", v)
	}
}

// A resumed --all says so at every verbosity, and that what the earlier run
// finished is not rotated again.
func TestResumedAllIsANote(t *testing.T) {
	const tok = "storage.garage.admin_token"
	w := newWorld(t, map[string]string{pw: "old", tok: "oldtok"})
	w.secrets.RotateRemaining = []string{tok}
	run, err := secretsrotate.PlanAll(w.env(pw, tok), []secretsrotate.Rotator{newRot(w, pw, "a"), newRot(w, tok, "a")})
	if err != nil {
		t.Fatal(err)
	}
	rec := &ui.Recorder{}
	run.ShowResume(rec)
	i := rec.Index("note", "resuming the --all run")
	if i < 0 || !strings.Contains(rec.Events[i].Extra, "not rotated again") || !strings.Contains(rec.Events[i].Extra, "--all --restart") {
		t.Fatalf("no note of the resume:\n%s", rec.Lines())
	}
	rec = &ui.Recorder{}
	run.Show(rec)
	if !rec.Has("note", "resuming the --all run") {
		t.Fatalf("the plan does not note the resume:\n%s", rec.Lines())
	}
}

// A resumed --all whose only paths left are pending is still a resume.
func TestResumedWhenEverythingLeftIsPending(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.secrets.RotateRemaining = []string{pw}
	w.secrets.Rotations = map[string]string{pw: "pw-next"}
	run, err := secretsrotate.PlanAll(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Resumed {
		t.Fatal("not resumed")
	}
}

// A path the earlier --all run left that this run has no rotation of is
// refused, naming it and the way to start again: otherwise it would stay on
// the list, and every later --all would stop short of it.
func TestResumedPathWithoutRotatorIsRefused(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.secrets.RotateRemaining = []string{"apps.gone.database_password", pw}
	_, err := secretsrotate.PlanAll(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a")})
	if err == nil || !strings.Contains(err.Error(), "apps.gone.database_password") || !strings.Contains(err.Error(), "--all --restart") {
		t.Fatalf("err = %v", err)
	}
}

// A secret whose precheck finds it kept is listed with why, and a resumed
// --all with nothing left but kept secrets clears what the file records when
// executed, so the next --all is a full one.
func TestKeptIsListedAndClearsWhatIsLeft(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.secrets.RotateRemaining = []string{pw}
	r := newRot(w, pw, "a")
	r.pre = &secretsrotate.KeptError{Path: pw, Reason: "nothing checks it"}
	run, err := secretsrotate.PlanAll(w.env(pw), []secretsrotate.Rotator{prechecked{r}})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Items) != 0 || len(run.Kept) != 1 || run.Kept[0].Path != pw || run.Kept[0].Reason != "nothing checks it" {
		t.Fatalf("items %v, kept %v", run.Items, run.Kept)
	}
	if !run.Resumed {
		t.Fatal("not resumed")
	}
	if err := run.Execute(w.env(pw), noConfirm); err != nil {
		t.Fatal(err)
	}
	if len(w.secrets.RotateRemaining) != 0 {
		t.Fatalf("rotate_remaining %v", w.secrets.RotateRemaining)
	}
}

// --all --restart discards what an earlier --all left and plans every
// secret again, recording the full list when executed.
func TestRestartPlansTheFullList(t *testing.T) {
	const tok = "storage.garage.admin_token"
	w := newWorld(t, map[string]string{pw: "old", tok: "oldtok"})
	w.secrets.RotateRemaining = []string{tok}
	run, err := secretsrotate.PlanAllRestart(w.env(pw, tok), []secretsrotate.Rotator{newRot(w, pw, "a"), newRot(w, tok, "a")})
	if err != nil {
		t.Fatal(err)
	}
	if run.Resumed || !run.Restarted || len(run.Items) != 2 {
		t.Fatalf("resumed %v, restarted %v, items %d", run.Resumed, run.Restarted, len(run.Items))
	}
	rec := &ui.Recorder{}
	run.ShowResume(rec)
	if !rec.Has("note", "starting --all again") {
		t.Fatalf("no note of the restart:\n%s", rec.Lines())
	}
	if err := run.Execute(w.env(pw, tok), noConfirm); err != nil {
		t.Fatal(err)
	}
	if w.get(pw) == "old" || w.get(tok) == "oldtok" || len(w.secrets.RotateRemaining) != 0 {
		t.Fatalf("pw %q, tok %q, remaining %v", w.get(pw), w.get(tok), w.secrets.RotateRemaining)
	}
}

// --all --restart is refused while a rotation is pending: it has to finish
// first, and the refusal names the command that finishes it.
func TestRestartRefusedWhilePending(t *testing.T) {
	w := newWorld(t, map[string]string{pw: "old"})
	w.secrets.Rotations = map[string]string{pw: "pw-next"}
	_, err := secretsrotate.PlanAllRestart(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a")})
	if err == nil || !strings.Contains(err.Error(), "paisans secrets rotate "+pw+" --execute") {
		t.Fatalf("err = %v", err)
	}
}

// pendingAtWhole is a rotation with its own staging that records whether any
// rotation was pending in the secrets file when it ran.
type pendingAtWhole struct {
	*rot
	ran     bool
	pending []string
}

func (h *pendingAtWhole) RunWhole(e secretsrotate.Env) error {
	s, err := e.Store.Load()
	if err != nil {
		return err
	}
	h.ran, h.pending = true, s.RotationPending()
	return nil
}

// The commands a rotation with its own staging runs (storage rotate-key, oidc
// client create) refuse while a rotation is pending, as every command that
// renders does; they run inside secrets rotate because every rotation before
// them in the run is promoted first, so none is pending when they start.
func TestWholeRunsWithNothingPending(t *testing.T) {
	const cs = "oidc_clients.outline.client_secret"
	w := newWorld(t, map[string]string{pw: "old", cs: "c"})
	w.secrets.Rotations = map[string]string{pw: "pw-next"}
	h := &pendingAtWhole{rot: newRot(w, cs, "a")}
	run, err := secretsrotate.PlanAll(w.env(pw), []secretsrotate.Rotator{newRot(w, pw, "a"), h})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Execute(w.env(pw), noConfirm); err != nil {
		t.Fatal(err)
	}
	if !h.ran || len(h.pending) != 0 {
		t.Fatalf("ran %v, pending when it ran: %v", h.ran, h.pending)
	}
}
