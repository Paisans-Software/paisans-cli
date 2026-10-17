// Package create asks the questions behind `paisans create` and writes a new
// deployment's paisans-stack.yaml from the answers.
//
// It does no I/O but what it is handed: the prompter reads the reader and
// writes the writer it is made with, and Render returns bytes. The command
// decides where they come from and where the file goes.
package create

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/ui"
)

// ErrAborted is input ending before every question was answered: Ctrl-D, a
// closed pipe, or a terminal gone away. Nothing is written after it, because
// a file built from some of the answers is a file nobody asked for.
var ErrAborted = errors.New("input ended before every question was answered")

// Prompter asks one question at a time on plain lines: a prompt, a default in
// brackets that Enter takes, and the question again, with the reason, for an
// answer that does not do.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
	// width is how wide prose is wrapped, read at each write so a terminal
	// resized between questions is wrapped to its new width; 0 wraps nothing.
	width func() int
}

// NewPrompter reads answers from in and writes questions to out. Prose is
// wrapped to out's width when out is a terminal.
func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out, width: func() int { return ui.TerminalWidth(out) }}
}

// Say writes explanation between questions, wrapped to the terminal's width:
// each line of it is a paragraph, so it is written whole rather than broken
// by hand at a width the terminal may not have. A blank line stays blank.
func (p *Prompter) Say(format string, args ...any) {
	for _, line := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		p.wrapped("", line)
	}
}

// wrapped writes text wrapped to the terminal's width, after mark on its
// first line and under its text on every other, so a numbered option keeps
// its number to the left of all of it.
func (p *Prompter) wrapped(mark, text string) {
	if mark == "" && strings.TrimSpace(text) == "" {
		fmt.Fprintln(p.out)
		return
	}
	width := p.width()
	if width > 0 {
		width = max(width-len(mark), 1)
	}
	hang := strings.Repeat(" ", len(mark))
	for i, line := range ui.Wrap(text, width) {
		if i == 0 {
			fmt.Fprintln(p.out, mark+line)
		} else {
			fmt.Fprintln(p.out, hang+line)
		}
	}
}

// line reads one answer, trimmed. A last line with no newline is still an
// answer; end of input with nothing on the line is ErrAborted.
func (p *Prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && s != "" {
			return strings.TrimSpace(s), nil
		}
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(p.out)
			return "", ErrAborted
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (p *Prompter) ask(q, def string) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", q, def)
		return
	}
	fmt.Fprintf(p.out, "%s: ", q)
}

func (p *Prompter) refuse(reason string) {
	p.wrapped("  ", reason)
}

// Text asks for a required answer. A blank line takes def, and with no def
// is asked again. check, when given, judges the answer, and its error is
// shown before the question is asked again.
func (p *Prompter) Text(q, def string, check func(string) error) (string, error) {
	for {
		p.ask(q, def)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if s == "" {
			p.refuse("an answer is required")
			continue
		}
		if check != nil {
			if err := check(s); err != nil {
				p.refuse(err.Error())
				continue
			}
		}
		return s, nil
	}
}

// Optional is Text for a question that may be left blank: a blank line takes
// def, which may itself be blank, and check judges only what was given.
func (p *Prompter) Optional(q, def string, check func(string) error) (string, error) {
	for {
		p.ask(q, def)
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if s != "" && check != nil {
			if err := check(s); err != nil {
				p.refuse(err.Error())
				continue
			}
		}
		return s, nil
	}
}

// YesNo asks a yes or no question, with def taken on Enter.
func (p *Prompter) YesNo(q string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", q, hint)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.refuse("answer y or n")
	}
}

// Choose asks q, a question written as one, for one of options, listed by
// number from 1, and returns its index. def is the index Enter takes.
func (p *Prompter) Choose(q string, options []string, def int) (int, error) {
	p.Say("%s", q)
	for i, o := range options {
		p.wrapped(fmt.Sprintf("  %d) ", i+1), o)
	}
	for {
		p.ask("Choose", strconv.Itoa(def+1))
		s, err := p.line()
		if err != nil {
			return 0, err
		}
		if s == "" {
			return def, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(options) {
			p.refuse(fmt.Sprintf("a number from 1 to %d", len(options)))
			continue
		}
		return n - 1, nil
	}
}

// Lines asks for one or more lines, pasted one per line and ended by a blank
// line: how a public key is handed over. A blank first line keeps def when
// there is one. check judges each line as it comes, given the lines kept
// before it; a line it refuses is dropped and said so, and the rest of the
// paste carries on. def is judged the same way when it is kept, and is asked
// for again if any of it is refused.
func (p *Prompter) Lines(q string, def []string, check func(got []string, s string) error) ([]string, error) {
	if len(def) > 0 {
		p.Say("%s, one per line, then a blank line (a blank line now keeps the %d given):", q, len(def))
	} else {
		p.Say("%s, one per line, then a blank line:", q)
	}
	var got []string
	for {
		s, err := p.line()
		if err != nil {
			return nil, err
		}
		if s == "" {
			if len(got) > 0 {
				return got, nil
			}
			if len(def) > 0 {
				if err := checkAll(def, check); err != nil {
					p.refuse(fmt.Sprintf("the %d given no longer do: %v. Paste them again", len(def), err))
					def = nil
					continue
				}
				return def, nil
			}
			p.refuse("at least one line is required")
			continue
		}
		if check != nil {
			if err := check(got, s); err != nil {
				p.refuse(err.Error())
				continue
			}
		}
		got = append(got, s)
	}
}

// checkAll judges lines as Lines judges a paste, one after another.
func checkAll(lines []string, check func(got []string, s string) error) error {
	if check == nil {
		return nil
	}
	for i, s := range lines {
		if err := check(lines[:i], s); err != nil {
			return err
		}
	}
	return nil
}
