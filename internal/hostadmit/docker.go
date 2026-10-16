package hostadmit

import (
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
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
// packages, and keeps a system Docker admit installs disabled.
func planDocker(p *planner) error {
	dk := p.f.Docker
	if dk.Snap {
		return errors.New(hostprep.SnapDockerAdmit)
	}
	if dk.Unreadable {
		return fmt.Errorf("dockerd is installed and dockerd --version printed no version admit can read, so admit cannot tell which Docker this is, and would otherwise install Docker's packages over it. Check that dockerd --version prints \"Docker version <version>, build <id>\", and admit again. Nothing was changed")
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
	s, added, err := p.profile.AdmitPackages(p.t, p.host, present)
	if err != nil {
		return err
	}
	return planSystemDocker(p, s, added)
}

// planAppArmor checks that Ubuntu's restriction on unprivileged user
// namespaces lets rootlesskit run: the profile Ubuntu's apparmor package
// installs at /etc/apparmor.d/rootlesskit must be loaded. A fresh Ubuntu
// 24.04 has it. When it is not, no package this run installs would change
// that, so admit refuses before installing anything.
func planAppArmor(p *planner) error {
	a := p.f.AppArmor
	switch {
	case !a.Restricted:
		p.present("apparmor: unprivileged user namespaces are not restricted")
	case a.Profile:
		p.present("apparmor: rootlesskit's profile is loaded")
	default:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded. The profile is /etc/apparmor.d/rootlesskit, from Ubuntu's apparmor package: install or update that package (apt-get install --reinstall apparmor), load the profile with apparmor_parser -r /etc/apparmor.d/rootlesskit, check aa-status lists rootlesskit, and admit again. Nothing was changed")
	}
	return nil
}
