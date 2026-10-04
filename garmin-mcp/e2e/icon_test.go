//go:build e2e

package e2e

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/mcpserver"
)

// This file covers the fork's server icon: the document a client shows next to
// the connector's name.
//
// The icon has to reach a client that holds nothing — no token, no session, no
// prior request — so it travels two ways, and both are tested here against the
// real binary: inline in the initialize result's serverInfo, which is the
// mechanism the specification defines, and on a public route, which is where a
// client that ignores that field and looks for an origin's favicon will go.
//
// Getting this wrong is not loud. A client offered no icon shows something else,
// and on a URL that once served another implementation it may keep showing that
// implementation's branding indefinitely.

// iconMIMEType is what both halves must report.
const iconMIMEType = "image/svg+xml"

// TestTheIconIsServedWithoutATokenAndDeclaredInServerInfo is the mutant this test
// catches: putting the icon route behind the transport's bearer middleware, or
// declaring the icon without serving it (or the reverse), each leave one of the
// two paths a client actually takes broken while the other still works.
func TestTheIconIsServedWithoutATokenAndDeclaredInServerInfo(t *testing.T) {
	fixture := setUpTwoPrincipals(t)
	server := fixture.server

	t.Run("public route", func(t *testing.T) {
		// No Authorization header is sent. An icon behind a token is an icon a
		// client cannot show before the user has authorized anything, which is
		// precisely when it is needed.
		response, err := server.client.Get(server.origin + mcpserver.IconPath)
		if err != nil {
			t.Fatalf("read the icon: %v", err)
		}
		defer func() { _ = response.Body.Close() }()

		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 unauthenticated", response.StatusCode)
		}
		if got := response.Header.Get("Content-Type"); got != iconMIMEType {
			t.Errorf("Content-Type = %q, want %q", got, iconMIMEType)
		}
		if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the icon body: %v", err)
		}
		if !strings.Contains(string(body), "<svg") {
			t.Errorf("the icon body is not an SVG document: %q", firstBytes(body))
		}
	})

	t.Run("favicon redirect", func(t *testing.T) {
		// A redirect that is followed proves nothing about where it pointed, so
		// this client is told not to follow it.
		client := *server.client
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		response, err := client.Get(server.origin + "/favicon.ico")
		if err != nil {
			t.Fatalf("read the favicon: %v", err)
		}
		defer func() { _ = response.Body.Close() }()

		if response.StatusCode != http.StatusFound {
			t.Fatalf("status = %d, want 302", response.StatusCode)
		}
		if got := response.Header.Get("Location"); got != mcpserver.IconPath {
			t.Errorf("Location = %q, want %q", got, mcpserver.IconPath)
		}
	})

	t.Run("declared in serverInfo", func(t *testing.T) {
		icons := initializeIcons(t, server, fixture.tokenA)
		if len(icons) == 0 {
			t.Fatal("serverInfo declares no icon")
		}
		icon := icons[0]
		if icon.MIMEType != iconMIMEType {
			t.Errorf("mimeType = %q, want %q", icon.MIMEType, iconMIMEType)
		}
		// The source is a data URI rather than a URL on purpose: stdio has no
		// origin, and a remote deployment's origin is configuration the server
		// package does not read. Decoding it is what proves a client receives the
		// document itself rather than a reference that may not resolve.
		prefix := "data:" + iconMIMEType + ";base64,"
		encoded, ok := strings.CutPrefix(icon.Source, prefix)
		if !ok {
			t.Fatalf("src does not start with %q: %q", prefix, firstBytes([]byte(icon.Source)))
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode the declared icon: %v", err)
		}
		if !strings.Contains(string(decoded), "<svg") {
			t.Errorf("the declared icon is not an SVG document: %q", firstBytes(decoded))
		}
	})
}

// declaredIcon is the subset of the specification's icon record this test reads.
type declaredIcon struct {
	Source   string `json:"src"`
	MIMEType string `json:"mimeType"`
}

// initializeIcons returns the icons the initialize result's serverInfo declares.
func initializeIcons(t *testing.T, server remoteServer, token string) []declaredIcon {
	t.Helper()

	// postStatefulInitialize rather than postInitialize: this deployment is
	// stateful, and the protocol version the latter declares is only accepted on
	// a stateless server. See oauthflow_setup.go's note on the two.
	response := postStatefulInitialize(t, server, token)
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("initialize status = %d, want 200 (body %s)", response.StatusCode, raw)
	}

	var envelope struct {
		Result struct {
			ServerInfo struct {
				Name  string         `json:"name"`
				Icons []declaredIcon `json:"icons"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(jsonRPCPayload(t, response), &envelope); err != nil {
		t.Fatalf("decode the initialize result: %v", err)
	}
	if envelope.Result.ServerInfo.Name == "" {
		t.Fatal("the initialize result carries no serverInfo at all")
	}
	return envelope.Result.ServerInfo.Icons
}

// jsonRPCPayload returns the JSON-RPC message in response, whichever of the two
// framings Streamable HTTP chose for it.
//
// The request advertised both, and which one the transport picks is its decision
// rather than this test's subject — so this reads either, instead of pinning a
// choice that is free to change.
func jsonRPCPayload(t *testing.T, response *http.Response) []byte {
	t.Helper()

	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the response body: %v", err)
		}
		return body
	}

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if payload, ok := strings.CutPrefix(scanner.Text(), "data:"); ok {
			return []byte(strings.TrimPrefix(payload, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the event stream: %v", err)
	}
	t.Fatal("the event stream carried no data line")
	return nil
}

// firstBytes bounds what a failure prints, so a malformed body cannot dump a
// whole document into the test log.
func firstBytes(body []byte) string {
	const limit = 120
	if len(body) > limit {
		return string(body[:limit]) + "…"
	}
	return string(body)
}
