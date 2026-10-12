package render_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/render"
	"github.com/paisans-software/paisans-stack/internal/validate"
)

// adminsToken is the fixture's admins heartbeat token for the auth app.
func adminsToken(t *testing.T, secrets *config.Secrets) string {
	t.Helper()
	token, _ := secrets.Apps["auth"]["admins_heartbeat_token"].(string)
	if token == "" {
		t.Fatal("the fixture secrets carry no admins heartbeat token for auth")
	}
	return token
}

// The admins group's health is one push monitor per Pocket ID app, not one
// per site: every instance pushes to the same token, so the monitor follows
// whichever instance is active. Its fields are the site heartbeat's: interval
// 60, grace 120, threshold 2, no URL.
func TestTheSeedExpectsAnAdminsHeartbeatPerPocketID(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	monitors := byName(renderSeed(t, cfg, secrets))
	m := monitors["auth — admins"]
	if m == nil {
		t.Fatal("no admins heartbeat for auth")
	}
	if m["monitor_type"] != "heartbeat" || m["heartbeat_token"] != adminsToken(t, secrets) {
		t.Errorf("auth admins heartbeat: %v", m)
	}
	if m["heartbeat_schedule_kind"] != "interval" || m["interval_seconds"] != float64(60) || m["heartbeat_grace_seconds"] != float64(120) {
		t.Errorf("auth admins heartbeat schedule: %v", m)
	}
	if m["failure_threshold"] != float64(2) {
		t.Errorf("auth admins heartbeat threshold: %v", m["failure_threshold"])
	}
	if _, ok := m["url"]; ok {
		t.Errorf("auth admins heartbeat carries a url: %v", m)
	}
	for name := range monitors {
		if strings.HasSuffix(name, " — admins") && name != "auth — admins" {
			t.Errorf("an admins heartbeat for something other than Pocket ID: %s", name)
		}
	}
}

// Every site running Pocket ID runs the pusher in the app's own stack, beside
// the reconciler it asks. Its URLs carry the token, so they are in a 0600 env
// file and nowhere in the 0644 compose file.
func TestEveryPocketIDSitePushesTheAdminsHeartbeat(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	token := adminsToken(t, secrets)
	sites := render.AppSites(cfg)["auth"]
	if len(sites) == 0 {
		t.Fatal("the fixture runs Pocket ID nowhere")
	}
	for _, site := range sites {
		stack := site + "/srv/paisans/f2a9/auth/"
		var env *render.File
		for i, f := range plan.Files {
			if f.Path == stack+"admins-heartbeat/heartbeat.env" {
				env = &plan.Files[i]
			}
		}
		if env == nil {
			t.Fatalf("%s renders no admins-heartbeat/heartbeat.env", site)
		}
		if env.Mode != 0o600 {
			t.Errorf("%s: the admins heartbeat env carries the token and is rendered %o", site, env.Mode)
		}
		if want := "ADMINS_HEARTBEAT_URLS=https://status.example.org/ping/" + token + "\n"; !strings.Contains(env.Content, want) {
			t.Errorf("%s: the admins heartbeat env lacks %q:\n%s", site, want, env.Content)
		}
		compose := files[stack+"compose.yaml"]
		if !strings.Contains(compose, "\n  admins-heartbeat:\n") {
			t.Errorf("%s: the Pocket ID stack has no admins-heartbeat service:\n%s", site, compose)
		}
		if !strings.Contains(compose, "image: "+render.HeartbeatImage+"\n") {
			t.Errorf("%s: the pusher does not run %s:\n%s", site, render.HeartbeatImage, compose)
		}
		if strings.Contains(compose, token) {
			t.Errorf("%s: the token is in the 0644 compose file", site)
		}
		if _, ok := files[stack+"admins-heartbeat/push.sh"]; !ok {
			t.Errorf("%s renders no admins-heartbeat/push.sh", site)
		}
	}
}

// With two monitors each seeds the admins heartbeat, wherever Pocket ID runs,
// and every instance pushes to both with the one token, in app order.
func TestTwoMonitorsBothExpectTheAdminsHeartbeat(t *testing.T) {
	cfg, secrets := withTwoMonitors(t)
	token := adminsToken(t, secrets)
	for self, site := range map[string]string{"status": "watch", "status-b": "watch-b"} {
		monitors := byName(renderSeedAt(t, cfg, secrets, site+"/srv/paisans/f2a9/"+self+"/monitors.json"))
		if m := monitors["auth — admins"]; m == nil || m["heartbeat_token"] != token {
			t.Errorf("%s does not expect auth's admins heartbeat: %v", self, m)
		}
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	files := planFiles(plan)
	want := "ADMINS_HEARTBEAT_URLS=https://status.example.org/ping/" + token + " https://status-b.example.org/ping/" + token + "\n"
	for _, site := range render.AppSites(cfg)["auth"] {
		if got := files[site+"/srv/paisans/f2a9/auth/admins-heartbeat/heartbeat.env"]; !strings.Contains(got, want) {
			t.Errorf("%s pushes to %q, want %q", site, got, want)
		}
	}
}

// A deployment with no monitor has nothing to push to: no pusher service and
// none of its files.
func TestNoMonitorMeansNoAdminsHeartbeatPusher(t *testing.T) {
	cfg := fixture(t)
	delete(cfg.Apps, "status")
	delete(cfg.Sites, "watch")
	secrets := fixtureSecrets(t)
	if refusals := validate.Check(cfg).Refusals(); len(refusals) > 0 {
		t.Fatalf("a deployment without a monitor is refused: %v", refusals)
	}
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range planFiles(plan) {
		if strings.Contains(path, "/admins-heartbeat/") {
			t.Errorf("%s is rendered with no monitor to push to", path)
		}
		if strings.HasSuffix(path, "/auth/compose.yaml") && strings.Contains(content, "admins-heartbeat") {
			t.Errorf("%s runs an admins heartbeat pusher with no monitor to push to", path)
		}
	}
}

// A Pocket ID app without a token, or with one the fork would silently
// replace, is refused at render by name, as a site's heartbeat token is.
func TestAMissingOrMalformedAdminsHeartbeatTokenIsRefused(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{"", "paisans init"},
		{"not-hex", "32 lowercase hex"},
		{"0123456789ABCDEF0123456789ABCDEF", "32 lowercase hex"},
	} {
		cfg, secrets := withMonitor(t, config.SMTP{}, nil)
		secrets.Apps["auth"]["admins_heartbeat_token"] = tc.token
		_, err := render.Build(cfg, secrets)
		if err == nil || !strings.Contains(err.Error(), "apps.auth.admins_heartbeat_token") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("token %q: got %v, want an error naming apps.auth.admins_heartbeat_token and %q", tc.token, err, tc.want)
		}
	}
	// With no monitor the token is read by nothing, so its absence is not
	// refused.
	cfg := fixture(t)
	delete(cfg.Apps, "status")
	delete(cfg.Sites, "watch")
	secrets := fixtureSecrets(t)
	delete(secrets.Apps["auth"], "admins_heartbeat_token")
	if _, err := render.Build(cfg, secrets); err != nil {
		t.Errorf("no monitor, no token: %v", err)
	}
}

// fakePusherCurl stands in for curl in the admins heartbeat script. Asked for
// /healthz it answers as the reconciler does, FAKE_BODY with a newline and
// then curl's -w status, or nothing and exit 7 when FAKE_CODE is 000. Every
// other request is a push: its arguments and whatever it was sent on stdin
// are appended to $FAKE_LOG, one push per line. After the first cycle (see
// fakePusherSleep) it records and answers nothing.
const fakePusherCurl = `#!/bin/sh
[ "$(cat "$FAKE_CYCLES")" = 1 ] || exit 7
for last; do :; done
case "$last" in
*/healthz)
	[ "$FAKE_CODE" = 000 ] && { printf '\n000'; exit 7; }
	printf '%s\n\n%s' "$FAKE_BODY" "$FAKE_CODE"
	exit 0 ;;
esac
data=
case " $* " in
*" --data-binary @- "*) data=$(cat) ;;
esac
echo "$* <$data>" >> "$FAKE_LOG"
`

// fakePusherSleep counts the script's cycles, and ends the script at the
// start of its second by signalling the shell that started it.
const fakePusherSleep = `#!/bin/sh
n=$(($(cat "$FAKE_CYCLES") + 1))
echo "$n" > "$FAKE_CYCLES"
[ "$n" -ge 2 ] && kill -TERM "$PPID"
exit 0
`

// runAdminsPusher runs the rendered push.sh for one cycle against the fakes
// and returns the pushes it made, one per line.
func runAdminsPusher(t *testing.T, code, body string) []string {
	t.Helper()
	cfg, secrets := withTwoMonitors(t)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	script, ok := planFiles(plan)["home-a/srv/paisans/f2a9/auth/admins-heartbeat/push.sh"]
	if !ok {
		t.Fatal("home-a renders no admins heartbeat script")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"curl": fakePusherCurl, "sleep": fakePusherSleep} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	push := filepath.Join(root, "push.sh")
	cycles := filepath.Join(root, "cycles")
	log := filepath.Join(root, "pushes")
	for path, content := range map[string]string{push: script, cycles: "0\n", log: ""} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", push)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"ADMINS_HEARTBEAT_URLS=https://one.example.org/ping/t1 https://two.example.org/ping/t1",
		"FAKE_CODE=" + code, "FAKE_BODY=" + body, "FAKE_CYCLES=" + cycles, "FAKE_LOG=" + log,
	}
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the script ran past one cycle: %s", out)
	}
	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var pushes []string
	for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		if line != "" {
			pushes = append(pushes, line)
		}
	}
	return pushes
}

// pushedTo is the push to url among pushes, empty when there is none.
func pushedTo(pushes []string, url string) string {
	for _, p := range pushes {
		if strings.Contains(p, " "+url+" <") {
			return p
		}
	}
	return ""
}

// A standby's reconciler answers 200 standby, and the pusher says nothing: the
// active instance is the one that speaks for the group.
func TestTheAdminsPusherIsSilentOnAStandby(t *testing.T) {
	if pushes := runAdminsPusher(t, "200", "standby"); len(pushes) != 0 {
		t.Errorf("a standby pushed: %v", pushes)
	}
}

// An active instance whose group is healthy pushes success to every monitor,
// with the site heartbeat's curl options.
func TestTheAdminsPusherPushesSuccessWhenHealthy(t *testing.T) {
	pushes := runAdminsPusher(t, "200", "admins has 2 members")
	if len(pushes) != 2 {
		t.Fatalf("pushes %v, want one to each monitor", pushes)
	}
	for _, url := range []string{"https://one.example.org/ping/t1", "https://two.example.org/ping/t1"} {
		p := pushedTo(pushes, url)
		if p == "" {
			t.Errorf("no success push to %s: %v", url, pushes)
			continue
		}
		for _, opt := range []string{"-fsS", "-m 10", "--retry 2"} {
			if !strings.Contains(p, opt) {
				t.Errorf("push %q lacks %s", p, opt)
			}
		}
		if strings.Contains(p, " -k") || strings.Contains(p, "--insecure") || strings.Contains(p, "--data-binary") {
			t.Errorf("push %q", p)
		}
	}
}

// A 503 is a failure, pushed to /fail with the reconciler's one line as the
// body, sent as plain text so the fork keeps it verbatim.
func TestTheAdminsPusherPushesFailureWithTheReason(t *testing.T) {
	reason := "admins has 1 member(s), fewer than 2"
	pushes := runAdminsPusher(t, "503", reason)
	if len(pushes) != 2 {
		t.Fatalf("pushes %v, want one to each monitor", pushes)
	}
	for _, url := range []string{"https://one.example.org/ping/t1/fail", "https://two.example.org/ping/t1/fail"} {
		p := pushedTo(pushes, url)
		if p == "" {
			t.Errorf("no failure push to %s: %v", url, pushes)
			continue
		}
		if !strings.HasSuffix(p, "<"+reason+">") {
			t.Errorf("push %q does not carry %q", p, reason)
		}
		if !strings.Contains(p, "Content-Type: text/plain") {
			t.Errorf("push %q is not sent as plain text", p)
		}
	}
}

// No answer, or any status but 200 and 503, is not the reconciler speaking,
// so nothing is pushed; the monitor's grace turns the silence into down.
func TestTheAdminsPusherIsSilentWithoutAnAnswer(t *testing.T) {
	for _, code := range []string{"000", "404", "500"} {
		if pushes := runAdminsPusher(t, code, "whatever"); len(pushes) != 0 {
			t.Errorf("status %s pushed: %v", code, pushes)
		}
	}
}

// The pusher's image is the toolkit's own, at an exact version, as the site
// heartbeat's is in the infrastructure stack: not one the configuration
// decides, so it is not among the images apply resolves and pins.
func TestTheAdminsPusherImageIsNotTheConfigurations(t *testing.T) {
	cfg, secrets := withMonitor(t, config.SMTP{}, nil)
	plan, err := render.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range render.AppSites(cfg)["auth"] {
		var auth bool
		for _, image := range plan.Images[site] {
			if image.Stack != "auth" {
				continue
			}
			auth = true
			if image.Image == render.HeartbeatImage || len(image.Services) == 0 {
				t.Errorf("%s: auth lists %s for services %v", site, image.Image, image.Services)
			}
		}
		if !auth {
			t.Errorf("%s: auth's images are not listed", site)
		}
	}
}
