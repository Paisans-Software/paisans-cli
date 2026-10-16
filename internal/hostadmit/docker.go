package hostadmit

import (
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/admitrules"
	"github.com/paisans-software/paisans-cli/internal/hostcheck"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
)

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
		if !hostcheck.AtLeast(dk.Engine, admitrules.MinRootlessEngine) {
			return fmt.Errorf("Docker Engine %s is installed, and admitting a deployment needs %s or later: from 29.5 a rootless container with host networking shares the host's network namespace, which every infrastructure container needs. Upgrade Docker yourself and admit again; upgrading your Docker is not admit's decision. Nothing was changed", dk.Engine, admitrules.MinRootlessEngine)
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
// 24.04 has it. Ubuntu 22.04 has neither the restriction nor the profile,
// unless a newer kernel brings the restriction and its owner turns it on;
// its apparmor ships no such profile, so the refusal there says what
// lifting the restriction for the whole host costs, and how to keep it
// lifted at boot, and leaves the choice to the owner. A host without the
// apparmor package is told to install it, the narrower fix. No package
// this run installs would change any of these, so admit refuses before
// installing anything.
func planAppArmor(p *planner) error {
	a := p.f.AppArmor
	switch {
	case !a.Restricted:
		p.present("apparmor: unprivileged user namespaces are not restricted")
	case a.Profile:
		p.present("apparmor: rootlesskit's profile is loaded")
	case !a.Package:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here, no rootlesskit profile is loaded, and the apparmor package is not installed, so nothing here ships one. Install it (apt-get install apparmor); Ubuntu's ships /etc/apparmor.d/rootlesskit from 24.04. Load that profile with apparmor_parser -r /etc/apparmor.d/rootlesskit, check aa-status lists rootlesskit, and admit again, which says what else to do if your release's apparmor has no such profile. Nothing was changed")
	case a.Shipped:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here and no rootlesskit profile is loaded. The profile is /etc/apparmor.d/rootlesskit, from Ubuntu's apparmor package: install or update that package (apt-get install --reinstall apparmor), load the profile with apparmor_parser -r /etc/apparmor.d/rootlesskit, check aa-status lists rootlesskit, and admit again. Nothing was changed")
	default:
		return fmt.Errorf("AppArmor restricts unprivileged user namespaces here, no rootlesskit profile is loaded, and this host's apparmor package ships none (Ubuntu's ships /etc/apparmor.d/rootlesskit from 24.04). Rootless Docker needs user namespaces, so admitting here means lifting the restriction for the whole host: every user and program on this host may then create unprivileged user namespaces again, the kernel attack surface the restriction exists to reduce. If it was turned on on purpose, keep it, and admit on another host instead. To lift it, run sysctl -w kernel.apparmor_restrict_unprivileged_userns=0; keep it at boot by writing kernel.apparmor_restrict_unprivileged_userns = 0 in /etc/sysctl.d/99-zz-paisans-userns.conf, which sorts after the files that set it, and changing every other line grep -rs apparmor_restrict_unprivileged_userns /etc/sysctl.conf /etc/sysctl.d /run/sysctl.d /usr/lib/sysctl.d finds; check sysctl kernel.apparmor_restrict_unprivileged_userns prints 0 after the next reboot; and admit again. Nothing was changed")
	}
	return nil
}
