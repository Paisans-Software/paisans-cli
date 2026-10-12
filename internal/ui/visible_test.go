package ui

import "testing"

// Visible leaves printable text as it is and shows every rune a terminal
// would act on as an escape, so nothing it returns can move the cursor.
func TestVisible(t *testing.T) {
	for in, want := range map[string]string{
		"bob@laptop":               "bob@laptop",
		"ghcr.io/x/mbin:1.0 → 1.1": "ghcr.io/x/mbin:1.0 → 1.1",
		"bob\x1b[1A\x1b[2K~ key":   `bob\x1b[1A\x1b[2K~ key`,
		"tab\there":                `tab\there`,
		"del\x7f c1\u009b2K":       `del\x7f c1\u009b2K`,
		"bidi\u202eevil":           `bidi\u202eevil`,
		"bad \xff byte":            `bad \xff byte`,
		"line\r\nbreak":            `line\r\nbreak`,
	} {
		if got := Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
}
