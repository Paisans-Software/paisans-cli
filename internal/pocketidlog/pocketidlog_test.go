package pocketidlog_test

import (
	"testing"
	"time"

	"github.com/paisans-software/paisans-cli/internal/pocketidlog"
)

const textFailure = `Oct 11 18:53:41 INF HTTP request completed method=POST path=/api/users/u-1/one-time-access-email status=204
Oct 11 18:53:41 ERR Failed to send one-time access token email error="failed to send email: dial tcp 10.0.0.9:587: i/o timeout" address=fern@example.org
`

const jsonFailure = `{"time":"2026-10-11T18:53:41Z","level":"ERROR","msg":"Failed to send one-time access token email","error":"failed to send email: 535 auth failed","address":"fern@example.org"}
`

// A failure logged for the admin's address is found, with Pocket ID's own
// error, in the default text format and in LOG_JSON's.
func TestEmailFailureFindsTheAddress(t *testing.T) {
	for name, tc := range map[string]struct{ logs, want string }{
		"text": {textFailure, "failed to send email: dial tcp 10.0.0.9:587: i/o timeout"},
		"json": {jsonFailure, "failed to send email: 535 auth failed"},
	} {
		got, ok := pocketidlog.EmailFailure(tc.logs, "fern@example.org")
		if !ok || got != tc.want {
			t.Errorf("%s: EmailFailure = %q, %v; want %q", name, got, ok, tc.want)
		}
	}
}

// Another admin's failure, or no failure, is not this admin's.
func TestEmailFailureIgnoresOtherAddresses(t *testing.T) {
	for _, logs := range []string{textFailure, jsonFailure, "", "Oct 11 18:53:41 INF started\n"} {
		if got, ok := pocketidlog.EmailFailure(logs, "sam@example.org"); ok {
			t.Errorf("EmailFailure(%q) = %q, want none", logs, got)
		}
	}
}

// An address is matched whole: one that only ends with it is someone else.
func TestEmailFailureMatchesTheWholeAddress(t *testing.T) {
	if _, ok := pocketidlog.EmailFailure(textFailure, "ern@example.org"); ok {
		t.Error("matched a suffix of the address")
	}
}

// The log command reads one compose service's log for a window, and nothing
// in it varies but the project and the window.
func TestLogCommand(t *testing.T) {
	got := pocketidlog.LogCommand("paisans-f2a9-auth", 45*time.Second)
	want := "docker compose -p 'paisans-f2a9-auth' logs --no-color --no-log-prefix --since 45s app 2>&1"
	if got != want {
		t.Errorf("LogCommand = %q\nwant %q", got, want)
	}
}

// Brief cuts Pocket ID's layers of "failed to" and the connection string
// format off an email error, and leaves any other error as it is.
func TestBrief(t *testing.T) {
	for in, want := range map[string]string{
		"failed to configure emailer: failed to init email sender 'smtp': invalid connection string: missing SMTP password; required format is 'smtp://<username>:<password>@<host>:<port>?fromAddress=<address>'": "invalid connection string: missing SMTP password",
		"dial tcp: i/o timeout":                       "dial tcp: i/o timeout",
		"failed to send email: 535 5.7.8 auth failed": "535 5.7.8 auth failed",
		"failed to send email":                        "failed to send email",
	} {
		if got := pocketidlog.Brief(in); got != want {
			t.Errorf("pocketidlog.Brief(%q) = %q, want %q", in, got, want)
		}
	}
}
