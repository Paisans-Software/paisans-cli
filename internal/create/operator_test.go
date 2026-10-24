package create

import "testing"

// A username YAML reads as null, a boolean or a number is refused at the
// prompt, since create and operator add write it plain.
func TestCheckOperatorIDRefusesNonStrings(t *testing.T) {
	for _, id := range []string{"null", "true", "false", "123", "0x1f"} {
		if checkOperatorID(id) == nil {
			t.Errorf("%s accepted", id)
		}
	}
	for _, id := range []string{"alice", "yes", "on", "a1"} {
		if err := checkOperatorID(id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}
