package hostadmit

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
)

// MinRootlessEngine is the oldest Docker Engine admit accepts: from 29.5 a
// rootless container with host networking shares the real host's network
// namespace, which every infrastructure container needs to bind the mesh
// address (Docker's rootless troubleshooting page, read 2026-10-15; spec,
// Observed R1).
const MinRootlessEngine = "29.5.0"

// systemDocker starts every command admit runs against the owner's system
// daemon, so an owner's DOCKER_HOST or docker context never redirects it.
const systemDocker = "docker -H unix:///var/run/docker.sock"

// planDocker refuses a snap Docker and an Engine older than
// MinRootlessEngine, since upgrading the owner's Docker is not admit's
// decision, and a present Docker that cannot run rootless; then plans the
// packages.
func planDocker(p *planner) error {
	dk := p.f.Docker
	if dk.Snap {
		return fmt.Errorf("Docker is installed as a snap, whose confinement keeps it from bind mounting under /srv and whose refreshes restart every container on snapd's schedule. Remove it yourself (snap remove docker), after checking nothing running depends on it, and admit again. Nothing was changed")
	}
	present := dk.Engine != ""
	if present {
		if !hostcheck.AtLeast(dk.Engine, MinRootlessEngine) {
			return fmt.Errorf("Docker Engine %s is installed, and admitting a deployment needs %s or later: from 29.5 a rootless container with host networking shares the host's network namespace, which every infrastructure container needs. Upgrade Docker yourself and admit again; upgrading your Docker is not admit's decision. Nothing was changed", dk.Engine, MinRootlessEngine)
		}
		if !p.f.Packages["docker-ce"] && !dk.Setuptool {
			return fmt.Errorf("Docker %s here is not Docker's own docker-ce and has no dockerd-rootless-setuptool.sh. The rootless extras come from Docker's apt repository, which installs beside docker-ce only. Install Docker from Docker's repository yourself, or admit on another host. Nothing was changed", dk.Engine)
		}
		p.present("docker: Engine " + dk.Engine)
	}
	s, err := p.profile.AdmitPackages(p.t, p.host, present)
	if err != nil {
		return err
	}
	p.section(s)
	return nil
}

// planAppArmor checks that Ubuntu's restriction on unprivileged user
// namespaces lets rootlesskit run: the profile docker-ce-rootless-extras
// installs must be loaded. When this run installs the extras, the check is
// a step after the install; when they are installed already and the
// profile is not loaded, admit refuses with the fix.
func planAppArmor(p *planner) error {
	a := p.f.AppArmor
	switch {
	case !a.Restricted:
		p.present("apparmor: unprivileged user namespaces are not restricted")
	case a.Profile:
		p.present("apparmor: rootlesskit's profile is loaded")
	case p.f.Packages["docker-ce-rootless-extras"]:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded, though docker-ce-rootless-extras is installed. Load the profile it installed with systemctl reload apparmor, check aa-status lists rootlesskit, and admit again. Nothing was changed")
	default:
		p.step(Step{
			Title:    "check rootlesskit's AppArmor profile",
			Describe: "apparmor: Ubuntu restricts unprivileged user namespaces, so the rootlesskit profile docker-ce-rootless-extras installs must be loaded before the user's daemon can start",
			Command:  `grep -q rootlesskit /sys/kernel/security/apparmor/profiles || { echo "AppArmor restricts unprivileged user namespaces and no rootlesskit profile is loaded after installing docker-ce-rootless-extras. Load it with systemctl reload apparmor, check aa-status lists rootlesskit, and run host admit again"; exit 1; }`,
			Kinds:    []string{"docker"},
		})
	}
	return nil
}
