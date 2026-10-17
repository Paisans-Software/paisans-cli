package apply

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FirstJSONArray decodes into v the first whole JSON array in out, a
// command's combined output. A program in a container can log to stderr
// beside its answer, before or after it, and the transport returns both
// together: patronictl logs every etcd endpoint it cannot reach. The array
// starts a line; anything after it is left unread. Output with no whole
// array, or one cut short, is an error, never an empty answer.
func FirstJSONArray(out string, v any) error {
	var last error
	offset := 0
	for _, line := range strings.SplitAfter(out, "\n") {
		start := offset
		offset += len(line)
		if !strings.HasPrefix(strings.TrimLeft(line, " \t\r"), "[") {
			continue
		}
		var raw json.RawMessage
		if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&raw); err != nil {
			last = err
			continue
		}
		if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '[' {
			continue
		}
		if err := json.Unmarshal(raw, v); err != nil {
			last = err
			continue
		}
		return nil
	}
	if last != nil {
		return last
	}
	return errors.New("no JSON list in the output")
}

// patroniLogLine is a line of Patroni's own logging, as patronictl writes it
// to stderr: "2026-10-17 07:18:38,024 - ERROR - Failed to get list of
// machines from ...".
var patroniLogLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} - [A-Z]+ - `)

// PatroniConfig reads `patronictl show-config`'s YAML from its combined
// output, without the lines of Patroni's own logging. An empty document is
// an error, so a missing answer never reads as a configuration with every
// setting off.
func PatroniConfig(out string) (map[string]any, error) {
	var live map[string]any
	if err := yaml.Unmarshal([]byte(withoutPatroniLog(out)), &live); err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, errors.New("it printed no settings")
	}
	return live, nil
}

// withoutPatroniLog is patronictl's combined output without the lines of
// its own logging.
func withoutPatroniLog(out string) string {
	var kept []string
	for _, line := range strings.SplitAfter(out, "\n") {
		if !patroniLogLine.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}
