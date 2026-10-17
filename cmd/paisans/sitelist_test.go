package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listTable runs a list command against the fixture configuration, named by
// --config from a directory that holds none, and returns its rows by site.
func listTable(t *testing.T, run func([]string, io.Writer) error) (map[string][]string, string) {
	t.Helper()
	configPath, _ := secretsDir(t, "version: 1\n")
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := run([]string{"--config", configPath}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	rows := map[string][]string{}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		rows[f[0]] = f[1:]
	}
	return rows, lines[0]
}

// site list names every declared site, sorted, with its roles and mesh
// address, from the file --config names rather than the working directory.
func TestSiteListNamesEverySiteWithItsRoles(t *testing.T) {
	rows, header := listTable(t, runSiteList)
	if strings.Join(strings.Fields(header), " ") != "SITE ROLES ADDRESS" {
		t.Errorf("header %q", header)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 sites, got %v", rows)
	}
	if got := rows["vm"]; len(got) != 2 || got[0] != "gateway,witness" {
		t.Errorf("vm: %v", got)
	}
	if got := rows["home-a"]; len(got) != 2 || got[0] != "data,apps" {
		t.Errorf("home-a: %v", got)
	}
}

// host list shows where each site's host is reached, with the defaults
// filled in: a site with no public address shows a dash there.
func TestHostListShowsEachSitesDestination(t *testing.T) {
	rows, header := listTable(t, runHostList)
	if strings.Join(strings.Fields(header), " ") != "SITE SSH PUBLIC PUBLIC6" {
		t.Errorf("header %q", header)
	}
	if got := rows["watch"]; len(got) != 3 || got[0] != "ubuntu@watch.example.org:22" || got[1] != "203.0.113.20" {
		t.Errorf("watch: %v", got)
	}
	if got := rows["home-a"]; len(got) != 3 || got[0] != "ubuntu@home-a.local:22" || got[1] != "-" {
		t.Errorf("home-a: %v", got)
	}
}

// Without --config, both read paisans-stack.yaml in the working directory,
// and a directory without one is an error rather than an empty list.
func TestListReadsTheWorkingDirectoryByDefault(t *testing.T) {
	configPath, _ := secretsDir(t, "version: 1\n")
	t.Chdir(filepath.Dir(configPath))
	var out bytes.Buffer
	if err := runSiteList(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "home-b") {
		t.Errorf("got %q", out.String())
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := runHostList(nil, &out); err == nil {
		t.Error("want an error with no paisans-stack.yaml")
	}
}

func TestListTakesNoArgument(t *testing.T) {
	var out bytes.Buffer
	if err := runSiteList([]string{"home-a"}, &out); err == nil {
		t.Error("site list home-a: want an error")
	}
}
