package loginweb

// The remote page names, which are also the template file names under pages/remote.
const (
	pageConsent = "consent"
	pageExpired = "expired"
	pagePending = "pending"
)

// remoteStylesheetPath is the one asset a remote page references. It sits under the
// login prefix rather than at the root, because on a public origin the root belongs
// to the deployment, not to this package.
const remoteStylesheetPath = "/login/style.css"

// remotePageData is everything a remote template may render.
//
// The field set is closed, and there is no field for an email address, a password, a
// one-time code, a transaction capability, or a Garmin continuation. A template
// therefore cannot print one, however it is written.
type remotePageData struct {
	// CSRFToken is the form token the next submission must carry. It is this
	// service's own token, independent of the client's OAuth state.
	CSRFToken string
	// Message is server-authored text explaining a refusal. It never quotes a
	// submitted value.
	Message string
	// MFAMethod is the delivery method Garmin named, and DeliveryUncertain reports
	// that delivery could not be confirmed.
	MFAMethod         string
	DeliveryUncertain bool

	// Client is the disclosure the authorization server validated: who is asking,
	// where the answer goes, and for what.
	Client Disclosure

	// Privacy is what the consent page says about the privacy notice: its label,
	// whether this person still has to accept it, and the date they accepted it
	// before. It carries no account data.
	Privacy privacyState

	// AccountPending reports that the operator has not approved this account yet.
	// The consent page says so before the buttons, because a page that offered an
	// Allow which grants nothing would be lying by omission.
	AccountPending bool

	// The field bounds, so the form advertises the limits the server enforces.
	MaxEmailLen    int
	MaxPasswordLen int
	MaxCodeLen     int
}

// newRemotePageData returns the data a remote page starts from.
func newRemotePageData(disclosure Disclosure, token, message string) remotePageData {
	return remotePageData{
		CSRFToken:      token,
		Message:        message,
		Client:         disclosure,
		MaxEmailLen:    MaxEmailLen,
		MaxPasswordLen: MaxPasswordLen,
		MaxCodeLen:     MaxCodeLen,
	}
}

// loadRemotePages parses the remote templates. They are a separate document set from
// the loopback ones: the two profiles state different things about who is asking and
// where the answer goes, and one shared document would make it easy to show the
// wrong statement on the wrong profile.
func loadRemotePages() (*pageSet, error) {
	// privacy.html is a partial rather than a page: it defines the two notice
	// blocks the consent page includes, and is never served on its own.
	return loadPageSet("pages/remote", []string{
		pageDisclosure, pageCredentials, pageMFA, pageConsent, pageNotFound, pageExpired,
		pagePending,
	}, "privacy.html")
}
