package mcpserver

// This file is a fork addition. It gives the deployment an identity a client can
// show, which upstream leaves unset: an MCP client that is offered no icon falls
// back on whatever it has — a generic mark, or, on a URL that once served a
// different implementation, that implementation's branding, which then outlives it.
//
// The mark is this project's own. It is deliberately NOT the Garmin logo: this
// server is not operated by Garmin, every page of the login flow says so in as many
// words, and a page carrying Garmin's mark would undo that statement. An operator
// who wants a different icon on their own deployment replaces icon.svg and rebuilds;
// nothing here reads a path, so there is no setting through which a request could
// point the deployment at someone else's image.

import (
	"embed"
	"encoding/base64"
	"net/http"
	"strconv"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IconPath is the public route the icon is served on. It is outside the login
// subtree and needs no token: a client fetches it before it has one.
const IconPath = "/icon.svg"

// iconMIMEType is the icon's media type. It is stated rather than sniffed, and the
// handler sends nosniff with it, so a browser cannot be talked into treating the
// document as anything else.
const iconMIMEType = "image/svg+xml"

// iconCacheControl lets a client hold the icon for a day. It changes only with a
// build, and a stale icon for a few hours is not a failure.
const iconCacheControl = "public, max-age=86400"

//go:embed icon.svg
var iconFS embed.FS

// iconBytes is the embedded document, read once at first use.
var iconBytes = sync.OnceValue(func() []byte {
	// The embed directive already failed the build if the file were missing, so
	// this read cannot fail for a reason a caller could act on.
	data, err := iconFS.ReadFile("icon.svg")
	if err != nil {
		panic("mcpserver: the embedded icon is unreadable: " + err.Error())
	}
	return data
})

// iconDataURI renders the icon as a data URI, computed once.
//
// serverInfo carries the icon this way rather than as a URL, because the two
// transports disagree about what a URL would even mean: stdio has no origin at all,
// and a remote deployment's origin is configuration this package does not read. A
// data URI is the same answer on both, and it reaches a client that has not yet made
// a second request.
var iconDataURI = sync.OnceValue(func() string {
	return "data:" + iconMIMEType + ";base64," +
		base64.StdEncoding.EncodeToString(iconBytes())
})

// serverIcons is the icon set the server advertises in its implementation record.
//
// One entry, with no theme: the mark is a filled tile that reads on a light or a
// dark background, so a client has nothing to choose between. Sizes is "any",
// which is what the specification says a scalable format declares.
func serverIcons() []mcp.Icon {
	return []mcp.Icon{{
		Source:   iconDataURI(),
		MIMEType: iconMIMEType,
		Sizes:    []string{"any"},
	}}
}

// IconHandler serves the icon at [IconPath].
//
// It answers GET and HEAD and refuses everything else: the route is a static
// document, and a route that accepted a body would be one more thing to reason
// about. The response carries no cookie and reads no request field, so it is the
// same answer for every caller.
func IconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body := iconBytes()
		header := w.Header()
		header.Set("Content-Type", iconMIMEType)
		header.Set("Content-Length", strconv.Itoa(len(body)))
		header.Set("Cache-Control", iconCacheControl)
		header.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}
