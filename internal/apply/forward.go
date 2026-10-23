package apply

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Dial opens a TCP connection to address, host:port, from the host: ssh's
// stdio forwarding (`ssh -W`), so the host resolves the name and its own
// route carries the connection, Eg: out through its egress, or across the
// mesh to a relay only the mesh reaches. Nothing runs on the host for it, and
// nothing is installed there.
//
// A remote command runs first, `true`, so that the connection to the host is
// made the way every other command makes it: retried when it never opened,
// asking about an unknown host key with the display held, and left open as
// the shared master the forward then rides on (see sshmux.go). A host that
// cannot be reached is an error wrapping ErrUnreachable, as for Run.
//
// The forward needs the host's sshd to allow TCP forwarding, OpenSSH's
// default (AllowTcpForwarding in sshd_config(5)). Over the shared master, a
// host that refuses it reaches the forward only as "Session open refused by
// peer", the same words as an address the host could not connect to, since
// sshd's own reason goes to the master's stderr; without a master ssh says
// "administratively prohibited". Either way the connection's first read
// ends with ErrForwardNotOpened.
func (t SSHTransport) Dial(address string) (net.Conn, error) {
	if _, err := t.run("true", nil); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	cleanup := func() {}
	if t.Destination != "" {
		cmd = exec.Command("ssh", t.ForwardArgs(nil, address)...)
	} else {
		if len(t.PublicKeys) == 0 {
			return nil, fmt.Errorf("%s: no public key to offer. Name an admin with a key in the site's ssh.keys, or pass --ssh", t.Describe())
		}
		dir, err := os.MkdirTemp("", "paisans-ssh-")
		if err != nil {
			return nil, err
		}
		cleanup = func() { os.RemoveAll(dir) }
		files, err := writeKeyFiles(dir, t.PublicKeys)
		if err != nil {
			cleanup()
			return nil, err
		}
		cmd = exec.Command("ssh", t.ForwardArgs(files, address)...)
	}
	conn, err := forward(cmd, t.Describe()+" to "+address)
	if err != nil {
		cleanup()
		return nil, err
	}
	conn.cleanup = cleanup
	return conn, nil
}

// ForwardArgs is ssh's argument list to forward stdio to address from the
// host, with the public key files at keyFiles, as SSHArgs builds one for a
// command: the shared connection's options first, and on the --ssh route the
// destination verbatim.
func (t SSHTransport) ForwardArgs(keyFiles []string, address string) []string {
	args := muxArgs()
	if t.Destination != "" {
		return append(args, "-W", address, t.Destination)
	}
	args = append(args, t.sectionOptions()...)
	for _, f := range keyFiles {
		args = append(args, "-i", f)
	}
	return append(args, "-W", address, t.User+"@"+t.Host)
}

// forwardConn is a net.Conn over an ssh's stdin and stdout. Its local half is
// one end of a net.Pipe, so deadlines work as on any connection, and two
// copies move the bytes between the other end and the ssh.
type forwardConn struct {
	net.Conn
	cmd     *exec.Cmd
	where   string
	stderr  *lockedBuffer
	done    chan struct{}
	once    sync.Once
	cleanup func()
}

// forward starts cmd, an ssh -W, as a connection.
func forward(cmd *exec.Cmd, where string) (*forwardConn, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: starting ssh: %w", where, err)
	}
	local, remote := net.Pipe()
	c := &forwardConn{Conn: local, cmd: cmd, where: where, stderr: stderr, done: make(chan struct{}), cleanup: func() {}}
	go func() {
		_, _ = io.Copy(stdin, remote)
		stdin.Close()
	}()
	go func() {
		_, _ = io.Copy(remote, stdout)
		// Every read of stdout is done, so ssh can be waited for, and its
		// stderr is whole before the local half sees the end.
		_ = cmd.Wait()
		close(c.done)
		remote.Close()
	}()
	return c, nil
}

// ErrForwardNotOpened is a forward whose connection the host never opened:
// ssh reported that the channel failed to open, Eg: "channel 0: open
// failed: connect failed: Connection refused" or "Stdio forwarding request
// failed: Session open refused by peer", because the address did not
// answer the host or the host's sshd does not forward TCP. ssh starts the
// forward before the host tries the address, so the dial succeeds and the
// connection's first read is where it fails.
var ErrForwardNotOpened = errors.New("the host did not open the forwarded connection")

// notOpened is what ssh prints, on any line of its stderr, when the host
// could not open the forward's channel: the address refused or was
// unroutable, or sshd refused to forward at all. Over a shared master it is
// one line; on a connection of its own, ssh follows "channel 0: open
// failed: ..." with "stdio forwarding failed" as it exits. Matched without
// case.
var notOpened = []string{"open failed", "forwarding request failed", "stdio forwarding failed"}

// forwardNotOpened is the line of ssh's stderr that says the channel was
// not opened, the most specific first, or "".
func forwardNotOpened(stderr string) string {
	for _, want := range notOpened {
		for _, line := range strings.Split(stderr, "\n") {
			if strings.Contains(strings.ToLower(line), want) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// forwardEnd is the end of a forward ssh gave a reason for: io.EOF, and
// ErrForwardNotOpened when the reason is that the channel never opened.
type forwardEnd struct {
	msg       string
	notOpened bool
}

func (e *forwardEnd) Error() string { return e.msg }

func (e *forwardEnd) Unwrap() []error {
	if e.notOpened {
		return []error{ErrForwardNotOpened, io.EOF}
	}
	return []error{io.EOF}
}

// Read is the forwarded connection's, and at its end says why ssh ended it,
// when ssh said: Eg: that the host could not connect to the address, which
// is ErrForwardNotOpened.
func (c *forwardConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if errors.Is(err, io.EOF) {
		<-c.done
		stderr := c.stderr.String()
		if why := forwardNotOpened(stderr); why != "" {
			return n, &forwardEnd{msg: fmt.Sprintf("%s: ssh: %s: %v", c.where, why, io.EOF), notOpened: true}
		}
		if why := lastLine(stderr); why != "" {
			return n, &forwardEnd{msg: fmt.Sprintf("%s: ssh: %s: %v", c.where, why, io.EOF)}
		}
	}
	return n, err
}

// Close ends the forward and the ssh carrying it.
func (c *forwardConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		select {
		case <-c.done:
		default:
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			<-c.done
		}
		c.cleanup()
	})
	return err
}

// lockedBuffer is a bytes.Buffer ssh writes its stderr to while the
// connection may read it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(l.b.String())
}
