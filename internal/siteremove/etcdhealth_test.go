package siteremove_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/siteremove"
)

// etcdctl exits 1 when any endpoint is unhealthy, and with --host-gone the
// gone site's own member is: the members that stay are healthy, so the
// removal goes ahead.
func TestHostGoneProceedsPastItsOwnUnhealthyMember(t *testing.T) {
	w := setup(t)
	w.unreachable["home-b"] = true
	w.etcdDown["home-b"] = true
	w.member("home-b")["Role"], w.member("home-b")["State"] = "Replica", "stopped"
	w.member("home-c")["Role"] = "Sync Standby"
	p := w.mustBuild("home-b", siteremove.Options{HostGone: true})
	if err := siteremove.Execute(p); err != nil {
		t.Fatal(err)
	}
}

// A member that stays and is unhealthy refuses the removal, by name, even
// when the gone site's own member is unhealthy too.
func TestHostGoneRefusesAnotherUnhealthyMember(t *testing.T) {
	w := setup(t)
	w.unreachable["home-b"] = true
	w.etcdDown["home-b"], w.etcdDown["home-c"] = true, true
	_, err := w.build("home-b", siteremove.Options{HostGone: true})
	if err == nil || !strings.Contains(err.Error(), "home-c's etcd is unhealthy") {
		t.Fatalf("err = %v, want home-c named as unhealthy", err)
	}
	if strings.Contains(err.Error(), "home-b's etcd is unhealthy") {
		t.Errorf("the gone site's own member is blamed: %v", err)
	}
}

// Output with no endpoint list in it refuses rather than guessing.
func TestEndpointHealthThatIsNotJSONRefuses(t *testing.T) {
	for _, hostGone := range []bool{false, true} {
		w := setup(t)
		w.healthOut = "Error: context deadline exceeded\n"
		if hostGone {
			w.unreachable["home-b"] = true
			w.etcdDown["home-b"] = true
		}
		_, err := w.build("home-b", siteremove.Options{HostGone: hostGone})
		if err == nil || !strings.Contains(err.Error(), "etcd's endpoint health") {
			t.Fatalf("--host-gone %v: err = %v, want the endpoint health refused", hostGone, err)
		}
	}
}

// patronictl logs beside its answer when it tries the gone site's etcd
// endpoint, and the removal reads the answer past them.
func TestPatronictlLogLinesDoNotStopTheRemoval(t *testing.T) {
	for _, hostGone := range []bool{false, true} {
		w := setup(t)
		w.patroniLog = true
		if hostGone {
			w.unreachable["home-b"] = true
			w.etcdDown["home-b"] = true
			w.member("home-b")["Role"], w.member("home-b")["State"] = "Replica", "stopped"
			w.member("home-c")["Role"] = "Sync Standby"
		}
		p, err := w.build("home-b", siteremove.Options{HostGone: hostGone})
		if err != nil {
			t.Fatalf("--host-gone %v: %v", hostGone, err)
		}
		if err := siteremove.Execute(p); err != nil {
			t.Fatalf("--host-gone %v: %v", hostGone, err)
		}
	}
}
