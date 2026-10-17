package hostadmit

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// installed is /etc/paisans/installed: what admit installed on this host
// that it did not have, each package by name and Docker's apt key and
// source by path, for every admitted deployment, and each directory admit
// created that evict removes once empty. Host evict names what is
// installed once the last deployment goes: it stays installed, and this
// list stays with it, so a later admit knows the system Docker as its own.
//
// A package is claimed as Installing before the install runs and becomes
// one of Packages when dpkg shows it installed as the install exits,
// however it exits (confirmClaims, from the install's exit trap). A run
// killed outright leaves admit's policy-rc.d in place, and apt may have
// run under it, so its claims for what dpkg shows installed still stand;
// any other claim is dropped, so a Docker the owner installs later is
// never taken for admit's.
type installed struct {
	Packages, Installing, Files, Dirs []string
	// file is the file's content as read, "" for none.
	file string
}

// packageName is a Debian package's name, as dpkg allows one.
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]+$`)

func parseInstalled(content, by string) (installed, error) {
	var in installed
	for _, line := range strings.Split(content, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, v, _ := strings.Cut(line, " ")
		switch {
		case entry == "package" && packageName.MatchString(v):
			in.Packages = append(in.Packages, v)
		case entry == "installing" && packageName.MatchString(v):
			in.Installing = append(in.Installing, v)
		case entry == "file" && path.IsAbs(v) && path.Clean(v) == v && ui.Visible(v) == v:
			in.Files = append(in.Files, v)
		case entry == "dir" && v == delegateDir:
			in.Dirs = append(in.Dirs, v)
		default:
			return in, fmt.Errorf("%s holds %s, which is not a line admit wrote. Remove the line and %s again. Nothing was changed", installedPath, ui.Visible(line), by)
		}
	}
	return in, nil
}

// text is the file's content: a header, then each package, each package
// being installed, each file and each directory.
func (in installed) text() string {
	out := "# Written by paisans site admit: what it installed on this host, which stays\n" +
		"# when paisans site evict removes the last admitted deployment, and each\n" +
		"# directory it created, which evict removes once empty.\n"
	for _, p := range in.Packages {
		out += "package " + p + "\n"
	}
	for _, p := range in.Installing {
		out += "installing " + p + "\n"
	}
	for _, f := range in.Files {
		out += "file " + f + "\n"
	}
	for _, d := range in.Dirs {
		out += "dir " + d + "\n"
	}
	return out
}

// with is in, added and the directories dirs together, each name once, in
// the order first recorded.
func (in installed) with(added hostprep.Installed, dirs ...string) installed {
	out := installed{Packages: slices.Clone(in.Packages), Installing: slices.Clone(in.Installing), Files: slices.Clone(in.Files), Dirs: slices.Clone(in.Dirs)}
	for _, d := range dirs {
		if !slices.Contains(out.Dirs, d) {
			out.Dirs = append(out.Dirs, d)
		}
	}
	for _, p := range added.Packages {
		if !slices.Contains(out.Packages, p) && !slices.Contains(out.Installing, p) {
			out.Installing = append(out.Installing, p)
		}
	}
	for _, f := range added.Files {
		if !slices.Contains(out.Files, f) {
			out.Files = append(out.Files, f)
		}
	}
	return out
}

// all is every package in, installed or being installed.
func (in installed) all() []string { return append(slices.Clone(in.Packages), in.Installing...) }

// dockerIsAdmits reports whether admit installed the host's docker-ce, so
// its system daemon is admit's to keep disabled.
func (in installed) dockerIsAdmits() bool { return slices.Contains(in.all(), "docker-ce") }

// confirmClaims defines confirm_claims, which turns each "installing"
// claim in the installed list into a package of admit's when dpkg has its
// files on disk: installed, or unpacked or half-configured by an install
// that failed, whose scripts may have enabled its units already. The
// install step runs it from its exit trap, once it holds the hold's lock,
// however the install exits and before a hold on Docker's daemons is
// released. It rewrites the list through a temporary file of its own and a
// rename, with awk rather than sed -i, which BSD sed reads differently.
const confirmClaims = `confirm_claims() {
  f=` + installedPath + `
  { [ -f "$f" ] && [ ! -L "$f" ]; } || return 0
  ok=
  for p in $(awk 'NF == 2 && $1 == "installing" && $2 ~ /^[a-z0-9][a-z0-9.+-]+$/ { print $2 }' "$f"); do
    s=$(dpkg-query -W -f='${Status}' "$p" 2>/dev/null) || continue
    case $s in *" not-installed"|*" config-files"|"") ;; *) ok="$ok $p" ;; esac
  done
  [ -n "$ok" ] || return 0
  t=$(mktemp "$f.XXXXXX") || return 1
  if awk -v ok="$ok " 'NF == 2 && $1 == "installing" && index(ok, " " $2 " ") { print "package " $2; next } { print }' "$f" > "$t" && chmod 644 "$t"; then
    mv -f "$t" "$f"
  else
    rm -f "$t"
    return 1
  fi
}
`

// readInstalled is the installed file as the host holds it, refused when
// it is not root's, mode 644, as admit writes it, or holds a line admit
// did not write: it decides whose the system Docker is. A package whose
// files dpkg does not have is left out, as is a claim with no policy of
// admit's left in place to say its install ran (installed, which the
// probe reads for every package the file names).
func readInstalled(p *planner, by string) (installed, error) {
	if f := p.f.Files[installedPath]; f.Present && (f.Link || f.UID != 0 || f.GID != 0 || f.Mode != "644") {
		return installed{}, fmt.Errorf("%s is not root's, mode 644, as admit writes it (it is a link, or its mode is %s, owner %d, group %d), and it says whose the system Docker is. Look at what it holds, remove it, and %s again. Nothing was changed", installedPath, ui.Visible(f.Mode), f.UID, f.GID, by)
	}
	content, _, err := p.t.ReadFile(installedPath)
	if err != nil {
		return installed{}, shown(err)
	}
	in, err := parseInstalled(content, by)
	if err != nil {
		return in, err
	}
	return installed{
		Packages:   slices.DeleteFunc(in.Packages, func(x string) bool { return !p.f.onDisk(x) }),
		Installing: slices.DeleteFunc(in.Installing, func(x string) bool { return !p.f.onDisk(x) || !p.f.Docker.HeldPolicy }),
		Files:      in.Files, Dirs: in.Dirs, file: content,
	}, nil
}

// planSystemDocker records what this run installs, before it installs it,
// with delegate.conf's directory when the delegation part will create it,
// and keeps the system Docker admit installs disabled. The install holds
// the daemon so it never starts (hostprep's docker-hold.sh.tmpl), rather
// than letting it start and then stopping it and undoing what it changed:
// as it starts, dockerd turns on forwarding for every interface, creates
// docker0, adds its NAT and filter chains and sets the FORWARD policy to
// DROP, and undoing all of that exactly needs to know each value as it
// was, while a daemon that never ran changed none of it. The units its
// packages enable are then disabled. A record that says admit disabled
// them keeps them as they are from then on, so an owner who enables the
// daemon later keeps it; a Docker the owner had before admit is never
// touched.
func planSystemDocker(p *planner, s hostprep.Section, added hostprep.Installed) error {
	was, err := readInstalled(p, "admit")
	if err != nil {
		return err
	}
	var dirs []string
	if !p.f.Dirs[delegateDir].Present {
		dirs = append(dirs, delegateDir)
	}
	now := was.with(added, dirs...)
	if now.text() != was.file && (was.file != "" || len(now.all())+len(now.Files)+len(now.Dirs) > 0) {
		p.step(Step{Title: "record what admit installs", Describe: "docker: " + installedPath + ", what admit installs on this host, which stays when the last deployment is evicted, and the directories it creates",
			File: &hostprep.File{Path: installedPath, Content: now.text(), Mode: 0o644}, Kinds: []string{"docker"}})
	}
	if len(added.Packages) > 0 {
		// AdmitPackages plans the install last. Whatever apt installed of
		// what it claimed is admit's, however it exits: the hold's own exit
		// trap calls confirm_claims before it releases the hold, and an
		// install with nothing to hold runs it from this trap. The hold's
		// lock is taken first, so a run refused there writes nothing, and a
		// signal exits through the trap.
		last := &s.Steps[len(s.Steps)-1]
		lock, err := p.profile.DockerHoldLock()
		if err != nil {
			return err
		}
		last.Command = "set -e\n" + lock + confirmClaims + "trap confirm_claims EXIT\ntrap 'exit 1' HUP INT TERM\n" + last.Command
	}
	p.section(s)
	installs := len(hostprep.SystemUnits(added.Packages)) > 0
	if !installs {
		if err := releaseHold(p, "docker"); err != nil {
			return err
		}
	}
	p.record.SystemDockerDisabled = now.dockerIsAdmits()
	// Only the units of what admit installed: a containerd.io the host had
	// is the owner's.
	units := hostprep.SystemUnits(now.all())
	if len(units) == 0 {
		return nil
	}
	recorded, unknown, err := disabledBefore(p)
	if err != nil {
		return err
	}
	running := slices.ContainsFunc(units, func(u string) bool {
		s := p.f.Docker.SystemUnits[u]
		return s.Enabled || s.Active
	})
	// Held disabled and never run: no unit enabled, and no network store,
	// which a daemon the owner started and stopped again leaves.
	p.heldDocker = slices.Contains(units, "docker.service") && !p.f.Docker.NetworkStore &&
		!slices.ContainsFunc(units, func(u string) bool { return p.f.Docker.SystemUnits[u].Enabled })
	// Never run: no unit active and no network store, as an admit stopped
	// between its install and the disable leaves it, held while it
	// installed. One that has run may be the owner's now, with their
	// containers, which disabling it would stop.
	ran := p.f.Docker.NetworkStore || slices.ContainsFunc(units, func(u string) bool { return p.f.Docker.SystemUnits[u].Active })
	switch {
	case !installs && !recorded && running && unknown != "":
		p.present("docker: the system daemon admit installed is running and left as it is, since " + unknown + " cannot be read and may say admit disabled it once")
	case !installs && !recorded && running && ran:
		return systemDockerRan(units, now.all())
	case installs || !recorded && running:
		p.step(Step{Title: "disable the system Docker",
			Describe: "docker: the deployments run their own rootless daemons, so the system daemons admit installed, which their packages enable, are disabled (" + strings.Join(units, ", ") + "), and were kept from starting while they installed: they never changed forwarding, docker0 or the firewall",
			Command:  "systemctl disable --now " + strings.Join(units, " ") + " && if ip link show docker0 >/dev/null 2>&1; then echo 'docker0 exists, so the system Docker ran and changed forwarding and the firewall as it started. It is disabled now: reboot the host, which clears what it changed, and admit again'; exit 1; fi",
			Kinds:    []string{"docker"}})
	case running:
		p.present("docker: the system daemon admit installed and disabled has been enabled since, and is left as it is")
	default:
		p.present("docker: the system daemon admit installed is disabled")
	}
	return nil
}

// systemDockerRan is the refusal for a system Docker admit installed that
// is enabled or running, has run, and that no admission record says admit
// disabled: it may run the owner's containers by now, and disabling it
// would stop them. The owner says whose it is.
func systemDockerRan(units, packages []string) error {
	lines := "the line package docker-ce"
	if slices.Contains(packages, "containerd.io") {
		lines = "the lines package docker-ce and package containerd.io"
		if !slices.Contains(packages, "docker-ce") {
			lines = "the line package containerd.io"
		}
	}
	return fmt.Errorf("the system Docker admit installed (%s) is enabled or running and has run, and no admission record says admit disabled it, so it may be running your containers, which disabling it would stop. Nothing was changed. To keep it as yours, remove %s from %s and admit again. To give it back to admit, run systemctl disable --now %s, reboot the host, which clears what it changed to forwarding, docker0 and the firewall, and admit again",
		strings.Join(units, ", "), lines, installedPath, strings.Join(units, " "))
}

// disabledBefore reports whether an admission record on the host, this
// deployment's or another's, says admit disabled the system Docker, and
// names another's record that cannot be read, and so may say so.
func disabledBefore(p *planner) (bool, string, error) {
	prev, err := p.previous()
	if err != nil {
		return false, "", err
	}
	if prev != nil && prev.SystemDockerDisabled {
		return true, "", nil
	}
	st, err := p.sysctls()
	if err != nil {
		return false, "", err
	}
	unknown := ""
	if len(st.unreadable) > 0 {
		unknown = ui.Visible(st.unreadable[0])
	}
	return slices.ContainsFunc(st.others, func(r admission.Record) bool { return r.SystemDockerDisabled }), unknown, nil
}

// releaseHold plans putting /usr/sbin/policy-rc.d back when a run that
// stopped while admit held Docker's daemons left the hold behind: it is
// never right outside a running install. prefix starts the step's
// sentence.
func releaseHold(p *planner, prefix string) error {
	if !p.f.Docker.Held {
		return nil
	}
	// With admit's policy in place, apt may have run under it, and that
	// install was admit's: what it claimed and dpkg has on disk is
	// admit's (readInstalled keeps only those claims while the policy is
	// there), and is recorded so, under the lock, before the hold is
	// released, so a run that dies between the two still finds the policy.
	// A leftover from before the policy confirms nothing: apt never ran.
	before := ""
	if p.f.Docker.HeldPolicy {
		before = confirmClaims + "confirm_claims\n"
	}
	command, err := p.profile.ReleaseDockerHold(before)
	if err != nil {
		return err
	}
	p.step(Step{Title: "put back /usr/sbin/policy-rc.d", Describe: prefix + ": an admit that stopped while it installed Docker left its hold on Docker's daemons; your own policy-rc.d, if you had one, goes back, and what that install claimed is recorded as installed",
		Command: command, Kinds: []string{prefix}})
	return nil
}
