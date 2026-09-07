//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// This file proves the fork's account-approval gate at the binary level: a
// principal the operator has not approved gets no access, and the refusal comes
// from the running server rather than from a unit test's fake.
//
// The gate lives on the token read (internal/store.SQLiteStore.checkApproved),
// not on the login, so that withdrawing an approval stops the account at its
// next request rather than at its next sign-in. Only an end-to-end request can
// show that: the token is minted by the real /token endpoint and then presented
// to the real MCP endpoint.
//
// Both principals are seeded before the process starts, for the reason
// seed_test.go states at length: nothing may write to the database while the
// server holds it. That is also why this test cannot withdraw an approval
// mid-run — it seeds one account that never had one instead, which exercises
// the same branch.

// TestAnAccountAwaitingApprovalIsRefusedAtTheMCPEndpoint is the mutant this test
// catches: a build that applied the approval gate only on the login path — the
// shape this fork shipped once already — would let a held account's token
// authenticate every subsequent request, which is the whole thing the gate
// exists to prevent.
func TestAnAccountAwaitingApprovalIsRefusedAtTheMCPEndpoint(t *testing.T) {
	var held, approved seededCode
	server := startRemoteServerSeeded(t, func(seedDir, origin string) {
		sqlite := openSeedStore(t, seedDir)
		defer func() { _ = sqlite.Close() }()

		seedClient(t, sqlite)
		held = seedOneCode(t, sqlite, origin,
			seedPrincipal(t, sqlite, "e2e-approval-held@example.test"))
		approved = seedOneCode(t, sqlite, origin,
			seedApprovedPrincipal(t, sqlite, "e2e-approval-granted@example.test"))
	})

	// Minting is not where the gate sits, and this asserts that rather than
	// assuming it: redeemCode fails the test on any status but 200, so a held
	// account reaching this line proves its code was redeemed like any other.
	heldToken := redeemCode(t, server, held)
	approvedToken := redeemCode(t, server, approved)

	// The positive control. Without it, a deployment that refused every request
	// for an unrelated reason would pass the assertion below.
	if status := initializeStatus(t, server, approvedToken); status != http.StatusOK {
		t.Fatalf("the approved account's token = %d, want 200", status)
	}

	refused := postInitialize(t, server,
		map[string]string{"Authorization": "Bearer " + heldToken})
	defer func() { _ = refused.Body.Close() }()

	if refused.StatusCode != http.StatusUnauthorized {
		t.Errorf("the held account's token = %d, want 401", refused.StatusCode)
	}
	if challenge := refused.Header.Get("WWW-Authenticate"); !strings.Contains(
		challenge, `error="invalid_token"`) {
		t.Errorf("challenge = %q, want error=\"invalid_token\"", challenge)
	}
}
