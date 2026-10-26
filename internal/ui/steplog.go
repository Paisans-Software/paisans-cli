package ui

import "sync"

// StepLog, when set, is told of each step as it opens, and the function it
// returns is told of the step as it ends. A run log sets it (see
// internal/runlog), so it records the steps of a reporter that draws nothing,
// as a nested command's are, beside the ones on the terminal.
var StepLog func(title string) (end func(mark, result string))

// markNames are the marks as a run log writes them.
var markNames = map[Mark]string{OK: "ok", Failed: "fail", Pending: "plan", Waiting: "wait", Warned: "warn"}

// logStep tells StepLog of a step opening, and returns what tells it of the
// step ending, once however often it is called.
func logStep(title string) func(Mark, string) {
	log := StepLog
	if log == nil {
		return func(Mark, string) {}
	}
	end := log(title)
	var once sync.Once
	return func(m Mark, result string) {
		once.Do(func() { end(markNames[m], result) })
	}
}
