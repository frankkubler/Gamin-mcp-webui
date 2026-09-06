package cmd

// The adapter between the browser login pages and the store, for the record that a
// person accepted the privacy notice.
//
// It exists for the same reason as the authorization adapter beside it:
// internal/loginweb declares the narrow interface it needs, internal/store owns the
// rows, and neither knows the other. This file is the only place that knows both,
// which is what keeps a database handle out of the page layer.

import (
	"context"
	"errors"
	"time"

	"github.com/tamcore/garmin-mcp/internal/loginweb"
	"github.com/tamcore/garmin-mcp/internal/store"
)

// privacyConsents records privacy notice acceptances in the SQLite store.
type privacyConsents struct {
	store *store.SQLiteStore
}

// The assertion this type exists for.
var _ loginweb.PrivacyConsents = (*privacyConsents)(nil)

// newPrivacyConsents binds the adapter to the store.
func newPrivacyConsents(sqlite *store.SQLiteStore) (*privacyConsents, error) {
	if sqlite == nil {
		return nil, errors.New("cmd: the privacy consent adapter needs a store")
	}
	return &privacyConsents{store: sqlite}, nil
}

// AcceptedPrivacyNotice reports an earlier acceptance of one exact notice text.
func (p *privacyConsents) AcceptedPrivacyNotice(
	ctx context.Context, principal, digest string,
) (time.Time, bool, error) {
	acceptance, found, err := p.store.PrivacyNoticeAcceptance(ctx, principal, digest)
	if err != nil {
		return time.Time{}, false, err
	}
	return acceptance.AcceptedAt, found, nil
}

// AcceptPrivacyNotice records an acceptance. The store keeps the first instant when
// the same text is accepted again, which is the idempotence the interface requires.
func (p *privacyConsents) AcceptPrivacyNotice(
	ctx context.Context, principal, digest, version string,
) error {
	_, err := p.store.AcceptPrivacyNotice(ctx, principal, digest, version)
	return err
}
