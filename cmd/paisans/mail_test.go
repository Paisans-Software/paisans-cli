package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/mesh"
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
	saved, savedLocal := smtpEnv, localSMTPEnv
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env { return server.Env() }
	localSMTPEnv = func() smtpcheck.Env { return server.Env() }
	t.Cleanup(func() { smtpEnv, localSMTPEnv = saved, savedLocal })
	return server
}

// The fixture's operators, whom a test message goes to.
var fixtureOperators = []string{"alice@example.org", "bob@example.org"}

// An SMTP password set with secrets set logs in and sends the operators one
// test message from each sender, from this machine, before it is written.
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
	if !rec.Has("done", "smtp.example.org:587, starttls, as robot@example.org, from this machine") {
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

// The password is tried from this machine, so it is set before any site is
// deployed: no site is dialled, and one that cannot be reached does not
// matter.
func TestSecretsSetTriesFromThisMachineWithNoSiteReached(t *testing.T) {
	const next = "not-a-real-smtp-password-0002"
	server := fakeSMTP(t, map[string]string{"robot@example.org": next})
	// Any dial through a site fails, as before a deployment: only this
	// machine's own dial reaches the server.
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env {
		return smtpcheck.Env{Dial: func(site, _ string) (net.Conn, error) {
			return nil, errors.New("ssh could not connect to " + site)
		}}
	}
	configPath, secretsPath := rotateDeployment(t, nil)
	withRecorder(t, false)
	if _, err := set(t, configPath, secretsPath, "external.smtp_password", next); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := mustSecrets(t, secretsPath).External["smtp_password"]; got != next {
		t.Errorf("stored %q", got)
	}
	if len(server.Messages()) == 0 {
		t.Error("no test message was sent")
	}
}

// A mail server this machine cannot reach leaves the password untried:
// it is written with a warning, since whether it works is the server's to
// say, and apply tries it from each site.
func TestSecretsSetWritesWhenThisMachineCannotReachTheServer(t *testing.T) {
	const next = "not-a-real-smtp-password-0003"
	server := fakeSMTP(t, nil)
	server.Unreachable = map[string]error{"": errors.New("dial tcp: connection refused")}
	configPath, secretsPath := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if _, err := set(t, configPath, secretsPath, "external.smtp_password", next); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := mustSecrets(t, secretsPath).External["smtp_password"]; got != next {
		t.Errorf("stored %q", got)
	}
	if !strings.Contains(rec.Lines(), "paisans deployment apply") {
		t.Errorf("the warning does not say apply tries it:\n%s", rec.Lines())
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

// refuseLocalDial makes this machine's own dial to the mail server fail, as
// an ISP blocking outbound 587 does. Dials through a site still work.
func refuseLocalDial(t *testing.T) {
	t.Helper()
	saved := localSMTPEnv
	localSMTPEnv = func() smtpcheck.Env {
		return smtpcheck.Env{Dial: func(string, string) (net.Conn, error) {
			return nil, errors.New("dial tcp 192.0.2.25:587: connect: connection refused")
		}}
	}
	t.Cleanup(func() { localSMTPEnv = saved })
}

// A mail server this machine cannot reach leaves a pasted SMTP password
// untried here, as `secrets set` leaves it: the run warns, naming this
// machine, and goes on. Before a site is handed the password, the password
// logs in from that site.
func TestSecretsRotateGoesOnWhenThisMachineCannotReachTheServer(t *testing.T) {
	const next = "not-a-real-smtp-password-0004"
	server := fakeSMTP(t, map[string]string{"robot@example.org": next})
	refuseLocalDial(t)
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	pastedAtTerminal(t, map[string]string{"external.smtp_password": next})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", true), &bytes.Buffer{})
	// The fake host refuses the apply after stage 2.
	if err == nil {
		t.Fatal("the fake host took an apply")
	}
	lines := rec.Lines()
	if !rec.Has("done", "stage 2, where it is checked") {
		t.Errorf("stage 2 did not pass:\n%s", lines)
	}
	for _, want := range []string{"from this machine", "connection refused", "tries it from there"} {
		if !strings.Contains(lines, want) {
			t.Errorf("the warning lacks %q:\n%s", want, lines)
		}
	}
	for _, bad := range []string{"reach the site", " could not be reached to try"} {
		if strings.Contains(lines+err.Error(), bad) {
			t.Errorf("output says %q:\n%s\n%v", bad, lines, err)
		}
	}
	var fromSite bool
	for _, d := range server.Dials() {
		fromSite = fromSite || d.Site != ""
	}
	if !fromSite || len(server.Logins()) == 0 {
		t.Errorf("the password was not tried from a site: dials %+v, logins %v", server.Dials(), server.Logins())
	}
	if got := mustSecrets(t, secretsPath).Rotations["external.smtp_password"]; got != next {
		t.Errorf("rotations hold %q", got)
	}
}

// When this machine cannot reach the server and a site's login is refused,
// the site is not handed the password, and the refusal names the reply.
func TestSecretsRotateStopsWhenTheSiteRefusesAnUntriedPassword(t *testing.T) {
	const next = "not-a-real-smtp-password-0005"
	fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	refuseLocalDial(t)
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	pastedAtTerminal(t, map[string]string{"external.smtp_password": next})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", true), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	if strings.Contains(err.Error()+rec.Lines(), next) {
		t.Error("the password was shown")
	}
	if strings.Contains(err.Error(), "fake: ") {
		t.Errorf("the site was handed the password: %v", err)
	}
}

// A password set beforehand, with secrets set, is the file's own value, yet
// it is still being handed out: when this machine could not try it, each
// site tries it before it is handed it, and a site that refuses it is not.
func TestSecretsRotateTriesASetPasswordFromTheSiteWhenThisMachineCannotReachTheServer(t *testing.T) {
	const next = "not-a-real-smtp-password-0007"
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	refuseLocalDial(t)
	w, rec := fakeRotateHosts(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	w.applied(t, configPath, secretsPath, nil)
	configPath, secretsPath = rewriteSecrets(t, configPath, secretsPath, func(text string) string {
		return strings.Replace(text, fixtureSMTPPassword, next, 1)
	})
	err := runSecretsRotate(rotateArgs(configPath, secretsPath, "external.smtp_password", "--execute"), terminalStdin(t, "", false), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v\n%s", err, rec.Lines())
	}
	if strings.Contains(err.Error(), "fake: ") {
		t.Errorf("the site was handed the password: %v", err)
	}
	var fromSite bool
	for _, d := range server.Dials() {
		fromSite = fromSite || d.Site != ""
	}
	if !fromSite {
		t.Errorf("not tried from a site: %+v", server.Dials())
	}
	if strings.Contains(err.Error()+rec.Lines(), next) {
		t.Error("the password was shown")
	}
}

// The same warning from secrets set names the connect stage and this
// machine, not a site.
func TestSecretsSetNamesThisMachineWhenItCannotReachTheServer(t *testing.T) {
	const next = "not-a-real-smtp-password-0006"
	fakeSMTP(t, nil)
	refuseLocalDial(t)
	configPath, secretsPath := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if _, err := set(t, configPath, secretsPath, "external.smtp_password", next); err != nil {
		t.Fatalf("err = %v", err)
	}
	lines := rec.Lines()
	if !strings.Contains(lines, "from this machine (connect: dial tcp") || strings.Contains(lines, "reach the site") {
		t.Errorf("the warning:\n%s", lines)
	}
	if got := mustSecrets(t, secretsPath).External["smtp_password"]; got != next {
		t.Errorf("stored %q", got)
	}
}

// One the server refuses is refused when pasted, before any host is checked
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
	if w.checked() {
		t.Error("a host was checked")
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
	if w.checked() {
		t.Error("a host was checked")
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
		err := runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, extra...), strings.NewReader(""), io.Discard)
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

// deployment apply --site --only tries only the apps in scope.
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

// meshMailDeployment is the fixture with its mail server on home-b's mesh
// address: plaintext with no login, as a relay run on a site's own mesh
// address for testing is, or, when not plaintext, starttls with the
// fixture's login.
func meshMailDeployment(t *testing.T, plaintext bool) (configPath, secretsPath string) {
	t.Helper()
	configPath, secretsPath = rotateDeployment(t, nil)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	mesh := "  host: 10.44.0.2\n  security: starttls\n  username: robot@example.org\n"
	if plaintext {
		mesh = "  host: 10.44.0.2\n  security: none\n"
	}
	text := strings.Replace(string(data), "  host: smtp.example.org\n  security: starttls\n  username: robot@example.org\n", mesh, 1)
	if text == string(data) {
		t.Fatal("the fixture's smtp block moved")
	}
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, secretsPath
}

// stubMeshLink answers meshLinkUp with up, by site, and returns the sites
// it was asked about.
func stubMeshLink(t *testing.T, up map[string]bool) *[]string {
	t.Helper()
	var asked []string
	saved := meshLinkUp
	meshLinkUp = func(_ *config.Config, site, _ string) (bool, error) {
		asked = append(asked, site)
		return up[site], nil
	}
	t.Cleanup(func() { meshLinkUp = saved })
	return &asked
}

// A mail server on the deployment's own mesh cannot be tried before the
// mesh is up, so the first apply warns and goes on rather than refusing a
// deployment it alone can found.
func TestApplySkipsMeshMailBeforeTheMeshIsUp(t *testing.T) {
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	configPath, secretsPath := meshMailDeployment(t, true)
	stubMeshLink(t, nil)
	failingSSH(t)
	for _, extra := range [][]string{{"--site", "home-a"}, nil} {
		rec := withRecorder(t, false)
		err := runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, extra...), strings.NewReader(""), io.Discard)
		if err != nil {
			for _, bad := range []string{"mail server", "nothing answered", "to try"} {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("apply %v refused on mail: %v", extra, err)
				}
			}
		}
		if !rec.Has("warned", "from home-a") || rec.Index("explain", "10.44.0.2 is on the mesh, 10.44.0.0/24, and psns-") < 0 || rec.Index("explain", "before the mesh exists") < 0 {
			t.Errorf("apply %v did not warn that mesh mail waits for the mesh: %v\n%s", extra, err, rec.Lines())
		}
	}
	if len(server.Dials()) != 0 {
		t.Errorf("mail was dialled before the mesh was up: %+v", server.Dials())
	}
}

// Once the mesh is up, as on a running deployment, mesh mail is tried, and
// a login the server refuses still refuses the apply.
func TestApplyTriesMeshMailOnceTheMeshIsUp(t *testing.T) {
	server := fakeSMTP(t, map[string]string{"robot@example.org": "the-right-one"})
	server.CertName = "10.44.0.2"
	configPath, secretsPath := meshMailDeployment(t, false)
	stubMeshLink(t, map[string]bool{"home-a": true, "home-b": true, "vm": true, "watch": true})
	failingSSH(t)
	withRecorder(t, false)
	for _, extra := range [][]string{{"--site", "home-a"}, nil} {
		err := runApply(append([]string{"--config", configPath, "--secrets", secretsPath, "--execute"}, extra...), strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "does not log in to its mail server") || !strings.Contains(err.Error(), "10.44.0.2") {
			t.Errorf("apply %v: err = %v, want the refused login", extra, err)
		}
	}
	if len(server.Dials()) == 0 {
		t.Error("mesh mail was not dialled with the mesh up")
	}
}

// The mesh must be up on the site holding the mail server's address too:
// with home-b's down, home-a's dial would reach nothing. An external relay
// reads no mesh at all.
func TestBeforeMeshReadsTheDiallingAndHoldingSites(t *testing.T) {
	configPath, secretsPath := meshMailDeployment(t, true)
	cfg := mustLoad(t, configPath)
	secrets := mustSecrets(t, secretsPath)
	asked := stubMeshLink(t, map[string]bool{"home-a": true})
	checks := beforeMesh(cfg, mailChecks(cfg, secrets, []string{"home-a"}, nil), nil)
	if len(checks) == 0 {
		t.Fatal("home-a has no mail check")
	}
	for _, c := range checks {
		if !strings.Contains(c.Skip, "not up on home-b yet, so it cannot be tried from home-a") {
			t.Errorf("skip = %q", c.Skip)
		}
	}
	if !slices.Equal(*asked, []string{"home-a", "home-b"}) {
		t.Errorf("read the mesh on %v, want home-a and home-b, once each", *asked)
	}

	stubMeshLink(t, map[string]bool{"home-a": true, "home-b": true})
	for _, c := range beforeMesh(cfg, mailChecks(cfg, secrets, []string{"home-a"}, nil), nil) {
		if c.Skip != "" {
			t.Errorf("skipped with the mesh up: %q", c.Skip)
		}
	}

	external := mustLoad(t, fixtureConfig())
	asked = stubMeshLink(t, nil)
	for _, c := range beforeMesh(external, mailChecks(external, secrets, nil, nil), nil) {
		if c.Skip != "" {
			t.Errorf("an external relay is skipped: %q", c.Skip)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("an external relay read the mesh on %v", *asked)
	}
}

// A site whose mesh cannot be read keeps its check, so its dial says why.
func TestBeforeMeshKeepsTheCheckWhenTheMeshCannotBeRead(t *testing.T) {
	configPath, secretsPath := meshMailDeployment(t, true)
	cfg := mustLoad(t, configPath)
	saved := meshLinkUp
	meshLinkUp = func(*config.Config, string, string) (bool, error) { return false, apply.ErrUnreachable }
	t.Cleanup(func() { meshLinkUp = saved })
	for _, c := range beforeMesh(cfg, mailChecks(cfg, mustSecrets(t, secretsPath), nil, nil), nil) {
		if c.Skip != "" {
			t.Errorf("skipped on an unread mesh: %q", c.Skip)
		}
	}
}

// A forward the site's host could not open refuses as a server that did not
// answer, not as one that answered and refused the login.
func TestApplyRefusesMailThatDoesNotAnswer(t *testing.T) {
	saved := smtpEnv
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env {
		return notOpenedEnv()
	}
	t.Cleanup(func() { smtpEnv = saved })
	configPath, secretsPath := rotateDeployment(t, nil)
	failingSSH(t)
	withRecorder(t, false)
	err := runApply([]string{"--config", configPath, "--secrets", secretsPath, "--execute", "--site", "home-a"}, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("apply was not refused")
	}
	for _, want := range []string{"nothing answered at smtp.example.org:587 from home-a", "declared by smtp", "Nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %v lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "does not log in") {
		t.Errorf("a server that did not answer is one that refused: %v", err)
	}
}

// validate warns, as apply does, that mesh mail waits for the mesh.
func TestValidateSkipsMeshMailBeforeTheMeshIsUp(t *testing.T) {
	server := fakeSMTP(t, nil)
	configPath, secretsPath := meshMailDeployment(t, true)
	stubMeshLink(t, nil)
	rec := withRecorder(t, false)
	if err := runValidate([]string{"--config", configPath, "--secrets", secretsPath}); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if rec.Index("explain", "before the mesh exists") < 0 {
		t.Errorf("no warning that mesh mail waits for the mesh:\n%s", rec.Lines())
	}
	if len(server.Dials()) != 0 {
		t.Errorf("mail was dialled before the mesh was up: %+v", server.Dials())
	}
}

// validate warns about a server that does not answer from a site, and goes
// on to the site's other checks, since the site itself was reached.
func TestValidateWarnsAboutMailThatDoesNotAnswer(t *testing.T) {
	saved := smtpEnv
	smtpEnv = func(*config.Config, map[string]string) smtpcheck.Env {
		return notOpenedEnv()
	}
	t.Cleanup(func() { smtpEnv = saved })
	configPath, secretsPath := rotateDeployment(t, nil)
	rec := withRecorder(t, false)
	if err := runValidate([]string{"--config", configPath, "--secrets", secretsPath}); err != nil {
		t.Fatalf("validate failed on a mail warning: %v", err)
	}
	if !rec.Has("warned", "from home-a") || rec.Index("explain", "Nothing answered at smtp.example.org:587 from home-a") < 0 {
		t.Errorf("a server that does not answer is not a warning:\n%s", rec.Lines())
	}
	if rec.Index("explain", "home-a could not be reached") >= 0 {
		t.Errorf("a server that does not answer is called an unreachable site:\n%s", rec.Lines())
	}
}

// notOpenedEnv is the site transport's Env with every forward refused by the
// host, as ssh reports it: the dial returns, and the first read ends with
// ssh's reason.
func notOpenedEnv() smtpcheck.Env {
	why := fmt.Errorf("ssh: Stdio forwarding request failed: Session open refused by peer: %w", apply.ErrForwardNotOpened)
	return smtpcheck.Env{
		Dial: func(string, string) (net.Conn, error) {
			local, remote := net.Pipe()
			go func() { _, _ = io.Copy(io.Discard, remote) }()
			return failingConn{Conn: local, err: why}, nil
		},
		NotOpened: func(err error) bool { return errors.Is(err, apply.ErrForwardNotOpened) },
	}
}

// failingConn is a connection whose every read fails with err.
type failingConn struct {
	net.Conn
	err error
}

func (c failingConn) Read([]byte) (int, error) { return 0, c.err }

// A mail server that takes the connection from this machine and closes it
// before it greets answered: secrets set refuses, and writes nothing, rather
// than writing the password untried.
func TestSecretsSetRefusesAServerThatClosesBeforeItGreets(t *testing.T) {
	saved := localSMTPEnv
	localSMTPEnv = func() smtpcheck.Env {
		return smtpcheck.Env{Dial: func(string, string) (net.Conn, error) {
			local, remote := net.Pipe()
			remote.Close()
			return local, nil
		}}
	}
	t.Cleanup(func() { localSMTPEnv = saved })
	configPath, secretsPath := rotateDeployment(t, nil)
	withRecorder(t, false)
	before, _ := os.ReadFile(secretsPath)
	_, err := set(t, configPath, secretsPath, "external.smtp_password", "not-a-real-smtp-password-0009")
	if err == nil || !strings.Contains(err.Error(), "does not work for") || !strings.Contains(err.Error(), "closed it before it greeted") {
		t.Fatalf("err = %v, want the server's close as a refusal", err)
	}
	if after, _ := os.ReadFile(secretsPath); !bytes.Equal(before, after) {
		t.Error("the secrets file changed")
	}
}

// meshLinkUp reads the deployment's interface through the site's ssh: an
// interface that is up, one that is absent, and one that exists but is down.
func TestMeshLinkUpReadsTheInterface(t *testing.T) {
	cfg := mustLoad(t, fixtureConfig())
	iface := cfg.Deployment().Interface()
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{`[{"ifname":"` + iface + `","flags":["POINTOPOINT","NOARP","UP","LOWER_UP"]}]`, true},
		{"absent", false},
		{`[{"ifname":"` + iface + `","flags":["POINTOPOINT","NOARP"]}]`, false},
	} {
		log := filepath.Join(t.TempDir(), "ssh.log")
		sshAnswering(t, "#!/bin/sh\nfor last; do :; done\nb=${last#*printf %s }\nb=${b%% |*}\nprintf %s \"$b\" | base64 -d >> "+log+"\nprintf '%s\\n' '"+tc.out+"'\n")
		up, err := meshLinkUp(cfg, "home-a", "")
		if err != nil {
			t.Fatalf("%s: %v", tc.out, err)
		}
		if up != tc.want {
			t.Errorf("%s: up = %v, want %v", tc.out, up, tc.want)
		}
		ran, _ := os.ReadFile(log)
		if !strings.Contains(string(ran), mesh.LinkCommand(iface)) {
			t.Errorf("ran %q, want %q", ran, mesh.LinkCommand(iface))
		}
	}
}
