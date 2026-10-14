package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/hostprep"
	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

// Cleaning a host deletes the deployment's configuration store: the
// repository, the peers file, the sync log, a pushed bundle a push left
// behind and the sync script's directory, and with the units its glob
// already covers, nothing of it is left.
func TestCleaningAHostDeletesItsConfigurationStore(t *testing.T) {
	w := setup(t)
	vm := w.hosts["vm"]
	repo, peers, syncLog, scripts := hostprep.ConfigRepo(dep), hostprep.ConfigPeers(dep), hostprep.ConfigSyncLog(dep), hostprep.ConfigScriptDir(dep)
	incoming := hostprep.ConfigIncoming(dep)
	other := "/var/lib/paisans/config/0c1d.git/HEAD"
	for path, content := range map[string]string{
		repo + "/HEAD": "ref: refs/heads/main\n", repo + "/objects/ab/cdef": "x",
		peers: "10.44.0.2\n", syncLog: "{}\n", syncLog + ".tmp": "{", scripts + "/config-sync.sh": "#!/bin/sh\n",
		incoming: "a bundle",
		"/etc/systemd/system/paisans-f2a9-config-daemon.service": "[Unit]\n",
		"/etc/systemd/system/paisans-f2a9-config-sync.timer":     "[Timer]\n",
		other: "another deployment's\n",
	} {
		vm.files[path] = content
	}
	dest, _ := config.ParseDestination("ubuntu@192.0.2.10")
	p, err := siteremove.BuildForced(w.cfg.WithoutSite("vm"), w.secrets, "vm", dest, vm, siteremove.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{repo, peers, syncLog, syncLog + ".tmp", incoming, scripts} {
		if !hasStepIn(stageNamed(p, "clean the host"), "vm", "delete", path) {
			t.Errorf("no deletion of %s:\n%s", path, printed(p))
		}
	}
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
	for path := range vm.files {
		if strings.HasPrefix(path, repo) || strings.HasPrefix(path, scripts) || path == peers || path == syncLog || path == syncLog+".tmp" || path == incoming || strings.HasPrefix(path, "/etc/systemd/system/paisans-f2a9-config-") {
			t.Errorf("%s is still on the host", path)
		}
	}
	if _, ok := vm.files[other]; !ok {
		t.Error("another deployment's repository was deleted")
	}
}
