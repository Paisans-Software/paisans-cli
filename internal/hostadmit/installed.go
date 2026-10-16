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
// source by path, for every admitted deployment. It is written before the
// install runs, so a run that stops after it still knows the system Docker
// is admit's, and host evict names it all once the last deployment goes:
// it stays installed.
type installed struct {
	Packages, Files []string
}

// packageName is a Debian package's name, as dpkg allows one.
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]+$`)

func parseInstalled(content, by string) (installed, error) {
	var in installed
	for _, line := range strings.Split(content, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, v, _ := strings.Cut(line, " ")
		switch {
		case kind == "package" && packageName.MatchString(v):
			in.Packages = append(in.Packages, v)
		case kind == "file" && path.IsAbs(v) && path.Clean(v) == v && ui.Visible(v) == v:
			in.Files = append(in.Files, v)
		default:
			return in, fmt.Errorf("%s holds %s, which is not a line admit wrote. Remove the line and %s again. Nothing was changed", installedPath, ui.Visible(line), by)
		}
	}
	return in, nil
}

// text is the file's content: a header, then each package, then each file.
func (in installed) text() string {
	out := "# Written by paisans host admit: what it installed on this host. It stays\n" +
		"# installed when paisans host evict removes the last admitted deployment.\n"
	for _, p := range in.Packages {
		out += "package " + p + "\n"
	}
	for _, f := range in.Files {
		out += "file " + f + "\n"
	}
	return out
}

// with is in and added together, each name once, in the order first
// recorded.
func (in installed) with(added hostprep.Installed) installed {
	out := installed{Packages: slices.Clone(in.Packages), Files: slices.Clone(in.Files)}
	for _, p := range added.Packages {
		if !slices.Contains(out.Packages, p) {
			out.Packages = append(out.Packages, p)
		}
	}
	for _, f := range added.Files {
		if !slices.Contains(out.Files, f) {
			out.Files = append(out.Files, f)
		}
	}
	return out
}

// dockerIsAdmits reports whether admit installed the host's docker-ce, so
// its system daemon is admit's to keep disabled.
func (in installed) dockerIsAdmits() bool { return slices.Contains(in.Packages, "docker-ce") }

// readInstalled is the installed file as the host holds it, refused when
// it is a link or holds a line admit did not write.
func readInstalled(p *planner, by string) (installed, error) {
	if p.f.Files[installedPath].Link {
		return installed{}, fmt.Errorf("%s is a symbolic link. Remove it and %s again. Nothing was changed", installedPath, by)
	}
	content, _, err := p.t.ReadFile(installedPath)
	if err != nil {
		return installed{}, shown(err)
	}
	return parseInstalled(content, by)
}

// systemDockerUnits are Docker's system units, which its packages enable.
const systemDockerUnits = "docker.socket docker.service containerd.service"

// planSystemDocker records what this run installs, before it installs it,
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
	now := was.with(added)
	if now.text() != was.text() {
		p.step(Step{Title: "record what admit installs", Describe: "docker: " + installedPath + ", what admit installs on this host, which stays when the last deployment is evicted",
			File: &hostprep.File{Path: installedPath, Content: now.text(), Mode: 0o644}, Kinds: []string{"docker"}})
	}
	p.section(s)
	installs := slices.Contains(added.Packages, "docker-ce")
	if p.f.Docker.Held && !installs {
		release, err := p.profile.ReleaseDockerHold()
		if err != nil {
			return err
		}
		p.step(Step{Title: "put back /usr/sbin/policy-rc.d", Describe: "docker: an admit that stopped while it installed Docker left its own policy-rc.d there; yours, if you had one, goes back",
			Command: release, Kinds: []string{"docker"}})
	}
	p.record.SystemDockerDisabled = now.dockerIsAdmits()
	if !now.dockerIsAdmits() {
		return nil
	}
	recorded, err := disabledBefore(p)
	if err != nil {
		return err
	}
	units := p.f.Docker.SystemUnits
	switch {
	case installs || !recorded && (units.Enabled || units.Active):
		p.step(Step{Title: "disable the system Docker",
			Describe: "docker: the deployments run their own rootless daemons, so Docker's system daemon, which its packages enable, is disabled, and was kept from starting while they installed: it never changed forwarding, docker0 or the firewall",
			Command:  "systemctl disable --now " + systemDockerUnits + " && if ip link show docker0 >/dev/null 2>&1; then echo 'docker0 exists, so the system Docker ran and changed forwarding and the firewall as it started. It is disabled now: reboot the host, which clears what it changed, and admit again'; exit 1; fi",
			Kinds:    []string{"docker"}})
	case units.Enabled || units.Active:
		p.present("docker: the system daemon admit installed and disabled has been enabled since, and is left as it is")
	default:
		p.present("docker: the system daemon admit installed is disabled")
	}
	return nil
}

// disabledBefore reports whether an admission record on the host, this
// deployment's or another's, says admit disabled the system Docker.
func disabledBefore(p *planner) (bool, error) {
	prev, err := p.previous()
	if err != nil {
		return false, err
	}
	if prev != nil && prev.SystemDockerDisabled {
		return true, nil
	}
	st, err := p.sysctls()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(st.others, func(r admission.Record) bool { return r.SystemDockerDisabled }), nil
}
