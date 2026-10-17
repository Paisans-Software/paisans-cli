package create

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func prompter(input string) (*Prompter, *strings.Builder) {
	var out strings.Builder
	return NewPrompter(strings.NewReader(input), &out), &out
}

func TestTextTakesTheDefaultOnEnter(t *testing.T) {
	p, out := prompter("\n")
	got, err := p.Text("Domain", "example.org", nil)
	if err != nil || got != "example.org" {
		t.Fatalf("Text = %q, %v; want the default", got, err)
	}
	if !strings.Contains(out.String(), "Domain [example.org]: ") {
		t.Errorf("the prompt does not show its default:\n%s", out)
	}
}

func TestTextAsksAgainUntilAnswered(t *testing.T) {
	p, out := prompter("\n  \nExample\n")
	got, err := p.Text("Name", "", nil)
	if err != nil || got != "Example" {
		t.Fatalf("Text = %q, %v", got, err)
	}
	if n := strings.Count(out.String(), "an answer is required"); n != 2 {
		t.Errorf("said an answer is required %d times, want 2:\n%s", n, out)
	}
}

func TestTextAsksAgainWithTheCheck(t *testing.T) {
	p, out := prompter("bad\ngood\n")
	got, err := p.Text("Word", "", func(s string) error {
		if s == "bad" {
			return errors.New("not that one")
		}
		return nil
	})
	if err != nil || got != "good" {
		t.Fatalf("Text = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "not that one") {
		t.Errorf("the check's reason is not shown:\n%s", out)
	}
}

func TestOptionalAcceptsBlank(t *testing.T) {
	p, _ := prompter("\n")
	got, err := p.Optional("Public address", "", nil)
	if err != nil || got != "" {
		t.Fatalf("Optional = %q, %v; want blank", got, err)
	}
}

func TestOptionalChecksWhatIsGiven(t *testing.T) {
	p, _ := prompter("x\n\n")
	got, err := p.Optional("Address", "", func(s string) error {
		if s == "x" {
			return errors.New("no")
		}
		return nil
	})
	if err != nil || got != "" {
		t.Fatalf("Optional = %q, %v", got, err)
	}
}

func TestYesNo(t *testing.T) {
	for _, c := range []struct {
		input string
		def   bool
		want  bool
	}{
		{"\n", true, true},
		{"\n", false, false},
		{"y\n", false, true},
		{"YES\n", false, true},
		{"n\n", true, false},
		{"maybe\nno\n", true, false},
	} {
		p, _ := prompter(c.input)
		got, err := p.YesNo("Ok?", c.def)
		if err != nil || got != c.want {
			t.Errorf("YesNo(%q, default %v) = %v, %v; want %v", c.input, c.def, got, err, c.want)
		}
	}
}

func TestYesNoShowsItsDefault(t *testing.T) {
	p, out := prompter("\n\n")
	p.YesNo("A", true)
	p.YesNo("B", false)
	if !strings.Contains(out.String(), "A [Y/n]: ") || !strings.Contains(out.String(), "B [y/N]: ") {
		t.Errorf("defaults not shown:\n%s", out)
	}
}

func TestChooseNumbersItsOptions(t *testing.T) {
	p, out := prompter("0\n4\nx\n2\n")
	got, err := p.Choose("Shape", []string{"one", "two", "three"}, 0)
	if err != nil || got != 1 {
		t.Fatalf("Choose = %d, %v; want 1", got, err)
	}
	for _, want := range []string{"  1) one\n", "  2) two\n", "  3) three\n", "Choose [1]: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out.String(), "a number from 1 to 3"); n != 3 {
		t.Errorf("refused %d answers, want 3:\n%s", n, out)
	}
}

func TestChooseAsksItsQuestionAsWritten(t *testing.T) {
	p, out := prompter("\n")
	if _, err := p.Choose("Where should it run?", []string{"here", "there"}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Where should it run?\n") {
		t.Errorf("the question is not asked as written:\n%s", out)
	}
}

// width fixes how wide the prompter's terminal is, as a terminal would.
func width(p *Prompter, cols int) { p.width = func() int { return cols } }

func TestSayWrapsAtTheTerminalsWidth(t *testing.T) {
	p, out := prompter("")
	width(p, 30)
	p.Say("\nThe health page reports the other machines failing, so it is best on a machine of its own.")
	want := "\n" +
		"The health page reports the\n" +
		"other machines failing, so it\n" +
		"is best on a machine of its\n" +
		"own.\n"
	if out.String() != want {
		t.Errorf("Say wrote\n%q\nwant\n%q", out, want)
	}
}

func TestSayUsesTheWholeWidth(t *testing.T) {
	line := strings.Repeat("word ", 30)
	p, out := prompter("")
	width(p, 200)
	p.Say("%s", line)
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Errorf("a %d character line on a 200 column terminal took %d lines:\n%s", len(line), got, out)
	}
}

func TestSayWrapsNothingOffATerminal(t *testing.T) {
	line := strings.Repeat("word ", 40)
	p, out := prompter("")
	p.Say("%s", line)
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Errorf("Say broke a line for output that is not a terminal:\n%s", out)
	}
}

func TestChooseOptionsWrapUnderTheirText(t *testing.T) {
	p, out := prompter("\n")
	width(p, 42)
	if _, err := p.Choose("Where should it run?", []string{
		"on a machine of its own (recommended)",
		"on gateway (takes the monitor down with it, no alerts when the gateway fails)",
	}, 0); err != nil {
		t.Fatal(err)
	}
	want := "Where should it run?\n" +
		"  1) on a machine of its own (recommended)\n" +
		"  2) on gateway (takes the monitor down\n" +
		"     with it, no alerts when the gateway\n" +
		"     fails)\n" +
		"Choose [1]: "
	if out.String() != want {
		t.Errorf("Choose wrote\n%s\nwant\n%s", out, want)
	}
}

func TestChooseTakesTheDefault(t *testing.T) {
	p, _ := prompter("\n")
	if got, err := p.Choose("Shape", []string{"one", "two"}, 1); err != nil || got != 1 {
		t.Fatalf("Choose = %d, %v; want the default 1", got, err)
	}
}

func TestLinesEndsAtABlankLine(t *testing.T) {
	p, _ := prompter("ssh-ed25519 AAAA a@x\n  ssh-ed25519 BBBB b@x  \n\nnext\n")
	got, err := p.Lines("Keys", nil, nil)
	if err != nil || !slices.Equal(got, []string{"ssh-ed25519 AAAA a@x", "ssh-ed25519 BBBB b@x"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if rest, _ := p.Text("Next", "", nil); rest != "next" {
		t.Errorf("Lines read past its blank line: next answer %q", rest)
	}
}

func TestLinesKeepsTheDefaultOnABlankFirstLine(t *testing.T) {
	p, out := prompter("\n")
	got, err := p.Lines("Keys", []string{"ssh-ed25519 AAAA a@x"}, nil)
	if err != nil || !slices.Equal(got, []string{"ssh-ed25519 AAAA a@x"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "keeps the 1 given") {
		t.Errorf("the prompt does not say a blank line keeps the default:\n%s", out)
	}
}

func TestLinesRequiresOne(t *testing.T) {
	p, out := prompter("\nssh-ed25519 AAAA a@x\n\n")
	got, err := p.Lines("Keys", nil, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "at least one line is required") {
		t.Errorf("an empty paste was not refused:\n%s", out)
	}
}

func TestLinesChecksEachLine(t *testing.T) {
	p, out := prompter("nonsense\nssh-ed25519 AAAA a@x\n\n")
	got, err := p.Lines("Keys", nil, func(_ []string, s string) error {
		if s == "nonsense" {
			return errors.New("not a public key")
		}
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"ssh-ed25519 AAAA a@x"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "not a public key") {
		t.Errorf("a bad line was not refused:\n%s", out)
	}
}

func TestLinesChecksALineAgainstThoseBefore(t *testing.T) {
	p, out := prompter("ssh-ed25519 AAAA a@x\nssh-ed25519 AAAA a@x\n\n")
	got, err := p.Lines("Keys", nil, func(got []string, s string) error {
		if slices.Contains(got, s) {
			return errors.New("listed above")
		}
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"ssh-ed25519 AAAA a@x"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "listed above") {
		t.Errorf("the repeated line was not refused:\n%s", out)
	}
}

// A default the check now refuses is not kept: the lines are asked again.
func TestLinesChecksTheDefault(t *testing.T) {
	p, out := prompter("\nssh-ed25519 BBBB b@x\n\n")
	got, err := p.Lines("Keys", []string{"ssh-ed25519 AAAA a@x"}, func(_ []string, s string) error {
		if strings.Contains(s, "AAAA") {
			return errors.New("taken")
		}
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"ssh-ed25519 BBBB b@x"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "the 1 given no longer do: taken") {
		t.Errorf("the refused default was not said:\n%s", out)
	}
}

func TestEndOfInputAborts(t *testing.T) {
	p, _ := prompter("")
	if _, err := p.Text("Name", "x", nil); !errors.Is(err, ErrAborted) {
		t.Errorf("Text at end of input = %v, want ErrAborted", err)
	}
	p, _ = prompter("partial")
	if got, err := p.Text("Name", "", nil); err != nil || got != "partial" {
		t.Errorf("a last line with no newline = %q, %v; want it read", got, err)
	}
	p, _ = prompter("ssh-ed25519 AAAA a@x\n")
	if _, err := p.Lines("Keys", nil, nil); !errors.Is(err, ErrAborted) {
		t.Errorf("Lines at end of input = %v, want ErrAborted", err)
	}
}
