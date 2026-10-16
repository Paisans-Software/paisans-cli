//go:build hostadmit_integration

// This test runs the built paisans binary, as root, in a real ubuntu:24.04
// container laid out as a stock host is: /etc/os-release and
// /etc/resolv.conf are links there. It plans admit and evict through the
// local transport, which is what runs on a host, so a read the fakes serve
// directly is read here the way the host reads it.
package hostadmit

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/admission"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// stockLinks makes the container's system files what they are on a stock
// Ubuntu 24.04 with systemd-resolved: os-release is already a link in the
// image, and resolv.conf, which Docker mounts as a file, becomes the link
// to the stub resolver's file. iproute2 and an enabled ufw, which a stock
// server has and admit needs, are set up, since the image lacks them.
const stockLinks = `set -e
apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iproute2 ufw >/dev/null && ufw allow 22/tcp >/dev/null && ufw --force enable >/dev/null
umount /etc/resolv.conf
mkdir -p /run/systemd/resolve
printf 'nameserver 127.0.0.53\noptions edns0 trust-ad\n' > /run/systemd/resolve/stub-resolv.conf
rm -f /etc/resolv.conf
ln -s ../run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
[ -L /etc/os-release ] && [ -L /etc/resolv.conf ]
set +e
`

func TestTheBinaryPlansOnAStockUbuntu(t *testing.T) {
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "paisans"), "github.com/paisans-software/paisans-cli/cmd/paisans")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cfg, err := config.Load(filepath.Join("..", "render", "testdata", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := admission.Build(cfg, "vm")
	if err != nil {
		t.Fatal(err)
	}
	// The deployment's own default user: with no admission record on the
	// host, it is the one user evict plans for.
	req.User = "paisans-f2a9"
	if err := os.WriteFile(filepath.Join(dir, "vm.admit"), admission.Encode(req), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"/p/paisans host admit --request /p/vm.admit --public-ports --data-dir /data",
		"/p/paisans host evict --request /p/vm.admit",
		"/p/paisans host evict --deployment " + req.Deployment + " --user " + req.User,
	}
	// One container runs all three, each reporting its exit status, so the
	// packages a stock server has are installed once.
	script := stockLinks
	for _, c := range commands {
		script += "echo '=== " + c + "'; " + c + "; echo \"=== exit $?\"\n"
	}
	out, err := exec.Command("docker", "run", "--rm", "--privileged", "--user", "0", "-v", dir+":/p:ro",
		// A volume, so --data-dir is on a filesystem Docker can store on.
		"--mount", "type=volume,dst=/data",
		probeImage, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Count(string(out), "=== exit 0") != len(commands) || strings.Contains(string(out), "FAIL") || strings.Contains(string(out), "symbolic link") {
		t.Errorf("a command failed:\n%s", out)
	}
	if !strings.Contains(string(out), "egress") {
		t.Errorf("admit planned no egress, so resolv.conf was not read:\n%s", out)
	}
}
