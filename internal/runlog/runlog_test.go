package runlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogRecordsStepsAndCommandsWithASummary(t *testing.T) {
	var b strings.Builder
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	l := newLog(&b, func() time.Time { return at })
	end := l.Step("classify the sites")
	at = at.Add(1500 * time.Millisecond)
	l.SSH("site-a", "true\nsecond line", "apply.Reach", 300*time.Millisecond, nil)
	l.SSH("site-b", "true", "apply.Reach", 10*time.Second, errors.New("timed out"))
	l.SSH("site-a", "dpkg --print-architecture", "hostarch.Read", 100*time.Millisecond, nil)
	end("ok", "11.2s")
	l.Close()

	out := b.String()
	for _, want := range []string{
		"step  open  classify the sites",
		"site-a: true ...  [apply.Reach]",
		"site-b FAILED: true  [apply.Reach]",
		"step  ok    classify the sites: 11.2s (1.50s)",
		"finished after 1.50s",
		"     2     0.40s  site-a",
		"     2    10.30s  apply.Reach",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "10.00s  site-b") > strings.Index(out, "0.40s  site-a") {
		t.Errorf("the summary does not list the costliest site first:\n%s", out)
	}
}

func TestANilLogRecordsNothing(t *testing.T) {
	var l *Log
	l.Step("a")("ok", "")
	l.SSH("site", "true", "x", time.Second, nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenKeepsTheNewestLogs(t *testing.T) {
	base := t.TempDir()
	dir := Dir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range Keep + 5 {
		name := fmt.Sprintf("20200101T0000%02dZ-deployment-apply.log", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Open(base, "0.0.0", "deployment apply", []string{"--execute"})
	if err != nil {
		t.Fatal(err)
	}
	if Current() != l {
		t.Errorf("the opened log is not current")
	}
	l.Close()
	if Current() != nil {
		t.Errorf("a closed log is still current")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != Keep {
		t.Errorf("%d logs kept, want %d", len(entries), Keep)
	}
	if _, err := os.Stat(filepath.Join(dir, "20200101T000000Z-deployment-apply.log")); !os.IsNotExist(err) {
		t.Errorf("the oldest log was kept")
	}
	content, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "paisans 0.0.0: deployment apply --execute\n") {
		t.Errorf("the log opens with %q", strings.SplitN(string(content), "\n", 2)[0])
	}
	if info, _ := os.Stat(l.Path()); info.Mode().Perm() != 0o600 {
		t.Errorf("the log's mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestCallerSkipsTheNamedFunctions(t *testing.T) {
	got := func() string { return Caller("runlog.TestCallerSkipsTheNamedFunctions.func") }()
	if got != "runlog.TestCallerSkipsTheNamedFunctions" {
		t.Errorf("Caller = %q", got)
	}
}

func TestAFailureKeepsWhatTheHostSaid(t *testing.T) {
	var b strings.Builder
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	l := newLog(&b, func() time.Time { return at })
	end := l.Step("recreate chat")
	l.SSH("site-a", "docker compose up -d", "apply.runAction", time.Second,
		errors.New("site-a: docker compose up -d: exit status 1\nError response from daemon: ports are not available: exposing port TCP 0.0.0.0:80\n"))
	l.SSH("site-a", "true", "apply.Reach", time.Second, errors.New("site-a: true: exit status 1"))
	end("fail", "site-a: docker compose up -d: exit status 1\nError response from daemon: ports are not available")
	l.Close()

	out := b.String()
	for _, want := range []string{
		"site-a FAILED: docker compose up -d  [apply.runAction]\n" +
			"                 | Error response from daemon: ports are not available: exposing port TCP 0.0.0.0:80\n",
		"step  fail  recreate chat: site-a: docker compose up -d: exit status 1 ... (0.00s)\n" +
			"                 | Error response from daemon: ports are not available\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "apply.Reach]\n                 |") {
		t.Errorf("a failure with nothing past its first line has a detail line:\n%s", out)
	}
}

func TestAFailuresDetailIsBounded(t *testing.T) {
	var b strings.Builder
	l := newLog(&b, time.Now)
	long := "site-a: x: exit status 1"
	for i := range MaxDetail + 10 {
		long += fmt.Sprintf("\nline %d", i)
	}
	l.SSH("site-a", "x", "apply.runAction", time.Second, errors.New(long))
	out := b.String()
	if strings.Contains(out, fmt.Sprintf("line %d\n", MaxDetail)) {
		t.Errorf("the detail runs past %d lines:\n%s", MaxDetail, out)
	}
	if !strings.Contains(out, "| ... 10 more lines\n") {
		t.Errorf("the detail does not say how much it left out:\n%s", out)
	}
}
