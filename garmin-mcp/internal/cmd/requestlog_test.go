package cmd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// records decodes the JSON lines a test logger collected.
func records(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func testLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, nil))
}

func TestARequestRecordNamesTheMethodPathAndStatus(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}), testLogger(&buffer))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/token", nil))

	got := records(t, &buffer)
	if len(got) != 1 {
		t.Fatalf("recorded %d lines, want 1", len(got))
	}
	for key, want := range map[string]any{
		"method": "POST", "path": "/token", "status": float64(http.StatusTooManyRequests),
	} {
		if got[0][key] != want {
			t.Errorf("%s = %v, want %v", key, got[0][key], want)
		}
	}
	if _, ok := got[0]["duration_ms"]; !ok {
		t.Error("the record carries no duration")
	}
}

// TestARequestRecordNeverCarriesTheQueryString is the mutant this test catches:
// logging r.URL.String() or r.RequestURI instead of r.URL.Path. Both carry the
// query, and the query on these routes carries single-use credentials — an
// authorization code on a client's callback, the state and PKCE challenge on
// /authorize. A request log that kept them would put them in the operator's
// journal in plain text.
func TestARequestRecordNeverCarriesTheQueryString(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSeeOther)
	}), testLogger(&buffer))

	const secret = "a-single-use-authorization-code"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/authorize?code="+secret+"&state=opaque&code_challenge=whatever", nil))

	whole := buffer.String()
	for _, forbidden := range []string{secret, "state=", "code_challenge", "?"} {
		if strings.Contains(whole, forbidden) {
			t.Errorf("the record carries %q:\n%s", forbidden, whole)
		}
	}
	if got := records(t, &buffer)[0]["path"]; got != "/authorize" {
		t.Errorf("path = %v, want the bare path", got)
	}
}

// TestTheWrapperStaysFlushable is the mutant this test catches: a
// ResponseWriter wrapper that does not forward Flush. The MCP transport answers
// over Server-Sent Events, so every streaming response would be held in a buffer
// until the request ended — breaking the clients that work today, which is the one
// outcome a diagnostic feature must not produce.
func TestTheWrapperStaysFlushable(t *testing.T) {
	t.Parallel()

	flushed := false
	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the handler no longer sees an http.Flusher")
			return
		}
		_, _ = w.Write([]byte("event: message\n"))
		flusher.Flush()

		// http.ResponseController is the other route to the same capability, and
		// it needs Unwrap rather than the assertion above.
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush: %v", err)
		}
		flushed = true
	}), testLogger(&bytes.Buffer{}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	if !flushed {
		t.Error("the handler could not flush")
	}
}

func TestABodyWithoutAnExplicitStatusIsRecordedAs200(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ready"))
	}), testLogger(&buffer))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if got := records(t, &buffer)[0]["status"]; got != float64(http.StatusOK) {
		t.Errorf("status = %v, want 200", got)
	}
}

func TestNoLoggerLeavesTheHandlerUntouched(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if got := logRequests(inner, nil); got == nil {
		t.Fatal("logRequests(nil logger) returned nothing")
	}
	// The handler is returned as it was, so a deployment that did not ask for the
	// log pays nothing at all -- not even a wrapper.
	handler := logRequests(inner, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}
