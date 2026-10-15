package apply

import (
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestForwardArgs(t *testing.T) {
	t.Cleanup(func() { mux.made, mux.dir = false, "" })
	mux.made, mux.dir = true, "" // no shared connection, so the arguments are the forward's own
	tr := SSHTransport{User: "deploy", Host: "203.0.113.10", Port: 2222}
	got := tr.ForwardArgs([]string{"/k/key-1.pub"}, "smtp.example.org:587")
	want := []string{"-p", "2222", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=10", "-i", "/k/key-1.pub", "-W", "smtp.example.org:587", "deploy@203.0.113.10"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
	over := SSHTransport{Destination: "home-a"}
	if got := over.ForwardArgs(nil, "[fd00::25]:25"); !slices.Equal(got, []string{"-W", "[fd00::25]:25", "home-a"}) {
		t.Errorf("--ssh args = %q", got)
	}
}

// A forward is a connection over the command's stdin and stdout; cat stands
// in for ssh -W, and echoes what it is sent.
func TestForwardCarriesBytesBothWays(t *testing.T) {
	c, err := forward(exec.Command("cat"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "EHLO localhost\r\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "EHLO localhost\r\n" {
		t.Errorf("read %q", buf)
	}
}

// ssh that cannot open the forward says why on stderr and exits: the
// connection's end carries its reason.
func TestForwardEndSaysWhy(t *testing.T) {
	c, err := forward(exec.Command("sh", "-c", `echo "channel 0: open failed: connect failed: Connection refused" >&2; exit 255`), "deploy@home-a to smtp.example.org:587")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "Connection refused") || !strings.Contains(err.Error(), "smtp.example.org:587") {
		t.Errorf("err = %v", err)
	}
}

func TestForwardCloseEndsTheCommand(t *testing.T) {
	c, err := forward(exec.Command("sleep", "30"), "test")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the command")
	}
}
