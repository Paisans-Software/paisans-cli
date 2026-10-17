package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Visible is s with every rune a terminal would not simply draw shown as a
// Go escape, Eg: ESC as \x1b, and printable text left as it is. A key
// comment, an image reference or a line of a host file is somebody else's
// text: printed raw, ESC or CSI and a few printable characters would move
// the cursor and rewrite a line already printed, so a review could be made
// to read as something it is not. Everything the review and doctor print
// from the configuration or from a host passes through it. Validation refuses
// these characters in the configuration already; this is the second line, for
// a host file and for anything validation does not reach.
func Visible(s string) string {
	clean := true
	for _, r := range s {
		if !visible(r) {
			clean = false
			break
		}
	}
	if clean && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case visible(r):
			b.WriteRune(r)
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
		i += size
	}
	return b.String()
}

// visible reports whether a terminal draws r as itself: a printable rune or
// a plain space.
func visible(r rune) bool {
	return r != utf8.RuneError && (r == ' ' || unicode.IsPrint(r))
}
