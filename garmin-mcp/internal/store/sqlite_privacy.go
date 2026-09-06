package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Privacy notice acceptances.
//
// This is the record that a person was shown the notice and accepted it. It is kept
// apart from consents, which record what a client was authorized to do: the two
// answer different questions, are withdrawn by different acts, and one is not
// evidence of the other.

// maxNoticeVersionLength bounds the human label a build carries for its notice.
const maxNoticeVersionLength = 64

// noticeHashLength is the length of a lowercase hex SHA-256 digest.
const noticeHashLength = 64

// A PrivacyNoticeAcceptance is one person's acceptance of one exact notice text.
//
// Hash identifies the text: it is the digest of the bytes that were rendered, so the
// row says what was accepted and not merely which label it went under. Version is
// the label that build carried, kept for a human reading a report.
type PrivacyNoticeAcceptance struct {
	PrincipalID string
	Hash        string
	Version     string
	AcceptedAt  time.Time
}

// AcceptPrivacyNotice records that a principal accepted one notice text.
//
// It is idempotent on the (principal, hash) key: accepting the same text twice keeps
// the first instant, because the first acceptance is the one that happened. A
// different text is a different row, never an overwrite, so the trail of what was
// accepted survives.
//
// It reports ErrPrincipalNotFound when no such principal exists, rather than writing
// an acceptance for an account that is not there.
func (s *SQLiteStore) AcceptPrivacyNotice(
	ctx context.Context, principalID, hash, version string,
) (PrivacyNoticeAcceptance, error) {
	if err := checkStoreRequest(ctx); err != nil {
		return PrivacyNoticeAcceptance{}, err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return PrivacyNoticeAcceptance{}, err
	}
	if err := checkNoticeHash(hash); err != nil {
		return PrivacyNoticeAcceptance{}, err
	}
	if err := checkNoticeVersion(version); err != nil {
		return PrivacyNoticeAcceptance{}, err
	}

	accepted := s.now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO privacy_notice_consents
		     (principal_id, notice_hash, notice_version, accepted_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (principal_id, notice_hash) DO NOTHING`,
		principalID, hash, version, formatTime(accepted))
	if err != nil {
		if isForeignKeyViolation(err) {
			return PrivacyNoticeAcceptance{}, fmt.Errorf(
				"store: principal %s does not exist: %w", principalID, ErrPrincipalNotFound)
		}
		return PrivacyNoticeAcceptance{}, fmt.Errorf("store: record privacy notice consent: %w", err)
	}

	// The row is read back rather than assumed: on a repeat acceptance the stored
	// instant is the earlier one, and that is the answer a caller must be given.
	stored, found, err := s.PrivacyNoticeAcceptance(ctx, principalID, hash)
	if err != nil {
		return PrivacyNoticeAcceptance{}, err
	}
	if !found {
		return PrivacyNoticeAcceptance{}, fmt.Errorf(
			"store: privacy notice consent vanished after insert: %w", ErrCorruptRecord)
	}
	return stored, nil
}

// PrivacyNoticeAcceptance reports whether a principal has accepted one exact notice
// text, and when.
//
// A missing row is not an error: it is the ordinary state of a person who has not
// been asked yet, or who was asked about a different text.
func (s *SQLiteStore) PrivacyNoticeAcceptance(
	ctx context.Context, principalID, hash string,
) (PrivacyNoticeAcceptance, bool, error) {
	if err := checkStoreRequest(ctx); err != nil {
		return PrivacyNoticeAcceptance{}, false, err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return PrivacyNoticeAcceptance{}, false, err
	}
	if err := checkNoticeHash(hash); err != nil {
		return PrivacyNoticeAcceptance{}, false, err
	}

	var version, acceptedText string
	err := s.db.QueryRowContext(ctx,
		`SELECT notice_version, accepted_at FROM privacy_notice_consents
		  WHERE principal_id = ? AND notice_hash = ?`,
		principalID, hash).Scan(&version, &acceptedText)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return PrivacyNoticeAcceptance{}, false, nil
	case err != nil:
		return PrivacyNoticeAcceptance{}, false, fmt.Errorf(
			"store: read privacy notice consent: %w", err)
	}

	accepted, err := parseTime(acceptedText)
	if err != nil {
		return PrivacyNoticeAcceptance{}, false, fmt.Errorf(
			"store: privacy notice consent timestamp: %w", err)
	}
	return PrivacyNoticeAcceptance{
		PrincipalID: principalID,
		Hash:        hash,
		Version:     version,
		AcceptedAt:  accepted,
	}, true, nil
}

// PrivacyNoticeAcceptances returns every acceptance one principal has recorded,
// most recent first. It is the report an operator reads; the login flow asks the
// narrower question above.
func (s *SQLiteStore) PrivacyNoticeAcceptances(
	ctx context.Context, principalID string,
) ([]PrivacyNoticeAcceptance, error) {
	if err := checkStoreRequest(ctx); err != nil {
		return nil, err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT notice_hash, notice_version, accepted_at FROM privacy_notice_consents
		  WHERE principal_id = ? ORDER BY accepted_at DESC, notice_hash`,
		principalID)
	if err != nil {
		return nil, fmt.Errorf("store: list privacy notice consents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var acceptances []PrivacyNoticeAcceptance
	for rows.Next() {
		var hash, version, acceptedText string
		if err := rows.Scan(&hash, &version, &acceptedText); err != nil {
			return nil, fmt.Errorf("store: scan privacy notice consent: %w", err)
		}
		accepted, parseErr := parseTime(acceptedText)
		if parseErr != nil {
			return nil, fmt.Errorf("store: privacy notice consent timestamp: %w", parseErr)
		}
		acceptances = append(acceptances, PrivacyNoticeAcceptance{
			PrincipalID: principalID,
			Hash:        hash,
			Version:     version,
			AcceptedAt:  accepted,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate privacy notice consents: %w", err)
	}
	return acceptances, nil
}

// checkNoticeHash refuses anything that is not a lowercase hex SHA-256 digest, so a
// caller cannot store a label, a path or a fragment of the notice itself in the
// column the flow compares on.
func checkNoticeHash(hash string) error {
	if len(hash) != noticeHashLength {
		return fmt.Errorf("%w: a notice hash is %d hex characters, got %d",
			ErrInvalidPrincipal, noticeHashLength, len(hash))
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: a notice hash is lowercase hex", ErrInvalidPrincipal)
		}
	}
	return nil
}

// checkNoticeVersion bounds the label and refuses a control character, which is the
// only way this column could carry something other than a name.
func checkNoticeVersion(version string) error {
	if version == "" {
		return fmt.Errorf("%w: a notice version label is required", ErrInvalidPrincipal)
	}
	if len(version) > maxNoticeVersionLength {
		return fmt.Errorf("%w: a notice version label is at most %d bytes",
			ErrInvalidPrincipal, maxNoticeVersionLength)
	}
	for _, r := range version {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: a notice version label carries no control character",
				ErrInvalidPrincipal)
		}
	}
	return nil
}
