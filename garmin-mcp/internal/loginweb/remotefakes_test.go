package loginweb_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tamcore/garmin-mcp/internal/loginweb"
)

// fakeTransaction is one server-owned transaction inside the fake authorization
// server. It holds no credential: the principal is a pseudonymous identifier.
type fakeTransaction struct {
	disclosure  loginweb.Disclosure
	redirectURI string
	state       string
	principal   string
	expiresAt   time.Time
}

// fakeAuthorizations is the OAuth seam under test control. It stands in for
// internal/oauthserver, so these tests depend on the interface declared in this
// package and on nothing else.
type fakeAuthorizations struct {
	mu       sync.Mutex
	txns     map[string]*fakeTransaction
	now      func() time.Time
	ttl      time.Duration
	beginErr error

	minted   int
	attaches int
	grants   int
	denials  int
}

func newFakeAuthorizations(now func() time.Time) *fakeAuthorizations {
	return &fakeAuthorizations{
		txns: make(map[string]*fakeTransaction),
		now:  now,
		ttl:  5 * time.Minute,
	}
}

func (f *fakeAuthorizations) Begin(
	_ context.Context, query url.Values,
) (loginweb.Authorization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.beginErr != nil {
		return loginweb.Authorization{}, f.beginErr
	}
	f.minted++
	clientID := query.Get("client_id")
	capability := fmt.Sprintf("capability-%d-%s", f.minted, clientID)
	tx := &fakeTransaction{
		disclosure: loginweb.Disclosure{
			ClientID:     clientID,
			ClientName:   clientName(clientID),
			RedirectURI:  query.Get("redirect_uri"),
			RedirectHost: testRedirectHost,
			Resource:     query.Get("resource"),
			Scopes:       strings.Fields(query.Get("scope")),
		},
		redirectURI: query.Get("redirect_uri"),
		state:       query.Get("state"),
		expiresAt:   f.now().Add(f.ttl),
	}
	f.txns[capability] = tx
	return loginweb.Authorization{
		Capability: capability,
		Disclosure: tx.disclosure,
		ExpiresAt:  tx.expiresAt,
	}, nil
}

// live returns the transaction a capability addresses, discarding it once its
// lifetime has elapsed. The caller holds the mutex.
func (f *fakeAuthorizations) live(capability string) (*fakeTransaction, error) {
	tx, ok := f.txns[capability]
	if !ok {
		return nil, loginweb.ErrNoTransaction
	}
	if !f.now().Before(tx.expiresAt) {
		delete(f.txns, capability)
		return nil, loginweb.ErrTransactionExpired
	}
	return tx, nil
}

func (f *fakeAuthorizations) Disclose(
	_ context.Context, capability string,
) (loginweb.Disclosure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.live(capability)
	if err != nil {
		return loginweb.Disclosure{}, err
	}
	return tx.disclosure, nil
}

func (f *fakeAuthorizations) AttachPrincipal(
	_ context.Context, capability, principal string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.live(capability)
	if err != nil {
		return err
	}
	if principal == "" {
		return loginweb.ErrNoTransaction
	}
	f.attaches++
	tx.principal = principal
	return nil
}

func (f *fakeAuthorizations) Principal(
	_ context.Context, capability string,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.live(capability)
	if err != nil {
		return "", err
	}
	return tx.principal, nil
}

func (f *fakeAuthorizations) Grant(
	ctx context.Context, capability string,
) (loginweb.Completion, error) {
	return f.finish(ctx, capability, true)
}

func (f *fakeAuthorizations) Deny(
	ctx context.Context, capability string,
) (loginweb.Completion, error) {
	return f.finish(ctx, capability, false)
}

// finish consumes the transaction, which is what makes a capability single-use.
func (f *fakeAuthorizations) finish(
	_ context.Context, capability string, allow bool,
) (loginweb.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.live(capability)
	if err != nil {
		return loginweb.Completion{}, err
	}
	if tx.principal == "" {
		return loginweb.Completion{}, loginweb.ErrNoTransaction
	}
	delete(f.txns, capability)

	params := "?error=access_denied"
	f.denials++
	if allow {
		f.denials--
		f.grants++
		params = "?code=" + testCodeParam
	}
	return loginweb.Completion{
		RedirectTo: tx.redirectURI + params + "&state=" + url.QueryEscape(tx.state),
	}, nil
}

func (f *fakeAuthorizations) counts() (attaches, grants, denials int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attaches, f.grants, f.denials
}

// fakePrivacyConsents is the privacy notice record under test control. It stores the
// acceptances in memory, keyed exactly as the real store is: by principal and by the
// digest of the text that was accepted.
type fakePrivacyConsents struct {
	mu       sync.Mutex
	accepted map[string]time.Time
	versions map[string]string
	now      func() time.Time

	readErr   error
	acceptErr error

	reads   int
	records int
}

func newFakePrivacyConsents(now func() time.Time) *fakePrivacyConsents {
	return &fakePrivacyConsents{
		accepted: make(map[string]time.Time),
		versions: make(map[string]string),
		now:      now,
	}
}

func (f *fakePrivacyConsents) key(principal, digest string) string {
	return principal + "\x00" + digest
}

func (f *fakePrivacyConsents) AcceptedPrivacyNotice(
	_ context.Context, principal, digest string,
) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reads++
	if f.readErr != nil {
		return time.Time{}, false, f.readErr
	}
	at, found := f.accepted[f.key(principal, digest)]
	return at, found, nil
}

func (f *fakePrivacyConsents) AcceptPrivacyNotice(
	_ context.Context, principal, digest, version string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.acceptErr != nil {
		return f.acceptErr
	}
	f.records++
	key := f.key(principal, digest)
	// Idempotent, like the store: the first acceptance is the one that happened.
	if _, seen := f.accepted[key]; !seen {
		f.accepted[key] = f.now()
		f.versions[key] = version
	}
	return nil
}

// count reports how many acceptances were written, for a test that asserts a page
// view or a denial recorded nothing.
func (f *fakePrivacyConsents) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records
}

// accept pre-records an acceptance, for the tests that start from a person who has
// already been asked.
func (f *fakePrivacyConsents) accept(principal, digest, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted[f.key(principal, digest)] = f.now()
	f.versions[f.key(principal, digest)] = version
}
