package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/create"
	"github.com/paisans-software/paisans-stack/internal/deployment"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// runCreate asks the questions behind a new deployment and writes its
// paisans.yaml.
//
// It writes one local file and nothing else: no secret, since init is the one
// command responsible for the secrets file, and no host, since the sites may
// not exist yet. The file is written only once validate refuses nothing in it
// and the operator has read it and said yes.
func runCreate(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("create", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", "paisans.yaml", "path to write the new deployment declaration to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	path := *configPath

	if err := refuseExisting(path); err != nil {
		return err
	}
	// The command is a conversation. A pipe of answers that drifts out of step
	// with the questions writes the wrong file without a word.
	if !stdinIsTerminal(stdin) {
		return &ui.Problem{
			Hint:    "create asks its questions at a terminal, and stdin is not one",
			Explain: "Run paisans create from a terminal. Nothing was written.",
		}
	}
	id, err := deployment.NewID()
	if err != nil {
		return err
	}

	by := create.Origin{Version: toolkitVersion(), Date: time.Now().Format("2006-01-02")}
	p := create.NewPrompter(stdin, stdout)
	w := create.NewWizard(p)
	p.Say("paisans create writes %s for a new deployment. Enter takes the answer", ui.ShortPath(path))
	p.Say("in brackets; nothing is written until you have read the file and said yes.")
	section := create.SectionAll
	for {
		if err := w.Ask(section); err != nil {
			return createAborted(err)
		}
		data, err := create.Render(w.Answers, id, by)
		if err != nil {
			return err
		}
		cfg, err := config.LoadBytes(data)
		if err != nil {
			return fmt.Errorf("the file create rendered does not load, which is a bug in create: %w", err)
		}
		result := validate.Check(cfg)

		fmt.Fprintf(stdout, "\n%s\n", data)
		reportValidation(r, path, result)
		if len(result.Findings) == 0 {
			r.Result("%s: no problems found", ui.ShortPath(path))
		}
		if result.Refused() {
			next, err := againOrQuit(p, result)
			if err != nil {
				return createAborted(err)
			}
			if next < 0 {
				return nothingWritten("you chose to quit")
			}
			section = next
			continue
		}
		write, err := p.YesNo(fmt.Sprintf("Write %s?", ui.ShortPath(path)), true)
		if err != nil {
			return createAborted(err)
		}
		if !write {
			return nothingWritten("you chose not to write it")
		}
		if err := writeNew(path, data); err != nil {
			return err
		}
		r.Result("Wrote %s.", path)
		p.Say("\nNext:")
		p.Say("  paisans secrets set external.smtp_password   # the SMTP password, read from stdin")
		p.Say("  paisans init                                 # checks the subnet, generates every secret")
		p.Say("  paisans apply                                # shows every step; --execute runs them")
		return nil
	}
}

// againOrQuit asks which questions to answer again after a refusal: the
// section the first refusal's key comes from, every question, or none. It
// returns -1 for none.
func againOrQuit(p *create.Prompter, result validate.Result) (create.Section, error) {
	first := create.SectionFor(result.Refusals()[0].Key)
	options := []string{"answer " + first.String() + " again"}
	sections := []create.Section{first}
	if first != create.SectionAll {
		options = append(options, "answer every question again")
		sections = append(sections, create.SectionAll)
	}
	options = append(options, "quit without writing anything")
	i, err := p.Choose("Validate refused the file", options, 0)
	if err != nil {
		return 0, err
	}
	if i == len(sections) {
		return -1, nil
	}
	return sections[i], nil
}

// refuseExisting refuses a path that already holds a file. create writes new
// deployments only; an existing paisans.yaml is edited, not recreated.
func refuseExisting(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return &ui.Problem{
			Hint:    fmt.Sprintf("%s already exists", ui.ShortPath(path)),
			Explain: "create writes a new deployment's file and never replaces one. Edit it instead, or name another path with --config. Nothing was written.",
		}
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return err
	}
	return nil
}

// writeNew writes data to path only if nothing is there, so a file that
// appeared while the questions were asked is refused rather than replaced.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, iofs.ErrExist) {
		return refuseExisting(path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func createAborted(err error) error {
	if errors.Is(err, create.ErrAborted) {
		return nothingWritten("input ended before every question was answered")
	}
	return err
}

func nothingWritten(why string) error {
	return &ui.Problem{Hint: "nothing written: " + why, Explain: "Run paisans create again to start over."}
}
