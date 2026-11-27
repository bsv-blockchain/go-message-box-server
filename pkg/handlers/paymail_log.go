package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// Bounds on what one paymail log record copies from the request and the
// response. Both come from unauthenticated callers, so neither may grow a log
// record without limit.
const (
	maxLoggedUserAgent = 200
	maxLoggedErrorBody = 1 << 10
)

// PaymailRequestLog wraps the public paymail routes and logs one record for
// each request: the route, the status, the error code for a refused request,
// the sender's user agent and the time taken. The handlers answer a 4xx
// without a log line, so without this record a sender such as HandCash can
// stop at any step and leave no trace of where. The bodies are not logged:
// they carry transactions and sender notes.
func PaymailRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &paymailLogWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		ua := r.UserAgent()
		if len(ua) > maxLoggedUserAgent {
			ua = ua[:maxLoggedUserAgent]
		}
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"user_agent", ua,
		}
		if rec.status >= http.StatusBadRequest {
			var e struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(rec.errBody.Bytes(), &e) == nil && e.Code != "" {
				args = append(args, "code", e.Code)
			}
		}
		logger.Info("paymail request", args...)
	})
}

// paymailLogWriter records the status and, for an error, the start of the
// body, which holds the error code.
type paymailLogWriter struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
	errBody     bytes.Buffer
}

func (w *paymailLogWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *paymailLogWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	if w.status >= http.StatusBadRequest {
		if room := maxLoggedErrorBody - w.errBody.Len(); room > 0 {
			w.errBody.Write(b[:min(room, len(b))])
		}
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *paymailLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
