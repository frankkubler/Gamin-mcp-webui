package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tamcore/garmin-mcp/internal/store"
)

// newApprovalStore opens a store with the approval gate on, which is the shape a
// deployment that requires a decision runs with. The gate is off by default, so
// every other test in this package exercises the upstream behaviour unchanged.
func newApprovalStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	clock := newFakeClock()
	opened, err := store.OpenSQLite(t.Context(), store.SQLiteConfig{
		Path:            testDBPath(t),
		Key:             testKey(t),
		Now:             clock.Now,
		RequireApproval: true,
	})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return opened
}

func TestANewAccountStartsPending(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	// Nothing ran to make this so: the absence of a row is the pending state, which
	// is what makes the gate fail closed for an account nobody has looked at.
	approval, err := opened.AccountApproval(ctx, principal.ID)
	if err != nil {
		t.Fatalf("AccountApproval: %v", err)
	}
	if approval.State != store.ApprovalPending {
		t.Errorf("state = %q, want pending", approval.State)
	}
	if !approval.DecidedAt.IsZero() {
		t.Error("a pending account carries no decision instant")
	}
	approved, err := opened.AccountApproved(ctx, principal.ID)
	if err != nil || approved {
		t.Errorf("AccountApproved = %v err = %v, want false", approved, err)
	}
}

func TestApprovingAndBlockingAnAccount(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	recorded, err := opened.SetAccountApproval(
		ctx, principal.ID, store.ApprovalApproved, "operateur", "vu avec l'equipe")
	if err != nil {
		t.Fatalf("SetAccountApproval: %v", err)
	}
	if recorded.State != store.ApprovalApproved || recorded.DecidedBy != "operateur" {
		t.Errorf("recorded %+v, want the decision as made", recorded)
	}
	if approved, _ := opened.AccountApproved(ctx, principal.ID); !approved {
		t.Error("an approved account does not report as approved")
	}

	// A second decision replaces the first: this row is the current state, and two
	// answers to "is this account approved" is not a state worth having.
	if _, err := opened.SetAccountApproval(
		ctx, principal.ID, store.ApprovalBlocked, "operateur", "depart"); err != nil {
		t.Fatalf("SetAccountApproval, blocking: %v", err)
	}
	blocked, err := opened.AccountApproval(ctx, principal.ID)
	if err != nil {
		t.Fatalf("AccountApproval: %v", err)
	}
	if blocked.State != store.ApprovalBlocked || blocked.Note != "depart" {
		t.Errorf("state = %+v, want the block recorded", blocked)
	}
	if approved, _ := opened.AccountApproved(ctx, principal.ID); approved {
		t.Error("a blocked account still reports as approved")
	}
}

func TestClearingADecisionReturnsToPending(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := opened.SetAccountApproval(
		ctx, principal.ID, store.ApprovalApproved, "operateur", ""); err != nil {
		t.Fatalf("SetAccountApproval: %v", err)
	}
	if err := opened.ClearAccountApproval(ctx, principal.ID); err != nil {
		t.Fatalf("ClearAccountApproval: %v", err)
	}

	approval, err := opened.AccountApproval(ctx, principal.ID)
	if err != nil {
		t.Fatalf("AccountApproval: %v", err)
	}
	if approval.State != store.ApprovalPending {
		t.Errorf("state = %q, want pending after clearing", approval.State)
	}
}

func TestSetAccountApprovalRefusesWhatItCannotStore(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)
	ctx := t.Context()

	principal, err := opened.CreatePrincipal(ctx, testEmailNormalized)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	if _, err := opened.SetAccountApproval(
		ctx, "00000000-0000-4000-8000-000000000000", store.ApprovalApproved, "x", ""); !errors.Is(
		err, store.ErrPrincipalNotFound) {
		t.Errorf("err = %v, want ErrPrincipalNotFound for an unknown account", err)
	}

	cases := map[string]struct {
		state     store.ApprovalState
		decidedBy string
		note      string
	}{
		"pending is not storable": {store.ApprovalPending, "x", ""},
		"unknown state":           {store.ApprovalState("maybe"), "x", ""},
		"control character":       {store.ApprovalApproved, "x", "deux\nlignes"},
		"note too long":           {store.ApprovalApproved, "x", strings.Repeat("n", 501)},
		"decided by too long":     {store.ApprovalApproved, strings.Repeat("o", 129), ""},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := opened.SetAccountApproval(
				ctx, principal.ID, input.state, input.decidedBy, input.note); err == nil {
				t.Error("the store accepted a decision it must refuse")
			}
		})
	}
}

func TestAPendingAccountCannotUseItsAccessToken(t *testing.T) {
	t.Parallel()
	opened := newApprovalStore(t)
	ctx := t.Context()

	grant := seedGrant(t, opened)

	// The token itself is intact; the account is what is not allowed through, and
	// the error says so rather than claiming the token was revoked.
	_, err := opened.LookupAccessToken(ctx, grant.access)
	if !errors.Is(err, store.ErrAccountNotApproved) {
		t.Fatalf("err = %v, want ErrAccountNotApproved", err)
	}

	if _, err := opened.SetAccountApproval(
		ctx, grant.principal.ID, store.ApprovalApproved, "operateur", ""); err != nil {
		t.Fatalf("SetAccountApproval: %v", err)
	}
	if _, err := opened.LookupAccessToken(ctx, grant.access); err != nil {
		t.Fatalf("LookupAccessToken after approval: %v", err)
	}
}

func TestAPendingAccountCannotAuthorizeAnMCPRequest(t *testing.T) {
	t.Parallel()
	opened := newApprovalStore(t)
	ctx := t.Context()

	grant := seedGrant(t, opened)

	// This is the read an MCP request actually goes through: the OAuth server's
	// VerifyAccessToken calls it, by way of the oauthstore adapter. Gating only
	// LookupAccessToken would leave this path open, so the gate has to be here.
	if _, err := opened.ReadAccessToken(ctx, grant.access); !errors.Is(
		err, store.ErrAccountNotApproved) {
		t.Fatalf("err = %v, want ErrAccountNotApproved on the request path", err)
	}

	// The refresh token reads back: the grant that consumes it is refused by
	// RotateRefreshToken, and the revocation endpoint has to keep working for an
	// account that was just blocked.
	if _, err := opened.ReadRefreshToken(ctx, grant.refresh); err != nil {
		t.Errorf("ReadRefreshToken: %v, want the record for the revocation path", err)
	}
	if _, err := opened.RotateRefreshToken(ctx, store.RefreshRotation{
		Presented:        grant.refresh,
		NextAccessToken:  store.NewSecret("next-access"),
		NextRefreshToken: store.NewSecret("next-refresh"),
		AccessLifetime:   10 * time.Minute,
		RefreshLifetime:  24 * time.Hour,
	}); !errors.Is(err, store.ErrAccountNotApproved) {
		t.Errorf("RotateRefreshToken err = %v, want the rotation refused too", err)
	}

	if _, err := opened.SetAccountApproval(
		ctx, grant.principal.ID, store.ApprovalApproved, "operateur", ""); err != nil {
		t.Fatalf("SetAccountApproval: %v", err)
	}
	if _, err := opened.ReadAccessToken(ctx, grant.access); err != nil {
		t.Errorf("ReadAccessToken after approval: %v", err)
	}
}

func TestWithdrawingAnApprovalStopsTheNextRequest(t *testing.T) {
	t.Parallel()
	opened := newApprovalStore(t)
	ctx := t.Context()

	grant := seedGrant(t, opened)
	if _, err := opened.SetAccountApproval(
		ctx, grant.principal.ID, store.ApprovalApproved, "operateur", ""); err != nil {
		t.Fatalf("SetAccountApproval: %v", err)
	}
	if _, err := opened.LookupAccessToken(ctx, grant.access); err != nil {
		t.Fatalf("LookupAccessToken: %v", err)
	}

	// The point of checking on access rather than at login: a withdrawal has to
	// bite at the next request, not at the next sign-in.
	if _, err := opened.SetAccountApproval(
		ctx, grant.principal.ID, store.ApprovalBlocked, "operateur", ""); err != nil {
		t.Fatalf("SetAccountApproval, blocking: %v", err)
	}
	if _, err := opened.LookupAccessToken(ctx, grant.access); !errors.Is(
		err, store.ErrAccountNotApproved) {
		t.Errorf("err = %v, want the live token refused", err)
	}
}

func TestTheGateIsOffUnlessTheStoreAsksForIt(t *testing.T) {
	t.Parallel()
	opened, _ := newTestStore(t)

	// Same pending account, a store opened the upstream way: the token works. This
	// is what keeps the fork's default behaviour identical to upstream's.
	grant := seedGrant(t, opened)
	if _, err := opened.LookupAccessToken(t.Context(), grant.access); err != nil {
		t.Errorf("LookupAccessToken: %v, want the upstream shape unaffected", err)
	}
}
