package hostprep

import (
	"fmt"
	"path"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/deployment"
)

// configDir holds every deployment's configuration store on a host, one
// <token>.git beside its <token>.peers and <token>.sync.json. The git daemon
// serves it as its base path, so the token alone names a repository.
const configDir = "/var/lib/paisans/config"

// ConfigRepo is the deployment's bare repository of configuration versions.
func ConfigRepo(d deployment.Deployment) string { return configDir + "/" + d.Token() + ".git" }

// ConfigPeers lists the mesh addresses the sync fetches from, one per line.
func ConfigPeers(d deployment.Deployment) string { return configDir + "/" + d.Token() + ".peers" }

// ConfigSyncLog is what the last sync reached, written by the sync script.
func ConfigSyncLog(d deployment.Deployment) string { return configDir + "/" + d.Token() + ".sync.json" }

// ConfigScriptDir holds the sync script, named for the deployment so that
// two deployments on one host keep their own.
func ConfigScriptDir(d deployment.Deployment) string { return "/usr/local/lib/" + d.Prefix() }

// configPeers is every other site's mesh address, in site name order.
func configPeers(cfg *config.Config, site string) []string {
	var peers []string
	for _, name := range cfg.SiteNames() {
		if name != site {
			peers = append(peers, cfg.Sites[name].Address)
		}
	}
	return peers
}

// repoCommand creates the repository where it is absent. The daemon runs as
// nobody and the repository belongs to root, so every directory must be 0755
// and every file 0644: umask and --shared=0644 give that to what git writes
// later, including what a sync fetches, and the chmod gives it to the files
// git init wrote with its own modes.
func repoCommand(repo string) string {
	q := "'" + repo + "'"
	return fmt.Sprintf("set -e; umask 022; install -d -m 0755 %s; git init --bare --quiet --shared=0644 %[2]s; find %[2]s -type d -exec chmod 0755 {} +; find %[2]s -type f -exec chmod 0644 {} +", "'"+configDir+"'", q)
}

// ConfigStore gives the site a repository of signed configuration versions,
// a daemon that serves it read only to the mesh, and a timer that fetches
// from peers what this site lacks. Each step is planned only where the host
// differs from it.
func (u ubuntu) ConfigStore(t Transport, d deployment.Deployment, self config.Site, peers []string) (Section, error) {
	var out Section
	kinds := []string{KindConfigStore}
	repo := ConfigRepo(d)

	_, found, err := t.ReadFile(repo + "/HEAD")
	if err != nil {
		return out, err
	}
	if found {
		out.Present = append(out.Present, "configuration store: repository at "+repo)
	} else {
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "create the configuration repository",
			Describe: "configuration store: create the bare repository at " + repo + ", readable by the user the daemon runs as",
			Command:  repoCommand(repo),
		})
	}

	data := map[string]string{"Prefix": d.Prefix(), "Token": d.Token(), "Address": self.Address, "WireGuardUnit": d.WireGuardUnit(), "Receive": ConfigReceiveShell}
	peerList := ""
	if len(peers) > 0 {
		// A final newline, since read in the sync script drops a last line without one.
		peerList = strings.Join(peers, "\n") + "\n"
	}
	script, err := snippet(u.tmpl("config-sync.sh.tmpl"), data)
	if err != nil {
		return out, err
	}
	daemon, err := snippet(u.tmpl("config-daemon.service.tmpl"), data)
	if err != nil {
		return out, err
	}
	syncService, err := snippet(u.tmpl("config-sync.service.tmpl"), data)
	if err != nil {
		return out, err
	}
	timer, err := snippet(u.tmpl("config-sync.timer.tmpl"), data)
	if err != nil {
		return out, err
	}

	daemonUnit := d.Prefix() + "-config-daemon.service"
	syncUnit := d.Prefix() + "-config-sync.service"
	timerUnit := d.Prefix() + "-config-sync.timer"
	files := []struct {
		file, content, what string
		mode                uint32
		unit                bool
	}{
		{ConfigPeers(d), peerList, "the peers to fetch from", 0o644, false},
		{ConfigScriptDir(d) + "/config-sync.sh", script, "the sync script", 0o755, false},
		{"/etc/systemd/system/" + daemonUnit, daemon, "the unit " + daemonUnit, 0o644, true},
		{"/etc/systemd/system/" + syncUnit, syncService, "the unit " + syncUnit, 0o644, true},
		{"/etc/systemd/system/" + timerUnit, timer, "the unit " + timerUnit, 0o644, true},
	}
	unitsChanged, daemonChanged := false, false
	for _, f := range files {
		current, ok, err := t.ReadFile(f.file)
		if err != nil {
			return out, err
		}
		if ok && current == f.content {
			out.Present = append(out.Present, "configuration store: "+f.file)
			continue
		}
		unitsChanged = unitsChanged || f.unit
		daemonChanged = daemonChanged || f.file == "/etc/systemd/system/"+daemonUnit
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "write " + path.Base(f.file),
			Describe: "configuration store: write " + f.what + " at " + f.file,
			File:     &File{Path: f.file, Content: f.content, Mode: f.mode},
		})
	}

	daemonEnabled, daemonActive, err := u.unitState(t, daemonUnit)
	if err != nil {
		return out, err
	}
	timerEnabled, timerActive, err := u.unitState(t, timerUnit)
	if err != nil {
		return out, err
	}
	if unitsChanged || !(daemonEnabled && daemonActive && timerEnabled && timerActive) {
		command := "systemctl daemon-reload && systemctl enable --now " + daemonUnit + " " + timerUnit
		if daemonChanged {
			command += " && systemctl restart " + daemonUnit
		}
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "start the configuration store",
			Describe: fmt.Sprintf("configuration store: enable and start %s and %s", daemonUnit, timerUnit),
			Command:  command,
		})
	} else {
		out.Present = append(out.Present, fmt.Sprintf("configuration store: %s and %s enabled and running", daemonUnit, timerUnit))
	}
	return out, nil
}
