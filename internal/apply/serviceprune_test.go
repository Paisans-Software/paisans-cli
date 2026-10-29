package apply_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// rmHost answers `docker rm -f`, refusing the containers in refuse.
type rmHost struct {
	volumeHost
	refuse map[string]bool
}

func (h *rmHost) Run(command string) (string, error) {
	h.commands = append(h.commands, command)
	if name, ok := strings.CutPrefix(command, "docker rm -f "); ok {
		name = strings.Trim(name, "'")
		if h.refuse[name] {
			return "Error response from daemon: cannot remove " + name, fmt.Errorf("exit status 1")
		}
		return name + "\n", nil
	}
	return "", nil
}

// Each container is removed by name, quoted; one Docker refuses does not stop
// the rest, and the failure names it.
func TestRemoveContainersRemovesEachAndReportsFailures(t *testing.T) {
	host := &rmHost{refuse: map[string]bool{"paisans-f2a9-talk-rabbitmq-1": true}}
	err := apply.RemoveContainers("home-a", []string{"paisans-f2a9-talk-rabbitmq-1", "paisans-f2a9-talk-amqproxy-1"}, host)
	if err == nil || !strings.Contains(err.Error(), "home-a: 1 container(s) were not removed") || !strings.Contains(err.Error(), "paisans-f2a9-talk-rabbitmq-1: Error response from daemon") {
		t.Fatalf("err = %v", err)
	}
	want := []string{"docker rm -f 'paisans-f2a9-talk-rabbitmq-1'", "docker rm -f 'paisans-f2a9-talk-amqproxy-1'"}
	if strings.Join(host.commands, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands = %q, want %q", host.commands, want)
	}
}
