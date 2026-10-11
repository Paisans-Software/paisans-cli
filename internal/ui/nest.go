package ui

import "sync"

// shower is a line that can show, while it runs, the step a command nested
// in it has open: a writer's step or group line.
type shower interface {
	showNested(text string)
}

// Nest is r for a command that runs under line, quietly: each step the
// command opens goes on to r, and while it is open line shows its title in
// parentheses after its elapsed time. The innermost open step is shown, its
// own progress after its title; when it ends the step enclosing it is shown
// again. done clears what line shows, for a command that left a step open;
// line's finished line never shows it. A line that draws nothing between
// lines shows nothing.
func Nest(line Step, r Reporter) (nested Reporter, done func()) {
	n := &nest{Reporter: r}
	if s, ok := line.(shower); ok {
		n.line = s
	}
	return n, n.done
}

type nest struct {
	Reporter
	line shower
	mu   sync.Mutex
	open []*nestStep
	// ended is set by done: a step opened after it shows nothing.
	ended bool
}

type nestStep struct {
	Step
	n        *nest
	title    string
	progress string
}

// Hold is r's, for a command that asks the operator something.
func (n *nest) Hold() (resume func()) { return Hold(n.Reporter) }

func (n *nest) Step(title string) Step {
	s := &nestStep{Step: n.Reporter.Step(title), n: n, title: title}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.ended {
		n.open = append(n.open, s)
		n.showLocked()
	}
	return s
}

func (n *nest) done() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ended = true
	n.open = nil
	n.showLocked()
}

// showLocked shows the innermost open step on the line, or nothing.
func (n *nest) showLocked() {
	if n.line == nil {
		return
	}
	text := ""
	if k := len(n.open); k > 0 {
		s := n.open[k-1]
		text = s.title
		if s.progress != "" {
			text += ": " + s.progress
		}
	}
	n.line.showNested(text)
}

// close takes s out of the open steps, wherever it is among them.
func (n *nest) close(s *nestStep) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, o := range n.open {
		if o == s {
			n.open = append(n.open[:i:i], n.open[i+1:]...)
			n.showLocked()
			return
		}
	}
}

func (s *nestStep) Done(result string)        { s.n.close(s); s.Step.Done(result) }
func (s *nestStep) Fail(err error)            { s.n.close(s); s.Step.Fail(err) }
func (s *nestStep) End(m Mark, result string) { s.n.close(s); s.Step.End(m, result) }

func (s *nestStep) Progress(text string) {
	s.Step.Progress(text)
	n := s.n
	n.mu.Lock()
	defer n.mu.Unlock()
	s.progress = text
	if k := len(n.open); k > 0 && n.open[k-1] == s {
		n.showLocked()
	}
}
