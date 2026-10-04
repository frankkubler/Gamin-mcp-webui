package cmd

// The adapter between the browser login pages and the mailer, for the notice an
// operator receives when an account is waiting for their decision.
//
// It exists for the same reason as the adapters beside it: internal/loginweb declares
// the narrow interface it needs, internal/notify owns the SMTP conversation, and
// internal/store owns the account. This file is the only place that knows all three.
//
// Two decisions live here rather than in either neighbour:
//
//   - The work happens in the background. AccountHeld returns immediately, so a slow
//     or unreachable mail server cannot hold a login page open.
//   - The context is detached from the request. The request's context is cancelled
//     the moment the response is written, which would abort every send; the mailer
//     applies its own timeout instead.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/tamcore/garmin-mcp/internal/config"
	"github.com/tamcore/garmin-mcp/internal/loginweb"
	"github.com/tamcore/garmin-mcp/internal/notify"
	"github.com/tamcore/garmin-mcp/internal/securefile"
	"github.com/tamcore/garmin-mcp/internal/store"
)

// heldAccounts tells the operator about an account that is waiting.
type heldAccounts struct {
	store  *store.SQLiteStore
	mailer *notify.Mailer
	logger *slog.Logger

	// wait is closed over by the tests to make the background send observable. Nil
	// in production, where nothing waits for the goroutine.
	done chan struct{}
}

// The assertion this type exists for.
var _ loginweb.HeldAccounts = (*heldAccounts)(nil)

// smtpSecretError reports a secret this deployment could not read.
//
// The cause names the path and the fault, never the content: securefile reports what
// it refused, not what it read. An absent path gets one sentence added, because
// "file does not exist" is true and useless to an operator looking at the file on
// their host — smtp-secret-file names a path inside THIS process's filesystem, so in
// a container the file arrives only through a mount, and the message that cost a real
// deployment an evening named a directory the operator had never heard of. Every
// other cause keeps securefile's own wording, which already says what was wrong with
// the mode, the owner, or the type.
func smtpSecretError(path string, cause error) error {
	if errors.Is(cause, fs.ErrNotExist) {
		return fmt.Errorf("reading the SMTP secret: %s is not there. smtp-secret-file "+
			"names a path inside this process's own filesystem, so a containerised "+
			"deployment has to mount the file at that path: %w", path, cause)
	}
	return fmt.Errorf("reading the SMTP secret %s: %w", path, cause)
}

// newHeldAccounts binds the adapter to the store and the mailer.
//
// A nil mailer is allowed and means the deployment configured no notification: the
// adapter is then a no-op, which is simpler than making the login server's field
// conditional in two places.
func newHeldAccounts(
	sqlite *store.SQLiteStore, mailer *notify.Mailer, logger *slog.Logger,
) (*heldAccounts, error) {
	if sqlite == nil {
		return nil, errors.New("cmd: the held-account adapter needs a store")
	}
	return &heldAccounts{store: sqlite, mailer: mailer, logger: logger}, nil
}

// AccountHeld starts the notice and returns.
func (h *heldAccounts) AccountHeld(ctx context.Context, principal string) {
	if h.mailer == nil {
		return
	}
	// The request's context carries its values — a trace id, a logger — but its
	// cancellation belongs to the response, not to this work.
	detached := context.WithoutCancel(ctx)
	go h.announce(detached, principal)
}

// announce reads the account and hands it to the mailer.
//
// Every failure ends here: the person has already been served their page, and there
// is nothing a login could do about a mail server anyway.
func (h *heldAccounts) announce(ctx context.Context, principal string) {
	if h.done != nil {
		defer close(h.done)
	}

	account, err := h.store.PrincipalByID(ctx, principal)
	if err != nil {
		h.log(ctx, "reading the held account for its notification failed", err)
		return
	}
	if _, err := h.mailer.AccountWaiting(ctx, principal, account.Email); err != nil {
		// The mailer has already logged the cause; this line says which stage
		// gave up, without repeating the address.
		h.log(ctx, "the held-account notification was not delivered", err)
	}
}

// log records one failure, never an address.
func (h *heldAccounts) log(ctx context.Context, message string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.WarnContext(ctx, message, slog.String("error", err.Error()))
}

// maxSMTPSecretBytes bounds the secret file. An application secret is a few dozen
// bytes; anything larger is a pointed-at-the-wrong-file mistake.
const maxSMTPSecretBytes = 4096

// newMailer builds the mailer the configuration describes.
//
// It returns a nil mailer when no SMTP host is configured, which is the default and a
// complete configuration: no mail is sent and the login flow is unchanged. A
// half-filled configuration is a start-up failure instead, because an operator who
// named a server and forgot the recipients wanted mail and would not get it.
func newMailer(cfg config.Config, logger *slog.Logger) (*notify.Mailer, error) {
	mail := notify.Config{
		Host:         cfg.SMTPHost,
		Port:         cfg.SMTPPort,
		User:         cfg.SMTPUser,
		From:         cfg.SMTPFrom,
		To:           cfg.SMTPTo,
		TLS:          notify.TLSMode(cfg.SMTPTLS),
		DashboardURL: cfg.DashboardURL,
		Logger:       logger,
	}

	if mail.Configured() && cfg.SMTPSecretFile != "" {
		content, err := securefile.ReadFile(cfg.SMTPSecretFile, maxSMTPSecretBytes)
		if err != nil {
			return nil, smtpSecretError(cfg.SMTPSecretFile, err)
		}
		mail.Secret = strings.TrimSpace(string(content))
	}

	mailer, err := notify.New(mail)
	if err != nil {
		return nil, fmt.Errorf("configuring the pending-account notification: %w", err)
	}
	return mailer, nil
}
