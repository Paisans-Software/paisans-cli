package hostprep

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// KindShell is the login user's own shell setup: the environment block in
// its ~/.bashrc and the ~/deployment link.
const KindShell = "shell"

// DeploymentLink is the name, in the login user's home, of the link to the
// deployment's root, /srv/paisans/<token>. The user holds one deployment,
// so the name is the same on every host.
const DeploymentLink = "deployment"

// bashrcBlock is the environment block site prepare keeps at the top of the
// login user's ~/.bashrc, between its markers.
//
// A shell on the host otherwise reaches the system's Docker socket, which
// the deployment's user cannot use, and not the user's own rootless daemon,
// which runs the deployment. So the block sets DOCKER_HOST at that daemon,
// at the address every command of the toolkit's gives it
// (apply.RootlessDockerHost), and XDG_RUNTIME_DIR first when the session has
// none, since a non-interactive ssh session does not always set it and
// systemctl --user (UserSystemctl) needs it as much as DOCKER_HOST does.
//
// It goes at the top because bash reads ~/.bashrc for a command sent over
// ssh as well as for an interactive shell, and the ~/.bashrc Debian and
// Ubuntu give a new user returns at once when the shell is not interactive:
// a block after that return would reach a person's shell and never `ssh
// host docker ps`. A login shell reads it too, through the ~/.profile that
// sources ~/.bashrc. Nothing in it prints, so a command's output and scp
// stay clean.
func bashrcBlock(d deployment.Deployment) string {
	begin, end := bashrcMarkers(d)
	return begin + "\n" +
		"# Kept by paisans site prepare, which replaces anything between these markers.\n" +
		`[ -n "$XDG_RUNTIME_DIR" ] || export XDG_RUNTIME_DIR="/run/user/$(id -u)"` + "\n" +
		`export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/docker.sock"` + "\n" +
		end + "\n"
}

// bashrcMarkers are the lines the block begins and ends with, named for the
// deployment.
func bashrcMarkers(d deployment.Deployment) (begin, end string) {
	return "# >>> " + d.Prefix() + " >>>", "# <<< " + d.Prefix() + " <<<"
}

// withBashrcBlock is have with the block as bashrcBlock writes it: an
// existing block replaced where it stands, or else the block put first.
func withBashrcBlock(d deployment.Deployment, have string) string {
	block := bashrcBlock(d)
	begin, end := bashrcMarkers(d)
	if i := strings.Index(have, begin+"\n"); i >= 0 && (i == 0 || have[i-1] == '\n') {
		if j := strings.Index(have[i:], end+"\n"); j >= 0 {
			return have[:i] + block + have[i+j+len(end)+1:]
		}
	}
	return block + have
}

// planBashrc keeps the environment block in the login user's ~/.bashrc,
// written as the user through a temporary file. Everything outside the
// markers is left as it is.
func planBashrc(t Transport, d deployment.Deployment, home string) (Section, error) {
	var out Section
	path := home + "/.bashrc"
	have, _, err := t.ReadFile(path)
	if err != nil {
		return out, err
	}
	want := withBashrcBlock(d, have)
	if want == have {
		out.Present = append(out.Present, "shell: "+path+" sets DOCKER_HOST at the deployment's Docker")
		return out, nil
	}
	tmp := path + ".paisans-tmp"
	command := fmt.Sprintf("set -e; umask 022; printf %%s %s | base64 -d > %s; mv -f %s %s",
		shellQuote(base64.StdEncoding.EncodeToString([]byte(want))), shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	out.Steps = append(out.Steps, Step{Kinds: []string{KindShell}, Title: "set DOCKER_HOST in .bashrc",
		Describe: "shell: keep the block at the top of " + path + " that sets DOCKER_HOST at the deployment's rootless Docker, so a shell or an ssh command on the host reaches it",
		Command:  command})
	return out, nil
}

// linkProbe reports what is at path: `link <target>` for a symbolic link,
// `other` for anything else, and nothing when nothing is there.
func linkProbe(path string) string {
	p := shellQuote(path)
	return fmt.Sprintf(`if [ -L %s ]; then printf 'link %%s\n' "$(readlink %s)"; elif [ -e %s ]; then echo other; fi`, p, p, p)
}

// planDeploymentLink keeps ~/deployment a link to the deployment's root, so
// the stacks are one cd from the user's home. A link elsewhere is pointed
// back; a file or directory of that name is not paisans's and is left,
// with a warning.
func planDeploymentLink(t Transport, d deployment.Deployment, home string) (Section, error) {
	var out Section
	path, target := home+"/"+DeploymentLink, d.Root()
	got, err := t.Run(linkProbe(path))
	if err != nil {
		return out, fmt.Errorf("reading %s on %s: %w", path, t.Describe(), err)
	}
	got = strings.TrimSpace(got)
	switch {
	case got == "link "+target:
		out.Present = append(out.Present, "shell: "+path+" links to "+target)
		return out, nil
	case got == "other":
		out.Foreign = append(out.Foreign, path)
		out.Warnings = append(out.Warnings, Warning{
			Hint:   "shell: " + path + " is not a link, so site prepare leaves it",
			Detail: "site prepare links " + path + " to " + target + ", and replaces only a link. Move what is there aside and prepare again to have the link."})
		return out, nil
	case got != "" && !strings.HasPrefix(got, "link "):
		return out, fmt.Errorf("reading %s on %s: unexpected answer %q", path, t.Describe(), got)
	}
	out.Steps = append(out.Steps, Step{Kinds: []string{KindShell}, Title: "link ~/" + DeploymentLink,
		Describe: "shell: link " + path + " to " + target + ", where the deployment's stacks are",
		// The check is made again as the link is made, so a directory that
		// appeared since the probe is never linked into.
		Command: fmt.Sprintf("if [ -L %[1]s ] || [ ! -e %[1]s ]; then ln -sfn %[2]s %[1]s; else echo '%[3]s is not a link; left as it is' >&2; exit 1; fi",
			shellQuote(path), shellQuote(target), path)})
	return out, nil
}
