package hostarch_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/hostarch"
)

type host struct {
	out string
	err error
}

func (h host) Run(command string) (string, error) {
	if command != hostarch.Command {
		return "", errors.New("unexpected " + command)
	}
	return h.out, h.err
}

func TestReadAcceptsEachSupportedArchitecture(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := hostarch.Read("home-a", host{out: arch + "\n"})
		if err != nil || got != arch {
			t.Errorf("%s: got %q, %v", arch, got, err)
		}
	}
}

func TestReadRefusesAnotherArchitectureByName(t *testing.T) {
	_, err := hostarch.Read("home-c", host{out: "armhf\n"})
	if err == nil || !strings.Contains(err.Error(), "home-c: the host reports armhf. The toolkit supports amd64 and arm64.") {
		t.Fatalf("got %v", err)
	}
}

func TestReadNamesTheSiteWhenTheProbeFails(t *testing.T) {
	_, err := hostarch.Read("home-a", host{out: "dpkg: not found", err: errors.New("exit status 127")})
	if err == nil || !strings.Contains(err.Error(), "home-a") {
		t.Fatalf("got %v", err)
	}
}

func TestMismatchNamesBothAndTheLineToDelete(t *testing.T) {
	err := hostarch.Mismatch("home-b", "amd64", "arm64")
	want := "home-b: paisans-stack.yaml records arch amd64, and the host reports arm64. If this is new hardware, delete the arch line from home-b and run again."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if hostarch.Mismatch("home-b", "arm64", "arm64") != nil || hostarch.Mismatch("home-b", "", "arm64") != nil {
		t.Fatal("agreeing or unrecorded is not a mismatch")
	}
}

func TestRunsMatchesOnTheArchitectureAlone(t *testing.T) {
	cases := []struct {
		platforms []string
		arch      string
		want      bool
	}{
		{[]string{"amd64", "arm64/v8"}, "arm64", true},
		{[]string{"amd64"}, "arm64", false},
		{[]string{"windows/amd64"}, "amd64", false},
		{[]string{"arm64"}, "arm64", true},
		{nil, "amd64", false},
	}
	for _, c := range cases {
		if got := hostarch.Runs(c.platforms, c.arch); got != c.want {
			t.Errorf("Runs(%v, %s) = %v", c.platforms, c.arch, got)
		}
	}
}
