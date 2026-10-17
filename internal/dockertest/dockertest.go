// Package dockertest is what every Docker-backed test, behind its build tag,
// calls first: the deployment's daemon is a rootless one, so a test that
// passes against a rootful daemon proves nothing about it.
package dockertest

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Rootless reports whether docker info's security options, as JSON, name
// rootless.
func Rootless(securityOptions string) bool {
	var opts []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(securityOptions)), &opts); err != nil {
		return false
	}
	return slices.Contains(opts, "name=rootless")
}

// RequireRootless skips the test when Docker is absent or not running, and
// fails it when the daemon is not rootless, unless PAISANS_DOCKER_ROOTFUL=1
// says to run against a rootful one, which it then logs.
func RequireRootless(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	out, err := exec.Command("docker", "info", "--format", "{{json .SecurityOptions}}").Output()
	if err != nil {
		t.Skipf("docker is not running: %v", err)
	}
	if Rootless(string(out)) {
		return
	}
	if os.Getenv("PAISANS_DOCKER_ROOTFUL") == "1" {
		t.Logf("running against a rootful Docker (PAISANS_DOCKER_ROOTFUL=1): this proves nothing about a deployment's rootless daemon")
		return
	}
	t.Fatalf("the Docker daemon DOCKER_HOST reaches is not rootless (security options %s). Point DOCKER_HOST at a rootless daemon, Eg: unix:///run/user/$(id -u)/docker.sock, or set PAISANS_DOCKER_ROOTFUL=1 to run against this one", strings.TrimSpace(string(out)))
}
