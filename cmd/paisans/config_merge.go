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
// the secrets file differs in on both sides: paisans-stack.yaml and .sops.yaml are
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
		return true, secretsMergeProblem("the base version " + base[:12])
	}
	theirSecrets, err := config.DecryptSecretsBytes(theirs.Secrets, id)
	if err != nil {
		return true, secretsMergeProblem("the head " + head[:12])
	}
	ours, err := config.LoadDeploymentSecrets(filepath.Join(l.Dir, "secrets.enc.yaml"), id)
	if err != nil {
		return true, secretsMergeProblem("the local secrets.enc.yaml")
	}
	merged, conflicts, err := configstore.MergeSecrets(baseSecrets, ours, theirSecrets)
	if err != nil {
		return true, &ui.Problem{Hint: "the secrets cannot be merged", Explain: err.Error() + ". Nothing was written."}
	}
	if len(conflicts) > 0 {
		for _, p := range conflicts {
			fmt.Fprintln(stdout, "secrets: "+p+" was changed differently on both sides")
		}
		return true, &ui.Problem{
			Hint:    "the local secrets and the sites' newest version change the same key differently",
			Explain: fmt.Sprintf("Both changed %s since %s. Nothing was written. %s", strings.Join(conflicts, ", "), base[:12], secretsConflictAdvice(conflicts)),
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
	if len(recipients) == 0 {
		return true, &ui.Problem{Hint: "the head's .sops.yaml names no age recipient", Explain: "The merged secrets would be written unencrypted. Nothing was written."}
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
	if err := recordPull(l, g, root); err != nil {
		return true, err
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

// secretsMergeProblem refuses without the underlying error: a parse error can
// quote a fragment of the document, which is a value.
func secretsMergeProblem(what string) error {
	return &ui.Problem{Hint: "cannot read " + what + " to merge the secrets", Explain: "It could not be decrypted and parsed on this workstation, and the secrets are merged key by key only with both sides readable. The reason is left out because it can quote a value."}
}

// secretsConflictAdvice says what settles conflicting keys, and that nothing
// happens by itself. The pull refuses while any key conflicts, so the head's
// value is taken by setting the key locally to it and pulling again; to keep
// the local value, take the head's first, so the pull can merge the rest, then
// set the key back and publish. paisans secrets set does the setting for a key
// it accepts; a pocket_id_groups key, which apply records, is not one.
func secretsConflictAdvice(conflicts []string) string {
	var settable, recorded bool
	for _, p := range conflicts {
		if strings.HasPrefix(p, "pocket_id_groups.") {
			recorded = true
		} else {
			settable = true
		}
	}
	out := []string{"The pull refuses while a key conflicts and takes neither side's value by itself.",
		"To take the head's value, set that key locally to the head's value and pull again.",
		"To keep the local value, take the head's first so the pull can merge the rest, pull, then set the key back to your value and publish."}
	if settable {
		out = append(out, "paisans secrets set sets a key it accepts.")
	}
	if recorded {
		out = append(out, "A pocket_id_groups key is recorded by apply, and paisans secrets set does not accept it; edit the file with sops.")
	}
	return strings.Join(out, " ")
}
