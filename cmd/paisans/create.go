package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/create"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/ui"
	"github.com/paisans-software/paisans-cli/internal/validate"
)

// runCreate asks the questions behind a new deployment and writes its
// paisans-stack.yaml.
//
// It writes one local file and nothing else: no secret, since init is the one
// command responsible for the secrets file, and no host, since the sites may
// not exist yet. The file is written once validate refuses nothing in it, and
// is not printed: the operator reads it on disk, where it stays, rather than
// scrolling back through a terminal. One validate refuses is written only when the operator asks for it as
// it stands, so that their answers are not lost; validate and apply go on
// refusing it until it is fixed.
func runCreate(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("create", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to write the new deployment declaration to")
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
	w.DefaultUser = deployment.Deployment{ID: id}.Prefix()
	if home, err := os.UserHomeDir(); err == nil {
		w.LocalKeys = create.FindLocalKeys(os.DirFS(filepath.Join(home, ".ssh")), "~/.ssh")
	}
	p.Say("paisans create writes %s for a new deployment. Enter takes the answer in brackets; nothing is written until validate refuses nothing in the file, or you ask for it as it stands.", ui.ShortPath(path))
	section := create.SectionAll
	for {
		if err := w.Ask(section); err != nil {
			return createAborted(err)
		}
		data, err := create.Render(w.Answers, id, by)
		if err != nil {
			return err
		}
		result, err := checkRendered(data)
		if err != nil {
			return err
		}

		p.Say("")
		reportValidation(r, path, result)
		if len(result.Findings) == 0 {
			r.Result("%s: no problems found", ui.ShortPath(path))
		}
		if result.Refused() {
			next, err := againOrQuit(p, result)
			if err != nil {
				return createAborted(err)
			}
			switch next {
			case quitCreate:
				return notWritten(r, p, path)
			case writeRefused:
				if err := writeNew(path, data); err != nil {
					return err
				}
				p.Say("")
				r.Result("Wrote %s, which validate refuses. Fix what it names, then run paisans validate before paisans init.", path)
				return nil
			}
			section = next
			continue
		}
		if err := writeNew(path, data); err != nil {
			return err
		}
		r.Result("Wrote %s.", path)
		p.Say("\nNext:")
		// init creates secrets.enc.yaml, which secrets set writes into, and
		// writes the id and subnet a site request carries, so it comes first.
		// It skips a site not admitted yet, which site admit checks as root.
		// With no key, at a terminal, secrets set asks for each secret only
		// the operator can give, whichever the file declares.
		p.Say("  paisans init          # checks the subnet, creates secrets.enc.yaml, generates every secret")
		p.Say("  paisans site request  # per site: --site <name> > <name>.admit, for its owner's sudo paisans site admit")
		p.Say("  paisans secrets set   # asks for each secret only you can give, such as the SMTP password")
		p.Say("  paisans apply         # shows every step; --execute runs them")
		return nil
	}
}

// quitCreate and writeRefused are againOrQuit's answers that are not a
// section to ask again.
const (
	quitCreate   create.Section = -1
	writeRefused create.Section = -2
)

// againOrQuit asks what to do after a refusal: answer again the section the
// first refusal's key comes from, or every question; write the file as it
// stands (writeRefused); or write nothing (quitCreate).
func againOrQuit(p *create.Prompter, result validate.Result) (create.Section, error) {
	first := create.SectionFor(result.Refusals()[0].Key)
	options := []string{"answer " + first.String() + " again"}
	sections := []create.Section{first}
	if first != create.SectionAll {
		options = append(options, "answer every question again")
		sections = append(sections, create.SectionAll)
	}
	options = append(options, "write the file as it stands, to fix by hand (validate and apply refuse it until then)", "quit without writing anything")
	sections = append(sections, writeRefused, quitCreate)
	i, err := p.Choose("Validate refused the file. What next?", options, 0)
	if err != nil {
		return 0, err
	}
	return sections[i], nil
}

// checkRendered is validate's verdict on the file create rendered. Each
// answer is checked as it is typed, so a file the loader refuses is a check
// the questions lack; its problems are shown as refusals all the same, so the
// operator can answer again or write the file to fix by hand.
func checkRendered(data []byte) (validate.Result, error) {
	cfg, err := config.LoadBytes(data)
	var loadErr *config.LoadError
	if errors.As(err, &loadErr) {
		var result validate.Result
		for _, problem := range loadErr.Problems {
			key, message := problem, problem
			if i := strings.IndexAny(problem, ": "); i > 0 {
				key, message = problem[:i], strings.TrimLeft(problem[i:], ": ")
			}
			result.Findings = append(result.Findings, validate.Finding{
				Level: validate.Refuse, Rule: "config-load", Key: key,
				Hint: "the file does not load", Message: message,
			})
		}
		return result, nil
	}
	if err != nil {
		return validate.Result{}, fmt.Errorf("the file create rendered does not load, which is a bug in create: %w", err)
	}
	return validate.Check(cfg), nil
}

// refuseExisting refuses a path that already holds a file. create writes new
// deployments only; an existing paisans-stack.yaml is edited, not recreated.
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

// notWritten reports an operator's choice, after a refusal, to leave the file
// unwritten. It is not a failure, so it is a result rather than a problem.
func notWritten(r ui.Reporter, p *create.Prompter, path string) error {
	p.Say("")
	r.Result("%s not written. Run paisans create again to start over.", ui.ShortPath(path))
	return nil
}

func nothingWritten(why string) error {
	return &ui.Problem{Hint: "nothing written: " + why, Explain: "Run paisans create again to start over."}
}
