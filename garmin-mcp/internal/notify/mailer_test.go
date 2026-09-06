package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is the transport seam under test control: it keeps what would have been
// sent, so the tests assert on the message rather than on a network conversation.
type recorder struct {
	mu       sync.Mutex
	messages [][]byte
	err      error
}

func (r *recorder) send(_ context.Context, _ Config, message []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.err != nil {
		return r.err
	}
	r.messages = append(r.messages, message)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.messages) == 0 {
		return ""
	}
	return string(r.messages[len(r.messages)-1])
}

// testConfig is a complete configuration wired to a recorder.
func testConfig(sink *recorder) Config {
	return Config{
		Host:   "smtp.example.test",
		Port:   587,
		User:   "operateur@example.test",
		Secret: "secret-applicatif",
		To:     []string{"operateur@example.test"},
		send:   sink.send,
	}
}

func TestNoHostMeansNoMailer(t *testing.T) {
	t.Parallel()

	// A deployment that configures nothing gets a nil mailer, and a nil mailer is
	// callable: the login path never has to check.
	mailer, err := New(Config{})
	if err != nil || mailer != nil {
		t.Fatalf("New(empty) = %v, %v; want nil, nil", mailer, err)
	}
	sent, err := mailer.AccountWaiting(t.Context(), "p-1", "rider@example.test")
	if sent || err != nil {
		t.Errorf("AccountWaiting on a nil mailer = %v, %v; want false, nil", sent, err)
	}
}

func TestAHalfFilledConfigurationIsRefused(t *testing.T) {
	t.Parallel()

	base := func() Config {
		return Config{
			Host: "smtp.example.test", Port: 587,
			User: "u@example.test", Secret: "s",
			To: []string{"o@example.test"},
		}
	}
	cases := map[string]func(Config) Config{
		"no port":        func(c Config) Config { c.Port = 0; return c },
		"port too high":  func(c Config) Config { c.Port = 70000; return c },
		"no account":     func(c Config) Config { c.User = ""; return c },
		"no secret":      func(c Config) Config { c.Secret = ""; return c },
		"no recipient":   func(c Config) Config { c.To = nil; return c },
		"unknown TLS":    func(c Config) Config { c.TLS = "none"; return c },
		"bad sender":     func(c Config) Config { c.From = "pas-une-adresse"; return c },
		"bad recipient":  func(c Config) Config { c.To = []string{"a@b@c"}; return c },
		"header in from": func(c Config) Config { c.From = "a@b.test\r\nBcc: x@y.test"; return c },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(mutate(base())); err == nil {
				t.Error("New accepted a configuration it cannot send with")
			}
		})
	}
}

func TestTheMessageSaysWhoIsWaiting(t *testing.T) {
	t.Parallel()
	sink := &recorder{}
	cfg := testConfig(sink)
	cfg.DashboardURL = "https://interface.example.test/"
	mailer, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sent, err := mailer.AccountWaiting(t.Context(), "p-42", "rider@example.test")
	if err != nil || !sent {
		t.Fatalf("AccountWaiting = %v, %v; want true, nil", sent, err)
	}

	message := sink.last()
	for _, want := range []string{
		"To: operateur@example.test",
		"From: operateur@example.test",
		"rider@example.test",
		"p-42",
		"https://interface.example.test/",
		"attend votre validation",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not carry %q", want)
		}
	}
	// Headers and body are separated by exactly one blank line, and the body is
	// CRLF throughout: a bare LF is what makes a message arrive truncated.
	if !strings.Contains(message, "\r\n\r\n") {
		t.Error("the message has no header separator")
	}
	if strings.Contains(strings.ReplaceAll(message, "\r\n", ""), "\n") {
		t.Error("the message carries a bare LF")
	}
}

func TestAnAddressCannotInjectAHeader(t *testing.T) {
	t.Parallel()
	sink := &recorder{}
	mailer, err := New(testConfig(sink))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The address comes from this server's own database, so this is defence in
	// depth rather than a live threat — but a header injection is cheap to prevent
	// and expensive to discover.
	if _, err := mailer.AccountWaiting(
		t.Context(), "p-1", "rider@example.test\r\nBcc: ailleurs@example.test"); err != nil {
		t.Fatalf("AccountWaiting: %v", err)
	}
	message := sink.last()
	entetes, corps, coupe := strings.Cut(message, "\r\n\r\n")
	if !coupe {
		t.Fatal("the message has no header separator")
	}

	// What an injection would look like: a header line of its own. The address
	// appearing inside another header's value is text, not a header, and that is
	// exactly what stripping CR and LF turns it into.
	for _, ligne := range strings.Split(entetes, "\r\n") {
		if strings.HasPrefix(ligne, "Bcc:") {
			t.Errorf("an address became a header of its own: %q", ligne)
		}
	}
	if strings.Contains(corps, "\r\nBcc:") {
		t.Error("an address started a line of its own in the body")
	}
}

func TestOneAccountIsAnnouncedOncePerInterval(t *testing.T) {
	t.Parallel()
	sink := &recorder{}
	cfg := testConfig(sink)
	cfg.Interval = time.Hour
	mailer, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	moment := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	mailer.now = func() time.Time { return moment }

	for range 3 {
		if _, err := mailer.AccountWaiting(t.Context(), "p-1", "rider@example.test"); err != nil {
			t.Fatalf("AccountWaiting: %v", err)
		}
	}
	// Someone who retries a login three times must not produce three e-mails.
	if sink.count() != 1 {
		t.Errorf("sent %d messages for three attempts, want one", sink.count())
	}

	// A different account is a different subject, quiet period or not.
	if _, err := mailer.AccountWaiting(t.Context(), "p-2", "autre@example.test"); err != nil {
		t.Fatalf("AccountWaiting, second account: %v", err)
	}
	if sink.count() != 2 {
		t.Errorf("sent %d messages, want the second account announced", sink.count())
	}

	// And the same account is announced again once the period has passed, because
	// an operator who missed the first message deserves a reminder.
	moment = moment.Add(time.Hour + time.Minute)
	if _, err := mailer.AccountWaiting(t.Context(), "p-1", "rider@example.test"); err != nil {
		t.Fatalf("AccountWaiting, after the interval: %v", err)
	}
	if sink.count() != 3 {
		t.Errorf("sent %d messages, want the reminder", sink.count())
	}
}

func TestAFailedSendIsRetriedNextTime(t *testing.T) {
	t.Parallel()
	sink := &recorder{err: errors.New("the SMTP server is unreachable")}
	cfg := testConfig(sink)
	cfg.Interval = time.Hour
	mailer, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := mailer.AccountWaiting(t.Context(), "p-1", "rider@example.test"); err == nil {
		t.Fatal("AccountWaiting hid a transport failure")
	}

	// A failure must not consume the quiet period: one network blip would
	// otherwise silence the notification for hours.
	sink.err = nil
	sent, err := mailer.AccountWaiting(t.Context(), "p-1", "rider@example.test")
	if err != nil || !sent {
		t.Fatalf("AccountWaiting after a failure = %v, %v; want true, nil", sent, err)
	}
}
