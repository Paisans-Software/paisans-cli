package apply

import (
	"fmt"
	"sort"
	"strings"
)

// RootlessDeleteLoop is a shell loop that deletes every entry of dir but the
// names in keep. The three globs together match every name but . and ..,
// dotfiles included, and a glob that matched nothing is skipped by the
// existence test, which a dangling symbolic link also passes.
//
// Each entry goes through rootlesskit, which runs a command as root of a new
// user namespace that maps the user's subordinate range, the namespace
// rootless Docker runs containers in (Docker's rootless documentation,
// Uninstall: rootlesskit rm -rf ~/.local/share/docker). A file a container's
// user wrote, owned by a subordinate UID, is the user's to remove there, and
// a plain rm run as the user is refused it.
//
// The loop does not stop on a failed delete by itself; run it after set -e,
// as RootlessEmptyDir does, so a refused entry fails the command.
func RootlessDeleteLoop(dir string, keep []string) string {
	q := shellQuote(dir)
	head := fmt.Sprintf(`for f in %[1]s/* %[1]s/.[!.]* %[1]s/..?*; do [ -e "$f" ] || [ -L "$f" ] || continue;`, q)
	if len(keep) == 0 {
		return head + ` rootlesskit rm -rf -- "$f"; done`
	}
	names := make([]string, 0, len(keep))
	for _, n := range keep {
		names = append(names, shellQuote(n))
	}
	sort.Strings(names)
	return head + fmt.Sprintf(` case "${f##*/}" in %s) ;; *) rootlesskit rm -rf -- "$f";; esac; done`, strings.Join(names, "|"))
}

// RootlessEmptyDir deletes everything in dir and keeps dir itself, so a
// container that mounts it finds it where it was, with the owner it had. A
// dir that is not there is nothing to empty. A dir that is a symbolic link
// is refused before anything is deleted, since the loop would follow it out
// of the deployment.
func RootlessEmptyDir(dir string) string {
	q := shellQuote(dir)
	return fmt.Sprintf(`set -e; if [ -L %s ]; then echo %s; exit 3; fi; %s`,
		q, shellQuote(dir+" is a symbolic link, so nothing in it was deleted"), RootlessDeleteLoop(dir, nil))
}
