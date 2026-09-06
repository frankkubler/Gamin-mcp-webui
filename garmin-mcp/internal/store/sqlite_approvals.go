package store

// The operator's decision about an account.
//
// Upstream, an account may be used from the instant its Garmin login succeeds. This
// fork lets an operator require a decision first, so a stranger who signs in gets an
// account that waits. The decision is stored here; it is enforced wherever an access
// token is read, and at the end of the browser login.
//
// Absence of a row is "pending". Nothing has to run for a new account to start out
// waiting, which is what makes the gate fail closed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// maxApprovalNoteLength and maxDecidedByLength bound the two operator-written columns.
const (
	maxApprovalNoteLength = 500
	maxDecidedByLength    = 128
)

// An ApprovalState is where an account stands with the operator.
type ApprovalState string

// The three states. Pending is never stored: it is the absence of a row.
const (
	// ApprovalPending means nobody has decided yet.
	ApprovalPending ApprovalState = "pending"
	// ApprovalApproved means the account may be used.
	ApprovalApproved ApprovalState = "approved"
	// ApprovalBlocked means the operator refused it. It differs from pending only
	// in what it tells a human, and both refuse the account.
	ApprovalBlocked ApprovalState = "blocked"
)

// valid reports whether a state may be stored. Pending is not storable by design.
func (s ApprovalState) valid() bool {
	return s == ApprovalApproved || s == ApprovalBlocked
}

// An Approval is one recorded decision.
type Approval struct {
	PrincipalID string
	State       ApprovalState
	DecidedAt   time.Time
	DecidedBy   string
	Note        string
}

// AccountApproval reports where one account stands.
//
// A principal with no row reports ApprovalPending with a zero instant, which is not
// an error: it is the ordinary state of an account nobody has looked at yet.
func (s *SQLiteStore) AccountApproval(
	ctx context.Context, principalID string,
) (Approval, error) {
	if err := checkStoreRequest(ctx); err != nil {
		return Approval{}, err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return Approval{}, err
	}

	var state, decidedText, decidedBy, note string
	err := s.db.QueryRowContext(ctx,
		`SELECT state, decided_at, decided_by, note FROM account_approvals
		  WHERE principal_id = ?`, principalID).Scan(&state, &decidedText, &decidedBy, &note)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Approval{PrincipalID: principalID, State: ApprovalPending}, nil
	case err != nil:
		return Approval{}, fmt.Errorf("store: read account approval: %w", err)
	}

	decided, err := parseTime(decidedText)
	if err != nil {
		return Approval{}, fmt.Errorf("store: account approval timestamp: %w", err)
	}
	return Approval{
		PrincipalID: principalID,
		State:       ApprovalState(state),
		DecidedAt:   decided,
		DecidedBy:   decidedBy,
		Note:        note,
	}, nil
}

// AccountApproved reports whether an account may be used.
//
// It answers the question the login flow asks, and answers it the same way the token
// path does: only a recorded approval lets an account through, so a missing row and a
// block both refuse.
func (s *SQLiteStore) AccountApproved(ctx context.Context, principalID string) (bool, error) {
	approval, err := s.AccountApproval(ctx, principalID)
	if err != nil {
		return false, err
	}
	return approval.State == ApprovalApproved, nil
}

// SetAccountApproval records a decision, replacing any earlier one.
//
// Replacing rather than appending is deliberate: this row is the current state the
// token path reads on every request, and two rows for one account would make "is this
// account approved" a question with two answers.
//
// It reports ErrPrincipalNotFound for an unknown account, so a typo cannot leave a
// decision about nobody sitting in the table.
func (s *SQLiteStore) SetAccountApproval(
	ctx context.Context, principalID string, state ApprovalState, decidedBy, note string,
) (Approval, error) {
	if err := checkStoreRequest(ctx); err != nil {
		return Approval{}, err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return Approval{}, err
	}
	if !state.valid() {
		return Approval{}, fmt.Errorf(
			"%w: an approval is %q or %q, got %q",
			ErrInvalidPrincipal, ApprovalApproved, ApprovalBlocked, state)
	}
	if err := checkOperatorText("decided by", decidedBy, maxDecidedByLength); err != nil {
		return Approval{}, err
	}
	if err := checkOperatorText("note", note, maxApprovalNoteLength); err != nil {
		return Approval{}, err
	}

	decided := s.now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO account_approvals (principal_id, state, decided_at, decided_by, note)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (principal_id) DO UPDATE SET
		     state = excluded.state, decided_at = excluded.decided_at,
		     decided_by = excluded.decided_by, note = excluded.note`,
		principalID, string(state), formatTime(decided), decidedBy, note)
	if err != nil {
		if isForeignKeyViolation(err) {
			return Approval{}, fmt.Errorf("store: principal %s does not exist: %w",
				principalID, ErrPrincipalNotFound)
		}
		return Approval{}, fmt.Errorf("store: record account approval: %w", err)
	}
	return Approval{
		PrincipalID: principalID,
		State:       state,
		DecidedAt:   decided.UTC(),
		DecidedBy:   decidedBy,
		Note:        note,
	}, nil
}

// ClearAccountApproval puts an account back to pending by removing its decision.
//
// Deleting is what "pending" means here: the state is the absence of a row, so an
// operator who wants to undo a decision leaves nothing behind rather than a third
// stored value that the token path would have to learn about.
func (s *SQLiteStore) ClearAccountApproval(ctx context.Context, principalID string) error {
	if err := checkStoreRequest(ctx); err != nil {
		return err
	}
	if err := checkIdentifier("principal id", principalID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM account_approvals WHERE principal_id = ?`, principalID); err != nil {
		return fmt.Errorf("store: clear account approval: %w", err)
	}
	return nil
}

// checkOperatorText bounds a column an operator writes and refuses a control
// character, so neither column can carry a line break into a report or a log.
func checkOperatorText(kind, value string, limit int) error {
	if len(value) > limit {
		return fmt.Errorf("%w: %s is at most %d bytes", ErrInvalidPrincipal, kind, limit)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s carries a control character", ErrInvalidPrincipal, kind)
		}
	}
	return nil
}
