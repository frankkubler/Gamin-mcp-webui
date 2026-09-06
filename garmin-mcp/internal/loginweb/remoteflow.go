package loginweb

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/tamcore/garmin-mcp/internal/garmin/auth"
)

// handleMFAForm renders the one-time code form. It is a separate page from the
// credential form, and reaching it proves only that a login is pending.
func (s *RemoteServer) handleMFAForm(w http.ResponseWriter, r *http.Request) {
	session, disclosure, ok := s.live(w, r)
	if !ok {
		return
	}
	if session.state() != auth.StateMFAPending {
		s.notFound(w)
		return
	}
	s.pages.render(w, http.StatusOK, pageMFA, s.challengeData(session, disclosure, ""))
}

// handleMFASubmit submits one one-time code against the server-side continuation.
func (s *RemoteServer) handleMFASubmit(w http.ResponseWriter, r *http.Request) {
	session, err := s.session(r)
	if err != nil {
		s.refuse(w, err)
		return
	}
	if !s.parseBounded(w, r) {
		return
	}
	if err := session.accept(s.now(), r.PostFormValue(fieldToken),
		auth.StateMFAPending, s.maxAttempts); err != nil {
		s.refuseSession(w, session, err)
		return
	}

	code := r.PostFormValue(fieldCode)
	if len(code) > MaxCodeLen {
		s.retry(w, r, session, pageMFA, msgFieldTooLong)
		return
	}

	attempt, err := s.authenticator.CompleteMFA(r.Context(), session.continuation(), code)
	dropCredentials(&code)

	if err != nil {
		if mfaFailureIsRetryable(err) {
			s.log(r.Context(), "the one-time code was not accepted")
			s.retry(w, r, session, pageMFA, msgCodeRejected)
			return
		}
		s.log(r.Context(), "the login could not continue")
		s.abandon(w, session)
		return
	}
	s.resolvePrincipal(w, r, session, attempt)
}

// challenge records an MFA challenge and sends the browser to the code form.
func (s *RemoteServer) challenge(
	w http.ResponseWriter, r *http.Request, session *remoteSession, attempt Attempt,
) {
	if err := session.challenged(attempt); err != nil {
		s.abandon(w, session)
		return
	}
	s.log(r.Context(), "garmin asked for a one-time code")
	http.Redirect(w, r, routeRemoteMFA, http.StatusSeeOther)
}

// resolvePrincipal records the principal a completed Garmin login resolved to and
// sends the browser to the consent page.
//
// Nothing is persisted yet. Whatever the login produced stays inside the bounded
// transaction until consent is confirmed, and a denial or an expiry discards it.
func (s *RemoteServer) resolvePrincipal(
	w http.ResponseWriter, r *http.Request, session *remoteSession, attempt Attempt,
) {
	if attempt.Principal == "" {
		s.abandon(w, session)
		return
	}
	if err := session.authenticated(attempt); err != nil {
		s.abandon(w, session)
		return
	}
	if err := s.authorizations.AttachPrincipal(
		r.Context(), session.capability, attempt.Principal); err != nil {
		s.abandon(w, session)
		return
	}
	s.log(r.Context(), "a garmin login resolved a principal")
	http.Redirect(w, r, routeRemoteConsent, http.StatusSeeOther)
}

// handleConsentForm renders the binding decision: the same client, redirect host,
// resource and scopes the disclosure named, now with an Allow and a Deny.
func (s *RemoteServer) handleConsentForm(w http.ResponseWriter, r *http.Request) {
	session, disclosure, ok := s.live(w, r)
	if !ok {
		return
	}
	if session.state() != auth.StateAuthenticated {
		s.notFound(w)
		return
	}
	// form-action is enforced against the policy of THIS document — the one that
	// contains the Allow/Deny form — evaluated before the form's POST is sent and
	// re-evaluated on every redirect the resulting navigation takes. The eventual
	// redirect to the client's origin is one of those hops, so the origin belongs
	// here, on the GET response, not on the POST response the browser never
	// consults for this check.
	context, ok := s.consentContext(w, r, session)
	if !ok {
		return
	}
	setOutboundRedirectCSP(w, disclosure.RedirectURI)
	s.pages.render(w, http.StatusOK, pageConsent,
		s.consentData(session, disclosure, context, ""))
}

// A consentContext is what the consent page and its submission both need to know
// about the account behind the transaction.
type consentContext struct {
	// principal is the account the Garmin login resolved.
	principal string
	// privacy is what the page says about the notice.
	privacy privacyState
	// pending reports that the operator has not approved this account. The page
	// says so and still offers the notice: accepting it is the person's decision
	// about their data, and it does not wait on the operator's decision about
	// their access.
	pending bool
}

// consentContext resolves what the consent page and its submission need to know about
// the account, answering the request itself when it cannot.
//
// A store that cannot be read is a 503 rather than a page: neither answer it could
// stand in for is safe. "Already accepted" would let someone through who never
// consented, and "not accepted yet" would ask for an acceptance the same broken
// store cannot record.
func (s *RemoteServer) consentContext(
	w http.ResponseWriter, r *http.Request, session *remoteSession,
) (consentContext, bool) {
	principal, err := s.authorizations.Principal(r.Context(), session.capability)
	if err != nil {
		s.refuseSession(w, session, err)
		return consentContext{}, false
	}
	if principal == "" {
		// The state machine says the login is complete, so a transaction with no
		// principal is an inconsistency rather than a user error.
		s.notFound(w)
		return consentContext{}, false
	}
	state, err := s.privacyStateFor(r.Context(), principal)
	if err != nil {
		s.unavailable(w)
		return consentContext{}, false
	}

	pending, err := s.accountPending(r.Context(), principal)
	if err != nil {
		s.unavailable(w)
		return consentContext{}, false
	}
	return consentContext{principal: principal, privacy: state, pending: pending}, true
}

// accountPending reports whether the operator still has to decide about this account.
// A nil Approvals is the ungated shape, where nothing is ever pending.
func (s *RemoteServer) accountPending(ctx context.Context, principal string) (bool, error) {
	if s.approvals == nil {
		return false, nil
	}
	approved, err := s.approvals.AccountApproved(ctx, principal)
	if err != nil {
		// Neither answer is safe to assume: "approved" would let a held account
		// through, and "pending" would tell someone their account is waiting when
		// nobody knows.
		return false, err
	}
	return !approved, nil
}

// consentData is the consent page's data: the disclosure, the form token, what the
// page says about the notice, and any message.
func (s *RemoteServer) consentData(
	session *remoteSession, disclosure Disclosure, context consentContext, message string,
) remotePageData {
	data := newRemotePageData(disclosure, session.formToken(), message)
	data.Privacy = context.privacy
	data.AccountPending = context.pending
	return data
}

// retryConsent re-renders the consent page after a submission that granted nothing.
//
// The session is still live and its form token has been rotated by the confirm that
// let this submission in, so the re-rendered form carries the new token. The redirect
// policy is set again because this document contains the Allow form, exactly as it
// does on the first render.
func (s *RemoteServer) retryConsent(
	w http.ResponseWriter, r *http.Request, session *remoteSession, message string,
) {
	disclosure, err := s.authorizations.Disclose(r.Context(), session.capability)
	if err != nil {
		s.refuseSession(w, session, err)
		return
	}
	context, ok := s.consentContext(w, r, session)
	if !ok {
		return
	}
	setOutboundRedirectCSP(w, disclosure.RedirectURI)
	s.pages.render(w, http.StatusBadRequest, pageConsent,
		s.consentData(session, disclosure, context, sanitizedMessage(message)))
}

// handleConsentSubmit ends the transaction.
//
// The session is consumed before the authorization server is called, so a second
// submission finds nothing whatever it carries, and the cookie is cleared in the
// same response. The redirect is the client's already-validated URI, built by the
// authorization server with the client's original state, and it is forwarded
// unchanged.
func (s *RemoteServer) handleConsentSubmit(w http.ResponseWriter, r *http.Request) {
	session, err := s.session(r)
	if err != nil {
		s.refuse(w, err)
		return
	}
	if !s.parseBounded(w, r) {
		return
	}
	if err := session.confirm(s.now(), r.PostFormValue(fieldToken),
		auth.StateAuthenticated); err != nil {
		s.refuseSession(w, session, err)
		return
	}

	decision := r.PostFormValue(fieldDecision)
	if len(decision) > maxDecisionLen {
		s.abandon(w, session)
		return
	}

	// The notice gates the grant only. A denial needs no acceptance: refusing the
	// notice and refusing the client must both be possible without accepting
	// anything, and both end the transaction with nothing granted.
	if decision == decisionAllow && !s.recordPrivacyConsent(w, r, session) {
		return
	}

	s.discard(session)
	s.clearCookie(w)

	completion, err := s.decide(r.Context(), session.capability, decision == decisionAllow)
	if err != nil {
		s.notFound(w)
		return
	}
	s.log(r.Context(), "the authorization transaction completed")
	// No CSP header is set here: this response's own headers are never consulted
	// for the form-action check on the redirect it issues. See handleConsentForm.
	http.Redirect(w, r, completion.RedirectTo, http.StatusSeeOther)
}

// recordPrivacyConsent enforces the notice and records the acceptance, reporting
// whether the grant may proceed.
//
// The acceptance is written before the authorization server is asked to grant
// anything, so a store that cannot record it refuses the grant rather than issuing
// a token whose consent was never persisted. It answers the request itself on every
// path that stops the grant, and leaves the session live when the person can still
// fix what stopped it.
func (s *RemoteServer) recordPrivacyConsent(
	w http.ResponseWriter, r *http.Request, session *remoteSession,
) bool {
	context, ok := s.consentContext(w, r, session)
	if !ok {
		return false
	}

	if context.privacy.Required {
		// A checkbox that was not ticked is absent, so this compares against the
		// one value the form may send rather than testing for presence.
		if r.PostFormValue(fieldPrivacy) != privacyAccepted {
			s.retryConsent(w, r, session, msgPrivacyRequired)
			return false
		}
		if err := s.privacy.AcceptPrivacyNotice(
			r.Context(), context.principal, s.notice.digest, s.notice.version); err != nil {
			s.unavailable(w)
			return false
		}
		s.log(r.Context(), "the privacy notice was accepted")
	}

	// The approval gate is applied here, after the acceptance is stored and before
	// anything is granted. The order is the point: accepting the notice is the
	// person's decision about their own data, and it must not wait on the operator's
	// decision about their access. A held account can therefore accept, its
	// acceptance is recorded, and it still receives no token.
	if context.pending {
		s.holdAccount(w, r, session)
		return false
	}
	return true
}

// holdAccount ends the transaction of an account that is still waiting for the
// operator, and shows the person what happened.
//
// Nothing is granted and the transaction is closed rather than left to expire: the
// person comes back through their client once the operator has decided. Whatever they
// accepted a moment ago is already stored.
func (s *RemoteServer) holdAccount(
	w http.ResponseWriter, r *http.Request, session *remoteSession,
) {
	// The denial's redirect target is discarded on purpose — the person stays here,
	// on a page that explains the wait, instead of being bounced back to a client
	// that would only say "denied".
	if _, err := s.authorizations.Deny(r.Context(), session.capability); err != nil {
		s.log(r.Context(), "closing the transaction of a held account failed")
	}
	s.discard(session)
	s.clearCookie(w)
	s.log(r.Context(), "an account is waiting for the operator's approval")

	data := emptyRemoteData("")
	data.Privacy = privacyState{Version: s.notice.version}
	data.AccountPending = true
	s.pages.render(w, http.StatusForbidden, pagePending, data)
}

// decide grants or denies. A denial persists nothing at all.
func (s *RemoteServer) decide(
	ctx context.Context, capability string, allow bool,
) (Completion, error) {
	if allow {
		return s.authorizations.Grant(ctx, capability)
	}
	return s.authorizations.Deny(ctx, capability)
}

// session reports the transaction the request's cookie addresses.
func (s *RemoteServer) session(r *http.Request) (*remoteSession, error) {
	cookie, err := r.Cookie(RemoteCookieName)
	if err != nil {
		return nil, ErrNoTransaction
	}
	return s.sessions.get(s.now(), strings.TrimSpace(cookie.Value))
}

// live resolves the session and its disclosure for a page render, answering the
// request itself when either is unavailable.
func (s *RemoteServer) live(
	w http.ResponseWriter, r *http.Request,
) (*remoteSession, Disclosure, bool) {
	session, err := s.session(r)
	if err != nil {
		s.refuse(w, err)
		return nil, Disclosure{}, false
	}
	disclosure, err := s.authorizations.Disclose(r.Context(), session.capability)
	if err != nil {
		s.refuseSession(w, session, err)
		return nil, Disclosure{}, false
	}
	return session, disclosure, true
}

// challengeData adds what the OTP page says about the challenge.
func (s *RemoteServer) challengeData(
	session *remoteSession, disclosure Disclosure, message string,
) remotePageData {
	data := newRemotePageData(disclosure, session.formToken(), message)
	data.MFAMethod, data.DeliveryUncertain = session.challenge()
	return data
}

// retry re-renders a form with generic text after a refused attempt. The text never
// quotes a submitted value and never distinguishes a wrong password from an unknown
// account.
func (s *RemoteServer) retry(
	w http.ResponseWriter, r *http.Request, session *remoteSession, page, message string,
) {
	disclosure, err := s.authorizations.Disclose(r.Context(), session.capability)
	if err != nil {
		s.refuseSession(w, session, err)
		return
	}
	s.pages.render(w, http.StatusUnauthorized, page,
		s.challengeData(session, disclosure, sanitizedMessage(message)))
}

// refuse renders the outcome of a refused request. An expired transaction says so,
// because the user can act on it; everything else is the generic 404.
func (s *RemoteServer) refuse(w http.ResponseWriter, cause error) {
	switch {
	case errors.Is(cause, ErrTransactionExpired):
		s.pages.render(w, http.StatusGone, pageExpired, emptyRemoteData(""))
	case errors.Is(cause, errAttemptsExhausted):
		s.pages.render(w, http.StatusTooManyRequests, pageNotFound,
			emptyRemoteData(sanitizedMessage(msgExhausted)))
	default:
		s.notFound(w)
	}
}

// refuseSession refuses and, for a terminal cause, discards the transaction so it
// stops being addressable at once.
func (s *RemoteServer) refuseSession(
	w http.ResponseWriter, session *remoteSession, cause error,
) {
	if errors.Is(cause, ErrTransactionExpired) || errors.Is(cause, errAttemptsExhausted) {
		s.discard(session)
		s.clearCookie(w)
	}
	s.refuse(w, cause)
}

// refuseAuthorization delivers a refused authorization request. A refusal may be
// redirected only once the authorization server has decided it has earned a
// destination; otherwise it is rendered here.
func (s *RemoteServer) refuseAuthorization(w http.ResponseWriter, r *http.Request, cause error) {
	refusal, ok := errors.AsType[Refusal](cause)
	if !ok {
		s.notFound(w)
		return
	}
	if location := refusal.Location(); location != "" {
		// No form is involved in this navigation at all — the browser arrived here
		// by the client's own top-level redirect to /authorize, not by submitting
		// anything this server rendered — so form-action never governs this hop.
		http.Redirect(w, r, location, http.StatusFound)
		return
	}
	status := max(refusal.Status(), http.StatusBadRequest)
	s.pages.render(w, status, pageNotFound,
		emptyRemoteData(sanitizedMessage(refusal.Description())))
}

// abandon ends a transaction that cannot continue, and answers generically.
func (s *RemoteServer) abandon(w http.ResponseWriter, session *remoteSession) {
	s.discard(session)
	s.clearCookie(w)
	s.notFound(w)
}

// discard makes a transaction terminal: unusable from this instant, with the pending
// Garmin continuation dropped.
func (s *RemoteServer) discard(session *remoteSession) {
	session.consume()
	s.sessions.drop(session.capability)
}

// notFound renders the generic page. It is the same page and the same status for an
// unknown route, a missing cookie, a wrong form token and an out-of-order
// submission, so a probe learns nothing from the difference.
func (s *RemoteServer) notFound(w http.ResponseWriter) {
	s.pages.render(w, http.StatusNotFound, pageNotFound, emptyRemoteData(""))
}

// unavailable reports that no new transaction can be started, without saying why.
func (s *RemoteServer) unavailable(w http.ResponseWriter) {
	s.pages.render(w, http.StatusServiceUnavailable, pageNotFound, emptyRemoteData(""))
}

// emptyRemoteData is the page data for a refusal: no client, no token, no bounds.
func emptyRemoteData(message string) remotePageData {
	return newRemotePageData(Disclosure{}, "", message)
}

// parseBounded bounds the body before parsing it and reports whether the caller may
// continue. An oversized body is answered with 413 and never parsed.
func (s *RemoteServer) parseBounded(w http.ResponseWriter, r *http.Request) bool {
	switch err := readBoundedForm(w, r); {
	case errors.Is(err, errBodyTooLarge):
		s.pages.render(w, http.StatusRequestEntityTooLarge, pageNotFound, emptyRemoteData(""))
		return false
	case err != nil:
		s.notFound(w)
		return false
	}
	return true
}

// setCookie installs the transaction capability.
//
// The __Host- prefix requires Secure and Path=/ and forbids Domain, which is what
// binds the cookie to this exact host: no sibling host on the registrable domain can
// set it or read it. SameSite is Lax rather than Strict because the browser arrives
// here by a cross-site top-level navigation from the client's application, and a
// Strict cookie would not be sent on the redirect that follows.
func (s *RemoteServer) setCookie(w http.ResponseWriter, session *remoteSession) {
	http.SetCookie(w, &http.Cookie{
		Name:     RemoteCookieName,
		Value:    session.capability,
		Path:     "/",
		MaxAge:   int(max(session.expires.Sub(s.now()).Seconds(), 1)),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCookie removes the capability from the browser as soon as the transaction is
// terminal, so a shared or restored session cannot present it again.
func (s *RemoteServer) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     RemoteCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
