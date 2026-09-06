// Package notify sends the operator an e-mail when an account is waiting for their
// decision.
//
// It is a fork addition. Nothing upstream sends mail, and nothing here is required:
// a deployment that configures no SMTP host simply has no mailer, and the login flow
// behaves exactly as it did.
//
// Two properties matter more than the feature itself, and the code is shaped around
// them:
//
//   - Sending must never delay or fail a login. The mailer is called off the request
//     path, and every error it produces is logged and dropped.
//   - Credentials travel only over TLS. The transport refuses to authenticate on a
//     cleartext connection, which is what net/smtp's PlainAuth enforces for us.
package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Errors a caller can act on.
var (
	// ErrIncompleteConfig reports a configuration that names some of the SMTP
	// settings but not enough of them to send anything. It is deliberately not the
	// same as "no mailer configured": a half-filled configuration is a mistake, and
	// starting quietly without mail would hide it.
	ErrIncompleteConfig = errors.New("notify: incomplete SMTP configuration")

	// ErrNoRecipients reports a mailer with nobody to send to.
	ErrNoRecipients = errors.New("notify: no recipient configured")
)

// TLSMode is how the connection to the SMTP server is protected.
type TLSMode string

const (
	// TLSStartTLS connects in the clear and upgrades with STARTTLS before
	// authenticating. This is port 587, and it is the default.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit wraps the connection in TLS from the first byte. This is
	// port 465.
	TLSImplicit TLSMode = "implicit"
)

// valid reports whether the mode is one this package implements. There is no
// cleartext mode: a password would travel in the clear, and no convenience is worth
// that.
func (m TLSMode) valid() bool { return m == TLSStartTLS || m == TLSImplicit }

// Config describes how to reach the SMTP server and who to tell.
type Config struct {
	// Host and Port are the SMTP server. Host empty means "no mail configured",
	// which is a complete and supported configuration.
	Host string
	Port int

	// User and Secret authenticate to the server. With Gmail, User is the full
	// address and Secret is an application secret generated for this deployment,
	// never the account's own sign-in secret.
	//
	// Secret arrives from an owner-only file rather than from a setting: this
	// project makes no credential configurable.
	User   string
	Secret string

	// From is the envelope and header sender. Empty falls back to Username, which is
	// what Gmail requires anyway.
	From string

	// To is the list of recipients. At least one is required once Host is set.
	To []string

	// TLS selects how the connection is protected. Empty means TLSStartTLS.
	TLS TLSMode

	// DashboardURL is an optional link to the web interface, put in the message so
	// the operator can act from their phone without hunting for the address.
	DashboardURL string

	// Interval bounds how often one account may be announced. A person who retries
	// a login three times must not produce three e-mails, and a person who comes
	// back a week later should produce a reminder. Zero means DefaultInterval.
	Interval time.Duration

	// Timeout bounds one send. Zero means DefaultTimeout.
	Timeout time.Duration

	// Logger receives the outcome of each send. Nil records nothing.
	Logger *slog.Logger

	// send is the seam the tests replace. Nil means the real SMTP transport.
	send func(ctx context.Context, cfg Config, message []byte) error
}

// Defaults.
const (
	// DefaultInterval is the quiet period per account.
	DefaultInterval = 6 * time.Hour
	// DefaultTimeout bounds one send.
	DefaultTimeout = 20 * time.Second
	// DefaultPort is the STARTTLS submission port.
	DefaultPort = 587
)

// Configured reports whether this configuration asks for mail at all.
func (c Config) Configured() bool { return strings.TrimSpace(c.Host) != "" }

// check validates a configuration that asks for mail.
func (c Config) check() error {
	if !c.Configured() {
		return nil
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("%w: port %d is not a port", ErrIncompleteConfig, c.Port)
	}
	if c.User == "" || c.Secret == "" {
		return fmt.Errorf("%w: an account and a secret are needed to send", ErrIncompleteConfig)
	}
	if len(c.To) == 0 {
		return ErrNoRecipients
	}
	if c.TLS != "" && !c.TLS.valid() {
		return fmt.Errorf("%w: unknown TLS mode %q", ErrIncompleteConfig, c.TLS)
	}
	for _, address := range append([]string{c.sender()}, c.To...) {
		if !plausibleAddress(address) {
			return fmt.Errorf("%w: %q is not an address", ErrIncompleteConfig, address)
		}
	}
	return nil
}

// sender is the From address, defaulting to the authenticated user.
func (c Config) sender() string {
	if c.From != "" {
		return c.From
	}
	return c.User
}

// plausibleAddress is a bounded sanity check, not an RFC 5322 parser. It exists to
// catch a configuration typo at start-up rather than at the first send, and to refuse
// a value carrying a header separator.
func plausibleAddress(address string) bool {
	if address == "" || len(address) > 320 {
		return false
	}
	if strings.ContainsAny(address, "\r\n") || strings.ContainsAny(address, " \t") {
		return false
	}
	// Exactly one "@", with something on each side. LastIndex alone let "a@b@c"
	// through, which a test caught.
	if strings.Count(address, "@") != 1 {
		return false
	}
	at := strings.Index(address, "@")
	return at > 0 && at < len(address)-1
}

// A Mailer announces waiting accounts, at most one message per account per interval.
//
// The zero value is not usable; build one with New. A nil *Mailer is usable and does
// nothing, which is how a deployment without SMTP configuration is represented.
type Mailer struct {
	cfg Config

	mu   sync.Mutex
	sent map[string]time.Time

	now func() time.Time
}

// New returns the mailer cfg describes, or nil when cfg asks for no mail.
//
// A nil mailer is a working mailer that sends nothing, so a caller never has to check
// before calling.
func New(cfg Config) (*Mailer, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if !cfg.Configured() {
		return nil, nil
	}
	if cfg.TLS == "" {
		cfg.TLS = TLSStartTLS
	}
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.send == nil {
		cfg.send = sendSMTP
	}
	return &Mailer{cfg: cfg, sent: make(map[string]time.Time), now: time.Now}, nil
}

// Secret reports the credential this mailer authenticates with. It exists for the
// composition root's tests, which check that the secret arrives from its file with the
// editor's trailing newline removed; nothing else reads it.
func (m *Mailer) Secret() string {
	if m == nil {
		return ""
	}
	return m.cfg.Secret
}

// AccountWaiting announces one account that is waiting for a decision.
//
// It reports whether a message was sent, which is what the tests assert on; a caller
// on the login path ignores it. An account announced within the quiet period is
// skipped, so a person who retries does not produce a burst.
func (m *Mailer) AccountWaiting(ctx context.Context, principal, email string) (bool, error) {
	if m == nil {
		return false, nil
	}
	if !m.shouldSend(principal) {
		return false, nil
	}

	message := m.compose(principal, email)
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	if err := m.cfg.send(ctx, m.cfg, message); err != nil {
		// The account is un-marked so the next login tries again: a failed send
		// must not consume the quiet period, or one network blip would silence
		// the notification for hours.
		m.forget(principal)
		m.log(ctx, slog.LevelWarn, "sending the pending-account notification failed", err)
		return false, err
	}
	m.log(ctx, slog.LevelInfo, "a pending-account notification was sent", nil)
	return true, nil
}

// shouldSend records the intent to send and reports whether the caller may.
func (m *Mailer) shouldSend(principal string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if last, seen := m.sent[principal]; seen && now.Sub(last) < m.cfg.Interval {
		return false
	}
	m.sent[principal] = now
	return true
}

// forget drops the record of an attempt that failed.
func (m *Mailer) forget(principal string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sent, principal)
}

// log records one outcome. The message never carries the account's address: the
// e-mail body does, and a log line is read by more eyes than a mailbox.
func (m *Mailer) log(ctx context.Context, level slog.Level, message string, err error) {
	if m.cfg.Logger == nil {
		return
	}
	if err != nil {
		m.cfg.Logger.Log(ctx, level, message, slog.String("error", err.Error()))
		return
	}
	m.cfg.Logger.Log(ctx, level, message)
}

// compose builds the RFC 5322 message.
//
// The subject is encoded because it carries an address that may hold non-ASCII, and
// every header value is stripped of CR and LF: the address comes from a database this
// server wrote, but a header injection is cheap to prevent and expensive to discover.
func (m *Mailer) compose(principal, email string) []byte {
	subject := mime.QEncoding.Encode("utf-8", "Compte en attente de validation : "+header(email))

	var body strings.Builder
	body.WriteString("Un compte attend votre validation sur ce déploiement garmin-mcp.\n\n")
	body.WriteString("Compte      : " + header(email) + "\n")
	body.WriteString("Identifiant : " + header(principal) + "\n")
	body.WriteString("Mis en attente le " + m.now().UTC().Format(time.RFC3339) + "\n")
	if m.cfg.DashboardURL != "" {
		body.WriteString("\nValider ou bloquer : " + header(m.cfg.DashboardURL) + "\n")
	}
	body.WriteString("\nTant que la validation n'est pas faite, ce compte n'obtient aucun jeton.\n")
	body.WriteString("La personne a vu une page le lui disant et doit revenir plus tard.\n")

	var message strings.Builder
	message.WriteString("From: " + header(m.cfg.sender()) + "\r\n")
	message.WriteString("To: " + header(strings.Join(m.cfg.To, ", ")) + "\r\n")
	message.WriteString("Subject: " + header(subject) + "\r\n")
	message.WriteString("Date: " + m.now().Format(time.RFC1123Z) + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	message.WriteString("\r\n")
	message.WriteString(strings.ReplaceAll(body.String(), "\n", "\r\n"))
	return []byte(message.String())
}

// header strips the two characters that could add a header to a message.
func header(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

// tlsConfigFor builds the TLS settings of one connection. It is a variable so a test
// can accept a self-signed certificate against a local server; production never
// replaces it.
var tlsConfigFor = func(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// sendSMTP is the real transport.
func sendSMTP(ctx context.Context, cfg Config, message []byte) error {
	address := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	dialer := &net.Dialer{}

	var (
		conn net.Conn
		err  error
	)
	if cfg.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{
			NetDialer: dialer,
			Config:    tlsConfigFor(cfg.Host),
		}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("notify: dialing %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("notify: SMTP handshake with %s: %w", cfg.Host, err)
	}
	defer func() { _ = client.Close() }()

	if cfg.TLS == TLSStartTLS {
		if err := client.StartTLS(tlsConfigFor(cfg.Host)); err != nil {
			return fmt.Errorf("notify: STARTTLS with %s: %w", cfg.Host, err)
		}
	}

	// PlainAuth refuses to send the secret over a connection it does not consider
	// secure, which is the check that keeps a misconfigured port from leaking it.
	if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Secret, cfg.Host)); err != nil {
		return fmt.Errorf("notify: authenticating to %s: %w", cfg.Host, err)
	}
	if err := client.Mail(cfg.sender()); err != nil {
		return fmt.Errorf("notify: MAIL FROM: %w", err)
	}
	for _, recipient := range cfg.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("notify: RCPT TO: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify: DATA: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		return fmt.Errorf("notify: writing the message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("notify: closing the message: %w", err)
	}
	return client.Quit()
}
