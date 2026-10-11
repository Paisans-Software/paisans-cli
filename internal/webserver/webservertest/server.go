// Package webservertest is a fake host running a web server, for the tests of
// the commands that write the toolkit's files into its include directory.
// It answers the commands internal/webserver sends, keeps the files they
// write, and records every command, so a test reads what reached the host.
package webservertest

import (
	"errors"
	"slices"
	"strings"
)

// Server is a host with a web server on it. It is a Runner for both the ssh
// user and sudo: Denied makes the ssh user's writes fail for want of
// permission, as under /etc, so they go through sudo.
type Server struct {
	// Files are the host's files by path.
	Files map[string]string
	// Config is what reading the server's configuration answers: the
	// Caddyfile, nginx -T's output, or Apache's include lines.
	Config string
	// Labels is the container's labels as docker inspect prints them, a
	// JSON object; empty is a container with none.
	Labels string
	// Validate fails the configuration check with its text when set,
	// Reload the reload, and Restart a container's restart.
	Validate, Reload, Restart string
	// Units are the systemd units active on the host.
	Units []string
	// Denied refuses the ssh user every write.
	Denied bool
	// Unreachable fails every command as ssh does for a host it cannot
	// reach.
	Unreachable bool

	// Asked is every command, each prefixed "sudo " when through sudo.
	Asked []string
}

// New is a server with no files.
func New(config string) *Server { return &Server{Files: map[string]string{}, Config: config} }

// Plain is the server as the ssh user, and Sudo through sudo.
func (s *Server) Plain() Runner { return Runner{s, false} }
func (s *Server) Sudo() Runner  { return Runner{s, true} }

// Runner is one way of running commands on the server.
type Runner struct {
	s    *Server
	sudo bool
}

func (r Runner) Run(command string) (string, error) { return r.s.run(command, r.sudo) }

// Ran reports whether any command contained key.
func (s *Server) Ran(key string) bool {
	for _, c := range s.Asked {
		if strings.Contains(c, key) {
			return true
		}
	}
	return false
}

func (s *Server) run(command string, sudo bool) (string, error) {
	label := command
	if sudo {
		label = "sudo " + command
	}
	s.Asked = append(s.Asked, label)
	if s.Unreachable {
		return "", errors.New("watch cannot be reached over ssh")
	}
	args := quoted(command)
	write := strings.Contains(command, "printf ") || strings.HasPrefix(command, "rm -f")
	if write && s.Denied && !sudo {
		return "mkdir: cannot create directory: Permission denied\n", errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(command, "systemctl is-active"):
		f := strings.Fields(command)
		if slices.Contains(s.Units, f[len(f)-1]) {
			return "", nil
		}
		return "", errors.New("exit status 3")
	case strings.HasPrefix(command, "docker inspect "):
		if s.Labels == "" {
			return "null\n", nil
		}
		return s.Labels + "\n", nil
	case strings.HasPrefix(command, "cat ") && len(args) > 0 && s.Files[args[0]] != "":
		return s.Files[args[0]], nil
	case strings.HasPrefix(command, "docker restart "):
		if s.Restart != "" {
			return s.Restart + "\n", errors.New("exit status 1")
		}
		return args[0] + "\n", nil
	case strings.Contains(command, "then cat"):
		if content, ok := s.Files[args[0]]; ok {
			return content, nil
		}
		return "__PAISANS_ABSENT__\n", nil
	case strings.Contains(command, "printf "):
		// mkdir -p dir; printf '%s' content > tmp; chmod tmp; mv tmp path
		s.Files[args[len(args)-1]] = args[2]
		return "", nil
	case strings.HasPrefix(command, "rm -f -- "):
		delete(s.Files, args[0])
		return "", nil
	case strings.Contains(command, "validate") || strings.Contains(command, "nginx -t") || strings.Contains(command, "configtest"):
		if s.Validate != "" {
			return s.Validate + "\n", errors.New("exit status 1")
		}
		return "Valid configuration\n", nil
	case strings.Contains(command, "reload") || strings.Contains(command, "graceful"):
		if s.Reload != "" {
			return s.Reload + "\n", errors.New("exit status 1")
		}
		return "", nil
	case strings.Contains(command, "cat ") || strings.Contains(command, "nginx -T") || strings.Contains(command, "grep -rhiE"):
		return s.Config, nil
	}
	return "sh: not found", errors.New("exit status 127")
}

// quoted is every single-quoted word of a command, unquoted, in order: the
// way internal/webserver quotes each path and content.
func quoted(command string) []string {
	var out []string
	for i := 0; i < len(command); i++ {
		if command[i] != '\'' {
			continue
		}
		var b strings.Builder
		for {
			j := strings.IndexByte(command[i+1:], '\'')
			if j < 0 {
				return out
			}
			b.WriteString(command[i+1 : i+1+j])
			i += j + 1
			if strings.HasPrefix(command[i:], `'\''`) {
				b.WriteByte('\'')
				i += 3
				continue
			}
			break
		}
		out = append(out, b.String())
	}
	return out
}
