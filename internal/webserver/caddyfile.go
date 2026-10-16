package webserver

import "strings"

// caddyBlocks reads a Caddyfile's blocks: the depth of braces each line
// starts at, and the line its global options block opens on, the first
// block at the top level with no key before it, or -1 when there is none.
// A brace is a token of its own; one inside a quoted token, or inside a
// placeholder such as {host}, is not.
func caddyBlocks(conf string) ([]int, int) {
	lines := strings.Split(conf, "\n")
	depth := make([]int, len(lines))
	d, options, keyed := 0, -1, false
	for i, line := range lines {
		depth[i] = d
		toks := caddyTokens(line)
		// An import at the top level is a directive of its own, not the
		// key of a block.
		directive := d == 0 && len(toks) > 0 && toks[0] == "import"
		for _, tok := range toks {
			switch {
			case tok == "{":
				if d == 0 && !keyed && options < 0 {
					options = i
				}
				d, keyed = d+1, false
			case tok == "}":
				if d > 0 {
					d--
				}
			case d == 0 && !directive:
				keyed = true
			}
		}
	}
	return depth, options
}

// caddyTokens splits one line of a Caddyfile into its tokens, up to a
// comment: a token starting with #.
func caddyTokens(line string) []string {
	var toks []string
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#':
			return toks
		case c == '"' || c == '`':
			j := i + 1
			for j < len(line) && line[j] != c {
				if c == '"' && line[j] == '\\' {
					j++
				}
				j++
			}
			toks = append(toks, line[i:min(j+1, len(line))])
			i = j + 1
		default:
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' && line[j] != '\r' {
				j++
			}
			toks = append(toks, line[i:j])
			i = j
		}
	}
	return toks
}

// quote is s quoted for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
