package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/configstore"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// secretsOnlyEdits reports whether a pull over local edits is one that only
// the secrets file differs in on both sides: paisans.yaml and .sops.yaml are
// each unchanged from the base or already the head's, so the head's copies are
// the right ones to keep. When it is not, the pull refuses, as it does for any
// edit it cannot reconcile.
func secretsOnlyEdits(local, base, head configstore.Files) bool {
	return (bytesEqual(local.Paisans, base.Paisans) || bytesEqual(local.Paisans, head.Paisans)) &&
		(bytesEqual(local.SOPS, base.SOPS) || bytesEqual(local.SOPS, head.SOPS))
}

// pullMergingSecrets handles a pull whose local files and head both differ
// from the base in the secrets file alone. A text merge of an encrypted file
// is meaningless, so the base, the local file and the head are decrypted in
// memory and compared key by key (configstore.MergeSecrets). Without
// conflicts the merged file is written, encrypted to the head's recipients,
// beside the head's other two files, and the head becomes the base, leaving
// the merge as a local edit to publish. With conflicts nothing is written and
// the refusal names the keys, never a value.
//
// handled is false when this is not that case, and the caller refuses as it
// always has.
func pullMergingSecrets(r ui.Reporter, l configstore.Local, g configstore.Gathered, id, head, base, root string, configured int, local, theirs configstore.Files, execute bool, stdin io.Reader, stdout io.Writer) (handled bool, err error) {
	if base == "" {
		return false, nil
	}
	cache, err := l.Cache()
	if err != nil {
		return false, nil
	}
	baseFiles, err := cache.Files(base)
	if err != nil || !secretsOnlyEdits(local, baseFiles, theirs) {
		return false, nil
	}
	baseSecrets, err := config.DecryptSecretsBytes(baseFiles.Secrets, id)
	if err != nil {
		return true, secretsMergeProblem("the base version's secrets", err)
	}
	theirSecrets, err := config.DecryptSecretsBytes(theirs.Secrets, id)
	if err != nil {
		return true, secretsMergeProblem("the head's secrets", err)
	}
	ours, err := config.LoadDeploymentSecrets(filepath.Join(l.Dir, "secrets.enc.yaml"), id)
	if err != nil {
		return true, err
	}
	merged, conflicts, err := configstore.MergeSecrets(baseSecrets, ours, theirSecrets)
	if err != nil {
		return true, err
	}
	if len(conflicts) > 0 {
		for _, p := range conflicts {
			fmt.Fprintln(stdout, "secrets: "+p+" was changed differently on both sides")
		}
		return true, &ui.Problem{
			Hint:    "the local secrets and the sites' newest version change the same key differently",
			Explain: fmt.Sprintf("Both changed %s since %s. Nothing was written. Take the head's value (move the local secrets.enc.yaml aside and pull again), then set the key again with paisans secrets set; a pull merges the rest.", strings.Join(conflicts, ", "), base[:12]),
		}
	}
	for _, p := range changedPaths(baseSecrets, ours, theirSecrets) {
		fmt.Fprintln(stdout, "secrets: "+p+" merges")
	}
	if !execute {
		r.Step("merge the local secrets into "+describe(g, head)).End(ui.Pending, "--execute writes them")
		if !g.Majority {
			r.Warn(fmt.Sprintf("only %d of %d sites answered directly", len(g.Direct()), configured), "--execute asks for the word "+partialWord+" at a terminal.")
		}
		return true, nil
	}
	if err := confirmPartial(stdin, stdout, g, configured); err != nil {
		return true, err
	}
	// The recipients are the ones the head's .sops.yaml names, read from a
	// scratch copy so that nothing in the working directory changes until the
	// merged file is encrypted.
	scratch, err := os.MkdirTemp("", "paisans-merge-")
	if err != nil {
		return true, err
	}
	defer os.RemoveAll(scratch)
	if err := os.WriteFile(filepath.Join(scratch, config.SOPSConfigName), theirs.SOPS, 0o600); err != nil {
		return true, err
	}
	recipients, err := config.Recipients(scratch)
	if err != nil {
		return true, err
	}
	out := filepath.Join(scratch, "secrets.enc.yaml")
	if err := config.WriteSecrets(out, merged, recipients); err != nil {
		return true, err
	}
	encrypted, err := os.ReadFile(out)
	if err != nil {
		return true, err
	}
	if err := l.WriteFiles(configstore.Files{Paisans: theirs.Paisans, Secrets: encrypted, SOPS: theirs.SOPS}); err != nil {
		return true, err
	}
	if err := l.SetHead(head); err != nil {
		return true, err
	}
	if root != "" {
		if err := l.SetRoot(root); err != nil {
			return true, err
		}
	}
	r.Result("merged the local secrets into %s; paisans config publish makes the merge a version", describe(g, head))
	return true, nil
}

// changedPaths is every key either side changed from the base, sorted: what
// the merge combines, as paths only.
func changedPaths(base, ours, theirs *config.Secrets) []string {
	b := configstore.Flatten(base)
	seen := map[string]bool{}
	for _, side := range []map[string]string{configstore.Flatten(ours), configstore.Flatten(theirs)} {
		for p, v := range side {
			if bv, ok := b[p]; !ok || bv != v {
				seen[p] = true
			}
		}
		for p := range b {
			if _, ok := side[p]; !ok {
				seen[p] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func secretsMergeProblem(what string, err error) error {
	return &ui.Problem{Hint: "cannot read " + what + " to merge the secrets", Explain: "The secrets are merged key by key, which needs both sides decrypted on this workstation.", Cause: err}
}
