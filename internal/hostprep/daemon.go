package hostprep

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/mesh"
)

// daemonConfig is the user's daemon.json, in a fixed field order.
type daemonConfig struct {
	UserlandProxy bool          `json:"userland-proxy"`
	Pools         []addressPool `json:"default-address-pools"`
	DataRoot      string        `json:"data-root,omitempty"`
}

type addressPool struct {
	Base string `json:"base"`
	Size int    `json:"size"`
}

// DaemonJSON is the deployment user's daemon.json, which host admit writes
// and host prepare keeps: the userland proxy off, so a published port
// carries the client's own address (spec, Observed R6); Docker's default
// pools less any overlapping the mesh, each cut in /24s, so no network of the
// user's takes the mesh's range; and the data root host admit's --data-dir
// gave, recorded in the admission record.
func DaemonJSON(meshSubnet, dataDir string) (string, error) {
	subnet, err := mesh.ParsePrefix(meshSubnet)
	if err != nil {
		return "", err
	}
	cfg := daemonConfig{UserlandProxy: false, DataRoot: dataDir}
	for _, b := range mesh.DefaultPools {
		pool, err := mesh.ParsePrefix(b)
		if err != nil {
			return "", err
		}
		if !mesh.Overlap(pool, subnet) {
			cfg.Pools = append(cfg.Pools, addressPool{Base: b, Size: 24})
		}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// containersProbe counts the containers the user's daemon runs.
const containersProbe = "docker ps -q | wc -l"

// planDaemonJSON keeps the user's daemon.json as DaemonJSON has it, written
// as the user through a temporary file. A daemon running a container is
// never restarted under it: the change applies at its next restart, and the
// plan warns.
func planDaemonJSON(t Transport, home, meshSubnet string, rec admission.Record) (Section, error) {
	var out Section
	want, err := DaemonJSON(meshSubnet, rec.DataDir)
	if err != nil {
		return out, err
	}
	dir := home + "/.config/docker"
	path := dir + "/daemon.json"
	have, found, err := t.ReadFile(path)
	if err != nil {
		return out, err
	}
	if found && have == want {
		out.Present = append(out.Present, "docker: "+path+" as admit wrote it")
		return out, nil
	}
	running, err := t.Run(containersProbe)
	if err != nil {
		return out, fmt.Errorf("asking the deployment's Docker what it runs: %w", err)
	}
	tmp := path + ".paisans-tmp"
	command := fmt.Sprintf("set -e; umask 077; mkdir -p %s; printf %%s %s | base64 -d > %s; mv -f %s %s",
		shellQuote(dir), shellQuote(base64.StdEncoding.EncodeToString([]byte(want))), shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	describe := "docker: write " + path + ": userland proxy off, address pools clear of the mesh"
	idle := strings.TrimSpace(running) == "0"
	if idle {
		command += "; " + UserSystemctl + " restart docker"
		describe += ", and restart the daemon, which runs no container"
	}
	out.Steps = append(out.Steps, Step{Kinds: []string{KindDocker}, Title: "write daemon.json", Describe: describe, Command: command})
	if !idle {
		out.Warnings = append(out.Warnings, Warning{
			Hint:   "docker: the deployment's Docker reads its new daemon.json at its next restart",
			Detail: "It runs containers, and host prepare never restarts a daemon under them. Restart it when the deployment can stop for a moment: " + UserSystemctl + " restart docker."})
	}
	return out, nil
}
