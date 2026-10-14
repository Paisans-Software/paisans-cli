package render_test

import (
	"testing"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// plaintextSMTP is a relay on the mesh that takes mail without TLS. The
// render does not judge the host: validate does (smtp-plaintext-public-host).
var plaintextSMTP = config.SMTP{
	Host: "10.44.0.25", Security: config.SMTPNone,
	Username: "robot@example.org", FromAddress: "hello@example.org",
}

// security: none reaches every consumer that sends mail as that consumer's
// own way of saying no TLS, at port 25 when no port is declared.
func TestPlaintextSMTPRendersForEveryConsumer(t *testing.T) {
	t.Run("pocket-id", func(t *testing.T) {
		own := plaintextSMTP
		env := renderAuth(t, nil, &own, nil)
		for name, want := range map[string]string{"SMTP_HOST": "10.44.0.25", "SMTP_PORT": "25", "SMTP_TLS": "none"} {
			if got := envValue(env, name); got != want {
				t.Errorf("%s=%s, want %s", name, got, want)
			}
		}
	})
	t.Run("account", func(t *testing.T) {
		env := buildAccount(t, func(cfg *config.Config, _ *config.Secrets) {
			cfg.SMTP = plaintextSMTP
		})[accountEnvA]
		for name, want := range map[string]string{"ACCOUNT_SMTP_HOST": "10.44.0.25", "ACCOUNT_SMTP_PORT": "25", "ACCOUNT_SMTP_TLS": "none"} {
			if got := envValue(env, name); got != want {
				t.Errorf("%s=%s, want %s", name, got, want)
			}
		}
	})
	t.Run("uptime", func(t *testing.T) {
		cfg, secrets := withMonitor(t, plaintextSMTP, nil)
		settings := renderSeed(t, cfg, secrets).Settings
		if settings["smtp_secure"] != false || settings["smtp_require_tls"] != false || settings["smtp_port"] != float64(25) || settings["smtp_host"] != "10.44.0.25" {
			t.Errorf("smtp settings are %v", settings)
		}
	})
	t.Run("mbin", func(t *testing.T) {
		env := renderTalk(t, plaintextSMTP, nil, nil)
		want := "smtp://robot%40example.org:fixture-not-a-secret-smtp@10.44.0.25:25?auto_tls=false"
		if got := envValue(env, "MAILER_DSN"); got != want {
			t.Errorf("MAILER_DSN=%s, want %s", got, want)
		}
	})
}
