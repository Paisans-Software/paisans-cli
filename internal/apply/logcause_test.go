package apply_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/apply"
	"github.com/paisans-software/paisans-stack/internal/ui"
)

// etcdLogs is the tail of an etcd that cannot bind its peer address, as
// `docker compose logs` prints it: one JSON object a line, the fatal one with
// a stack trace, and an informational line after it.
const etcdLogs = `etcd-1  | {"level":"info","ts":"2026-10-11T09:00:00Z","caller":"embed/etcd.go:128","msg":"configuring peer listeners","listen-peer-urls":["http://10.45.0.1:2380"]}
etcd-1  | {"level":"warn","ts":"2026-10-11T09:00:00Z","msg":"retrying","error":"a warning, not the cause"}
etcd-1  | {"level":"error","ts":"2026-10-11T09:00:00Z","caller":"embed/etcd.go:536","msg":"creating peer listener failed","error":"listen tcp 10.45.0.1:2380: bind: cannot assign requested address","stacktrace":"go.etcd.io/etcd/server/v3/embed.configurePeerListeners\n\tgo.etcd.io/etcd/server/v3/embed/etcd.go:536"}
etcd-1  | {"level":"fatal","ts":"2026-10-11T09:00:00Z","caller":"etcdmain/etcd.go:204","msg":"discovery failed","error":"listen tcp 10.45.0.1:2380: bind: cannot assign requested address","stacktrace":"go.etcd.io/etcd/server/v3/etcdmain.startEtcdOrProxyV2\n\tgo.etcd.io/etcd/server/v3/etcdmain/etcd.go:204\nmain.main\n\tgo.etcd.io/etcd/server/v3/main.go:40"}
etcd-1  | {"level":"info","ts":"2026-10-11T09:00:01Z","msg":"closing peer listeners"}
`

func TestLogCauseReadsTheErrorFieldOfJSONLogs(t *testing.T) {
	got := apply.LogCause(etcdLogs)
	if got != "listen tcp 10.45.0.1:2380: bind: cannot assign requested address" {
		t.Fatalf("got %q", got)
	}
}

func TestLogCauseFallsBackToMsgOfAnErrorLevelLine(t *testing.T) {
	got := apply.LogCause(`app-1  | {"level":"ERROR","msg":"database unreachable"}` + "\n")
	if got != "database unreachable" {
		t.Fatalf("got %q", got)
	}
}

func TestLogCauseReadsPlainTextLogs(t *testing.T) {
	logs := "app-1  | starting up\n" +
		"app-1  | Error: SQLSTATE[08006] could not connect to server\n" +
		"app-1  | retrying in 5s\n"
	if got := apply.LogCause(logs); got != "Error: SQLSTATE[08006] could not connect to server" {
		t.Fatalf("got %q", got)
	}
}

// A Go panic's trace is not the cause; the panic line is.
func TestLogCauseSkipsAGoStackTrace(t *testing.T) {
	logs := "app-1  | panic: runtime error: invalid memory address\n" +
		"app-1  | \n" +
		"app-1  | goroutine 1 [running]:\n" +
		"app-1  | panic({0x1234, 0x5678})\n" +
		"app-1  | \t/usr/local/go/src/runtime/panic.go:1038 +0x215\n" +
		"app-1  | main.main()\n" +
		"app-1  | \t/src/main.go:12 +0x1d\n"
	if got := apply.LogCause(logs); got != "panic: runtime error: invalid memory address" {
		t.Fatalf("got %q", got)
	}
}

func TestLogCauseFindsNothingInQuietLogs(t *testing.T) {
	for _, logs := range []string{
		"",
		"app-1  | listening on :8080\n",
		`app-1  | {"level":"info","msg":"no error here","error":""}` + "\n",
	} {
		if got := apply.LogCause(logs); got != "" {
			t.Errorf("%q: got %q", logs, got)
		}
	}
}

func TestLogCauseIsCapped(t *testing.T) {
	got := apply.LogCause("app-1  | error: " + strings.Repeat("x", 400) + "\n")
	if n := len([]rune(got)); n > 160 {
		t.Fatalf("a cause of %d characters", n)
	}
}

// logsHost answers `ps` with one restarting etcd and `logs` with its tail.
type logsHost struct {
	psHost
	logs string
}

func (h logsHost) Run(command string) (string, error) {
	if strings.Contains(command, " logs ") {
		return h.logs, nil
	}
	return `{"Service":"etcd","State":"restarting","ExitCode":0}`, nil
}

// By default a stack that is not healthy says which service, one line of why,
// and what was and was not done; the raw log tail is for --verbose.
func TestAnUnhealthyStackShowsItsCauseAndKeepsTheLogsForVerbose(t *testing.T) {
	err := apply.WaitHealthy(apply.Fixture, "home-a", "infra", logsHost{logs: etcdLogs}, "The apply stopped here")
	if err == nil {
		t.Fatal("a restarting etcd passed")
	}
	var plain bytes.Buffer
	ui.PrintError(&plain, err, false)
	out := plain.String()
	for _, want := range []string{"stack infra is not healthy", "etcd (restarting", "etcd: listen tcp 10.45.0.1:2380: bind: cannot assign requested address", "The apply stopped here"} {
		if !strings.Contains(out, want) {
			t.Errorf("default output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"log lines", "etcd-1", "stacktrace", "closing peer listeners"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("default output carries %q:\n%s", unwanted, out)
		}
	}
	var verbose bytes.Buffer
	ui.PrintError(&verbose, err, true)
	for _, want := range []string{"--- etcd, last 30 log lines ---", "closing peer listeners"} {
		if !strings.Contains(verbose.String(), want) {
			t.Errorf("verbose output lacks %q:\n%s", want, verbose.String())
		}
	}
}

// Logs with nothing that reads as an error add no cause line.
func TestAnUnhealthyStackWithQuietLogsAddsNoCause(t *testing.T) {
	err := apply.WaitHealthy(apply.Fixture, "home-a", "infra", logsHost{logs: "etcd-1  | starting\n"}, "The apply stopped here")
	if _, explain := ui.Describe(err); explain != "The apply stopped here" {
		t.Errorf("a cause line from quiet logs:\n%s", explain)
	}
}
