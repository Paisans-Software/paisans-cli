package hostprep

import (
	"fmt"
	"path"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/deployment"
)

// ConfigDir holds the deployment's configuration store on a host, in its
// state directory: the repository <token>.git beside <token>.peers and
// <token>.sync.json. The git daemon serves it as its base path, so the token
// alone names the repository, and every peer fetches git://<address>/<token>.git.
func ConfigDir(d deployment.Deployment) string { return d.StatePath("config") }

// ConfigRepo is the deployment's bare repository of configuration versions.
func ConfigRepo(d deployment.Deployment) string { return ConfigDir(d) + "/" + d.Token() + ".git" }

// ConfigPeers lists the mesh addresses the sync fetches from, one per line.
func ConfigPeers(d deployment.Deployment) string { return ConfigDir(d) + "/" + d.Token() + ".peers" }

// ConfigSyncLog is what the last sync reached, written by the sync script.
func ConfigSyncLog(d deployment.Deployment) string {
	return ConfigDir(d) + "/" + d.Token() + ".sync.json"
}

// ConfigIncoming is where a pushed bundle lands for the length of one push,
// beside the repository it is taken into. A push that is cut off can leave
// it behind, so site remove deletes it with the rest of the store.
func ConfigIncoming(d deployment.Deployment) string {
	return ConfigDir(d) + "/" + d.Token() + ".incoming.bundle"
}

// ConfigScriptDir holds the sync script, in the deployment's state directory.
func ConfigScriptDir(d deployment.Deployment) string { return d.StatePath("bin") }

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

// repoCommand creates the repository where it is absent, the user's and 700
// like the rest of the state directory: the daemon that serves it runs as
// the same user.
func repoCommand(d deployment.Deployment) string {
	return fmt.Sprintf("set -e; umask 077; install -d -m 700 %s %s; git init --bare --quiet %s",
		shellQuote(d.StateDir()), shellQuote(ConfigDir(d)), shellQuote(ConfigRepo(d)))
}

// ConfigStore gives the site a repository of signed configuration versions,
// a daemon that serves it read only to the mesh, and a timer that fetches
// from peers what this site lacks, all of it the login user's, whose home
// is given: the units are its own, under the linger host admit enabled.
// Each step is planned only where the host differs from it.
func (u ubuntu) ConfigStore(t Transport, d deployment.Deployment, self config.Site, peers []string, home string) (Section, error) {
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
			Describe: "configuration store: create the bare repository at " + repo + ", the deployment's user's alone",
			Command:  repoCommand(d),
		})
	}

	data := map[string]string{
		"Prefix": d.Prefix(), "Token": d.Token(), "Address": self.Address, "Receive": ConfigReceiveShell,
		"ConfigDir": ConfigDir(d), "Repo": ConfigRepo(d), "Peers": ConfigPeers(d), "Log": ConfigSyncLog(d),
		"Script": ConfigScriptDir(d) + "/config-sync.sh",
	}
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
	scope := userScope(home)
	unitDir := scope.dir
	files := []struct {
		file, content, what string
		mode                uint32
		unit                bool
	}{
		{ConfigPeers(d), peerList, "the peers to fetch from", 0o644, false},
		{ConfigScriptDir(d) + "/config-sync.sh", script, "the sync script", 0o755, false},
		{unitDir + "/" + daemonUnit, daemon, "the unit " + daemonUnit, 0o644, true},
		{unitDir + "/" + syncUnit, syncService, "the unit " + syncUnit, 0o644, true},
		{unitDir + "/" + timerUnit, timer, "the unit " + timerUnit, 0o644, true},
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
		daemonChanged = daemonChanged || f.file == unitDir+"/"+daemonUnit
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "write " + path.Base(f.file),
			Describe: "configuration store: write " + f.what + " at " + f.file,
			File:     &File{Path: f.file, Content: f.content, Mode: f.mode},
		})
	}

	daemonEnabled, daemonActive, err := u.unitState(t, scope, daemonUnit)
	if err != nil {
		return out, err
	}
	timerEnabled, timerActive, err := u.unitState(t, scope, timerUnit)
	if err != nil {
		return out, err
	}
	// The daemon listens on the mesh address, which exists only while
	// WireGuard is up. A user unit cannot be ordered after the system's
	// wg-quick, so its start waits for that address, and it is started here
	// whether or not the mesh is up yet, with --no-block: the mesh comes up
	// after host prepare, so a start that waited for it would never return.
	if unitsChanged || !(daemonEnabled && daemonActive && timerEnabled && timerActive) {
		// reenable for a changed unit, so links its earlier [Install] made
		// go with it.
		enable, start := "enable", "start"
		if daemonChanged {
			enable, start = "reenable", "restart"
		}
		command := fmt.Sprintf("%[1]s daemon-reload && %[1]s enable --now %[2]s && %[1]s %[3]s %[4]s && %[1]s %[5]s --no-block %[4]s", scope.systemctl, timerUnit, enable, daemonUnit, start)
		out.Steps = append(out.Steps, Step{
			Kinds:    kinds,
			Title:    "start the configuration store",
			Describe: fmt.Sprintf("configuration store: enable %s and %s as the deployment's user, and start them", daemonUnit, timerUnit),
			Command:  command,
		})
	} else {
		out.Present = append(out.Present, fmt.Sprintf("configuration store: %s and %s enabled and running", daemonUnit, timerUnit))
	}
	return out, nil
}
