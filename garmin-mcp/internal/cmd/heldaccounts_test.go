package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/config"
)

// writeSecret puts an owner-only secret file in place and returns its path.
func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "smtp.secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the secret: %v", err)
	}
	return path
}

// mailConfig is a configuration that asks for mail, complete but for what a test
// changes.
func mailConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		SMTPHost:       "smtp.example.test",
		SMTPPort:       587,
		SMTPUser:       "operateur@example.test",
		SMTPSecretFile: writeSecret(t, "secret-applicatif\n"),
		SMTPTo:         []string{"operateur@example.test"},
		SMTPTLS:        "starttls",
	}
}

func TestNoSMTPHostBuildsNoMailer(t *testing.T) {
	t.Parallel()

	// The default configuration: no mail, and that is a complete answer rather
	// than an error.
	mailer, err := newMailer(config.Config{}, nil)
	if err != nil || mailer != nil {
		t.Fatalf("newMailer(empty) = %v, %v; want nil, nil", mailer, err)
	}
}

func TestTheSecretComesFromItsFile(t *testing.T) {
	t.Parallel()

	mailer, err := newMailer(mailConfig(t), nil)
	if err != nil {
		t.Fatalf("newMailer: %v", err)
	}
	if mailer == nil {
		t.Fatal("a configured deployment got no mailer")
	}
	// The trailing newline of the file is not part of the secret: an editor adds
	// it, and an SMTP server would refuse the login.
	if got := mailer.Secret(); got != "secret-applicatif" {
		t.Errorf("secret = %q, want it trimmed", got)
	}
}

func TestAConfigurationThatCannotSendIsRefusedAtStartUp(t *testing.T) {
	t.Parallel()

	cases := map[string]func(config.Config) config.Config{
		"no recipient":  func(c config.Config) config.Config { c.SMTPTo = nil; return c },
		"no account":    func(c config.Config) config.Config { c.SMTPUser = ""; return c },
		"no secret":     func(c config.Config) config.Config { c.SMTPSecretFile = ""; return c },
		"absent secret": func(c config.Config) config.Config { c.SMTPSecretFile = "/nowhere"; return c },
		"unknown TLS":   func(c config.Config) config.Config { c.SMTPTLS = "cleartext"; return c },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// An operator who named a server and got a detail wrong wanted mail
			// and would not get it: that is a start-up failure, not a silent
			// deployment without notifications.
			if _, err := newMailer(mutate(mailConfig(t)), nil); err == nil {
				t.Error("newMailer accepted a configuration it cannot send with")
			}
		})
	}
}

// TestAnAbsentSecretSaysWhereItWasLookedFor is the mutant this test catches:
// forwarding securefile's error unchanged leaves an operator with
// `inspect "/run/secrets": file does not exist` and a file that plainly exists on
// their host, with nothing in the message to suggest the path is read inside the
// container. That exact message cost a real deployment an evening.
func TestAnAbsentSecretSaysWhereItWasLookedFor(t *testing.T) {
	t.Parallel()

	cfg := mailConfig(t)
	cfg.SMTPSecretFile = "/run/secrets/smtp"

	_, err := newMailer(cfg, nil)
	if err == nil {
		t.Fatal("a secret that is not there was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, cfg.SMTPSecretFile) {
		t.Errorf("the refusal does not name the path it looked for: %v", err)
	}
	for _, want := range []string{"smtp-secret-file", "own filesystem", "mount"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// A mode or owner fault is a different mistake and keeps securefile's own
	// wording. The discriminator is this file's added sentence, not the word
	// "mount": securefile's permission message names an fsGroup mount recursion
	// itself, which an earlier version of this test mistook for the advice above.
	cfg = mailConfig(t)
	if err := os.Chmod(cfg.SMTPSecretFile, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := newMailer(cfg, nil); err == nil {
		t.Fatal("a world-readable secret was accepted")
	} else if strings.Contains(err.Error(), "own filesystem") {
		t.Errorf("a permission fault was reported as a missing mount: %v", err)
	}
}

func TestAWorldReadableSecretIsRefused(t *testing.T) {
	t.Parallel()

	cfg := mailConfig(t)
	if err := os.Chmod(cfg.SMTPSecretFile, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := newMailer(cfg, nil)
	if err == nil {
		t.Fatal("a secret every local account can read was accepted")
	}
	// And the refusal names the fault, not the content.
	if strings.Contains(err.Error(), "secret-applicatif") {
		t.Errorf("the error quotes the secret: %v", err)
	}
}
