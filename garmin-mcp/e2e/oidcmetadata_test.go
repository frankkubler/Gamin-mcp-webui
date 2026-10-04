//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// This file covers the discovery path an OpenID Connect client asks for.
//
// The deployment is an OAuth 2.0 authorization server, not an OpenID provider. It
// serves the RFC 8414 document under its own name, and under this one too, because
// a 404 here ends a client's discovery even when every other document answered.
// That is not hypothetical: a real deployment produced exactly this sequence --
// the MCP endpoint 200, both OAuth documents 200, and this path 404 -- and the
// client reported a dependency failure with nothing else to go on.

// TestTheOIDCDiscoveryPathServesTheSameDocument is the mutant this test catches:
// registering the route but pointing it at a different handler, or letting the two
// documents drift apart. A client that reads one and authenticates against the
// other must not be able to tell them apart.
func TestTheOIDCDiscoveryPathServesTheSameDocument(t *testing.T) {
	server := startRemoteServer(t)

	oauth := readMetadata(t, server, "/.well-known/oauth-authorization-server")
	oidc := readMetadata(t, server, "/.well-known/openid-configuration")

	if len(oidc) == 0 {
		t.Fatal("the OpenID discovery path served an empty document")
	}
	for _, required := range []string{"issuer", "authorization_endpoint", "token_endpoint"} {
		if _, ok := oidc[required]; !ok {
			t.Errorf("the document carries no %q", required)
		}
	}

	// Byte-identical content, compared field by field so a failure names the one
	// that drifted.
	for key, want := range oauth {
		got, ok := oidc[key]
		if !ok {
			t.Errorf("the OpenID document is missing %q", key)
			continue
		}
		if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if len(oidc) != len(oauth) {
		t.Errorf("the two documents carry %d and %d fields", len(oidc), len(oauth))
	}
}

// readMetadata fetches one discovery document without a token: a client has none
// when it reads these.
func readMetadata(t *testing.T, server remoteServer, path string) map[string]any {
	t.Helper()

	response, err := server.client.Get(server.origin + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s = %d, want 200 unauthenticated (body %s)", path, response.StatusCode, raw)
	}
	var document map[string]any
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return document
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
