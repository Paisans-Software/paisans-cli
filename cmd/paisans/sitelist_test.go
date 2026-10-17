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

// site list names every declared site, sorted, with its roles, its mesh
// address and where its host is reached, defaults filled in, from the file
// --config names rather than the working directory. A site with no public
// address shows a dash there.
func TestSiteListNamesEverySite(t *testing.T) {
	rows, header := listTable(t, runSiteList)
	if strings.Join(strings.Fields(header), " ") != "SITE ROLES ADDRESS SSH PUBLIC PUBLIC6" {
		t.Errorf("header %q", header)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 sites, got %v", rows)
	}
	want := map[string]string{
		"vm":     "gateway,witness 10.44.0.3 ubuntu@vm.example.org:22 - -",
		"home-a": "data,apps 10.44.0.1 ubuntu@home-a.local:22 - -",
		"watch":  "monitor 10.44.0.4 ubuntu@watch.example.org:22 203.0.113.20 -",
	}
	for site, w := range want {
		if got := strings.Join(rows[site], " "); got != w {
			t.Errorf("%s: got %q, want %q", site, got, w)
		}
	}
}

// Without --config, site list reads paisans-stack.yaml in the working directory,
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
	if err := runSiteList(nil, &out); err == nil {
		t.Error("want an error with no paisans-stack.yaml")
	}
}

func TestListTakesNoArgument(t *testing.T) {
	var out bytes.Buffer
	if err := runSiteList([]string{"home-a"}, &out); err == nil {
		t.Error("site list home-a: want an error")
	}
}
