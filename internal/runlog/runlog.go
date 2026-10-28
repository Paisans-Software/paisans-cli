// Package runlog keeps a timed record of one command's run on the
// workstation: each step as it opens and ends, and each command sent to a
// host over ssh, with how long it took and what in Paisans sent it. It
// answers "why did that take so long" after the fact, without a trace.
//
// A run's log is runs/<UTC time>-<command>.log under the operator's data
// directory. It holds no secret: a command sent to a host carries none on
// its line (what must not appear in `ps` goes over stdin), and each line is
// cut to its first line and redacted besides. A failure keeps what follows
// its first line too, the host's own output, as indented detail lines: that
// is the one part of a failed run the terminal shows and the log otherwise
// would not, and it is redacted before it reaches the log, as every error a
// transport returns is. The newest Keep logs are kept.
package runlog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Keep is how many run logs are kept; opening one removes the oldest beyond
// it.
const Keep = 20

// MaxDetail is how many lines of a failure's detail are kept. A host's
// output past it is rarely the cause, and a log of a failed run stays
// readable.
const MaxDetail = 40

// Dir is where run logs go under the data directory base.
func Dir(base string) string { return filepath.Join(base, "runs") }

// Log is one run's record. A nil *Log records nothing, so a caller holds one
// without asking whether a run is being logged.
type Log struct {
	mu      sync.Mutex
	w       io.Writer
	f       *os.File
	path    string
	started time.Time
	now     func() time.Time
	// calls is each site's commands sent, and their total time; callers is
	// the same by what sent them, for the summary.
	calls   map[string]*tally
	callers map[string]*tally
}

type tally struct {
	n    int
	took time.Duration
}

// Open starts a run log under base for command, Eg: "deployment apply",
// heads it with the version and the command line, and makes it current: the
// one Step and SSH write to. Close ends it.
func Open(base, version, command string, args []string) (*Log, error) {
	dir := Dir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	name := now.Format("20060102T150405Z") + "-" + strings.ReplaceAll(command, " ", "-") + ".log"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	l := newLog(f, time.Now)
	l.f, l.path = f, path
	fmt.Fprintf(f, "paisans %s: %s %s\nstarted %s\n\n", version, command, strings.Join(args, " "), now.Format(time.RFC3339))
	prune(dir, Keep)
	setCurrent(l)
	return l, nil
}

func newLog(w io.Writer, now func() time.Time) *Log {
	return &Log{w: w, started: now(), now: now, calls: map[string]*tally{}, callers: map[string]*tally{}}
}

// Path is where the log is written.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close writes the summary, closes the file, and leaves no log current.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	setCurrent(nil)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.summaryLocked()
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// at is the time since the run started, as each line begins.
func (l *Log) at() string {
	return fmt.Sprintf("%8.2fs", l.now().Sub(l.started).Seconds())
}

// Step records a step opening, and returns what records it ending.
func (l *Log) Step(title string) (end func(mark, result string)) {
	if l == nil {
		return func(string, string) {}
	}
	opened := l.now()
	l.mu.Lock()
	fmt.Fprintf(l.w, "%s  step  open  %s\n", l.at(), title)
	l.mu.Unlock()
	return func(mark, result string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		line := title
		if result != "" {
			line += ": " + firstLine(result)
		}
		fmt.Fprintf(l.w, "%s  step  %-5s %s (%.2fs)\n", l.at(), mark, line, l.now().Sub(opened).Seconds())
		if mark == "fail" {
			l.detailLocked(result)
		}
	}
}

// SSH records one command sent to site, which took took and failed with
// err, or nil. by is what in Paisans sent it (see Caller).
func (l *Log) SSH(site, command, by string, took time.Duration, err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	status := ""
	if err != nil {
		status = " FAILED"
	}
	fmt.Fprintf(l.w, "%s  ssh   %5.2fs %s%s: %s  [%s]\n", l.at(), took.Seconds(), site, status, clip(firstLine(command), 120), by)
	if err != nil {
		l.detailLocked(err.Error())
	}
	add(l.calls, site, took)
	add(l.callers, by, took)
}

// detailLocked writes the lines of text after its first, each indented
// under the line that failed, at most MaxDetail of them.
func (l *Log) detailLocked(text string) {
	_, rest, ok := strings.Cut(strings.TrimRight(text, "\n"), "\n")
	if !ok {
		return
	}
	lines := strings.Split(rest, "\n")
	shown := lines[:min(len(lines), MaxDetail)]
	for _, line := range shown {
		fmt.Fprintf(l.w, "                 | %s\n", strings.TrimRight(line, "\r"))
	}
	if left := len(lines) - len(shown); left > 0 {
		fmt.Fprintf(l.w, "                 | ... %d more lines\n", left)
	}
}

func add(m map[string]*tally, key string, took time.Duration) {
	t := m[key]
	if t == nil {
		t = &tally{}
		m[key] = t
	}
	t.n++
	t.took += took
}

// summaryLocked writes the run's time, and its ssh commands by site and by
// what sent them, the costliest first.
func (l *Log) summaryLocked() {
	fmt.Fprintf(l.w, "\nfinished after %.2fs\n", l.now().Sub(l.started).Seconds())
	for _, part := range []struct {
		head string
		m    map[string]*tally
	}{{"ssh commands by site", l.calls}, {"ssh commands by caller", l.callers}} {
		if len(part.m) == 0 {
			continue
		}
		fmt.Fprintf(l.w, "\n%s:\n", part.head)
		keys := make([]string, 0, len(part.m))
		for k := range part.m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if a, b := part.m[keys[i]].took, part.m[keys[j]].took; a != b {
				return a > b
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			t := part.m[k]
			fmt.Fprintf(l.w, "  %4d  %7.2fs  %s\n", t.n, t.took.Seconds(), k)
		}
	}
}

var (
	currentMu sync.Mutex
	current   *Log
)

func setCurrent(l *Log) {
	currentMu.Lock()
	defer currentMu.Unlock()
	current = l
}

// Current is the log being written, or nil for none.
func Current() *Log {
	currentMu.Lock()
	defer currentMu.Unlock()
	return current
}

// Caller names the first function on the calling goroutine's stack whose
// name, after its module path, starts with none of skip, Eg: with skip
// "apply.SSHTransport." the function that called the transport.
func Caller(skip ...string) string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		name := shortName(f.Function)
		if !skipped(name, skip) {
			return name
		}
		if !more {
			return name
		}
	}
}

func skipped(name string, skip []string) bool {
	for _, s := range skip {
		if strings.HasPrefix(name, s) {
			return true
		}
	}
	return false
}

// shortName is a function's name after its module path, Eg:
// "apply.(*planner).plan".
func shortName(fn string) string {
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		fn = fn[i+1:]
	}
	return fn
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// prune removes the oldest logs in dir beyond keep. Names start with the
// time, so they sort oldest first.
func prune(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs)
	for len(logs) > keep {
		os.Remove(filepath.Join(dir, logs[0]))
		logs = logs[1:]
	}
}
