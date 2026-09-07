//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/tamcore/garmin-mcp/internal/policy"
	"github.com/tamcore/garmin-mcp/internal/store"
)

// This file pins the name of the scope that authorizes the write tier.
//
// The name is a trap worth a test of its own. compat/tools.json — the pinned
// upstream manifest — spells the per-tool scope of a workout write
// "garmin:workouts:write", and that string is plausible enough that a
// deployment can be configured with it end to end without anything complaining:
// internal/config accepts any non-blank scope on a client, the authorization
// server advertises whatever the clients register (internal/cmd/remote.go
// passes clients.Scopes() straight into ScopesSupported), the /authorize call
// succeeds, and the minted token carries it. Only the tool policy disagrees,
// and it disagrees silently, one refusal at a time — internal/policy/tier.go's
// RequiredScope demands ScopeWrite, which is "garmin:write".
//
// So the assertion is a comparison, not a check of one deployment: two tokens
// that differ in nothing but that string, against one deployment with the
// write tier enabled. Without the negative half a passing test would only show
// that some scope works; without the positive half it would not show that the
// tier is reachable at all.

// wrongWriteScope is the upstream manifest's per-tool scope name. It is not a
// scope this server's policy knows, which is the whole point.
const wrongWriteScope = "garmin:workouts:write"

// TestTheWriteTierIsAuthorizedByGarminWriteAndNothingElse is the mutant this
// test catches: renaming policy.ScopeWrite, or "fixing" a deployment by
// registering the manifest's name on the OAuth client, leaves every other test
// green — the write tier simply stays invisible, which is indistinguishable
// from a deployment that never enabled it.
func TestTheWriteTierIsAuthorizedByGarminWriteAndNothingElse(t *testing.T) {
	var right, wrong seededCode
	server := startDestructiveRemoteServer(t, func(dir, origin string) {
		sqlite := openSeedStore(t, dir)
		defer func() { _ = sqlite.Close() }()

		seedClient(t, sqlite)
		right = seedCodeWithScopes(t, sqlite, origin, "e2e-scope-right@example.test",
			[]string{remoteScope, string(policy.ScopeWrite)})
		wrong = seedCodeWithScopes(t, sqlite, origin, "e2e-scope-wrong@example.test",
			[]string{remoteScope, wrongWriteScope})
	})

	rightTools := listToolNames(t, server, redeemCode(t, server, right))
	wrongTools := listToolNames(t, server, redeemCode(t, server, wrong))

	// upload_workout is a write-tier tool, and the manifest files it under the
	// scope name this test proves does not work.
	const writeTool = "upload_workout"
	if !rightTools[writeTool] {
		t.Errorf("%q is absent with %q granted, so the write tier is not reachable at all",
			writeTool, policy.ScopeWrite)
	}
	if wrongTools[writeTool] {
		t.Errorf("%q is present with only %q granted: the policy accepted a scope it does not define",
			writeTool, wrongWriteScope)
	}

	// And the whole tier moves together, so the difference is the tier rather
	// than one tool's own registration.
	if len(rightTools) <= len(wrongTools) {
		t.Errorf("%d tools with %q, %d with %q: granting the write scope must reveal more",
			len(rightTools), policy.ScopeWrite, len(wrongTools), wrongWriteScope)
	}
}

// seedCodeWithScopes is seedOneCode with the granted scope set under the
// caller's control, which is the one thing this file varies.
func seedCodeWithScopes(
	t *testing.T, sqlite *store.SQLiteStore, origin, email string, scopes []string,
) seededCode {
	t.Helper()

	verifier, challenge := pkcePair(t)
	params := seedAuthCodeParams{
		principalID: seedApprovedPrincipal(t, sqlite, email),
		clientID:    remoteClientID,
		redirectURI: remoteRedirectURI,
		resource:    mcpURLFor(origin),
		scopes:      scopes,
		challenge:   challenge,
	}
	seedConsent(t, sqlite, params)
	return seededCode{
		principalID: params.principalID,
		code:        seedAuthCode(t, sqlite, params),
		verifier:    verifier,
	}
}

// listToolNames opens one MCP session with token and returns the set of tool
// names tools/list serves it. The listing is the policy's own per-tool
// decision made visible: internal/policy filters the catalogue with the same
// call tools/call would make.
func listToolNames(t *testing.T, server remoteServer, token string) map[string]bool {
	t.Helper()

	client := mcpStreamingClient(server)
	sessionID := initializeConfirmSession(t, client, server, token)

	accepted := postAndDiscard(t, client, server, token, sessionID,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer func() { _ = accepted.Body.Close() }()
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("notifications/initialized status = %d, want 202", accepted.StatusCode)
	}

	ctx, cancel := context.WithTimeout(t.Context(), remoteRequestTimeout)
	defer cancel()
	resp, reader := openMCPStream(t, ctx, client, server, http.MethodPost, token, sessionID,
		`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("tools/list status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}

	envelope, err := reader.next()
	if err != nil {
		t.Fatalf("read the tools/list response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("tools/list returned a JSON-RPC error: %s", envelope.Error)
	}
	return toolNamesIn(t, envelope.Result)
}

// toolNamesIn decodes the tool names out of a tools/list result.
func toolNamesIn(t *testing.T, result json.RawMessage) map[string]bool {
	t.Helper()

	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &listing); err != nil {
		t.Fatalf("decode the tools/list result: %v", err)
	}
	if listing.NextCursor != "" {
		t.Fatal("the listing is paginated; this test assumes one page")
	}
	names := make(map[string]bool, len(listing.Tools))
	for _, tool := range listing.Tools {
		names[tool.Name] = true
	}
	if len(names) == 0 {
		t.Fatal(fmt.Sprint("tools/list served no tools at all"))
	}
	return names
}
