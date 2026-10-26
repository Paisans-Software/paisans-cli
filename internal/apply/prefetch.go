package apply

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Prefetch is t with the answers to commands and to reading files fetched
// ahead, in one round trip to the host, for a caller about to read many
// things in turn: each costs a round trip of its own otherwise, which over
// ssh is a tenth of a second or more before the command starts.
//
// Only an SSHTransport is asked ahead. Any other transport, a test's fake
// among them, is returned as it is, and answers each command when it is
// asked, as it did before.
//
// The commands must only read: each is run whether or not the caller asks
// for it, in the order given. A prefetched answer is used once, for the
// first Run of exactly that command or ReadFile of exactly that path. Once
// the caller writes anything through the transport (WriteFile, RunInput, or
// a Run of any command not fetched ahead), every answer not yet used is
// dropped, since the write may have changed it, and each is asked of the
// host again.
func Prefetch(t Transport, commands, files []string) Transport {
	s, ok := t.(SSHTransport)
	if !ok || len(commands)+len(files) == 0 {
		return t
	}
	p := &prefetched{Transport: s, runs: map[string]batchResult{}, reads: map[string]batchResult{}}
	all := append(append([]string{}, commands...), readCommands(files)...)
	results, err := s.runBatch(all)
	if err != nil {
		// The host could not be asked ahead; each command is asked when
		// the caller asks, which reports the failure where it belongs.
		return t
	}
	for i, c := range commands {
		p.runs[c] = results[i]
	}
	for i, f := range files {
		p.reads[f] = results[len(commands)+i]
	}
	return p
}

// prefetched answers what Prefetch fetched ahead, then asks the host.
type prefetched struct {
	Transport
	mu    sync.Mutex
	runs  map[string]batchResult
	reads map[string]batchResult
}

func (p *prefetched) take(m map[string]batchResult, key string) (batchResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := m[key]
	if ok {
		delete(m, key)
	}
	return r, ok
}

// drop forgets every answer not yet used, after a write.
func (p *prefetched) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.runs)
	clear(p.reads)
}

func (p *prefetched) Run(command string) (string, error) {
	if r, ok := p.take(p.runs, command); ok {
		return r.out, r.err(p.Transport.(SSHTransport), command)
	}
	p.drop()
	return p.Transport.Run(command)
}

func (p *prefetched) ReadFile(path string) (string, bool, error) {
	if r, ok := p.take(p.reads, path); ok {
		if err := r.err(p.Transport.(SSHTransport), readFileCommand(path)); err != nil {
			return "", false, err
		}
		return parseReadFile(r.out)
	}
	return p.Transport.ReadFile(path)
}

func (p *prefetched) WriteFile(path, content string, mode uint32) error {
	p.drop()
	return p.Transport.WriteFile(path, content, mode)
}

func (p *prefetched) RunInput(command, stdin string) (string, error) {
	p.drop()
	return p.Transport.RunInput(command, stdin)
}

// batchResult is one command's combined output and exit status.
type batchResult struct {
	out  string
	code int
}

// err is the error Run would have returned for command exiting with r's
// status, or nil for a status of 0.
func (r batchResult) err(t SSHTransport, command string) error {
	if r.code == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s: exit status %d\n%s", t.Describe(), t.named(command), r.code, strings.TrimRight(r.out, "\n"))
}

// batchMark opens each command's answer in a batch's output.
const batchMark = "__PAISANS_BATCH__"

// batchScript runs each command in a subshell of its own, so an exit in one
// ends only it, and prints for each, in order, a line of the mark, its index,
// its exit status and its combined output in base64, which no output can
// mistake for the mark. The x printed after the output keeps its trailing
// newlines, which $(...) would strip. Its first line is a comment naming it.
func batchScript(commands []string) string {
	var b strings.Builder
	// The first line names the batch, for a run log, which shows a
	// command's first line.
	fmt.Fprintf(&b, "# %d commands at once\n", len(commands))
	for i, c := range commands {
		fmt.Fprintf(&b, "o=$( (\n%s\n) 2>&1; c=$?; printf x; exit $c ); c=$?; o=${o%%x}\n", c)
		fmt.Fprintf(&b, "printf '%s %d %%s ' \"$c\"; printf %%s \"$o\" | base64 | tr -d '\\n'; echo\n", batchMark, i)
	}
	return b.String()
}

// parseBatch reads batchScript's output for n commands.
func parseBatch(out string, n int) ([]batchResult, error) {
	results := make([]batchResult, n)
	seen := make([]bool, n)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != batchMark {
			continue
		}
		i, err := strconv.Atoi(fields[1])
		if err != nil || i < 0 || i >= n {
			return nil, fmt.Errorf("a batch answer names no command it was sent: %q", line)
		}
		code, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("a batch answer has no exit status: %q", line)
		}
		var body []byte
		if len(fields) > 3 {
			if body, err = base64.StdEncoding.DecodeString(fields[3]); err != nil {
				return nil, fmt.Errorf("a batch answer could not be decoded: %w", err)
			}
		}
		results[i], seen[i] = batchResult{out: string(body), code: code}, true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("the batch ended before command %d answered", i)
		}
	}
	return results, nil
}

// runBatch runs commands on the host in one round trip.
func (t SSHTransport) runBatch(commands []string) ([]batchResult, error) {
	out, err := t.Run(batchScript(commands))
	if err != nil {
		return nil, err
	}
	return parseBatch(out, len(commands))
}

// readFileCommand is what ReadFile runs to read path.
func readFileCommand(path string) string {
	return fmt.Sprintf("if [ -f %s ]; then cat %s; else echo %s; fi", shellQuote(path), shellQuote(path), absentMark)
}

func readCommands(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = readFileCommand(p)
	}
	return out
}

// absentMark is what readFileCommand prints for a file that is not there.
const absentMark = "__PAISANS_ABSENT__"

// parseReadFile is ReadFile's answer from readFileCommand's output.
func parseReadFile(out string) (string, bool, error) {
	if strings.TrimSpace(out) == absentMark {
		return "", false, nil
	}
	return out, true, nil
}
