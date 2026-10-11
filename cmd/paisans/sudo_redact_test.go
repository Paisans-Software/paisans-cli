package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/ui"
	"github.com/paisans-software/paisans-stack/internal/webserver"
)

// sudoPlaceholder stands in for a sudo password. It is not anyone's.
const sudoPlaceholder = "not-a-real-password"

// leakySudo is the monitor's host through sudo, on a host that prints the
// sudo password back: the first line of every answer is the password, and
// every error carries it too. docker inspect alone answers as it is, so a
// compose project can be found to read. composeFile fails to be read.
type leakySudo struct {
	r           webserver.Runner
	composeFile string
}

func (l leakySudo) Run(command string) (string, error) {
	if l.composeFile != "" && strings.HasPrefix(command, "cat '"+l.composeFile+"'") {
		return sudoPlaceholder + "\ncat: " + l.composeFile + ": Permission denied\n", errors.New("exit status 1: " + sudoPlaceholder)
	}
	out, err := l.r.Run(command)
	if strings.HasPrefix(command, "docker inspect ") {
		return out, err
	}
	if err != nil {
		err = fmt.Errorf("%w: %s", err, sudoPlaceholder)
	}
	return sudoPlaceholder + "\n" + out, err
}

// deniedPlain is the host as the ssh user, who is not in the docker group
// and may not read the compose file, so both go through sudo.
type deniedPlain struct {
	r           webserver.Runner
	composeFile string
}

func (d deniedPlain) Run(command string) (string, error) {
	if strings.Contains(command, "docker") || (d.composeFile != "" && strings.Contains(command, d.composeFile)) {
		return "permission denied while trying to connect to the Docker daemon socket\n", errors.New("exit status 1")
	}
	return d.r.Run(command)
}

// leakyHost reaches f's server through those two.
func leakyHost(f *filesFake, composeFile string) webserver.Host {
	return webserver.Host{Plain: deniedPlain{f.server.Plain(), composeFile}, Sudo: leakySudo{f.server.Sudo(), composeFile}}
}

// Whatever a host prints back, the sudo password the operator typed never
// reaches the screen through the web server step: not detecting the server
// (docker refused, then through sudo), not writing, validating and
// reloading its site file, not reading its compose file, and not in the
// error the run ends on. Each path is run with --verbose, through the
// reporter that draws and the one tests record with.
func TestTheWebServerStepNeverShowsTheSudoPassword(t *testing.T) {
	// SudoAuth registers the password as it is typed; here it is
	// registered as that would.
	ui.Secret(sudoPlaceholder)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *filesFake
	}{
		{"detect", func(t *testing.T) *filesFake {
			f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
			findWebServer = func(_ *config.Config, target webserver.Target, _ bool) webserver.WebServer {
				return webserver.Detect(leakyHost(f, ""), target)
			}
			return f
		}},
		{"write and validate", func(t *testing.T) *filesFake {
			f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
			f.server.Denied = true
			f.server.Validate = "Error: adapting config using caddyfile: unrecognized directive"
			return f
		}},
		{"reload and restart", func(t *testing.T) *filesFake {
			f := fakeWebServerFiles(t, mountedCaddy, "import /etc/caddy/paisans.d/*\n")
			f.server.Denied = true
			f.server.Reload = "Error: sending configuration to instance: connection refused"
			f.server.Restart = "Error response from daemon: cannot restart container"
			return f
		}},
		{"compose", func(t *testing.T) *filesFake {
			return composeCaddy(t, "services:\n  web:\n    volumes:\n      - ./Caddyfile:/etc/caddy/Caddyfile\n")
		}},
	} {
		for _, drawn := range []bool{true, false} {
			fakeConverge(t, nil)
			path := externalFixture(t)
			f := tc.setup(t)
			compose := ""
			if tc.name == "compose" {
				compose = "/opt/x/compose.yaml"
			}
			webServerFilesHost = func(*config.Config, string, bool) webserver.Host { return leakyHost(f, compose) }
			var b bytes.Buffer
			var rec *ui.Recorder
			if drawn {
				saved := reporterOverride
				reporterOverride = ui.NewPlain(&b, true)
				t.Cleanup(func() { reporterOverride = saved })
			} else {
				rec = recordConverge(t)
				rec.Verbose_ = true
			}
			err := convergeAt(t, path, "--execute", "--verbose")
			if err != nil {
				reportError(&b, err, true)
			}
			if rec != nil {
				b.WriteString(rec.Lines())
			}
			if !strings.Contains(b.String(), "[redacted]") {
				t.Errorf("%s: the host's answers were never shown, so this shows nothing:\n%s", tc.name, b.String())
			}
			if strings.Contains(b.String(), sudoPlaceholder) {
				t.Errorf("%s (drawn %v): the output shows the sudo password:\n%s", tc.name, drawn, b.String())
			}
		}
	}
}

// failingSudo is a host whose sudo settling fails with the password in the
// error, as a host that printed it back would make it.
type failingSudo struct{ manifestHost }

func (failingSudo) SettlesSudo() bool { return true }
func (failingSudo) PrepareSudo() error {
	return fmt.Errorf("%w: %w", apply.ErrSudo, &ui.Problem{
		Hint:    "sudo on box did not accept the password",
		Explain: "sudo said: " + sudoPlaceholder,
		Cause:   errors.New("box: sudo did not accept the password:\n" + sudoPlaceholder),
	})
}

// The sudo section that settles each host's password before an apply, and
// the error it ends the run on, show nothing of the password either.
func TestSettlingSudoNeverShowsThePassword(t *testing.T) {
	ui.Secret(sudoPlaceholder)
	var b bytes.Buffer
	r := ui.NewPlain(&b, true)
	h := &failingSudo{manifestHost{files: map[string]string{}}}
	err := settleSudo(r, []string{"watch"}, func(string) apply.Transport { return h })
	if err == nil {
		t.Fatal("settling succeeded")
	}
	reportError(&b, err, true)
	if !strings.Contains(b.String(), "[redacted]") || strings.Contains(b.String(), sudoPlaceholder) {
		t.Errorf("output:\n%s", b.String())
	}
}
