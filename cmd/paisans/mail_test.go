package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/secretsgen"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck"
	"github.com/paisans-software/paisans-cli/internal/smtpcheck/smtptest"
)

// fixtureSMTPPassword is the fixture's external.smtp_password.
const fixtureSMTPPassword = "fixture-not-a-secret-smtp"

// fakeSMTP answers every mail check with an in-process server that logs in
// only users, by username and password; nil takes any.
func fakeSMTP(t *testing.T, users map[string]string) *smtptest.Server {
	t.Helper()
	server := smtptest.New()
	server.Users = users
	saved := smtpEnv
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env { return server.Env() }
	t.Cleanup(func() { smtpEnv = saved })
	return server
}

// The fixture's operators, whom a test message goes to.
var fixtureOperators = []string{"alice@example.org", "bob@example.org"}

// An SMTP password set with secrets set logs in and sends the operators one
// test message from each sender, from a site running the apps, before it is
// written.
func TestSecretsSetTriesAnSMTPPassword(t *testing.T) {
	const next = "not-a-real-smtp-password-0001"
	server := fakeSMTP(t, map[string]string{"robot@example.org": next})
	configPath, secretsPath := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if _, err := set(t, configPath, secretsPath, "external.smtp_password", next+"\n"); err != nil {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	if got := mustSecrets(t, secretsPath).External["smtp_password"]; got != next {
		t.Errorf("stored %q", got)
	}
	msgs := server.Messages()
	// talk, auth and account send as hello@example.org; health sends as the
	// same address with a name of its own, so one message covers them all.
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].From != "hello@example.org" || !slices.Equal(msgs[0].To, fixtureOperators) {
		t.Errorf("sent from %s to %v", msgs[0].From, msgs[0].To)
	}
	if !strings.Contains(msgs[0].Data, "`paisans secrets set` is changing external.smtp_password") {
		t.Errorf("message:\n%s", msgs[0].Data)
	}
	if !rec.Has("done", "smtp.example.org:587, starttls, as robot@example.org, from home-a") {
		t.Errorf("the check is not shown:\n%s", rec.Lines())
	}
	if strings.Contains(rec.Lines()+msgs[0].Data, next) {
		t.Error("the password was shown")
	}
}

// One the server refuses is not written, and the refusal names the server's
// reply and not the password.
func TestSecretsSetRefusesAnSMTPPasswordThatDoesNotLogIn(t *testing.T) {
	const wrong = "not-a-real-smtp-password-wrong"
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	configPath, secretsPath := rotateDeployment(t, nil)
	withRecorder(t, false)
	before, _ := os.ReadFile(secretsPath)
	_, err := set(t, configPath, secretsPath, "external.smtp_password", wrong)
	if err == nil {
		t.Fatal("a password the server refuses was written")
	}
	for _, want := range []string{"external.smtp_password does not work", "535 5.7.8", "Nothing was written"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %v lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), wrong) {
		t.Error("the refusal shows the password")
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
	if len(server.Messages()) != 0 {
		t.Error("a message was sent")
	}
}

// A site that cannot be reached leaves the password untried, and so unwritten.
func TestSecretsSetRefusesWhenTheSiteIsNotReached(t *testing.T) {
	server := fakeSMTP(t, nil)
	server.Unreachable = map[string]error{"home-a": errors.New("ssh could not connect to the host")}
	configPath, secretsPath := rotateDeployment(t, nil)
	withRecorder(t, false)
	before, _ := os.ReadFile(secretsPath)
	_, err := set(t, configPath, secretsPath, "external.smtp_password", "not-a-real-smtp-password-0002")
	if err == nil || !strings.Contains(err.Error(), "home-a could not be reached") || !strings.Contains(err.Error(), "Nothing was written") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
}

// The walk of owed secrets tries an SMTP password the same way: one the
// server refuses stops the walk, and only what was written before it is.
func TestSecretsSetWalkTriesAnSMTPPassword(t *testing.T) {
	fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	configPath, secretsPath := rotateDeployment(t, func(text string) string {
		return strings.Replace(text, `smtp_password: "`+fixtureSMTPPassword+`"`, `smtp_password: ""`, 1)
	})
	withRecorder(t, false)
	saved := readOwedSecret
	readOwedSecret = func(o secretsgen.Owed) (string, error) {
		if o.Name == "external.smtp_password" {
			return "not-a-real-smtp-password-wrong", nil
		}
		return "", nil
	}
	t.Cleanup(func() { readOwedSecret = saved })
	stdin := terminalStdin(t, "", true)
	err := runSecretsSet([]string{"--config", configPath, "--secrets", secretsPath}, stdin)
	if err == nil || !strings.Contains(err.Error(), "535") || !strings.Contains(err.Error(), "Nothing was written") {
		t.Fatalf("err = %v", err)
	}
	if got := mustSecrets(t, secretsPath).External["smtp_password"]; got != "" {
		t.Errorf("stored %q", got)
	}
}

// A pasted SMTP password logs in and sends one test message when pasted, and
// is written under rotations only then; stage 2 logs in again and sends
// nothing more.
func TestSecretsRotateTriesAPastedSMTPPassword(t *testing.T) {
	const next = "not-a-real-smtp-password-0003"
	server := fakeSMTP(t, map[string]string{"robot@example.org": next})
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	pastedAtTerminal(t, map[string]string{"external.smtp_password": next})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", true), &bytes.Buffer{})
	// The fake host refuses the apply after stage 2.
	if err == nil {
		t.Fatal("the fake host took an apply")
	}
	if !rec.Has("done", "stage 2, where it is checked") {
		t.Errorf("stage 2 did not pass:\n%s", rec.Lines())
	}
	if n := len(server.Messages()); n != 1 {
		t.Errorf("%d messages sent, want the one when pasted", n)
	}
	if n := len(server.Logins()); n < 2 {
		t.Errorf("%d logins, want the paste's and stage 2's", n)
	}
	if got := mustSecrets(t, secretsPath).Rotations["external.smtp_password"]; got != next {
		t.Errorf("rotations hold %q", got)
	}
}

// One the server refuses is refused when pasted, before any host is claimed
// and with nothing under rotations.
func TestSecretsRotateRefusesAPastedSMTPPasswordThatDoesNotLogIn(t *testing.T) {
	const wrong = "not-a-real-smtp-password-wrong"
	fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	pastedAtTerminal(t, map[string]string{"external.smtp_password": wrong})
	before, _ := os.ReadFile(secretsPath)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "535 5.7.8") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	if strings.Contains(err.Error(), wrong) {
		t.Error("the refusal shows the password")
	}
	if w.claimed() {
		t.Error("a host was claimed")
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
}

// A value put in the file some other way than secrets set, which does not
// log in, is refused when the run is planned, before anything is written.
func TestSecretsRotateRefusesASetSMTPPasswordThatDoesNotLogIn(t *testing.T) {
	fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	configPath, secretsPath = rewriteSecrets(t, configPath, secretsPath, func(text string) string {
		return strings.Replace(text, fixtureSMTPPassword, "not-a-real-smtp-password-hand-edited", 1)
	})
	before, _ := os.ReadFile(secretsPath)
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", false), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not pass where it is checked") || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	if w.claimed() {
		t.Error("a host was claimed")
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
}

// apply logs in with every app's mail settings from its site before it
// changes anything, and refuses on a failure, naming the app, its setting
// and the server's reply.
func TestApplyRefusesMailThatDoesNotLogIn(t *testing.T) {
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	configPath, secretsPath := rotateDeployment(t, nil)
	log := failingSSH(t)
	withRecorder(t, false)
	for _, extra := range [][]string{{"--site", "home-a"}, nil} {
		err := runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--sudo=false", "--execute"}, extra...), strings.NewReader(""), io.Discard)
		if err == nil {
			t.Fatalf("apply %v was not refused", extra)
		}
		for _, want := range []string{"does not log in to its mail server", "declared by smtp", "From home-a the server answered: log in: 535 5.7.8", "external.smtp_password", "Nothing was changed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("apply %v: err %v lacks %q", extra, err, want)
			}
		}
		if strings.Contains(err.Error(), fixtureSMTPPassword) {
			t.Error("the refusal shows the password")
		}
	}
	if len(server.Messages()) != 0 {
		t.Error("apply sent a message")
	}
	reached, _ := os.ReadFile(log)
	for _, change := range []string{"paisans-tmp", "mkdir", "docker compose"} {
		if strings.Contains(string(reached), change) {
			t.Errorf("a refused apply changed a host (%s):\n%s", change, reached)
		}
	}
}

// apply --site --only tries only the apps in scope.
func TestApplyTriesOnlyTheAppsInScope(t *testing.T) {
	cfg := mustLoad(t, fixtureConfig())
	secrets := mustSecrets(t, fixtureSecretsPath())
	all := mailChecks(cfg, secrets, []string{"home-a"}, nil)
	only := mailChecks(cfg, secrets, []string{"home-a"}, []string{"talk"})
	if len(all) == 0 || len(only) != 1 || !slices.Equal(only[0].Apps, []string{"talk"}) {
		t.Errorf("all %+v, only talk %+v", all, only)
	}
	if none := mailChecks(cfg, secrets, []string{"home-a"}, []string{"blog"}); len(none) != 0 {
		t.Errorf("an app that sends no mail is tried: %+v", none)
	}
}

// validate tries every app's mail settings and only warns: a login the
// server refuses, and a site it cannot reach, which skips the check.
func TestValidateWarnsAboutMail(t *testing.T) {
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	server.Unreachable = map[string]error{"watch": fmt.Errorf("deploy@watch: %w", apply.ErrUnreachable)}
	configPath, secretsPath := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if err := runValidate([]string{"--config", configPath, "--secrets", secretsPath}); err != nil {
		t.Fatalf("validate failed on a mail warning: %v", err)
	}
	if !rec.Has("warned", "from home-a") || rec.Index("explain", "From home-a the server answered: log in: 535 5.7.8") < 0 {
		t.Errorf("the refused login is not a warning:\n%s", rec.Lines())
	}
	if rec.Index("explain", "watch could not be reached (ssh could not connect), so health's mail was not checked") < 0 {
		t.Errorf("the unreachable site is not a skipped check:\n%s", rec.Lines())
	}
	if !rec.Has("result", "from the mail check") {
		t.Errorf("result:\n%s", rec.Lines())
	}
	if len(server.Messages()) != 0 {
		t.Error("validate sent a message")
	}
}

// --offline reaches no site, and a secrets file that cannot be read skips
// the check with a note; neither fails validate.
func TestValidateMailOfflineAndWithoutSecrets(t *testing.T) {
	server := fakeSMTP(t, nil)
	configPath, _ := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if err := runValidate([]string{"--config", configPath, "--offline"}); err != nil {
		t.Fatal(err)
	}
	if len(server.Dials()) != 0 || rec.Has("section", "mail") {
		t.Errorf("--offline tried the mail settings:\n%s", rec.Lines())
	}
	rec = withRecorder(t, false)
	if err := runValidate([]string{"--config", configPath, "--secrets", configPath + ".missing"}); err != nil {
		t.Fatal(err)
	}
	if !rec.Has("note", "the mail settings were not checked") || len(server.Dials()) != 0 {
		t.Errorf("an unreadable secrets file:\n%s", rec.Lines())
	}
}
