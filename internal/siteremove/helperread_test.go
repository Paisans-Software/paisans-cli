package siteremove_test

import (
	"regexp"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/render"
)

// chainedRead answers the WireGuard helper's reads that apply chains into
// one command (echo paisans-show:FIELD && sudo -n HELPER show FIELD && ...)
// by running each part through run, as a shell would. ok is false for any
// other command.
func chainedRead(command string, run func(string) (string, error)) (out string, ok bool, err error) {
	if !strings.HasPrefix(command, "echo paisans-show:") {
		return "", false, nil
	}
	var b strings.Builder
	for _, part := range strings.Split(command, " && ") {
		if mark, isEcho := strings.CutPrefix(part, "echo "); isEcho {
			b.WriteString(mark + "\n")
			continue
		}
		got, err := run(part)
		b.WriteString(got)
		if err != nil {
			return b.String(), true, err
		}
	}
	return b.String(), true, nil
}

// showRunning answers the helper's show field for an interface that runs
// conf, as wg show prints it. ok is false for a field it does not read.
func showRunning(field, conf string) (out string, ok bool) {
	var peers []map[string]string
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if line == "[Peer]" {
			peers = append(peers, map[string]string{})
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found && len(peers) > 0 {
			peers[len(peers)-1][strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	var b strings.Builder
	switch field {
	case "public-key":
		m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)`).FindStringSubmatch(conf)
		if m == nil {
			return "(none)\n", true
		}
		key, err := render.PublicKey(m[1])
		if err != nil {
			return "(none)\n", true
		}
		return key + "\n", true
	case "listen-port":
		// The kernel picks a port for an interface whose file has none.
		if m := regexp.MustCompile(`(?m)^ListenPort = (\S+)`).FindStringSubmatch(conf); m != nil {
			return m[1] + "\n", true
		}
		return "40123\n", true
	case "peers":
		for _, peer := range peers {
			b.WriteString(peer["PublicKey"] + "\n")
		}
	case "allowed-ips":
		for _, peer := range peers {
			ips := "(none)"
			if peer["AllowedIPs"] != "" {
				ips = strings.ReplaceAll(peer["AllowedIPs"], ", ", " ")
			}
			b.WriteString(peer["PublicKey"] + "\t" + ips + "\n")
		}
	case "persistent-keepalive":
		for _, peer := range peers {
			keepalive := peer["PersistentKeepalive"]
			if keepalive == "" || keepalive == "0" {
				keepalive = "off"
			}
			b.WriteString(peer["PublicKey"] + "\t" + keepalive + "\n")
		}
	default:
		return "", false
	}
	return b.String(), true
}
