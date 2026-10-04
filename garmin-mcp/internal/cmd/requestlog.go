package cmd

// One log record per HTTP request, for a deployment whose operator cannot
// otherwise see what a client received.
//
// The server's own records are coarse by design: a lifecycle line at start-up and
// vocabulary-only progress lines for a login. Nothing says that a client got a 400,
// a 403, a 404 or a 429 -- and those are exactly the answers an operator needs when
// a client reports "connected, but no tools". Diagnosing that from the outside is
// guesswork, and it cost a real deployment an evening.
//
// Two decisions that are the point of this file:
//
//   - The query string is NEVER recorded. /authorize carries the client's state and
//     PKCE challenge there, and a client's own callback carries an authorization
//     code. A request log that kept the query would turn the operator's journal into
//     a place where single-use credentials sit in plain text.
//   - The wrapper forwards Flush. The MCP transport answers over Server-Sent Events,
//     which needs the handler chain to stay flushable; a wrapper that swallowed it
//     would leave every streaming response buffered. That is the regression most
//     likely to be shipped here, so it has its own test.

import (
	"log/slog"
	"net/http"
	"time"
)

// logRequests wraps next so each request produces one record on logger.
//
// A nil logger returns next unchanged, so the caller needs no condition of its own.
func logRequests(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		// r.URL.Path, never r.URL.String() and never r.RequestURI: those carry
		// the query.
		logger.InfoContext(r.Context(), "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status()),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()))
	})
}

// A statusRecorder remembers the status a handler wrote.
//
// It embeds the ResponseWriter rather than replacing it, and forwards the one
// optional interface this server's handlers actually use.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

// WriteHeader records the status on its way through.
func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write covers the handler that writes a body without calling WriteHeader, which
// net/http answers with 200.
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// status reports the recorded status, or 200 for a handler that wrote neither a
// status nor a body.
func (s *statusRecorder) status() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}

// Flush forwards to the wrapped writer when it can flush.
//
// Server-Sent Events are unusable without it: the MCP transport writes an event and
// flushes, and a wrapper that dropped the capability would hold every event until
// the response ended.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the wrapped writer to [http.ResponseController], which is how
// recent code reaches Flush, SetWriteDeadline and the rest without a type
// assertion for each.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
