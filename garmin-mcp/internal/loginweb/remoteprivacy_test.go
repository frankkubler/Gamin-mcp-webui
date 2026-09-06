package loginweb_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The privacy notice on the consent page: what it shows, what it refuses, and what
// it records.
//
// The digest of the notice never appears in these tests. That is deliberate: a test
// that knew the digest would be asserting an implementation detail, and the property
// that matters is behavioural — a person who has accepted the text this build serves
// is not asked again, and one who has not cannot grant anything.

func TestConsentPageShowsTheNoticeAndAsksForAcceptance(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	page := h.reachConsent()

	// The summary is on the page itself, not behind the disclosure: a reader who
	// opens nothing still sees what is recorded and what is not.
	for _, want := range []string{
		"Ce que ce déploiement enregistre à votre sujet",
		"Jamais enregistré",
		"Lire la notice en entier",
		`name="privacy_accepted"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the consent page does not carry %q", want)
		}
	}
	// The full text is in the document too, so accepting does not depend on a
	// second request that could fail or be blocked.
	if !strings.Contains(page, "Ce qui est écrit dans la base") {
		t.Error("the full notice is not in the consent page")
	}
	if h.privacy.count() != 0 {
		t.Error("rendering the page recorded an acceptance")
	}
}

func TestGrantingWithoutAcceptingTheNoticeIsRefused(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	page := h.reachConsent()
	resp, again := h.decideWithoutAccepting(page, decisionAllow)

	wantStatus(t, resp, http.StatusBadRequest, "POST /login/consent")
	if !strings.Contains(again, "Acceptez la notice de confidentialité") {
		t.Error("the re-rendered page does not say what has to be done")
	}
	if !strings.Contains(again, `name="privacy_accepted"`) {
		t.Error("the re-rendered page dropped the acceptance box")
	}
	_, grants, denials := h.authz.counts()
	if grants != 0 || denials != 0 {
		t.Errorf("grants = %d denials = %d, want the transaction still open", grants, denials)
	}
	if h.privacy.count() != 0 {
		t.Error("a refused submission recorded an acceptance")
	}

	// The session survives, so the person can tick the box and go on: the form
	// token was rotated, and the re-rendered page carries the new one.
	final := h.decide(again, decisionAllow)
	wantStatus(t, final, http.StatusSeeOther, "POST /login/consent after accepting")
	if h.privacy.count() != 1 {
		t.Errorf("recorded %d acceptances, want exactly one", h.privacy.count())
	}
}

func TestAcceptingRecordsExactlyOneConsent(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	resp := h.decide(h.reachConsent(), decisionAllow)

	wantStatus(t, resp, http.StatusSeeOther, "POST /login/consent")
	if h.privacy.count() != 1 {
		t.Errorf("recorded %d acceptances, want exactly one", h.privacy.count())
	}
	if _, grants, _ := h.authz.counts(); grants != 1 {
		t.Errorf("grants = %d, want the grant to follow the acceptance", grants)
	}
}

func TestDenyingNeedsNoAcceptance(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	page := h.reachConsent()
	resp, _ := h.decideWithoutAccepting(page, decisionDeny)

	// Refusing the notice and refusing the client are the same act for someone who
	// does not want this: it must not require accepting anything first.
	wantStatus(t, resp, http.StatusSeeOther, "POST /login/consent with deny")
	if h.privacy.count() != 0 {
		t.Error("a denial recorded an acceptance")
	}
	if _, _, denials := h.authz.counts(); denials != 1 {
		t.Errorf("denials = %d, want the denial to have gone through", denials)
	}
}

func TestASecondLoginIsNotAskedAgain(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	h.decide(h.reachConsent(), decisionAllow)

	// Same person, same notice text: the page states when they accepted it and
	// offers no box, and granting again records nothing new.
	second := h.reachConsent()
	if strings.Contains(second, `name="privacy_accepted"`) {
		t.Error("a person who already accepted this notice was asked again")
	}
	if !strings.Contains(second, "Vous avez accepté cette notice le") {
		t.Error("the page does not state when the notice was accepted")
	}
	resp := h.decide(second, decisionAllow)
	wantStatus(t, resp, http.StatusSeeOther, "POST /login/consent, second time")
	if h.privacy.count() != 1 {
		t.Errorf("recorded %d acceptances, want the first one only", h.privacy.count())
	}
}

func TestAnUnreadableConsentStoreStopsTheGrant(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})
	h.authorize()
	h.submitRemoteCredentials(h.continueToCredentials())
	h.privacy.readErr = errors.New("the consent store is unreachable")

	resp, _ := h.b.get(pathConsent)

	// Neither answer the page could stand in for is safe, so it serves neither.
	wantStatus(t, resp, http.StatusServiceUnavailable, "GET /login/consent")
	if _, grants, _ := h.authz.counts(); grants != 0 {
		t.Error("a grant went through while the consent store was unreadable")
	}
}

func TestAConsentThatCannotBeRecordedStopsTheGrant(t *testing.T) {
	t.Parallel()
	h := newRemote(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()})

	page := h.reachConsent()
	h.privacy.acceptErr = errors.New("the consent store is read-only")
	resp := h.decide(page, decisionAllow)

	// The ordering this asserts is the point of the feature: no token is issued
	// against a consent that was not persisted.
	wantStatus(t, resp, http.StatusServiceUnavailable, "POST /login/consent")
	if _, grants, _ := h.authz.counts(); grants != 0 {
		t.Errorf("grants = %d, want none when the acceptance could not be recorded", grants)
	}
}

// The approval gate: an account the operator has not decided about reaches no page
// that could grant anything.

// fakeApprovals is the operator's decision under test control.
type fakeApprovals struct {
	approved map[string]bool
	err      error
	asked    int
}

func (f *fakeApprovals) AccountApproved(_ context.Context, principal string) (bool, error) {
	f.asked++
	if f.err != nil {
		return false, f.err
	}
	return f.approved[principal], nil
}

// newGatedRemote is a remote profile with the approval gate wired in.
func newGatedRemote(t *testing.T, approvals *fakeApprovals) *remoteHarness {
	t.Helper()
	return newRemoteWith(t, &fakeAuthenticator{loginAttempt: remoteSucceeded()}, approvals)
}

func TestAHeldAccountNeverReachesTheConsentPage(t *testing.T) {
	t.Parallel()
	h := newGatedRemote(t, &fakeApprovals{approved: map[string]bool{}})

	h.authorize()
	resp, page := h.submitCredentialsPage(h.continueToCredentials())

	// The login succeeded and the account exists; what it may not do is go on.
	wantStatus(t, resp, http.StatusForbidden, "POST /login/credentials for a held account")
	if !strings.Contains(page, "attend la validation de l'exploitant") {
		t.Error("the page does not say the account is waiting")
	}
	// The notice is on it: the account exists, so its data is already stored.
	if !strings.Contains(page, "Ce que ce déploiement enregistre à votre sujet") {
		t.Error("the pending page does not carry the privacy notice")
	}
	if _, grants, denials := h.authz.counts(); grants != 0 || denials != 1 {
		t.Errorf("grants = %d denials = %d, want the transaction closed and nothing granted",
			grants, denials)
	}
	if h.privacy.count() != 0 {
		t.Error("a held account recorded a privacy acceptance")
	}

	// The session is gone with it: the consent page is not reachable by hand.
	consent, _ := h.b.get(pathConsent)
	if consent.StatusCode == http.StatusOK {
		t.Error("the consent page is reachable after the account was held")
	}
}

func TestAnApprovedAccountPassesTheGate(t *testing.T) {
	t.Parallel()
	approvals := &fakeApprovals{approved: map[string]bool{testPrincipal: true}}
	h := newGatedRemote(t, approvals)

	page := h.reachConsent()
	resp := h.decide(page, decisionAllow)

	wantStatus(t, resp, http.StatusSeeOther, "POST /login/consent")
	if approvals.asked == 0 {
		t.Error("the gate was never asked")
	}
	if _, grants, _ := h.authz.counts(); grants != 1 {
		t.Errorf("grants = %d, want the approved account through", grants)
	}
}

func TestAnUnreadableApprovalStoreStopsTheLogin(t *testing.T) {
	t.Parallel()
	h := newGatedRemote(t, &fakeApprovals{err: errors.New("the approval store is unreachable")})

	h.authorize()
	resp := h.submitRemoteCredentials(h.continueToCredentials())

	wantStatus(t, resp, http.StatusServiceUnavailable, "POST /login/credentials")
	if _, grants, _ := h.authz.counts(); grants != 0 {
		t.Error("a grant went through while the approval store was unreadable")
	}
}
