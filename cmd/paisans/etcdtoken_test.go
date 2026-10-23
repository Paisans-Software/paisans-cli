package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/render"
)

// A member with no record founds with the token another founder recorded,
// with PatroniScope when that founder recorded none, and otherwise with this
// run's random token, the same on every call; a member that cannot be read
// stops it.
func TestEtcdFoundingTokenAgreesWithTheOtherFounders(t *testing.T) {
	cfg, err := config.Load(fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Etcd.Members) < 2 {
		t.Fatalf("the fixture has etcd members %v", cfg.Etcd.Members)
	}
	site, other := cfg.Etcd.Members[0], cfg.Etcd.Members[1]
	saved := convergeReadInitial
	defer func() { convergeReadInitial = saved }()
	answer := func(m string) (render.EtcdInitial, bool, error) { return render.EtcdInitial{}, false, nil }
	convergeReadInitial = func(_ *config.Config, m string) (render.EtcdInitial, bool, error) { return answer(m) }

	first, err := etcdFoundingToken(cfg, site)
	if err != nil || !strings.HasPrefix(first, "paisans-f2a9-") || len(first) != len("paisans-f2a9-")+16 {
		t.Fatalf("no founder recorded: %q %v", first, err)
	}
	if again, _ := etcdFoundingToken(cfg, site); again != first {
		t.Errorf("one run chose two tokens: %q and %q", first, again)
	}

	answer = func(m string) (render.EtcdInitial, bool, error) {
		if m == other {
			return render.EtcdInitial{State: render.EtcdStateNew, Cluster: "x", Token: "paisans-f2a9-recorded"}, true, nil
		}
		return render.EtcdInitial{}, false, nil
	}
	if got, err := etcdFoundingToken(cfg, site); err != nil || got != "paisans-f2a9-recorded" {
		t.Errorf("another founder's token: %q %v", got, err)
	}

	answer = func(m string) (render.EtcdInitial, bool, error) {
		if m == other {
			return render.EtcdInitial{State: render.EtcdStateNew, Cluster: "x"}, true, nil
		}
		return render.EtcdInitial{}, false, nil
	}
	if got, err := etcdFoundingToken(cfg, site); err != nil || got != "" {
		t.Errorf("a founder recorded before tokens: %q %v, want PatroniScope's empty token", got, err)
	}

	answer = func(m string) (render.EtcdInitial, bool, error) {
		if m == other {
			return render.EtcdInitial{State: render.EtcdStateExisting, Cluster: "x"}, true, nil
		}
		return render.EtcdInitial{}, false, nil
	}
	if got, _ := etcdFoundingToken(cfg, site); got != first {
		t.Errorf("a joiner's record was taken for a founder's: %q", got)
	}

	answer = func(string) (render.EtcdInitial, bool, error) {
		return render.EtcdInitial{}, false, errors.New("unreachable")
	}
	if _, err := etcdFoundingToken(cfg, site); err == nil {
		t.Error("an unread member did not stop it")
	}
}
