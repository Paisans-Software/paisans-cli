package hostadmit

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/registry"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// planClaim claims the host in its registry first, so a conflict found
// under the claim's lock stops admit with nothing changed. The entry is
// built from the request alone, so admit from a request and admit from the
// configuration claim alike. The read-only check here refuses it before
// the plan is shown: another site of this deployment on this machine, or a
// token, root, interface, port, subnet or exclusive role another
// deployment holds, the public ports among them.
func planClaim(p *planner) error {
	id, e := registry.ForRequest(p.in.Request, p.in.Now)
	r, err := registry.Read(p.t)
	if err != nil {
		return shown(err)
	}
	mine, claimed := r.Deployments[id]
	if claimed && registry.AnotherSite(mine, e) {
		return fmt.Errorf("this machine already runs site %s of this deployment, at %s, and this request is for site %s at %s. A site is a machine: its files, containers and WireGuard interface are named for the deployment, so a second site of it here would overwrite the first. Admit it on another machine. Nothing was changed",
			ui.Visible(mine.Site), ui.Visible(mine.Address), ui.Visible(e.Site), e.Address)
	}
	if c := registry.Conflicts(r, id, e); len(c) > 0 {
		return shown(registry.Refused(p.t, e, c[0]))
	}
	if claimed {
		mine.ClaimedAt = e.ClaimedAt
		if mine == e {
			p.present("registry: this host is claimed for deployment " + p.d.Token())
			return nil
		}
	}
	command, err := registry.ClaimCommand(id, e)
	if err != nil {
		return err
	}
	p.step(Step{Title: "claim the host",
		Describe: "registry: record deployment " + p.d.Token() + " in " + registry.Path + ", so no other deployment here takes its token, directory, interface, port, subnet or roles (" + e.Roles + ")",
		Command:  command, Kinds: []string{"registry"}})
	return nil
}

// planAdmissionDir settles /etc/paisans before any part writes into it
// (egress, the sysctls, the record): root's, mode exactly 755, so the
// deployment's user reads its record without sudo and none can write one.
// A directory made under a stricter umask, or another's, is mended here.
func planAdmissionDir(p *planner) error {
	dir := p.f.Dirs[admission.Dir]
	switch {
	case !dir.Present:
		p.step(Step{Title: "create " + admission.Dir, Describe: "record: root's, mode 755: every deployment's user reads its own record, none can write one",
			Command: "install -d -m 755 -o root -g root " + admission.Dir, Kinds: []string{"record"}})
	case len(dir.Mode) < 10 || dir.Mode[0] != 'd':
		return fmt.Errorf("%s is %q, not a directory: it must be root's, so no deployment can write another's record. Fix it and admit again. Nothing was changed", admission.Dir, ui.Visible(dir.Mode))
	case !readableByAll(dir):
		p.step(Step{Title: "make " + admission.Dir + " root's alone", Describe: fmt.Sprintf("record: it is %s, owner %d; made root's, mode 755", ui.Visible(dir.Mode), dir.UID),
			Command: "chown root:root " + admission.Dir + " && chmod 755 " + admission.Dir, Kinds: []string{"record"}})
	}
	return nil
}

// readableByAll reports whether a directory, as ls -ldn printed it, is a
// real directory owned root:root with mode exactly 755.
func readableByAll(d DirState) bool {
	return d.Present && !d.Unsafe && d.Mode == "drwxr-xr-x" && d.UID == 0 && d.GID == 0
}

// planRecord writes the admission record, root's and mode 0644 so the
// deployment's own user reads it without sudo, through the transport's
// write, which writes a temporary file beside it and renames it. A record
// that says the same but for its time is left with its time; one found
// with another mode or owner is rewritten.
func planRecord(p *planner) error {
	path, err := admission.Path(p.d.Token())
	if err != nil {
		return err
	}
	f := p.f.Files[path]
	if f.Link {
		return fmt.Errorf("%s is a symbolic link. Remove it and admit again. Nothing was changed", path)
	}
	prev, err := p.previous()
	if err != nil {
		return err
	}
	if prev != nil && f.Mode == "644" && f.UID == 0 {
		same := *prev
		same.AdmittedAt = p.record.AdmittedAt
		if reflect.DeepEqual(same, p.record) {
			p.present("record: " + path + " as admitted at " + ui.Visible(prev.AdmittedAt))
			return nil
		}
	}
	content, err := admission.EncodeRecord(p.record)
	if err != nil {
		return err
	}
	describe := "record: who the deployment runs as and what you granted, which its own commands read"
	if f.Present && (f.Mode != "644" || f.UID != 0) {
		describe += fmt.Sprintf("; found mode %s, owner %d, restored to root's and 0644", ui.Visible(f.Mode), f.UID)
	}
	p.step(Step{Title: "write the admission record", Describe: describe,
		File: &hostprep.File{Path: path, Content: string(content), Mode: admission.Mode}, Kinds: []string{"record"}})
	return nil
}

// keysMarker separates, in what keysProbe prints, the user's
// authorized_keys from the sidecar.
const keysMarker = "paisans-sidecar"

// keysProbe reads, as the user, its authorized_keys and the sidecar admit
// and host prepare keep of the keys they appended. awk 1 ends each file's
// last line, so the marker is always a line of its own.
func keysProbe(token string) string {
	return "# paisans host admit: keys, read as the user\n" +
		`if [ -f "$HOME/.ssh/authorized_keys" ]; then awk 1 "$HOME/.ssh/authorized_keys"; fi` + "\n" +
		"echo " + keysMarker + "\n" +
		"if [ -f " + sidecarPath(token) + " ]; then awk 1 " + sidecarPath(token) + "; fi"
}

// sidecarPath is where a deployment records the keys it appended to its
// user's authorized_keys, in its own directory, which is the user's.
func sidecarPath(token string) string {
	return "/srv/paisans/" + token + "/.paisans/authorized_keys.owned"
}

// keysSeparator divides the keys step's stdin: the key lines to append,
// then the sidecar to write.
const keysSeparator = "--- sidecar ---"

// keysScript appends the keys on its stdin to authorized_keys and writes
// the sidecar after the separator, each through a temporary file and a
// rename. It runs as the user, so a link the user planted is followed with
// the user's rights alone. An authorized_keys that cannot be read stops it
// before anything is replaced, so no key the owner put there is lost.
func keysScript(token string) string {
	side := sidecarPath(token)
	return `set -e; umask 077; mkdir -p "$HOME/.ssh" ` + "/srv/paisans/" + token + `/.paisans
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
awk -v k="$tmp/keys" -v s="$tmp/sidecar" '$0 == "` + keysSeparator + `" { out = 1; next } { if (out) print > s; else print > k }'
touch "$tmp/keys" "$tmp/sidecar"
f="$HOME/.ssh/authorized_keys"
{ if [ -e "$f" ] || [ -L "$f" ]; then awk 1 "$f"; fi; cat "$tmp/keys"; } > "$HOME/.ssh/.authorized_keys.paisans-tmp"
mv -f "$HOME/.ssh/.authorized_keys.paisans-tmp" "$f"
cat "$tmp/sidecar" > ` + side + `.paisans-tmp
mv -f ` + side + `.paisans-tmp ` + side
}

// keysCommand is the keys step's command for user, whose home is home,
// with its UID read on the host when it runs, for a test that runs it
// without a plan.
func keysCommand(user, home, token string) string {
	return userCommand(user, apply.ShellQuote(home), "$(id -u "+apply.ShellQuote(user)+")", keysScript(token))
}

// planKeys adds each operator's key the user's authorized_keys lacks, and
// records it in the sidecar as host prepare records one, so a later host
// prepare, run by the operators as the user, can take a key away. Admit
// never removes a key: the owner may have put one there.
func planKeys(p *planner) error {
	user := p.in.Request.User
	have := map[string]bool{}
	var owned []hostprep.OwnedKey
	if p.f.User.Exists {
		out, err := p.t.Run(asUser(p, keysProbe(p.d.Token())))
		if err != nil {
			return fmt.Errorf("reading %s's keys as %s: %w", user, user, shown(err))
		}
		lines := strings.Split(out, "\n")
		sidecar := ""
		for i, line := range lines {
			if line == keysMarker {
				lines, sidecar = lines[:i], strings.Join(lines[i+1:], "\n")
				break
			}
		}
		for _, line := range lines {
			if k, _, err := config.ParseKeyLine(line); err == nil {
				have[k.Fingerprint] = true
			}
		}
		owned = hostprep.ParseOwnedKeys(sidecar)
	}
	var add []string
	for _, o := range p.in.Request.Operators {
		for _, key := range o.Keys {
			k, _, err := config.ParseKeyLine(key + " " + o.Name)
			if err != nil {
				return fmt.Errorf("operator %s's key: %w", ui.Visible(o.Name), err)
			}
			if have[k.Fingerprint] {
				continue
			}
			have[k.Fingerprint] = true
			add = append(add, key+" "+o.Name)
			owned = append(owned, hostprep.OwnedKey{Mark: "added", Fingerprint: k.Fingerprint, Name: o.Name})
		}
	}
	if len(add) == 0 {
		p.present("keys: every operator's key is in " + user + "'s authorized_keys")
		return nil
	}
	p.step(Step{Title: "add the operators' keys",
		Describe: fmt.Sprintf("keys: %d operator %s into %s's authorized_keys, written as %s, and recorded in %s for host prepare", len(add), plural(len(add), "key"), user, user, sidecarPath(p.d.Token())),
		Command:  asUser(p, keysScript(p.d.Token())),
		Input:    strings.Join(add, "\n") + "\n" + keysSeparator + "\n" + hostprep.OwnedKeysFile(user, owned),
		Kinds:    []string{hostprep.KindSSHKeys}, Provisional: !p.f.User.Exists})
	return nil
}

// CheckUser runs docker version as the deployment's user on the host t
// reaches as root, which is admit's last step on both paths, run by the
// command once Execute is done: the user's own daemon answers on its own
// socket. It returns the Engine's version.
func CheckUser(t apply.Transport, req admission.Request) (string, error) {
	user := apply.ShellQuote(req.User)
	home := `"$(getent passwd ` + user + ` | cut -d: -f6)"`
	out, err := t.Run(userCommand(req.User, home, "$(id -u "+user+")", "docker version --format '{{.Server.Version}}'"))
	if err != nil {
		return "", fmt.Errorf("%s's Docker does not answer on its own socket: %w", ui.Visible(req.User), shown(err))
	}
	return ui.Visible(strings.TrimSpace(out)), nil
}
