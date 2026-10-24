package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/configstore"
	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/draft"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// What follows finds a deployment's system copy, the one directory per
// deployment id under the data directory that holds the only trusted state
// on this workstation, and starts drafts from it
// (docs/specs/2026-10-24-system-copy.md, *The system copy* and *Drafts*). A
// folder holds a draft, the three files, and nothing a trust decision reads.

// draftAt is the draft whose paisans-stack.yaml is at configPath. The three
// files of a version have fixed names, so the configuration must be named
// paisans-stack.yaml for the draft to be the files a version holds. Dir is
// absolute, so every message naming it says where the files are.
func draftAt(configPath string) (draft.Draft, error) {
	if filepath.Base(configPath) != config.FileName {
		return draft.Draft{}, &ui.Problem{Hint: "--config must name a file called " + config.FileName, Explain: "A stored version holds " + config.FileName + ", secrets.enc.yaml and .sops.yaml by those names, so the files beside --config must carry them. Got " + configPath + "."}
	}
	dir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return draft.Draft{}, &ui.Problem{Hint: "the directory of --config cannot be resolved", Explain: errLine(err) + ".", Cause: err}
	}
	return draft.Draft{Dir: dir}, nil
}

// recoverHint is how a deployment with no system copy on this workstation
// gets one: from the sites, by a recovery or, invited to it, by the join.
func recoverHint(id string) string {
	if id == "" {
		id = "<id>"
	}
	return "Recover it from the sites: " + progname.Cmd() + " deployment recover --from <user@host> --id " + id + " --root <sha> --config <empty directory>/" + config.FileName + " --execute. Invited to it, join it with " + progname.Cmd() + " operator register --execute in an empty directory."
}

// systemFor finds the deployment of the draft at configPath (its id), or,
// with no draft there, the one id names, or the only system copy on the
// workstation. It refuses a missing system copy, and creates nothing: a
// system copy comes only from a recovery, a join or a deploy.
func systemFor(configPath, id string) (sys configstore.Local, d draft.Draft, deploymentID string, err error) {
	d, err = draftAt(configPath)
	if err != nil {
		return sys, d, "", err
	}
	st, err := openStore()
	if err != nil {
		return sys, d, "", err
	}
	path := filepath.Join(d.Dir, config.FileName)
	switch {
	case d.Has():
		cfg, err := config.Load(path)
		if err != nil {
			return sys, d, "", &ui.Problem{Hint: "the draft at " + path + " does not load", Explain: errLine(err) + ". Its id names the deployment it is a draft of.", Cause: err}
		}
		if cfg.ID == "" {
			return sys, d, "", &ui.Problem{Hint: "the draft at " + path + " names no deployment", Explain: "Its id names the deployment it is a draft of, and it has none."}
		}
		if id != "" && id != cfg.ID {
			return sys, d, "", &ui.Problem{Hint: "--id names another deployment than the draft", Explain: fmt.Sprintf("The draft at %s is of deployment %s; --id names %s. Leave --id out, or use a folder with no draft.", path, cfg.ID, id)}
		}
		deploymentID = cfg.ID
	case id != "":
		if !deployment.ValidID(id) {
			return sys, d, "", &ui.Problem{Hint: fmt.Sprintf("%q is not a deployment id", id), Explain: "--id takes the whole id, as " + progname.Cmd() + " deployment list prints it for each deployment on this workstation."}
		}
		deploymentID = id
	default:
		listed, err := st.List()
		if err != nil {
			return sys, d, "", err
		}
		switch len(listed) {
		case 0:
			return sys, d, "", &ui.Problem{Hint: "this workstation holds no deployment", Explain: "There is no draft in " + d.Dir + " and no system copy to start one from. " + recoverHint("")}
		case 1:
			deploymentID = listed[0].ID
		default:
			var ids []string
			for _, l := range listed {
				ids = append(ids, l.ID)
			}
			return sys, d, "", &ui.Problem{Hint: "this workstation holds more than one deployment", Explain: "There is no draft in " + d.Dir + " to say which. Name one with --id: " + strings.Join(ids, ", ") + "."}
		}
	}
	sys, ok, err := st.Held(deploymentID)
	if err != nil {
		return sys, d, "", err
	}
	if !ok {
		return sys, d, "", &ui.Problem{Hint: "this workstation holds no system copy of " + deploymentID, Explain: "Trust in a deployment's versions starts only from what a recovery, a join or a deploy recorded here. " + recoverHint(deploymentID)}
	}
	return sys, d, deploymentID, nil
}

// startDraft writes the system copy's head files into d with the marker
// naming that head. A system copy with no head has nothing to draft from.
// The head is taken as trusted without judging it again: a system copy's
// config-head is written only after a trust decision, by a deploy of a
// version this workstation signed, a recovery or join that judged it, or a
// fetch that checks out a verified head; a version a fetch sets aside never
// becomes the head.
func startDraft(sys configstore.Local, d draft.Draft) error {
	m, f, err := systemHead(sys)
	if err != nil {
		return err
	}
	return d.Write(f, m)
}

// systemHead is the version the system copy's files are: its head, by
// revision and hash, and the head's files as its store holds them.
func systemHead(sys configstore.Local) (draft.Marker, configstore.Files, error) {
	id := filepath.Base(sys.Dir)
	none := &ui.Problem{Hint: "the system copy of " + id + " holds no version", Explain: "There is nothing in " + sys.Dir + " to start a draft from. " + recoverHint(id)}
	head, err := sys.Head()
	if err != nil {
		return draft.Marker{}, configstore.Files{}, err
	}
	if head == "" {
		return draft.Marker{}, configstore.Files{}, none
	}
	cache, ok, err := sys.HeldStore()
	if err != nil {
		return draft.Marker{}, configstore.Files{}, err
	}
	if !ok {
		return draft.Marker{}, configstore.Files{}, none
	}
	return headOf(cache, id, head)
}

// headOf is version sha of cache as a draft starts from it: the marker
// naming it and its files.
func headOf(cache configstore.Repo, id, sha string) (draft.Marker, configstore.Files, error) {
	f, err := cache.Files(sha)
	if err != nil {
		return draft.Marker{}, configstore.Files{}, err
	}
	rev := cache.Revision(sha)
	if rev < 1 {
		return draft.Marker{}, configstore.Files{}, &ui.Problem{Hint: "version " + shortSHA(sha) + " of " + id + " has no revision", Explain: "Its Paisans-Revision trailer is not one positive number, so it is no version a draft can start from. " + recoverHint(id)}
	}
	return draft.Marker{Revision: rev, SHA: sha}, f, nil
}

// draftFiles is each of the three files the folder of d holds.
func draftFiles(d draft.Draft) []string {
	var held []string
	for _, name := range []string{config.FileName, configstore.FileSecrets, configstore.FileSOPS} {
		if _, err := os.Lstat(filepath.Join(d.Dir, name)); err == nil {
			held = append(held, filepath.Join(d.Dir, name))
		}
	}
	return held
}

// ensureDraft is startDraft when the folder of configPath has no draft, and
// otherwise nothing; every command that writes the three files calls it
// first, so its change is made to a draft.
func ensureDraft(configPath, id string) (draft.Draft, error) {
	d, err := draftAt(configPath)
	if err != nil || d.Has() {
		return d, err
	}
	if held := draftFiles(d); len(held) > 0 {
		return d, &ui.Problem{Hint: "there is part of a draft in " + d.Dir, Explain: "It holds " + strings.Join(held, ", ") + " and no " + config.FileName + ", and starting a draft would replace them. Move them away, or use another folder."}
	}
	sys, d, _, err := systemFor(configPath, id)
	if err != nil {
		return d, err
	}
	return d, startDraft(sys, d)
}
