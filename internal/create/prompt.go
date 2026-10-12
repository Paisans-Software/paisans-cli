// Package create asks the questions behind `paisans create` and writes a new
// deployment's paisans.yaml from the answers.
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
}

// NewPrompter reads answers from in and writes questions to out.
func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Say writes a line of explanation between questions.
func (p *Prompter) Say(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
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
	fmt.Fprintf(p.out, "  %s\n", reason)
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

// Choose asks for one of options, listed by number from 1, and returns its
// index. def is the index Enter takes.
func (p *Prompter) Choose(q string, options []string, def int) (int, error) {
	fmt.Fprintf(p.out, "%s:\n", q)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, o)
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
// there is one. check judges each line as it comes; a line it refuses is
// dropped and said so, and the rest of the paste carries on.
func (p *Prompter) Lines(q string, def []string, check func(string) error) ([]string, error) {
	if len(def) > 0 {
		fmt.Fprintf(p.out, "%s, one per line, then a blank line (a blank line now keeps the %d given):\n", q, len(def))
	} else {
		fmt.Fprintf(p.out, "%s, one per line, then a blank line:\n", q)
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
				return def, nil
			}
			p.refuse("at least one line is required")
			continue
		}
		if check != nil {
			if err := check(s); err != nil {
				p.refuse(err.Error())
				continue
			}
		}
		got = append(got, s)
	}
}
