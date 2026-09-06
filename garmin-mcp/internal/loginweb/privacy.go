package loginweb

// The privacy notice and its acceptance.
//
// The notice is a document this build carries, not a setting: it is embedded, it is
// digested at start-up, and an acceptance is recorded against that digest. An
// operator who edits the text therefore cannot leave people holding a consent to
// something they were never shown — the digest changes, no stored acceptance matches
// it, and everyone is asked again. That is the whole mechanism, and it needs no
// operator action to work.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PrivacyNoticeVersion is the human label this build's notice carries.
//
// It is stored beside the digest so a report reads a date rather than 64 hex
// characters. It is not what decides whether a person is asked again: the digest is.
// Bump it when you edit pages/remote/privacy.html, so the two agree.
const PrivacyNoticeVersion = "2026-09-06-fr"

// privacyNoticeFile is the embedded document whose bytes are the notice.
const privacyNoticeFile = "pages/remote/privacy.html"

// ErrNoPrivacyConsents reports a nil RemoteConfig.PrivacyConsents.
//
// It is a start-up failure rather than a silent degradation on purpose: a consent
// gate that is switched off by a missing field is worse than no gate at all, because
// the pages still say the acceptance was recorded.
var ErrNoPrivacyConsents = errors.New("loginweb: no privacy consent store")

// PrivacyConsents records what a person accepted, and reports what they accepted
// before.
//
// The interface lives with its consumer and is deliberately narrow: this package
// passes an opaque principal identifier and an opaque digest, and never sees a
// stored row, an e-mail address or anything else about the account.
type PrivacyConsents interface {
	// AcceptedPrivacyNotice reports whether principal has accepted the notice with
	// this digest, and when. A principal who was never asked is not an error.
	AcceptedPrivacyNotice(
		ctx context.Context, principal, digest string,
	) (time.Time, bool, error)

	// AcceptPrivacyNotice records an acceptance. It must be idempotent: a repeat
	// keeps the first instant, because the first acceptance is the one that
	// happened.
	AcceptPrivacyNotice(ctx context.Context, principal, digest, version string) error
}

// Approvals reports the operator's decision about an account.
//
// The interface lives with its consumer and is deliberately narrow: this package
// passes an opaque principal identifier and gets a yes or a no. It never sees who
// decided, when, or why — that belongs to the interface an operator uses, not to a
// login page.
type Approvals interface {
	// AccountApproved reports whether this account may be used. An account nobody
	// has decided about must report false, so a new account is held rather than
	// let through.
	AccountApproved(ctx context.Context, principal string) (bool, error)
}

// privacyNotice is the identity of the text this build serves.
type privacyNotice struct {
	// version is the human label.
	version string
	// digest is the lowercase hex SHA-256 of the notice document.
	digest string
}

// loadPrivacyNotice digests the embedded notice.
//
// The digest is taken over the template source rather than over a rendered page. The
// document holds no dynamic value — no name, no date, no account — so the two differ
// only by the {{define}} wrappers, and the source digest changes if and only if the
// text a reader sees changes. Digesting the source also means the value is fixed at
// start-up rather than recomputed per request.
func loadPrivacyNotice() (privacyNotice, error) {
	raw, err := assets.ReadFile(privacyNoticeFile)
	if err != nil {
		return privacyNotice{}, fmt.Errorf("loginweb: reading the privacy notice: %w", err)
	}
	sum := sha256.Sum256(raw)
	return privacyNotice{version: PrivacyNoticeVersion, digest: hex.EncodeToString(sum[:])}, nil
}

// privacyState is what the consent page says about the notice.
//
// Every field is server-authored: a label this build carries, a flag, and a date
// formatted here. Nothing a request submitted reaches it.
type privacyState struct {
	// Version is the label of the text being shown.
	Version string
	// Required reports that this person has not accepted this text yet, so the
	// form must carry the acceptance box and the server must find it ticked.
	Required bool
	// AcceptedOn is the date of an earlier acceptance of this same text, empty
	// when Required is true.
	AcceptedOn string
}

// privacyStateFor asks the store what this principal has already accepted.
//
// A store failure is reported rather than assumed either way: treating an unreadable
// store as "already accepted" would grant access to someone who never consented, and
// treating it as "not accepted" would silently record a fresh acceptance that the
// same broken store cannot keep.
func (s *RemoteServer) privacyStateFor(
	ctx context.Context, principal string,
) (privacyState, error) {
	acceptedAt, found, err := s.privacy.AcceptedPrivacyNotice(ctx, principal, s.notice.digest)
	if err != nil {
		return privacyState{}, err
	}
	state := privacyState{Version: s.notice.version, Required: !found}
	if found {
		state.AcceptedOn = acceptedAt.UTC().Format(time.DateOnly)
	}
	return state, nil
}
